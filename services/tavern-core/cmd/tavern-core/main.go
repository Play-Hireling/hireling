// tavern-core: task queue with leases, policy gate, budgets, the quest and the
// Front Desk (where the owner talks to the tavern). Go, state on Redis.
//
//	TAVERN_ADDR        listen address (default :8080)
//	TAVERN_AGENTS_DIR  agents/<id>/profile.yaml root (default /agents)
//	REDIS_URL          redis://redis:6379/0 — unset = in-memory store (dev only)
//	GOAL_TITLE, GOAL_TARGET, GOAL_CURRENCY, GOAL_SOURCE, GOAL_KINDS, GOAL_PERIOD
//	                   seed the quest (C1) when the store has none; later
//	                   changes go through PUT /goal and POST /goal/progress
//	FRONTDESK_OWNER_TOKEN  bearer token of the owner (orders, decisions, status); unset = owner endpoints closed
//	FRONTDESK_AGENT_TOKEN  bearer token any hireling of this tavern may use
//	FRONTDESK_TOKEN_<ID>   a hireling's own token (overrides the shared one for that hireling)
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Play-Hireling/hireling/services/tavern-core/internal/asks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/autonomy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/httpapi"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
	"github.com/redis/go-redis/v9"
)

// healthcheck is the Docker HEALTHCHECK: the image is distroless, so the binary
// probes its own /healthz (store ping included).
func healthcheck() int {
	addr := env("TAVERN_ADDR", ":8080")
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get("http://" + addr + "/healthz")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	res.Body.Close()
	return 0
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	logger := log.New(os.Stderr, "tavern-core ", log.LstdFlags|log.LUTC)
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	gate, err := policy.LoadDir(env("TAVERN_AGENTS_DIR", "/agents"))
	if err != nil {
		logger.Fatalf("profiles: %v", err)
	}

	var store tasks.Store = tasks.NewMemory()
	var spent usage.Store = usage.NewMemory()
	var quest goal.Store = goal.NewMemory()
	var grants policy.Grants = policy.NewMemoryGrants()
	var autoLog autonomy.Store = autonomy.NewMemory()
	var askBook asks.Store = asks.NewMemory()
	var persist frontdesk.Persister
	ready := func() error { return nil }
	if url := os.Getenv("REDIS_URL"); url != "" {
		opt, err := redis.ParseURL(url)
		if err != nil {
			logger.Fatalf("REDIS_URL: %v", err)
		}
		c := redis.NewClient(opt)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := c.Ping(ctx).Err(); err != nil {
			logger.Printf("redis not ready yet: %v (will keep trying on /healthz)", err)
		}
		cancel()
		store = &tasks.Redis{C: c}
		spent = &usage.Redis{C: c}
		quest = &goal.Redis{C: c}
		grants = &policy.RedisGrants{C: c}
		autoLog = &autonomy.Redis{C: c}
		askBook = &asks.Redis{C: c}
		persist = &frontdesk.Redis{C: c}
		ready = func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return c.Ping(ctx).Err()
		}
	} else {
		logger.Printf("REDIS_URL unset: using the in-memory store (state is lost on restart)")
	}

	approver := func(agent string) bool {
		if agent == "owner" {
			return true
		}
		p := gate.Profile(agent)
		return p != nil && p.Planner
	}
	quests := &goal.Service{Store: quest, Approver: approver}
	seedGoal(logger, quests)
	// C3: every task declares a contribution kind from the quest (DefaultKinds until one is set).
	kinds := func(ctx context.Context) []string {
		if g, err := quests.Get(ctx); err == nil {
			return g.AllowedKinds()
		}
		return goal.DefaultKinds
	}
	queue := &tasks.Service{Store: store, Approver: approver, Kinds: kinds}
	desk := newDesk(gate, persist)
	if err := desk.Load(context.Background()); err != nil {
		logger.Printf("front desk: %v (starting empty)", err)
	}
	if desk.OwnerToken == "" {
		logger.Printf("FRONTDESK_OWNER_TOKEN unset: owner endpoints are closed")
	}
	if err := desk.Check(); err != nil {
		logger.Fatalf("front desk: %v", err) // never prints a token
	}
	srv := &httpapi.Server{Gate: gate, Tasks: queue, Usage: spent, Goal: quests, Grants: grants, Log: autoLog, Asks: askBook, Ready: ready, Extra: desk.Register, Owner: desk.IsOwner}
	hs := &http.Server{
		Addr:              env("TAVERN_ADDR", ":8080"),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
	}()
	// B4: expired leases go back to the queue on their own, and are logged.
	go sweepLeases(ctx, logger, queue, 30*time.Second)
	logger.Printf("listening on %s with agents %v", hs.Addr, gate.Agents())
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatal(err)
	}
}

// seedGoal defines the quest from GOAL_* when the store is empty (first boot,
// or a dev box without Redis). An existing record always wins.
func seedGoal(logger *log.Logger, s *goal.Service) {
	title := os.Getenv("GOAL_TITLE")
	if title == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Get(ctx); err == nil {
		return
	}
	target, err := strconv.ParseFloat(env("GOAL_TARGET", "0"), 64)
	if err != nil {
		logger.Printf("GOAL_TARGET: %v (quest not seeded)", err)
		return
	}
	d := goal.Define{Title: title, Target: target, Currency: os.Getenv("GOAL_CURRENCY"), Source: os.Getenv("GOAL_SOURCE"), Period: os.Getenv("GOAL_PERIOD")}
	if k := os.Getenv("GOAL_KINDS"); k != "" {
		d.Kinds = strings.Split(k, ",")
	}
	g, err := s.Set(ctx, "owner", d)
	if err != nil {
		logger.Printf("quest not seeded: %v", err)
		return
	}
	logger.Printf("%s", g.Bar())
}

// sweepLeases returns expired leases to the queue every `every` (B4). Each one
// is an `expired` event on the timeline and a log line here.
func sweepLeases(ctx context.Context, logger *log.Logger, s *tasks.Service, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			freed, err := s.Sweep(ctx)
			if err != nil {
				logger.Printf("lease sweep: %v", err)
			}
			for _, x := range freed {
				logger.Printf("lease expired: %s (%s) — back in the queue", x.ID, x.Owner)
			}
		}
	}
}

// newDesk builds the Front Desk from the loaded profiles and the token variables.
func newDesk(gate *policy.Gate, persist frontdesk.Persister) *frontdesk.Desk {
	d := &frontdesk.Desk{
		AgentTokens: map[string]string{},
		SharedToken: os.Getenv("FRONTDESK_AGENT_TOKEN"),
		OwnerToken:  os.Getenv("FRONTDESK_OWNER_TOKEN"),
		Persist:     persist,
	}
	for _, id := range gate.Agents() {
		p := gate.Profile(id)
		if p == nil {
			continue
		}
		d.Agents = append(d.Agents, p.DeskID)
		if t := os.Getenv(p.TokenEnv()); t != "" {
			d.AgentTokens[p.DeskID] = t
		}
	}
	return d
}
