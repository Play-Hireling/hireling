package loop

import (
	"context"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
)

// reasoningTurner records the reasoning set before each turn and reports a latency.
type reasoningTurner struct {
	fakeTurner
	levels []string
}

func (r *reasoningTurner) SetReasoning(level string) { r.levels = append(r.levels, level) }
func (r *reasoningTurner) Latency() (time.Duration, time.Duration) {
	return 800 * time.Millisecond, 1500 * time.Millisecond
}

// A13 — conversation runs light; a task (and the planner) keeps the default.
func TestChatReasoningPerTurn(t *testing.T) {
	ctx := context.Background()
	tr := &reasoningTurner{fakeTurner: fakeTurner{out: "ok"}}
	l, _ := leaseLoop(t, &tr.fakeTurner)
	l.Turner = tr
	l.Profile.Runtime.ChatReasoning = "low"
	l.Handle(ctx, frontdesk.Command{ID: "c1", Text: "oi"})
	l.HandleMessage(ctx, bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-1", Text: "hooks"})
	l.Profile.Planner = true
	l.Handle(ctx, frontdesk.Command{ID: "c2", Text: "plan the week"})
	l.Profile.Planner = false
	l.Profile.Runtime.ChatReasoning = "default"
	l.Handle(ctx, frontdesk.Command{ID: "c3", Text: "oi"})
	want := []string{"low", "", "", ""}
	if len(tr.levels) != len(want) {
		t.Fatalf("levels = %v", tr.levels)
	}
	for i := range want {
		if tr.levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v", tr.levels, want)
		}
	}
	if l.lastLatency[1] != 1500*time.Millisecond {
		t.Fatalf("latency not kept: %v", l.lastLatency)
	}
}
