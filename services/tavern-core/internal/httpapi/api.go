// Package httpapi exposes tavern-core over HTTP (docs/api/tavern-core.md):
//
//	GET  /healthz
//	GET  /agents                      loaded hireling ids
//	GET  /agents/{id}/budget          budget from the profile + today's usage (A2)
//	POST /agents/{id}/usage           one turn's token usage and cost, from the runtime (A2)
//	GET  /agents/{id}/usage           today's totals and the recent turns
//	POST /actions/check               policy gate → free | capped | owner ("use": count a capped use (D2), consume the owner's grant, remember a block)
//	GET  /agents/{id}/blocked?since=  actions the gate blocked for a hireling (to attach to its card)
//	POST /actions/pending             a decision card that grants those actions if the owner says yes
//	POST /actions/answer              the owner's choice on that card → the actions granted (one use each), only after the Front Desk confirms the owner's answer
//	                                  action "web_call" (D8): counted against the profile's web_calls_per_day → free | budget
//	                                  action "tokens" (D7, with turn_tokens): today's tokens + the running turn's against tokens_per_day → free | budget
//	GET  /autonomy?day=&agent=&limit= the Autonomy Log (D4): who, what, why, outcome, turn cost
//	POST /asks                        D11: before a card opens — yours (send it back) | joined (open card on the task) | ask
//	POST /asks/opened                 the card the runtime opened (one per task)
//	POST /asks/answer                 the owner's choice → the card's follow-ups and whether they picked the recommendation (owner bearer, same owner token check (ownerOnly); not Approvals.Confirm)
//	GET  /asks?agent=                 open cards and per-hireling counters ("could have decided alone")
//	GET  /tasks · POST /tasks
//	POST /tasks/{id}/lease · /renew · /handoff · /finish
//	POST /tasks/{id}/approve · /deny    B6: the Innkeeper decides a requested handoff
//
// With the owner token (Authorization: Bearer, the Front Desk's owner token):
//
//	GET  /api/tasks                   {"tasks": [...]} the task board
//	GET  /api/tasks/{id}              {"task": {...}} one task with its history
//	GET  /api/quest                   {"quest": {...}} the shared quest (404 until set)
//	GET/POST /tasks and /tasks/{id}/* the task board the hirelings use
//	PUT  /goal                        define the quest
//	POST /actions/check|pending|answer
//	POST /asks/answer                 close a D11 card (ownerOnly only; not Approvals.Confirm)
//
// The hirelings send that same bearer from FRONTDESK_OWNER_TOKEN. They still
// identify themselves with X-Hireling. POST /goal/progress, POST /asks,
// POST /asks/opened and POST /agents/{id}/usage take that bearer too.
// GET /goal, GET /asks, GET /agents/*, GET /autonomy and GET usage stay open
// on the Compose network: they do not mutate, and the port is 127.0.0.1.
// X-Hireling: owner is case-sensitive (exactly "owner").
// POST /asks/answer takes the choice from the JSON body. Only the owner
// bearer may send it. It does not call Approvals.Confirm (that proves a
// gate grant, which this card is not). The Front Desk (/api/*,
// internal/frontdesk) is mounted on the same server and carries its own
// bearer tokens.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/services/tavern-core/internal/asks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/autonomy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

type Server struct {
	Gate  *policy.Gate
	Tasks *tasks.Service
	Usage usage.Store
	Goal  *goal.Service // C1: the shared quest (nil = feature off)
	// Grants holds the owner's approvals for blocked actions (D1/D3; nil = off).
	Grants policy.Grants
	// Log is the Autonomy Log (D4; nil = off): every "use" check is written.
	Log autonomy.Store
	// Asks is the book of questions to the owner (D11; nil = off: every ask opens a card).
	Asks asks.Store
	// Approvals proves the owner's answer against the Front Desk before a
	// grant (nil = off: /actions/answer refuses every grant, fail closed).
	Approvals Approver
	Ready     func() error     // store ping
	Now       func() time.Time // defaults to time.Now
	// Extra registers more routes on the same mux (the Front Desk).
	Extra func(*http.ServeMux)
	// ClientDir is the self-host game (index.html and assets) served at
	// /tavern/. When it holds an index.html, GET / redirects there and the
	// owner panel lives at /panel/. Empty (or no index.html) means /tavern/
	// answers 404, GET / is the panel, and the panel states how to build the
	// client. Compose mounts the data-only image's copy at /tavern/client.
	ClientDir string
	// Owner reports whether a request carries the owner token (the Front
	// Desk's IsOwner). It guards GET /api/tasks, /api/tasks/{id},
	// /api/quest, every /tasks route, PUT /goal and every /actions/* route.
	// nil = those routes answer 401 to everyone.
	Owner func(*http.Request) bool
}

// Approver ties a gate card to the owner's decision and proves the answer
// (D1/D3). The Front Desk implements it.
//   - Bind attaches the card's actions to an open decision deskID opened,
//     once, before the owner answers; it states on the decision what a yes
//     grants and returns the option that grants.
//   - Confirm returns the option the owner chose and the one that grants,
//     once the decision is answered.
//
// An unknown, open or foreign decision, or one without a card, is
// ErrNotApproved; a card for an answered decision is ErrDecided, a second
// card ErrBound.
type Approver interface {
	Bind(ctx context.Context, decision, agent, deskID string, actions []string) (approve string, err error)
	Confirm(ctx context.Context, decision, agent, deskID string) (chosen, approve string, err error)
}

// ErrNotApproved is what an Approver returns (wrapped) when the decision is
// not one the owner answered for this hireling: the handler answers 403.
var ErrNotApproved = frontdesk.ErrNotApproved

// ErrApprovalsOff is the 503 when no Approver is set: the gate stays closed,
// it never falls back to trusting the body.
var ErrApprovalsOff = errors.New("approvals: no Front Desk to confirm the answer, so nothing is granted")

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.root) // OS5: the tavern when a client is mounted, else the owner panel
	mux.HandleFunc("GET /panel", s.panelSlash)
	mux.HandleFunc("GET /panel/", s.panel) // OS5: the owner panel (not the pixel-art tavern)
	mux.HandleFunc("GET /client.css", s.clientCSS)
	mux.HandleFunc("GET /client.js", s.clientJS)
	mux.HandleFunc("GET /tavern", s.tavernSlash)
	mux.HandleFunc("GET /tavern/", s.serveTavern)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /agents", s.agents)
	mux.HandleFunc("GET /agents/{id}/budget", s.budget)
	mux.HandleFunc("POST /agents/{id}/usage", s.ownerOnly(s.addUsage))
	mux.HandleFunc("GET /agents/{id}/usage", s.getUsage)
	mux.HandleFunc("POST /actions/check", s.ownerOnly(s.check))
	mux.HandleFunc("GET /agents/{id}/blocked", s.blocked)
	mux.HandleFunc("POST /actions/pending", s.ownerOnly(s.pending))
	mux.HandleFunc("POST /actions/answer", s.ownerOnly(s.answer))
	mux.HandleFunc("GET /autonomy", s.autonomyLog)
	mux.HandleFunc("POST /asks", s.ownerOnly(s.preAsk))
	mux.HandleFunc("POST /asks/opened", s.ownerOnly(s.askOpened))
	mux.HandleFunc("POST /asks/answer", s.ownerOnly(s.askAnswer))
	mux.HandleFunc("GET /asks", s.listAsks)
	mux.HandleFunc("GET /goal", s.getGoal)
	mux.HandleFunc("PUT /goal", s.ownerOnly(s.setGoal))
	mux.HandleFunc("POST /goal/progress", s.ownerOnly(s.reportGoal))
	mux.HandleFunc("GET /tasks", s.ownerOnly(s.listTasks))
	mux.HandleFunc("GET /tasks/{id}", s.ownerOnly(s.getTask))
	mux.HandleFunc("GET /agents/{id}/queue", s.queue)
	mux.HandleFunc("POST /tasks", s.ownerOnly(s.createTask))
	mux.HandleFunc("POST /tasks/{id}/lease", s.ownerOnly(s.lease))
	mux.HandleFunc("POST /tasks/{id}/renew", s.ownerOnly(s.renew))
	mux.HandleFunc("POST /tasks/{id}/handoff", s.ownerOnly(s.handoff))
	mux.HandleFunc("POST /tasks/{id}/finish", s.ownerOnly(s.finish))
	mux.HandleFunc("POST /tasks/{id}/release", s.ownerOnly(s.release))
	mux.HandleFunc("POST /tasks/{id}/fail", s.ownerOnly(s.failTurn))
	mux.HandleFunc("POST /tasks/{id}/unblock", s.ownerOnly(s.unblock))
	mux.HandleFunc("POST /tasks/{id}/approve", s.ownerOnly(s.approve))
	mux.HandleFunc("POST /tasks/{id}/deny", s.ownerOnly(s.deny))
	// The owner's read-only board and quest under /api/*, with the same token.
	mux.HandleFunc("GET /api/tasks", s.ownerOnly(s.ownerTasks))
	mux.HandleFunc("GET /api/tasks/{id}", s.ownerOnly(s.ownerTask))
	mux.HandleFunc("GET /api/quest", s.ownerOnly(s.ownerQuest))
	if s.Extra != nil {
		s.Extra(mux)
	}
	// HTMLSecurity is the one CSP wrapper. Any HTML handler on this mux,
	// including a future /tavern/, gets the same policy.
	return HTMLSecurity(jsonOnly(mux))
}

// jsonOnly refuses a write to the internal routes (everything outside /api/*)
// whose body is not declared JSON: 415. A web page the owner opens can reach
// 127.0.0.1 and send a "simple" cross-site POST (text/plain, no preflight);
// it cannot send application/json without a CORS preflight, which this
// server never answers. The runtimes, the gate hook and bin/tavern always
// send application/json. /tasks, PUT /goal, /actions/* and the Front Desk
// (/api/*) also need the owner bearer, which a cross-site request cannot
// carry either.
func jsonOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !strings.HasPrefix(r.URL.Path, "/api/") {
				mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mt != "application/json" {
					writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	code := http.StatusBadRequest
	switch {
	case errors.Is(err, tasks.ErrNotFound):
		code = http.StatusNotFound
	case errors.Is(err, tasks.ErrNotOwner), errors.Is(err, tasks.ErrFinished), errors.Is(err, tasks.ErrNotLeased), errors.Is(err, tasks.ErrNotPending):
		code = http.StatusConflict
	case errors.Is(err, tasks.ErrNotApprover), errors.Is(err, goal.ErrForbidden), errors.Is(err, goal.ErrNotSource):
		code = http.StatusForbidden
	case errors.Is(err, goal.ErrNotSet):
		code = http.StatusNotFound
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// agent is who is calling: the body's field when present, else X-Hireling.
func agent(r *http.Request, body string) string {
	if body != "" {
		return body
	}
	return strings.TrimSpace(r.Header.Get("X-Hireling"))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	if s.Ready != nil {
		if err := s.Ready(); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "store unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "agents": s.Gate.Agents()})
}

func (s *Server) agents(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Gate.Agents())
}

func (s *Server) budget(w http.ResponseWriter, r *http.Request) {
	p := s.Gate.Profile(r.PathValue("id"))
	if p == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
		return
	}
	day, err := s.today(r, p.ID)
	if err != nil {
		fail(w, err)
		return
	}
	used := day.Totals.Tokens()
	now := s.now().UTC()
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":             p.ID,
		"model":             p.Model,
		"day":               day.Day,
		"tokens_per_day":    p.Budget.TokensPerDay,
		"web_calls_per_day": p.Budget.WebCallsPerDay,
		"tokens_used":       used, // today, UTC
		// D7: at or over tokens_per_day the runtime holds P2/P3 work until
		// the UTC day turns; 0 in the profile means no token budget.
		"over_budget":    p.Budget.TokensPerDay > 0 && used >= int64(p.Budget.TokensPerDay),
		"resets_at":      time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		"cost_usd":       day.Totals.CostUSD,
		"turns":          day.Turns,
		"web_calls_used": s.webUsed(r, p.ID), // D8
	})
}

func (s *Server) today(r *http.Request, agent string) (usage.Day, error) {
	if s.Usage == nil {
		return usage.Day{Agent: agent, Day: usage.DayKey(s.now())}, nil
	}
	return s.Usage.Day(r.Context(), agent, usage.DayKey(s.now()))
}

// addUsage records one turn. The agent in the path must be a loaded profile
// and match the body (or the X-Hireling header when the body has none).
func (s *Server) addUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Gate.Profile(id) == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
		return
	}
	// Lenient decode: the runtime's record carries fields this side does not
	// keep (`totals.model`, future additions) and must never be refused for them.
	var t usage.Turn
	if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(&t); err != nil {
		fail(w, err)
		return
	}
	t.Agent = agent(r, t.Agent)
	if t.Agent != id {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "agent mismatch"})
		return
	}
	if err := usage.Validate(&t, s.now()); err != nil {
		fail(w, err)
		return
	}
	if s.Usage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "usage store not configured"})
		return
	}
	if err := s.Usage.Add(r.Context(), t); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Answer with the day the turn counts for (its own date, not the server's).
	day, err := s.Usage.Day(r.Context(), id, usage.DayKey(t.At))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, day)
}

func (s *Server) getUsage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Gate.Profile(id) == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
		return
	}
	day, err := s.today(r, id)
	if err != nil {
		fail(w, err)
		return
	}
	var recent []usage.Turn
	if s.Usage != nil {
		if recent, err = s.Usage.Recent(r.Context(), id, 20); err != nil {
			fail(w, err)
			return
		}
	}
	if recent == nil {
		recent = []usage.Turn{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"today": day, "recent": recent})
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var req policy.Request
	if err := decode(r, &req); err != nil {
		fail(w, err)
		return
	}
	req.Agent = agent(r, req.Agent)
	action := strings.ToLower(strings.TrimSpace(req.Action))
	switch action {
	case WebCall:
		s.webCall(w, r, req)
		return
	case Tokens:
		s.tokens(w, r, req)
		return
	}
	d := s.Gate.Check(req)
	// D2: a capped action counts against its window; past the cap it is the owner's.
	if d.Level == policy.Capped && strings.HasSuffix(action, "_usd") {
		// A cap in dollars needs the amount of each spend, which the hook
		// cannot read from a tool call: every spend is the owner's until it can.
		d = policy.Decision{Level: policy.Owner, Reason: fmt.Sprintf("spend cap in USD (%d/%s): the amount is not known yet, so the owner decides", d.Limit, d.Period), Limit: d.Limit, Period: d.Period}
	}
	if req.Use && s.Grants != nil && d.Level == policy.Capped {
		window := policy.PeriodKey(d.Period, s.now())
		ttl := 26 * time.Hour
		if d.Period == "week" {
			ttl = 8 * 24 * time.Hour
		}
		used, ok, err := s.Grants.Use(r.Context(), req.Agent, action, window, d.Limit, ttl)
		if err != nil {
			fail(w, err)
			return
		}
		if ok {
			d.Used, d.Counted = used, true
		} else {
			d = policy.Decision{Level: policy.Owner, Reason: fmt.Sprintf("cap reached (D2): %d/%d this %s", used, d.Limit, d.Period), Limit: d.Limit, Period: d.Period, Used: used}
		}
	}
	if req.Use && s.Grants != nil && d.Level == policy.Owner {
		ok, err := s.Grants.Consume(r.Context(), req.Agent, action)
		switch {
		case err != nil:
			fail(w, err)
			return
		case ok:
			d = policy.Decision{Level: policy.Free, Reason: "the owner approved this once (" + string(d.Level) + ": " + d.Reason + ")"}
		default:
			if err := s.Grants.Blocked(r.Context(), req.Agent, policy.Act{Action: action, Target: req.Target, At: s.now()}); err != nil {
				fail(w, err)
				return
			}
		}
	}
	if req.Use && s.Log != nil {
		outcome := "blocked"
		if d.Level == policy.Free || (d.Level == policy.Capped && d.Counted) {
			outcome = "done"
		}
		e := autonomy.Entry{At: s.now(), Agent: req.Agent, Action: action, Target: req.Target, Outcome: outcome,
			Level: string(d.Level), Reason: d.Reason, Used: d.Used, Limit: d.Limit, Period: d.Period,
			Why: req.Why, Turn: req.Turn, Task: req.Task}
		if d.Level != policy.Capped && !strings.Contains(d.Reason, "cap reached") {
			e.Used, e.Limit, e.Period = 0, 0, ""
		}
		// Fail closed: an action that cannot be logged does not run (D4).
		if err := s.Log.Append(r.Context(), e); err != nil {
			fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, d)
}

// autonomyLog lists one UTC day of the Autonomy Log (default today), newest
// first, with the cost of the turn each action happened in.
func (s *Server) autonomyLog(w http.ResponseWriter, r *http.Request) {
	if s.Log == nil {
		writeJSON(w, http.StatusOK, map[string]any{"day": autonomy.Day(s.now()), "entries": []autonomy.Entry{}})
		return
	}
	q := r.URL.Query()
	day := q.Get("day")
	if day == "" {
		day = autonomy.Day(s.now())
	} else if _, err := time.Parse("2006-01-02", day); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "day must be YYYY-MM-DD"})
		return
	}
	limit := 500
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "limit must be a positive number"})
			return
		}
		limit = min(n, autonomy.Keep)
	}
	entries, err := s.Log.List(r.Context(), day, q.Get("agent"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	if entries == nil {
		entries = []autonomy.Entry{}
	}
	// Cost: the usage record of the same turn (A2 keys turns by command id).
	if s.Usage != nil {
		turns := map[string]map[string]usage.Totals{}
		for i, e := range entries {
			if e.Turn == "" {
				continue
			}
			byID, ok := turns[e.Agent]
			if !ok {
				byID = map[string]usage.Totals{}
				if recent, err := s.Usage.Recent(r.Context(), e.Agent, 200); err == nil {
					for _, t := range recent {
						byID[t.CommandID] = t.Totals
					}
				}
				turns[e.Agent] = byID
			}
			if t, ok := byID[e.Turn]; ok {
				c := t.CostUSD
				entries[i].TurnCostUSD, entries[i].TurnTokens = &c, t.Tokens()
			}
		}
	}
	done, blocked := 0, 0
	for _, e := range entries {
		if e.Outcome == "done" {
			done++
		} else {
			blocked++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"day": day, "done": done, "blocked": blocked, "entries": entries})
}

func (s *Server) blocked(w http.ResponseWriter, r *http.Request) {
	if s.Grants == nil {
		writeJSON(w, http.StatusOK, []policy.Act{})
		return
	}
	since := s.now().Add(-policy.BlockedTTL)
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "since must be RFC 3339"})
			return
		}
		since = t
	}
	acts, err := s.Grants.RecentBlocked(r.Context(), r.PathValue("id"), since)
	if err != nil {
		fail(w, err)
		return
	}
	if acts == nil {
		acts = []policy.Act{}
	}
	writeJSON(w, http.StatusOK, acts)
}

func (s *Server) pending(w http.ResponseWriter, r *http.Request) {
	var p policy.Pending
	if err := decode(r, &p); err != nil {
		fail(w, err)
		return
	}
	p.Agent = agent(r, p.Agent)
	if p.Decision == "" || p.Agent == "" || len(p.Actions) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "decision, agent and actions are required"})
		return
	}
	if s.Grants == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "grants are off"})
		return
	}
	if s.Approvals == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrApprovalsOff.Error()})
		return
	}
	prof := s.Gate.Profile(p.Agent)
	if prof == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "unknown hireling: " + p.Agent})
		return
	}
	// The card is tied to the decision now, while it is open: an answered
	// decision takes no new card (an old yes is not reused), a decision takes
	// one card (its actions are not swapped while the owner reads it), and the
	// option that grants comes from the decision, not from the hireling.
	names := make([]string, 0, len(p.Actions))
	for _, a := range p.Actions {
		names = append(names, a.Action)
	}
	approve, err := s.Approvals.Bind(r.Context(), p.Decision, p.Agent, prof.DeskID, names)
	if err != nil {
		code := http.StatusServiceUnavailable
		switch {
		case errors.Is(err, ErrNotApproved):
			code = http.StatusForbidden
		case errors.Is(err, frontdesk.ErrDecided), errors.Is(err, frontdesk.ErrBound):
			code = http.StatusConflict
		case errors.Is(err, frontdesk.ErrCardTooLong), errors.Is(err, frontdesk.ErrNotAffirmative), errors.Is(err, frontdesk.ErrBadAction):
			code = http.StatusBadRequest
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	p.Approve = approve
	if err := s.Grants.Pend(r.Context(), p); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision string `json:"decision"`
		Choice   string `json:"choice"`
		Agent    string `json:"agent"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	agent := strings.TrimSpace(in.Agent)
	if agent == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "body must be {decision, choice, agent}"})
		return
	}
	if s.Approvals == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": ErrApprovalsOff.Error()})
		return
	}
	p := s.Gate.Profile(agent)
	if p == nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "unknown hireling: " + agent})
		return
	}
	// The body proves nothing: a hireling can reach tavern-core, but only the
	// owner token can answer on the Front Desk. The grant follows the answer
	// the desk recorded, never the choice in the body: only the option that
	// grants (the decision's first) is a yes; any other answer uses up the
	// card and grants nothing.
	chosen, approve, err := s.Approvals.Confirm(r.Context(), in.Decision, agent, p.DeskID)
	if err != nil {
		code := http.StatusServiceUnavailable
		if errors.Is(err, ErrNotApproved) {
			code = http.StatusForbidden
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	granted := []policy.Act{}
	if s.Grants != nil {
		yes := ""
		if strings.EqualFold(strings.TrimSpace(chosen), strings.TrimSpace(approve)) {
			yes = approve
		}
		acts, err := policy.AnswerFor(r.Context(), s.Grants, in.Decision, agent, yes)
		if errors.Is(err, policy.ErrOtherAgent) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
			return
		}
		if err != nil && !errors.Is(err, policy.ErrNoPending) {
			fail(w, err)
			return
		}
		granted = append(granted, acts...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"granted": granted})
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Tasks.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if ts == nil {
		ts = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, ts)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in tasks.CreateInput
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	in.RequestedBy = agent(r, in.RequestedBy)
	t, err := s.Tasks.Create(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

type agentBody struct {
	Agent string `json:"agent,omitempty"`
}

func (s *Server) lease(w http.ResponseWriter, r *http.Request) {
	var b agentBody
	_ = decode(r, &b)
	t, err := s.Tasks.Lease(r.Context(), r.PathValue("id"), agent(r, b.Agent))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) renew(w http.ResponseWriter, r *http.Request) {
	var b agentBody
	_ = decode(r, &b)
	t, err := s.Tasks.Renew(r.Context(), r.PathValue("id"), agent(r, b.Agent))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent string `json:"agent,omitempty"`
		To    string `json:"to"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := s.Tasks.Handoff(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.To)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent  string       `json:"agent,omitempty"`
		Status tasks.Status `json:"status"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := s.Tasks.Finish(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.Status)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

type decisionBody struct {
	Agent string `json:"agent,omitempty"`
	Note  string `json:"note,omitempty"`
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) { s.decide(w, r, true) }
func (s *Server) deny(w http.ResponseWriter, r *http.Request)    { s.decide(w, r, false) }

func (s *Server) decide(w http.ResponseWriter, r *http.Request, approve bool) {
	var b decisionBody
	_ = decode(r, &b)
	t, err := s.Tasks.Decide(r.Context(), r.PathValue("id"), agent(r, b.Agent), approve, b.Note)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- Quest (C1) ------------------------------------------------------------

// questView is GET /goal: the record plus the percentage for the bar.
type questView struct {
	*goal.Goal
	Percent float64  `json:"percent"`
	Bar     string   `json:"bar"`
	Kinds   []string `json:"kinds"`
	// C3: declared contributions by kind — open tasks and done tasks.
	ExpectedOpen map[string]int `json:"expected_open"`
	ExpectedDone map[string]int `json:"expected_done"`
}

// QuestSnapshot is the object GET /api/status puts in "quest", or nil when
// none is set. The owner panel reads it from that 200 response. GET /api/quest
// stays 404 until a quest exists.
func (s *Server) QuestSnapshot(ctx context.Context) any {
	if s == nil || s.Goal == nil {
		return nil
	}
	g, err := s.Goal.Get(ctx)
	if err != nil || g == nil || g.Title == "" {
		return nil
	}
	if s.Tasks == nil || s.Tasks.Store == nil {
		return questView{Goal: g, Percent: g.Percent(), Bar: g.Bar(), Kinds: g.AllowedKinds(), ExpectedOpen: map[string]int{}, ExpectedDone: map[string]int{}}
	}
	return s.quest(ctx, g)
}

func (s *Server) quest(ctx context.Context, g *goal.Goal) questView {
	v := questView{Goal: g, Percent: g.Percent(), Bar: g.Bar(), Kinds: g.AllowedKinds(), ExpectedOpen: map[string]int{}, ExpectedDone: map[string]int{}}
	if all, err := s.Tasks.Store.List(ctx); err == nil {
		v.ExpectedOpen, v.ExpectedDone = tasks.Expected(all)
	}
	return v
}

func (s *Server) getGoal(w http.ResponseWriter, r *http.Request) {
	if s.Goal == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quest not enabled"})
		return
	}
	g, err := s.Goal.Get(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.quest(r.Context(), g))
}

// setGoal defines the quest (PUT /goal): the owner or a planner, by X-Hireling.
func (s *Server) setGoal(w http.ResponseWriter, r *http.Request) {
	if s.Goal == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quest not enabled"})
		return
	}
	var d goal.Define
	if err := decode(r, &d); err != nil {
		fail(w, err)
		return
	}
	g, err := s.Goal.Set(r.Context(), agent(r, ""), d)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.quest(r.Context(), g))
}

// reportGoal moves the bar (POST /goal/progress {current}): only the goal's
// source (the quest's source), by X-Hireling or the body's `from`.
func (s *Server) reportGoal(w http.ResponseWriter, r *http.Request) {
	if s.Goal == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quest not enabled"})
		return
	}
	var in struct {
		From    string   `json:"from"`
		Current *float64 `json:"current"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	if in.Current == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "current is required"})
		return
	}
	g, err := s.Goal.Report(r.Context(), agent(r, in.From), *in.Current)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.quest(r.Context(), g))
}

// getTask is GET /tasks/{id}.
func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ownerOnly answers 401 unless the request carries the owner token.
// The token is the Authorization bearer. A query parameter is never read.
func (s *Server) ownerOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Owner == nil || !s.Owner(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// ownerTasks is GET /api/tasks: the board, as GET /tasks, in an object.
func (s *Server) ownerTasks(w http.ResponseWriter, r *http.Request) {
	ts, err := s.Tasks.List(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	if ts == nil {
		ts = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": ts})
}

// ownerTask is GET /api/tasks/{id}.
func (s *Server) ownerTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.Tasks.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": t})
}

// ownerQuest is GET /api/quest: the quest as GET /goal shows it.
func (s *Server) ownerQuest(w http.ResponseWriter, r *http.Request) {
	if s.Goal == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "quest not enabled"})
		return
	}
	g, err := s.Goal.Get(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"quest": s.quest(r.Context(), g)})
}

// queue is GET /agents/{id}/queue (B3): the agent's queued tasks in the order
// to take them — P0 > P1 > P2 > P3, ties by goal contribution, then oldest.
func (s *Server) queue(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.Gate.Profile(id) == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown agent"})
		return
	}
	ts, err := s.Tasks.Queue(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if ts == nil {
		ts = []*tasks.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent": id, "tasks": ts})
}

// release is POST /tasks/{id}/release {status: queued|waiting_owner, progress} (B4/B5).
func (s *Server) release(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent    string       `json:"agent,omitempty"`
		Status   tasks.Status `json:"status"`
		Progress string       `json:"progress,omitempty"`
		Reason   string       `json:"reason,omitempty"` // "interrupt" (B5): back to the queue, `interrupted` event
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	var t *tasks.Task
	var err error
	if b.Reason == "interrupt" {
		t, err = s.Tasks.Interrupt(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.Progress)
	} else {
		t, err = s.Tasks.Release(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.Status, b.Progress)
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// failTurn is POST /tasks/{id}/fail {error, max} (B11): one failed turn by the
// holder; back to the queue, or blocked needs_human at the threshold.
func (s *Server) failTurn(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent string `json:"agent,omitempty"`
		Error string `json:"error,omitempty"`
		Max   int    `json:"max,omitempty"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := s.Tasks.Fail(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.Error, b.Max)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// unblock is POST /tasks/{id}/unblock {action} (B11): the owner answered the
// escalation; drop cancels, anything else re-queues with the count reset.
func (s *Server) unblock(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Agent  string `json:"agent,omitempty"`
		Action string `json:"action"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := s.Tasks.Unblock(r.Context(), r.PathValue("id"), agent(r, b.Agent), b.Action)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// ---- D11: autonomy first ----------------------------------------------------

// AskVerdict is the answer of POST /asks.
type AskVerdict struct {
	Verdict   string   `json:"verdict"` // yours | joined | ask
	Level     string   `json:"level,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Decision  string   `json:"decision,omitempty"` // joined: the open card
	FollowUps []string `json:"follow_ups,omitempty"`
}

// preAsk decides what happens to an `ask` block before a card is opened:
//   - the task already has an open card → the question joins it (one card per task);
//   - the named action is Free, or Capped with uses left in its window, and
//     the gate did not block anything in that turn → it goes back to the
//     hireling ("this one is yours"), once per task (or turn) and action;
//   - anything else → ask: the runtime opens the card.
func (s *Server) preAsk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Agent    string `json:"agent"`
		Task     string `json:"task"`
		Turn     string `json:"turn"`
		Action   string `json:"action"`
		Question string `json:"question"`
		Blocked  bool   `json:"blocked"` // the gate blocked an action in this turn
	}
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	in.Agent = agent(r, in.Agent)
	if in.Agent == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent is required"})
		return
	}
	if s.Asks == nil {
		writeJSON(w, http.StatusOK, AskVerdict{Verdict: "ask"})
		return
	}
	ctx := r.Context()
	if in.Task != "" {
		o, err := s.Asks.Join(ctx, in.Agent, in.Task, in.Question)
		if err != nil {
			fail(w, err)
			return
		}
		if o != nil {
			writeJSON(w, http.StatusOK, AskVerdict{Verdict: "joined", Decision: o.Decision, FollowUps: o.FollowUps,
				Reason: "task " + in.Task + " already has a card waiting on the owner"})
			return
		}
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action == "" || in.Blocked {
		writeJSON(w, http.StatusOK, AskVerdict{Verdict: "ask"})
		return
	}
	d := s.Gate.Check(policy.Request{Agent: in.Agent, Action: action})
	yours, reason := false, d.Reason
	switch {
	case d.Level == policy.Free:
		yours = true
	case d.Level == policy.Capped && !strings.HasSuffix(action, "_usd") && s.Grants != nil && d.Limit > 0:
		used, err := s.Grants.Count(ctx, in.Agent, action, policy.PeriodKey(d.Period, s.now()))
		if err != nil {
			fail(w, err)
			return
		}
		if used < d.Limit {
			yours, reason = true, fmt.Sprintf("within the cap: %d/%d used this %s", used, d.Limit, d.Period)
		}
	}
	if !yours {
		writeJSON(w, http.StatusOK, AskVerdict{Verdict: "ask", Level: string(d.Level), Reason: d.Reason})
		return
	}
	key := in.Task
	if key == "" {
		key = in.Turn
	}
	if key == "" {
		key = "-"
	}
	first, err := s.Asks.Bounce(ctx, in.Agent, key, action)
	if err != nil {
		fail(w, err)
		return
	}
	if !first {
		// Asked again after being sent back: the owner sees it, and the card says so.
		writeJSON(w, http.StatusOK, AskVerdict{Verdict: "ask", Level: string(d.Level), Reason: "asked again after being told it is the hireling's own (" + reason + ")"})
		return
	}
	writeJSON(w, http.StatusOK, AskVerdict{Verdict: "yours", Level: string(d.Level), Reason: reason})
}

func (s *Server) askOpened(w http.ResponseWriter, r *http.Request) {
	var o asks.Open
	if err := decode(r, &o); err != nil {
		fail(w, err)
		return
	}
	o.Agent = agent(r, o.Agent)
	if o.Agent == "" || o.Decision == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent and decision are required"})
		return
	}
	o.Action = strings.ToLower(strings.TrimSpace(o.Action))
	o.At = s.now()
	if s.Asks != nil {
		if err := s.Asks.Opened(r.Context(), o); err != nil {
			fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, o)
}

// askAnswer is POST /asks/answer. ownerOnly runs first: the same owner token
// check (ownerOnly) as the other owner routes, a constant-time compare of the
// Authorization bearer. The choice is the JSON body's `choice`; only the
// owner may send it. It does not call Approvals.Confirm: that interface
// proves a gate grant, and this card is the owner's answer in the body.
// A query token is never read. An empty owner token does not match.
func (s *Server) askAnswer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision string `json:"decision"`
		Choice   string `json:"choice"`
	}
	if err := decode(r, &in); err != nil {
		fail(w, err)
		return
	}
	if s.Asks == nil || in.Decision == "" {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	a, err := s.Asks.Answer(r.Context(), in.Decision, in.Choice)
	if errors.Is(err, asks.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"found": false})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": true, "card": a})
}

func (s *Server) listAsks(w http.ResponseWriter, r *http.Request) {
	if s.Asks == nil {
		writeJSON(w, http.StatusOK, map[string]any{"open": []asks.Open{}, "stats": []asks.Stats{}})
		return
	}
	open, stats, err := s.Asks.List(r.Context(), r.URL.Query().Get("agent"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"open": open, "stats": stats})
}

// ---- D8: web-call budget ----------------------------------------------------

// WebCall is the action the gate hook sends for Hermes' web_search and
// web_extract: a read of the web, counted per UTC day against the profile's
// budget.web_calls_per_day (0 = no budget).
const WebCall = "web_call"

// webCall answers a web_call check. Within the day's budget it is Free (and,
// with "use", counted); past it the level is "budget": blocked until the UTC
// day turns, and written to the Autonomy Log. Calls within the budget are
// reads and stay out of the log, like every read.
func (s *Server) webCall(w http.ResponseWriter, r *http.Request, req policy.Request) {
	p := s.Gate.Profile(req.Agent)
	if p == nil {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Owner, Reason: "unknown agent"})
		return
	}
	limit := p.Budget.WebCallsPerDay
	if limit <= 0 || s.Grants == nil {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Free, Reason: "no web-call budget in the profile"})
		return
	}
	ctx := r.Context()
	day := usage.DayKey(s.now())
	var used int
	var ok bool
	var err error
	if req.Use {
		used, ok, err = s.Grants.Use(ctx, req.Agent, WebCall, day, limit, 26*time.Hour)
	} else {
		used, err = s.Grants.Count(ctx, req.Agent, WebCall, day)
		ok = used < limit
	}
	if err != nil {
		fail(w, err)
		return
	}
	if ok {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Free, Reason: fmt.Sprintf("web call %d/%d today (D8)", used, limit),
			Limit: limit, Period: "day", Used: used, Counted: req.Use})
		return
	}
	d := policy.Decision{Level: policy.Budget, Reason: fmt.Sprintf("daily web-call budget reached (D8): %d/%d, resets at 00:00 UTC", used, limit),
		Limit: limit, Period: "day", Used: used}
	if req.Use && s.Log != nil {
		e := autonomy.Entry{At: s.now(), Agent: req.Agent, Action: WebCall, Target: req.Target, Outcome: "blocked",
			Level: string(d.Level), Reason: d.Reason, Used: used, Limit: limit, Period: "day", Why: req.Why, Turn: req.Turn, Task: req.Task}
		if err := s.Log.Append(ctx, e); err != nil {
			fail(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) webUsed(r *http.Request, agent string) int {
	if s.Grants == nil {
		return 0
	}
	n, err := s.Grants.Count(r.Context(), agent, WebCall, usage.DayKey(s.now()))
	if err != nil {
		return 0
	}
	return n
}

// ---- D7: token budget inside a turn ------------------------------------------

// Tokens is the action the gate hook checks before every tool call of a P2/P3
// turn (the runtime does not ask for P0/P1 work, which runs over budget). The
// usage record of a turn only arrives when it ends, so the hook sends the
// running turn's spend as `turn_tokens`.
const Tokens = "tokens"

// tokens answers a D7 check: today's recorded tokens plus the running turn's
// against the profile's tokens_per_day (0 = no budget). Under it the call is
// Free and stays out of the Autonomy Log, like a web call within its budget;
// at or over it the level is "budget" and the refusal is logged. A refusal
// that cannot be logged is still a refusal: nothing runs either way.
func (s *Server) tokens(w http.ResponseWriter, r *http.Request, req policy.Request) {
	p := s.Gate.Profile(req.Agent)
	if p == nil {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Owner, Reason: "unknown agent"})
		return
	}
	limit := p.Budget.TokensPerDay
	if limit <= 0 {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Free, Reason: "no token budget in the profile"})
		return
	}
	day, err := s.today(r, p.ID)
	if err != nil {
		fail(w, err)
		return
	}
	turn := max(req.TurnTokens, 0)
	used := day.Totals.Tokens() + turn
	if used < int64(limit) {
		writeJSON(w, http.StatusOK, policy.Decision{Level: policy.Free, Reason: fmt.Sprintf("tokens %d/%d today (D7)", used, limit),
			Limit: limit, Period: "day", Used: int(used)})
		return
	}
	d := policy.Decision{Level: policy.Budget, Limit: limit, Period: "day", Used: int(used),
		Reason: fmt.Sprintf("daily token budget reached (D7): %d/%d tokens today, %d of them in this turn; resets at 00:00 UTC", used, limit, turn)}
	if req.Use && s.Log != nil {
		e := autonomy.Entry{At: s.now(), Agent: req.Agent, Action: Tokens, Target: req.Target, Outcome: "blocked",
			Level: string(d.Level), Reason: d.Reason, Used: d.Used, Limit: limit, Period: "day", Why: req.Why, Turn: req.Turn, Task: req.Task}
		_ = s.Log.Append(r.Context(), e)
	}
	writeJSON(w, http.StatusOK, d)
}
