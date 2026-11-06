package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/gate"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// overTokens is tavern-core's answer to the D7 in-turn check once the day's
// tokens are spent: `budget` for the `tokens` action, free otherwise.
type overTokens struct{ asked []gate.Context }

func (o *overTokens) Check(_ context.Context, action, _ string, c gate.Context) (gate.Decision, error) {
	if action == gate.Tokens {
		o.asked = append(o.asked, c)
		return gate.Decision{Level: "budget", Reason: "daily token budget reached (D7): 100300/100000 tokens today"}, nil
	}
	return gate.Decision{Level: "free"}, nil
}

type spent int64

func (s spent) Tokens(context.Context, time.Time) (int64, error) { return int64(s), nil }

// hookedTurner is the Innkeeper's model with the real gate hook in front of
// its tools: it tries one tool call mid-turn, and when the hook refuses it
// wraps up with what it has, as budgetStop tells it to.
type hookedTurner struct {
	hook    *gate.Hook
	turn    map[string]any // gate-turn.json as the hook saw it
	outcome gate.Outcome
	calls   int
}

func (h *hookedTurner) Run(ctx context.Context, _ string) (string, error) {
	h.calls++
	raw, _ := os.ReadFile(filepath.Join(h.hook.Home, gate.TurnFile))
	_ = json.Unmarshal(raw, &h.turn)
	h.outcome = h.hook.Decide(ctx, gate.Payload{Tool: "read_file", Input: map[string]any{"path": "notes/queue.md"}})
	if !h.outcome.Allow {
		return "Signup fix first. (Stopped by the token budget before checking the notes; the rest waits for the reset.)", nil
	}
	return morningNote, nil
}

// The morning turn is a P3 routine for D7 in-turn (): when the
// day's tokens run out mid-turn, the gate hook cuts it like any P3 work. The
// composed plan and its log line still stand, with what the turn wrote
// before the cut. The pre-turn check passed (under budget at the start).
func TestDailyMorningTurnCutMidTurnAsP3(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, _ := dailyLoop(t, h, c, &now)
	l.Profile.Budget.TokensPerDay = 100000
	l.Budget = &fakeBudget{b: tavern.Budget{TokensPerDay: 100000, TokensUsed: 90000}} // under at the start
	home := t.TempDir()
	l.GateTurnFile = filepath.Join(home, gate.TurnFile)
	checker := &overTokens{}
	tr := &hookedTurner{hook: &gate.Hook{Agent: "innkeeper", Home: home, Checker: checker, Spend: spent(10300)}}
	l.Turner = tr

	if !l.dailyTick(ctx) {
		t.Fatal("not posted")
	}
	if tr.calls != 1 {
		t.Fatal("the pre-turn check held a turn that was under budget")
	}
	// The hook saw a P3 turn with the profile's budget, asked tavern-core, and cut it.
	if tr.turn["priority"] != "P3" || tr.turn["tokens_per_day"] != 100000.0 || tr.turn["turn"] != "daily-2026-11-05" {
		t.Fatalf("gate-turn.json = %v", tr.turn)
	}
	if tr.outcome.Allow || !strings.Contains(tr.outcome.Message, "Stopped by the token budget (D7)") ||
		len(checker.asked) != 1 || checker.asked[0].TurnTokens != 10300 {
		t.Fatalf("not cut: %+v asked=%+v", tr.outcome, checker.asked)
	}
	// The plan stands, with the wrap-up as its note, and reached the log.
	p := f.plan("2026-11-05")
	if p.Text == "" || p.LoggedAt == nil || !strings.HasPrefix(p.Commentary, "Signup fix first. (Stopped by the token budget") {
		t.Fatalf("plan = %+v", p)
	}
	if lines := logLines(s); len(lines) != 2 || !strings.HasPrefix(lines[0].Topic, "Daily plan · 2026-11-05") ||
		!strings.HasPrefix(lines[1].Result, "Signup fix first.") {
		t.Fatalf("log = %+v", lines)
	}
	// The P3 label is the morning turn's only: the next turn is not a routine.
	l.writeGateTurn(ctx, "c1", "", "the owner's command")
	raw, _ := os.ReadFile(l.GateTurnFile)
	if !strings.Contains(string(raw), `"priority":"P0"`) {
		t.Fatalf("next turn = %s", raw)
	}
}
