package loop

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
	"github.com/redis/go-redis/v9"
)

// fakeBudget is tavern-core's budget and task reads.
type fakeBudget struct {
	b     tavern.Budget
	err   error
	reads int
	prio  map[string]string
}

func (f *fakeBudget) Budget(context.Context) (*tavern.Budget, error) {
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	b := f.b
	return &b, nil
}

func (f *fakeBudget) GetTask(_ context.Context, id string) (*tavern.Task, error) {
	p, ok := f.prio[id]
	if !ok {
		return nil, errors.New("404")
	}
	return &tavern.Task{ID: id, Priority: p}, nil
}

// D7: over the token budget only P0/P1 work runs; P2/P3 bar work is held
// (not acked) and runs once the budget turns over. the owner's commands run.
func TestOverBudgetHoldsP2P3(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	tr := &fakeTurner{out: "ok"}
	l := newLoop(h, tr)
	l.Profile.Budget.TokensPerDay = 1000
	l.TurnsDir = t.TempDir()
	l.Bus = newBar(t, "test")
	if err := l.Bus.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	fb := &fakeBudget{b: tavern.Budget{Day: "2026-11-05", TokensPerDay: 1000, TokensUsed: 1200, ResetsAt: "2026-11-06T00:00:00Z"},
		prio: map[string]string{"T-0": "P0", "T-2": "P2"}}
	l.Budget = fb
	sender := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	for _, m := range []bar.Message{
		{To: "test", Type: "request", Task: "T-2", Text: "routine post"},
		{To: "test", Type: "request", Task: "T-0", Text: "hotfix now"},
		{To: "test", Type: "note", Text: "fyi"},
		{To: "test", Type: "alert", Text: "egress blocked a host"},
	} {
		if _, err := sender.Send(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	if len(msgs) != 4 {
		t.Fatalf("read %d", len(msgs))
	}
	var ran []string
	l.Turner = turnerFunc(func(text string) { ran = append(ran, text) })
	l.handleBatch(ctx, msgs)
	if len(ran) != 2 || !strings.Contains(strings.Join(ran, "|"), "hotfix now") || !strings.Contains(strings.Join(ran, "|"), "egress blocked") {
		t.Fatalf("over budget ran %d: %v", len(ran), ran)
	}
	if len(l.held) != 2 {
		t.Fatalf("held = %v", l.held)
	}
	if p, _ := l.Bus.Pending(ctx); len(p) != 2 {
		t.Fatalf("held work must stay pending (unacked), got %d", len(p))
	}
	ev, _ := l.Bus.C.XRange(ctx, bar.Events, "-", "+").Result()
	n := 0
	for _, e := range ev {
		if e.Values["kind"] == "budget.exceeded" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("budget.exceeded events = %d, want one per day", n)
	}

	// A command from the owner is not touched by the budget.
	tr2 := &fakeTurner{out: "done"}
	l.Turner = tr2
	l.Handle(ctx, frontdesk.Command{ID: "c1", Text: "the owner's order"})
	if tr2.seen == "" {
		t.Fatal("the owner's command did not run over budget")
	}

	// The UTC day turns: the held work runs on the next idle tick.
	fb.b = tavern.Budget{Day: "2026-11-06", TokensPerDay: 1000, TokensUsed: 0}
	l.budget.b = nil
	ran = nil
	l.Turner = turnerFunc(func(text string) { ran = append(ran, text) })
	l.handleBatch(ctx, nil)
	if len(ran) != 2 || len(l.held) != 0 {
		t.Fatalf("after reset ran %d, held %d", len(ran), len(l.held))
	}
	if p, _ := l.Bus.Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after reset = %d", len(p))
	}
}

// No budget in the profile, or tavern-core unreachable → nothing is held.
func TestBudgetFailsOpen(t *testing.T) {
	_, h := newServer(t)
	l := newLoop(h, &fakeTurner{})
	fb := &fakeBudget{b: tavern.Budget{TokensPerDay: 10, TokensUsed: 99}}
	l.Budget = fb
	if over, _ := l.overBudget(context.Background()); over || fb.reads != 0 {
		t.Fatalf("no tokens_per_day in the profile: over=%v reads=%d", over, fb.reads)
	}
	l.Profile.Budget.TokensPerDay = 10
	fb.err = errors.New("connection refused")
	if over, _ := l.overBudget(context.Background()); over {
		t.Fatal("an unreachable tavern-core held work")
	}
	fb.err = nil
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	l.overBudget(context.Background())
	l.overBudget(context.Background())
	if fb.reads != 2 {
		t.Fatalf("a budget read is cached for %v: reads=%d", budgetTTL, fb.reads)
	}
}

type turnerFunc func(text string)

func (f turnerFunc) Run(_ context.Context, text string) (string, error) {
	f(text)
	return "ok", nil
}

// D9: during DeepSeek's peak hours routines (P3) wait for off-peak, when
// every token costs half; P0–P2 run at once. Not for a non-DeepSeek model.
func TestRoutinesWaitForOffPeak(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	l := newLoop(h, &fakeTurner{})
	l.Profile.Model = "deepseek-v4-flash"
	l.TurnsDir = t.TempDir()
	l.Bus = newBar(t, "test")
	if err := l.Bus.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	l.Budget = &fakeBudget{prio: map[string]string{"T-2": "P2", "T-3": "P3"}}
	now := time.Date(2026, 11, 10, 7, 30, 0, 0, time.UTC) // Tuesday, peak 06:00–10:00
	l.now = func() time.Time { return now }
	sender := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	for _, m := range []bar.Message{
		{To: "test", Type: "request", Task: "T-3", Text: "weekly cleanup"},
		{To: "test", Type: "request", Task: "T-2", Text: "handoff from the Dev"},
	} {
		if _, err := sender.Send(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	var ran []string
	l.Turner = turnerFunc(func(text string) { ran = append(ran, text) })
	l.handleBatch(ctx, msgs)
	if len(ran) != 1 || !strings.Contains(ran[0], "handoff from the Dev") || len(l.held) != 1 {
		t.Fatalf("peak: ran %v, held %d", ran, len(l.held))
	}
	ev, _ := l.Bus.C.XRange(ctx, bar.Events, "-", "+").Result()
	if n := countKind(ev, "offpeak.deferred"); n != 1 {
		t.Fatalf("offpeak.deferred events = %d", n)
	}
	l.handleBatch(ctx, nil) // idle tick, still peak
	if len(ran) != 1 {
		t.Fatal("a routine ran during peak hours")
	}
	now = time.Date(2026, 11, 10, 10, 0, 5, 0, time.UTC) // off-peak
	l.handleBatch(ctx, nil)
	if len(ran) != 2 || !strings.Contains(ran[1], "weekly cleanup") || len(l.held) != 0 {
		t.Fatalf("off-peak: ran %v, held %d", ran, len(l.held))
	}

	// A hireling on another provider has no off-peak price: nothing waits.
	l.Profile.Model = "anthropic/claude-sonnet"
	now = time.Date(2026, 11, 10, 7, 30, 0, 0, time.UTC)
	if r := l.holdReason(ctx, bar.Message{ID: "x", Task: "T-3"}); r != "" {
		t.Fatalf("non-DeepSeek model held work: %q", r)
	}
}

func countKind(ev []redis.XMessage, kind string) int {
	n := 0
	for _, e := range ev {
		if e.Values["kind"] == kind {
			n++
		}
	}
	return n
}
