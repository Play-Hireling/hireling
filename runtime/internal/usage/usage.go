// Package usage records token usage and cost per call (the second half of
// feature A2). Hermes keeps every API call's token counts in its session store
// (`$HERMES_HOME/state.db`, tables `sessions` and `session_model_usage`); a
// `hermes chat -q` turn is one session, plus side sessions such as title
// generation. After each turn the runtime reads the sessions started since the
// turn began, prices them with DeepSeek's public rates and reports the result:
// one line in the container log, one JSON line in `$HERMES_HOME/usage/`, and
// one `POST /agents/{id}/usage` to tavern-core (the mana bar and cost panel).
package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure Go driver: the runtime binary stays static
)

// Record is the usage of one model on one endpoint during one turn.
type Record struct {
	Model           string  `json:"model"`
	Provider        string  `json:"provider,omitempty"` // Hermes billing_provider (custom = DeepSeek, openrouter = fallback)
	BaseURL         string  `json:"base_url,omitempty"`
	Task            string  `json:"task,omitempty"` // "" = the turn itself; "title_generation" etc. for side calls
	Calls           int64   `json:"calls"`
	InputTokens     int64   `json:"input_tokens"`      // prompt tokens computed (cache miss)
	CacheReadTokens int64   `json:"cache_read_tokens"` // prompt tokens served from the prefix cache
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CostUSD         float64 `json:"cost_usd"`
	Priced          bool    `json:"priced"` // false = unknown rates (fallback provider or unknown model); cost is 0
}

// Turn is what the runtime records for one command.
type Turn struct {
	Agent     string    `json:"agent"`
	CommandID string    `json:"command_id"`
	At        time.Time `json:"at"`
	Sessions  int       `json:"sessions"`
	Records   []Record  `json:"records"`
	Totals    Record    `json:"totals"`
	// Latency of the turn through the warm API (A13): request → first words
	// and request → end, in milliseconds. 0 = not measured (CLI turn).
	FirstWordsMS int64 `json:"first_words_ms,omitempty"`
	TotalMS      int64 `json:"total_ms,omitempty"`
}

// String is the log line: `usage: 6 calls, 14454 in + 68096 cached, 1005 out (+313 reasoning), US$ 0.0059`.
func (t Turn) String() string {
	s := fmt.Sprintf("usage: %d calls, %d in + %d cached, %d out", t.Totals.Calls, t.Totals.InputTokens, t.Totals.CacheReadTokens, t.Totals.OutputTokens)
	if t.Totals.ReasoningTokens > 0 {
		s += fmt.Sprintf(" (+%d reasoning)", t.Totals.ReasoningTokens)
	}
	if t.Totals.Priced {
		s += fmt.Sprintf(", US$ %.4f", t.Totals.CostUSD)
	} else {
		s += ", cost unknown"
	}
	return s
}

// Store reads Hermes' session store.
type Store struct {
	DB string // path to state.db
}

// ErrNoStore is returned when state.db does not exist yet (no turn ever ran).
var ErrNoStore = errors.New("usage: hermes state.db not found")

// Since aggregates every API call of the sessions started at or after `since`.
// The store is opened read-only so a running Hermes is never blocked.
func (s *Store) Since(ctx context.Context, since time.Time) ([]Record, int, error) {
	if _, err := os.Stat(s.DB); err != nil {
		return nil, 0, ErrNoStore
	}
	db, err := sql.Open("sqlite", "file:"+s.DB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, 0, err
	}
	defer db.Close()

	const q = `
SELECT u.model, u.billing_provider, u.billing_base_url, u.task,
       u.api_call_count, u.input_tokens, u.cache_read_tokens, u.output_tokens, u.reasoning_tokens,
       s.id
FROM session_model_usage u JOIN sessions s ON s.id = u.session_id
WHERE s.started_at >= ?
ORDER BY s.started_at, u.first_seen`
	rows, err := db.QueryContext(ctx, q, float64(since.UnixNano())/1e9)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	byKey := map[string]*Record{}
	sessions := map[string]bool{}
	var keys []string
	for rows.Next() {
		var r Record
		var sid string
		if err := rows.Scan(&r.Model, &r.Provider, &r.BaseURL, &r.Task, &r.Calls, &r.InputTokens, &r.CacheReadTokens, &r.OutputTokens, &r.ReasoningTokens, &sid); err != nil {
			return nil, 0, err
		}
		sessions[sid] = true
		k := r.Model + "|" + r.BaseURL + "|" + r.Task
		if cur, ok := byKey[k]; ok {
			cur.Calls += r.Calls
			cur.InputTokens += r.InputTokens
			cur.CacheReadTokens += r.CacheReadTokens
			cur.OutputTokens += r.OutputTokens
			cur.ReasoningTokens += r.ReasoningTokens
			continue
		}
		keys = append(keys, k)
		rec := r
		byKey[k] = &rec
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	sort.Strings(keys)
	out := make([]Record, 0, len(keys))
	for _, k := range keys {
		out = append(out, *byKey[k])
	}
	return out, len(sessions), nil
}

// Answer returns the raw markdown of the turn's final reply: the last
// assistant message with text and no tool calls, in the sessions started at or
// after `since`. The `hermes chat -q` stdout renders markdown for a terminal
// and drops code fences, so the plan/request/decision/ask blocks only survive
// here (seen, 2026-11-04: the Innkeeper's ```plan block reached the
// runtime as `plan{…}` and no task was created). "" when there is none.
func (s *Store) Answer(ctx context.Context, since time.Time) (string, error) {
	if _, err := os.Stat(s.DB); err != nil {
		return "", ErrNoStore
	}
	db, err := sql.Open("sqlite", "file:"+s.DB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return "", err
	}
	defer db.Close()
	const q = `
SELECT m.content
FROM messages m JOIN sessions s ON s.id = m.session_id
WHERE s.started_at >= ? AND m.role = 'assistant'
  AND COALESCE(m.content, '') <> ''
  AND COALESCE(m.tool_calls, '') IN ('', '[]', 'null')
ORDER BY s.started_at DESC, m.id DESC
LIMIT 1`
	var content sql.NullString
	err = db.QueryRowContext(ctx, q, float64(since.UnixNano())/1e9).Scan(&content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(content.String), nil
}

// Price fills CostUSD/Priced on every record at the rates in force at `at`
// and returns the totals. The totals are Priced only when every record is.
func Price(recs []Record, at time.Time) (Record, []Record) {
	tot := Record{Priced: len(recs) > 0}
	for i := range recs {
		recs[i].CostUSD, recs[i].Priced = Cost(recs[i], at)
		tot.Calls += recs[i].Calls
		tot.InputTokens += recs[i].InputTokens
		tot.CacheReadTokens += recs[i].CacheReadTokens
		tot.OutputTokens += recs[i].OutputTokens
		tot.ReasoningTokens += recs[i].ReasoningTokens
		tot.CostUSD += recs[i].CostUSD
		tot.Priced = tot.Priced && recs[i].Priced
	}
	return tot, recs
}

// Collect builds the Turn for one command: sessions since `started`, priced at
// `started` (the rate window a turn begins in is the one it is billed in, give
// or take minutes).
func (s *Store) Collect(ctx context.Context, agent, commandID string, started time.Time) (Turn, error) {
	recs, n, err := s.Since(ctx, started.Add(-time.Second))
	if err != nil {
		return Turn{}, err
	}
	t := Turn{Agent: agent, CommandID: commandID, At: started.UTC(), Sessions: n}
	t.Totals, t.Records = Price(recs, started)
	return t, nil
}

// Append writes the turn as one JSON line to dir/<YYYY-MM-DD>.jsonl.
func Append(dir string, t Turn) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	name := filepath.Join(dir, t.At.UTC().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// Short is the suffix added to the front desk result so the owner sees the cost of
// every answer in the tavern: ` · 15.5k tokens · US$ 0.006`.
func (t Turn) Short() string {
	tokens := t.Totals.InputTokens + t.Totals.CacheReadTokens + t.Totals.OutputTokens + t.Totals.ReasoningTokens
	if tokens == 0 {
		return ""
	}
	s := " · " + humanTokens(tokens) + " tokens"
	if t.Totals.Priced {
		s += fmt.Sprintf(" · US$ %s", trimFloat(t.Totals.CostUSD))
	}
	return s
}

func humanTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return trimFloat(float64(n)/1_000_000) + "M"
	case n >= 1_000:
		return trimFloat(float64(n)/1_000) + "k"
	}
	return fmt.Sprint(n)
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}
