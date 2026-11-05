// Package usage keeps what every hireling spent, per turn and per UTC day
// (feature A2, second half; the mana bar of the tavern and the cost panel G7
// read from here). The runtime posts one record per command; tavern-core
// adds it to the day's counters.
package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Turn is the body of POST /agents/{id}/usage, as the runtime sends it
// (runtime/internal/usage.Turn). Unknown fields are kept opaque in Records.
type Turn struct {
	Agent     string            `json:"agent"`
	CommandID string            `json:"command_id"`
	At        time.Time         `json:"at"`
	Sessions  int               `json:"sessions,omitempty"`
	Records   []json.RawMessage `json:"records,omitempty"`
	Totals    Totals            `json:"totals"`
}

// Totals are the counters kept per day.
type Totals struct {
	Calls           int64   `json:"calls"`
	InputTokens     int64   `json:"input_tokens"`
	CacheReadTokens int64   `json:"cache_read_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Priced          bool    `json:"priced"`
}

// Tokens is every token the turn consumed (what counts against tokens_per_day).
func (t Totals) Tokens() int64 {
	return t.InputTokens + t.CacheReadTokens + t.OutputTokens + t.ReasoningTokens
}

// Day is the aggregate for one agent on one UTC day.
type Day struct {
	Agent   string `json:"agent"`
	Day     string `json:"day"` // YYYY-MM-DD, UTC
	Turns   int64  `json:"turns"`
	Totals  Totals `json:"totals"`
	Unknown int64  `json:"unpriced_turns"` // turns whose cost could not be priced (fallback provider)
}

// ErrInvalid is returned for a turn without agent or command id.
var ErrInvalid = errors.New("usage: agent and command_id are required")

// Store persists turns; Memory and Redis implement it.
type Store interface {
	Add(ctx context.Context, t Turn) error
	Day(ctx context.Context, agent, day string) (Day, error)
	Recent(ctx context.Context, agent string, n int) ([]Turn, error)
}

// Validate checks the record and defaults At to now.
func Validate(t *Turn, now time.Time) error {
	if t.Agent == "" || t.CommandID == "" {
		return ErrInvalid
	}
	if t.At.IsZero() {
		t.At = now
	}
	return nil
}

// DayKey is the UTC day a turn counts for.
func DayKey(at time.Time) string { return at.UTC().Format("2006-01-02") }

// Memory is the in-memory store (dev and tests).
type Memory struct {
	mu     sync.Mutex
	days   map[string]*Day
	recent map[string][]Turn
}

func NewMemory() *Memory { return &Memory{days: map[string]*Day{}, recent: map[string][]Turn{}} }

func (m *Memory) Add(_ context.Context, t Turn) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := t.Agent + "|" + DayKey(t.At)
	d, ok := m.days[k]
	if !ok {
		d = &Day{Agent: t.Agent, Day: DayKey(t.At)}
		m.days[k] = d
	}
	add(d, t)
	m.recent[t.Agent] = append([]Turn{t}, m.recent[t.Agent]...)
	if len(m.recent[t.Agent]) > keep {
		m.recent[t.Agent] = m.recent[t.Agent][:keep]
	}
	return nil
}

func (m *Memory) Day(_ context.Context, agent, day string) (Day, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.days[agent+"|"+day]; ok {
		return *d, nil
	}
	return Day{Agent: agent, Day: day}, nil
}

func (m *Memory) Recent(_ context.Context, agent string, n int) ([]Turn, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.recent[agent]
	if n < len(r) {
		r = r[:n]
	}
	return append([]Turn(nil), r...), nil
}

func add(d *Day, t Turn) {
	d.Turns++
	d.Totals.Calls += t.Totals.Calls
	d.Totals.InputTokens += t.Totals.InputTokens
	d.Totals.CacheReadTokens += t.Totals.CacheReadTokens
	d.Totals.OutputTokens += t.Totals.OutputTokens
	d.Totals.ReasoningTokens += t.Totals.ReasoningTokens
	d.Totals.CostUSD += t.Totals.CostUSD
	if !t.Totals.Priced {
		d.Unknown++
	}
	d.Totals.Priced = d.Unknown == 0
}

// keep is how many recent turns are listed per agent.
const keep = 200

// Redis keeps one hash per agent and day (`tavern:usage:<agent>:<day>`, counters
// via HINCRBY/HINCRBYFLOAT, 90-day TTL) and one list of recent turns
// (`tavern:usage:<agent>:turns`, JSON, trimmed to `keep`).
type Redis struct {
	C      *redis.Client
	Prefix string // default "tavern"
}

func (r *Redis) key(parts ...string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	k := p + ":usage"
	for _, s := range parts {
		k += ":" + s
	}
	return k
}

const dayTTL = 90 * 24 * time.Hour

func (r *Redis) Add(ctx context.Context, t Turn) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	day := r.key(t.Agent, DayKey(t.At))
	list := r.key(t.Agent, "turns")
	pipe := r.C.TxPipeline()
	pipe.HIncrBy(ctx, day, "turns", 1)
	pipe.HIncrBy(ctx, day, "calls", t.Totals.Calls)
	pipe.HIncrBy(ctx, day, "input_tokens", t.Totals.InputTokens)
	pipe.HIncrBy(ctx, day, "cache_read_tokens", t.Totals.CacheReadTokens)
	pipe.HIncrBy(ctx, day, "output_tokens", t.Totals.OutputTokens)
	pipe.HIncrBy(ctx, day, "reasoning_tokens", t.Totals.ReasoningTokens)
	pipe.HIncrByFloat(ctx, day, "cost_usd", t.Totals.CostUSD)
	if !t.Totals.Priced {
		pipe.HIncrBy(ctx, day, "unpriced_turns", 1)
	}
	pipe.Expire(ctx, day, dayTTL)
	pipe.LPush(ctx, list, raw)
	pipe.LTrim(ctx, list, 0, keep-1)
	_, err = pipe.Exec(ctx)
	return err
}

func (r *Redis) Day(ctx context.Context, agent, day string) (Day, error) {
	h, err := r.C.HGetAll(ctx, r.key(agent, day)).Result()
	if err != nil {
		return Day{}, err
	}
	d := Day{Agent: agent, Day: day}
	n := func(k string) int64 { v, _ := strconv.ParseInt(h[k], 10, 64); return v }
	d.Turns = n("turns")
	d.Totals = Totals{Calls: n("calls"), InputTokens: n("input_tokens"), CacheReadTokens: n("cache_read_tokens"), OutputTokens: n("output_tokens"), ReasoningTokens: n("reasoning_tokens")}
	d.Totals.CostUSD, _ = strconv.ParseFloat(h["cost_usd"], 64)
	d.Unknown = n("unpriced_turns")
	d.Totals.Priced = d.Unknown == 0
	return d, nil
}

func (r *Redis) Recent(ctx context.Context, agent string, n int) ([]Turn, error) {
	raws, err := r.C.LRange(ctx, r.key(agent, "turns"), 0, int64(n-1)).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Turn, 0, len(raws))
	for _, raw := range raws {
		var t Turn
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			return nil, fmt.Errorf("usage: bad record: %w", err)
		}
		out = append(out, t)
	}
	return out, nil
}
