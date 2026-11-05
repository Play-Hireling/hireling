// Package autonomy is the Autonomy Log (D4): every action a hireling took on
// the outside world under its own autonomy (Free, Capped within the cap, or a
// one-time grant from the owner) and every one the gate stopped, with who, what,
// why and the turn it happened in. The gate writes it on each "use" check
// (POST /actions/check, D1); the HTTP layer adds the cost of that turn from
// the usage counters (A2) when it lists the log.
package autonomy

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Entry is one line of the log.
type Entry struct {
	At      time.Time `json:"at"`
	Agent   string    `json:"agent"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	Outcome string    `json:"outcome"` // done | blocked
	Level   string    `json:"level"`   // free | capped | the owner (what the gate answered)
	Reason  string    `json:"reason,omitempty"`
	Used    int       `json:"used,omitempty"`  // capped: uses in the window after this one
	Limit   int       `json:"limit,omitempty"` // capped: the cap
	Period  string    `json:"period,omitempty"`
	Why     string    `json:"why,omitempty"`  // the hireling's stated reason, or the turn's topic
	Turn    string    `json:"turn,omitempty"` // command or message id (joins the usage record)
	Task    string    `json:"task,omitempty"`
	// Filled when listed: the cost of the whole turn the action happened in
	// (one turn can take several actions; the cost is not split).
	TurnCostUSD *float64 `json:"turn_cost_usd,omitempty"`
	TurnTokens  int64    `json:"turn_tokens,omitempty"`
}

// Day names the UTC day an entry belongs to.
func Day(t time.Time) string { return t.UTC().Format("2006-01-02") }

// Store keeps the log per UTC day, newest first.
type Store interface {
	Append(ctx context.Context, e Entry) error
	List(ctx context.Context, day, agent string, limit int) ([]Entry, error)
}

// Keep bounds one day's log; Retention is how long a day is kept.
const (
	Keep      = 10000
	Retention = 90 * 24 * time.Hour
)

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func normalize(e Entry) Entry {
	e.Target, e.Why, e.Reason = clip(e.Target, 200), clip(e.Why, 300), clip(e.Reason, 200)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	e.At = e.At.UTC()
	return e
}

func match(e Entry, agent string) bool { return agent == "" || e.Agent == agent }

// Memory is Store in process memory (tests, REDIS_URL unset).
type Memory struct {
	mu   sync.Mutex
	days map[string][]Entry
}

func NewMemory() *Memory { return &Memory{days: map[string][]Entry{}} }

func (m *Memory) Append(_ context.Context, e Entry) error {
	e = normalize(e)
	m.mu.Lock()
	defer m.mu.Unlock()
	d := Day(e.At)
	l := append([]Entry{e}, m.days[d]...)
	if len(l) > Keep {
		l = l[:Keep]
	}
	m.days[d] = l
	return nil
}

func (m *Memory) List(_ context.Context, day, agent string, limit int) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.days[day] {
		if match(e, agent) {
			out = append(out, e)
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// Redis keeps `tavern:autonomy:<YYYY-MM-DD>`, a JSON list, newest first,
// trimmed to Keep and expired after Retention.
type Redis struct {
	C      *redis.Client
	Prefix string // default "tavern"
}

func (r *Redis) key(day string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":autonomy:" + day
}

func (r *Redis) Append(ctx context.Context, e Entry) error {
	e = normalize(e)
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	k := r.key(Day(e.At))
	_, err = r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LPush(ctx, k, raw)
		p.LTrim(ctx, k, 0, Keep-1)
		p.Expire(ctx, k, Retention)
		return nil
	})
	return err
}

func (r *Redis) List(ctx context.Context, day, agent string, limit int) ([]Entry, error) {
	raws, err := r.C.LRange(ctx, r.key(day), 0, Keep-1).Result()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, s := range raws {
		var e Entry
		if json.Unmarshal([]byte(s), &e) != nil || !match(e, agent) {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}
