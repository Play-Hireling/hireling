package usage

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newStore builds a state.db with the subset of Hermes' schema the reader uses
// and the rows seen after the first e2e turn (2026-11-03).
func newStore(t *testing.T) (*Store, time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, model TEXT, started_at REAL NOT NULL, ended_at REAL)`,
		`CREATE TABLE session_model_usage (session_id TEXT NOT NULL, model TEXT NOT NULL, billing_provider TEXT NOT NULL DEFAULT '',
			billing_base_url TEXT NOT NULL DEFAULT '', billing_mode TEXT NOT NULL DEFAULT '', task TEXT NOT NULL DEFAULT '',
			api_call_count INTEGER NOT NULL DEFAULT 0, input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_write_tokens INTEGER NOT NULL DEFAULT 0, reasoning_tokens INTEGER NOT NULL DEFAULT 0,
			estimated_cost_usd REAL NOT NULL DEFAULT 0, actual_cost_usd REAL NOT NULL DEFAULT 0, cost_status TEXT, cost_source TEXT,
			first_seen REAL NOT NULL, last_seen REAL NOT NULL)`,
		// an older turn that must not be counted
		`INSERT INTO sessions VALUES ('local-old','oneshot','deepseek-v4-flash',1793700000,1793700020)`,
		`INSERT INTO session_model_usage VALUES ('local-old','deepseek-v4-flash','custom','https://api.deepseek.com/v1','','',3,9000,500,0,0,0,0,0,'unknown','none',1793700001,1793700019)`,
		// the turn under test
		`INSERT INTO sessions VALUES ('local-a26b','oneshot','deepseek-v4-flash',1793734907.751202,1793734929.1552737)`,
		`INSERT INTO session_model_usage VALUES ('local-a26b','deepseek-v4-flash','custom','https://api.deepseek.com/v1','','',6,14454,1005,68096,0,313,0,0,'unknown','none',1793734917.48,1793734927.43)`,
		`INSERT INTO session_model_usage VALUES ('local-a26b','deepseek-flash','custom','https://api.deepseek.com/v1/','','title_generation',1,470,12,0,0,0,0,0,NULL,NULL,1793734928.6,1793734928.6)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return &Store{DB: path}, time.Unix(1793734907, 0)
}

func TestCollect(t *testing.T) {
	s, started := newStore(t)
	turn, err := s.Collect(context.Background(), "test", "cmd-1", started)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Sessions != 1 || len(turn.Records) != 2 {
		t.Fatalf("sessions=%d records=%d, want 1 and 2: %+v", turn.Sessions, len(turn.Records), turn.Records)
	}
	tot := turn.Totals
	if tot.Calls != 7 || tot.InputTokens != 14924 || tot.CacheReadTokens != 68096 || tot.OutputTokens != 1017 || tot.ReasoningTokens != 313 {
		t.Fatalf("totals %+v", tot)
	}
	if !tot.Priced {
		t.Fatal("DeepSeek turn must be priced")
	}
	// 2026-11-03 19:41 UTC is a Tuesday outside the peak windows → off-peak (half rates).
	want := (68096*0.003 + 14924*0.15 + (1017+313)*0.6) / 1e6
	if math.Abs(tot.CostUSD-want) > 1e-9 {
		t.Fatalf("cost %.6f want %.6f", tot.CostUSD, want)
	}
	if got := turn.String(); got != "usage: 7 calls, 14924 in + 68096 cached, 1017 out (+313 reasoning), US$ 0.0032" {
		t.Fatalf("log line: %q", got)
	}
	if got := turn.Short(); got != " · 84.35k tokens · US$ 0.003" {
		t.Fatalf("short: %q", got)
	}
}

// TestTokens: the running spend of the turn is the sum tavern-core counts, and
// an older turn stays out.
func TestTokens(t *testing.T) {
	s, started := newStore(t)
	n, err := s.Tokens(context.Background(), started.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(14924 + 68096 + 1017 + 313); n != want {
		t.Fatalf("tokens = %d, want %d", n, want)
	}
	if _, err := (&Store{DB: filepath.Join(t.TempDir(), "none.db")}).Tokens(context.Background(), started); err != ErrNoStore {
		t.Fatalf("missing store: %v", err)
	}
}

// TestAnswerKeepsFences: the stored reply is the raw markdown the model wrote,
// fences included (the terminal render dropped them, 2026-11-04).
func TestAnswerKeepsFences(t *testing.T) {
	s, started := newStore(t)
	db, err := sql.Open("sqlite", s.DB)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT, tool_calls TEXT)`,
		`INSERT INTO messages (session_id, role, content) VALUES ('local-old','assistant','an older answer')`,
		`INSERT INTO messages (session_id, role, content) VALUES ('local-a26b','user','Prepare the Monday post')`,
		`INSERT INTO messages (session_id, role, content, tool_calls) VALUES ('local-a26b','assistant','Let me look.','[{"id":"c1"}]')`,
		`INSERT INTO messages (session_id, role, content) VALUES ('local-a26b','tool','README.md')`,
		"INSERT INTO messages (session_id, role, content) VALUES ('local-a26b','assistant','Two tasks.\n\n```plan\n{\"tasks\": []}\n```')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	db.Close()
	got, err := s.Answer(context.Background(), started.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Two tasks.\n\n```plan\n{\"tasks\": []}\n```" {
		t.Fatalf("answer %q", got)
	}
	if got, _ := s.Answer(context.Background(), started.Add(time.Hour)); got != "" {
		t.Fatalf("no session after since: %q", got)
	}
}

func TestCollectMissingStore(t *testing.T) {
	s := &Store{DB: filepath.Join(t.TempDir(), "none.db")}
	if _, err := s.Collect(context.Background(), "test", "x", time.Now()); err != ErrNoStore {
		t.Fatalf("err = %v, want ErrNoStore", err)
	}
}

func TestPricing(t *testing.T) {
	peakAt := time.Date(2026, 11, 3, 7, 0, 0, 0, time.UTC)  // Tuesday 07:00 UTC
	offAt := time.Date(2026, 11, 3, 19, 41, 0, 0, time.UTC) // Tuesday 19:41 UTC
	weekend := time.Date(2026, 11, 7, 7, 0, 0, 0, time.UTC) // Saturday 07:00 UTC
	if !IsPeak(peakAt) || IsPeak(offAt) || IsPeak(weekend) {
		t.Fatal("peak window wrong")
	}
	r := Record{Model: "deepseek-v4-pro", InputTokens: 1_000_000, OutputTokens: 1_000_000}
	if c, ok := Cost(r, peakAt); !ok || math.Abs(c-(1.32+3.96)) > 1e-9 {
		t.Fatalf("pro peak cost %v %v", c, ok)
	}
	if c, _ := Cost(r, offAt); math.Abs(c-(1.32+3.96)/2) > 1e-9 {
		t.Fatalf("pro off-peak cost %v", c)
	}
	// The OpenRouter fallback has other rates: recorded, not priced.
	if c, ok := Cost(Record{Model: "deepseek/deepseek-v4-flash", BaseURL: "https://openrouter.ai/api/v1", InputTokens: 10}, offAt); ok || c != 0 {
		t.Fatalf("fallback must not be priced: %v %v", c, ok)
	}
	if _, ok := RatesFor("gpt-5", offAt); ok {
		t.Fatal("unknown model must not be priced")
	}
}

func TestAppend(t *testing.T) {
	dir := t.TempDir()
	turn := Turn{Agent: "test", CommandID: "c1", At: time.Date(2026, 11, 3, 19, 41, 0, 0, time.UTC)}
	if err := Append(dir, turn); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, turn); err != nil {
		t.Fatal(err)
	}
	m, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(m) != 1 || filepath.Base(m[0]) != "2026-11-03.jsonl" {
		t.Fatalf("files: %v", m)
	}
}

func TestLatencies(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)
	for _, ms := range []int64{1000, 2000, 3000, 20000} {
		if err := Append(dir, Turn{Agent: "dev", At: day, FirstWordsMS: ms / 2, TotalMS: ms}); err != nil {
			t.Fatal(err)
		}
	}
	_ = Append(dir, Turn{Agent: "dev", At: day}) // a CLI turn: not measured
	l := Latencies(dir, day)
	if l.Turns != 4 || l.TotalP50 != 2*time.Second || l.TotalP95 != 20*time.Second || l.FirstP50 != time.Second {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(l.String(), "4 turns today") || Latencies(dir, day.AddDate(0, 0, 1)).String() != "no measured turn today" {
		t.Fatal(l.String())
	}
}

func TestOffPeakAt(t *testing.T) {
	for in, want := range map[string]string{
		"2026-11-10T02:15:00Z": "2026-11-10T04:00:00Z", // Tue, first peak window
		"2026-11-10T07:30:00Z": "2026-11-10T10:00:00Z", // Tue, second
		"2026-11-10T05:00:00Z": "2026-11-10T05:00:00Z", // between the windows
		"2026-11-10T12:00:00Z": "2026-11-10T12:00:00Z", // afternoon
		"2026-11-14T07:30:00Z": "2026-11-14T07:30:00Z", // Saturday
	} {
		at, _ := time.Parse(time.RFC3339, in)
		if got := OffPeakAt(at).Format(time.RFC3339); got != want {
			t.Errorf("OffPeakAt(%s) = %s, want %s", in, got, want)
		}
	}
	if !Discounted("deepseek/deepseek-v4-pro") || !Discounted("deepseek-flash") || Discounted("anthropic/claude-sonnet") {
		t.Fatal("Discounted")
	}
}
