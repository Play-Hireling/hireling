package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// spendFake is the running turn's spend in Hermes' store.
type spendFake struct {
	n     int64
	err   error
	since time.Time
}

func (s *spendFake) Tokens(_ context.Context, since time.Time) (int64, error) {
	s.since = since
	return s.n, s.err
}

// tavernTokens answers the D7 check as tavern-core does: today's recorded
// tokens plus the running turn's against the limit.
type tavernTokens struct {
	fake
	today, limit int64
}

func (f *tavernTokens) Check(ctx context.Context, action, target string, c Context) (Decision, error) {
	if action == Tokens && f.err == nil {
		f.asked = append(f.asked, action)
		f.ctx = append(f.ctx, c)
		if used := f.today + c.TurnTokens; used >= f.limit {
			return Decision{Level: "budget", Reason: fmt.Sprintf("daily token budget reached (D7): %d/%d tokens today", used, f.limit)}, nil
		}
		return Decision{Level: "free"}, nil
	}
	return f.fake.Check(ctx, action, target, c)
}

func writeTurn(t *testing.T, home, prio string, perDay int, at time.Time) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"turn": "msg-1", "task": "T-9", "topic": "weekly carousel",
		"at": at.UTC().Format(time.RFC3339Nano), "priority": prio, "tokens_per_day": perDay})
	if err := os.WriteFile(filepath.Join(home, TurnFile), raw, 0o640); err != nil {
		t.Fatal(err)
	}
}

// D7 inside a turn (a real case: one hireling spent 1,798,130 tokens
// against 150,000 in one turn of 29 tool calls): before each tool call of a
// P2/P3 turn the running spend goes to tavern-core with today's; once over,
// the next call is refused and the model is told to wrap up. P0/P1 run on.
func TestTokenBudgetCutsTheTurn(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	started := time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)
	writeTurn(t, home, "P2", 150000, started)
	sp := &spendFake{n: 20000}
	tv := &tavernTokens{fake: fake{answers: map[string]Decision{"open_pr": {Level: "free"}}}, today: 100000, limit: 150000}
	h := &Hook{Agent: "social-media", Home: home, Checker: tv, Spend: sp}

	// Under the budget: local and external calls run; the turn's spend is sent.
	if o := h.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "brief.md"}}); !o.Allow {
		t.Fatalf("under budget, local call blocked: %+v", o)
	}
	if !sp.since.Equal(started.Add(-time.Second)) || tv.asked[0] != Tokens || tv.ctx[0].TurnTokens != 20000 || tv.ctx[0].Turn != "msg-1" || tv.ctx[0].Task != "T-9" {
		t.Fatalf("budget check: since %v, asked %v, ctx %+v", sp.since, tv.asked, tv.ctx)
	}
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create --fill"}}); !o.Allow {
		t.Fatalf("under budget, free action blocked: %+v", o)
	}

	// The turn crosses the budget: the next call, of any kind, is refused.
	sp.n = 50000
	tv.asked = nil
	o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "ls"}})
	if o.Allow || !strings.Contains(o.Message, "Stopped by the token budget (D7)") || !strings.Contains(o.Message, "150000/150000") ||
		!strings.Contains(o.Message, "Finish now") || strings.Contains(o.Message, "`ask` block") {
		t.Fatalf("over budget mid-turn: %+v", o)
	}
	o = h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create --fill"}})
	if o.Allow || len(tv.asked) != 2 || tv.asked[1] != Tokens {
		t.Fatalf("over budget, the external action must not reach its own check (no grant or cap used): %+v asked %v", o, tv.asked)
	}

	// P0/P1 work still runs over budget, without asking.
	for _, prio := range []string{"P0", "P1"} {
		writeTurn(t, home, prio, 150000, started)
		tv.asked = nil
		if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "ls"}}); !o.Allow || len(tv.asked) != 0 {
			t.Fatalf("%s over budget: %+v asked %v", prio, o, tv.asked)
		}
	}
	// A turn file without a priority is P2 (as in the bar loop).
	writeTurn(t, home, "", 150000, started)
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "ls"}}); o.Allow {
		t.Fatal("no priority over budget: allowed")
	}
	// No budget in the profile, or no turn file: nothing to check.
	writeTurn(t, home, "P3", 0, started)
	tv.asked = nil
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "ls"}}); !o.Allow || len(tv.asked) != 0 {
		t.Fatalf("no budget: %+v asked %v", o, tv.asked)
	}
	if o := (&Hook{Agent: "dev", Home: t.TempDir(), Checker: tv, Spend: sp}).Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "ls"}}); !o.Allow || len(tv.asked) != 0 {
		t.Fatalf("no turn file: %+v asked %v", o, tv.asked)
	}
}

// D7 when the budget cannot be read: tavern-core down → local work goes on
// (external actions fail closed as for D1) until the turn alone has spent a
// whole day's budget; an unreadable store is logged, not cut.
func TestTokenBudgetUnreadable(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	writeTurn(t, home, "P2", 150000, time.Now())
	sp := &spendFake{n: 90000}
	down := &tavernTokens{fake: fake{err: errors.New("dial tcp: connection refused")}, limit: 150000}
	h := &Hook{Agent: "social-media", Home: home, Checker: down, Spend: sp}
	o := h.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "x"}})
	if !o.Allow || len(o.Checked) != 1 || !strings.HasPrefix(o.Checked[0], "tokens=unknown(") {
		t.Fatalf("tavern-core down, under one day's budget: %+v", o)
	}
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "git push origin feat/x"}}); o.Allow {
		t.Fatal("tavern-core down: external action allowed")
	}
	sp.n = 150000
	o = h.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "x"}})
	if o.Allow || !strings.Contains(o.Message, "this turn alone has used 150000 tokens") || o.Checked[0] != "tokens=budget(local)" {
		t.Fatalf("tavern-core down, the turn alone over the budget: %+v", o)
	}
	// An older tavern-core judges the unknown action as an action (owner).
	old := &Hook{Agent: "social-media", Home: home, Checker: &fake{}, Spend: &spendFake{n: 10}}
	if o := old.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "x"}}); !o.Allow || !strings.Contains(o.Checked[0], "unexpected level") {
		t.Fatalf("unexpected answer: %+v", o)
	}
	// Hermes' store unreadable: logged, not cut.
	h.Spend = &spendFake{err: errors.New("database is locked")}
	if o := h.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "x"}}); !o.Allow || !strings.HasPrefix(o.Checked[0], "tokens=unread(") {
		t.Fatalf("store unreadable: %+v", o)
	}
}

// The D7 wire: the running spend goes as turn_tokens with the `tokens`
// action, and a budget answer becomes Hermes' block directive.
func TestTokenBudgetOverHTTP(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(Decision{Level: "budget", Reason: "daily token budget reached (D7): 151000/150000 tokens today, 51000 of them in this turn; resets at 00:00 UTC"})
	}))
	defer srv.Close()
	home := t.TempDir()
	writeTurn(t, home, "P3", 150000, time.Now())
	h := &Hook{Agent: "social-media", Home: home, Checker: &HTTPChecker{BaseURL: srv.URL, Agent: "social-media"}, Spend: &spendFake{n: 51000}}
	var out bytes.Buffer
	var logged string
	payload := `{"hook_event_name":"pre_tool_call","tool_name":"browser_navigate","tool_input":{"url":"https://x.example"}}`
	if err := h.Run(context.Background(), strings.NewReader(payload), &out, func(f string, a ...any) { logged = fmt.Sprintf(f, a...) }); err != nil {
		t.Fatal(err)
	}
	if got["action"] != Tokens || got["turn_tokens"] != float64(51000) || got["target"] != "browser_navigate" || got["use"] != true {
		t.Fatalf("request = %v", got)
	}
	m := map[string]string{}
	_ = json.Unmarshal(out.Bytes(), &m)
	if m["action"] != "block" || !strings.Contains(m["message"], "51000 of them in this turn") {
		t.Fatalf("directive = %s", out.String())
	}
	if !strings.Contains(logged, "block") || !strings.Contains(logged, "tokens=budget") {
		t.Fatalf("log line = %q", logged)
	}
}

// The files the D7 check reads are not the hireling's to edit.
func TestClassifyRefusesBudgetFiles(t *testing.T) {
	for _, c := range []struct {
		tool  string
		input map[string]any
		cwd   string
	}{
		{"write_file", map[string]any{"path": "/opt/data/gate-turn.json"}, "/tmp"},
		{"terminal", map[string]any{"command": `echo '{"priority":"P0"}' > gate-turn.json`}, "/opt/data"},
		{"terminal", map[string]any{"command": "sqlite3 /opt/data/state.db 'DELETE FROM session_model_usage'"}, "/tmp"},
	} {
		if v := Classify(c.tool, c.input, Env{Home: "/opt/data", Cwd: c.cwd}); v.Kind != Refuse {
			t.Errorf("%s %v: kind %d, want refuse", c.tool, c.input, v.Kind)
		}
	}
}
