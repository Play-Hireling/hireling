package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// GateAct is one action the autonomy gate blocked (tavern-core policy.Act).
type GateAct = tavern.GateAct

// GateGrants is tavern-core's side of the owner's approvals (D1/D3).
type GateGrants interface {
	RecentBlocked(ctx context.Context, since time.Time) ([]GateAct, error)
	Pend(ctx context.Context, decision string, acts []GateAct, approve string) error
	Answer(ctx context.Context, decision, choice string) ([]GateAct, error)
}

// grantLine is the line on the owner's card that says what a yes allows.
func grantLine(id string, acts []GateAct) string {
	if len(acts) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var parts []string
	for _, a := range acts {
		if seen[a.Action] {
			continue
		}
		seen[a.Action] = true
		p := a.Action
		if t := strings.TrimSpace(a.Target); t != "" && t != a.Action {
			if r := []rune(t); len(r) > 60 {
				t = string(r[:59]) + "…"
			}
			p += " (" + t + ")"
		}
		parts = append(parts, p)
	}
	return fmt.Sprintf("Approving lets %s do once: %s.", id, strings.Join(parts, "; "))
}

func actNames(acts []GateAct) string {
	var n []string
	for _, a := range acts {
		n = append(n, "`"+a.Action+"`")
	}
	return strings.Join(n, ", ")
}

// writeGateTurn tells the gate hook which turn is running (D4) and, with a
// token budget, the work's priority and tokens_per_day, so the hook can stop
// a P2/P3 turn that crosses the budget (D7). Best effort: the log loses the
// turn link and the turn is only held to the budget between turns, never the
// action.
func (l *Loop) writeGateTurn(ctx context.Context, id, task, text string) {
	if l.GateTurnFile == "" {
		return
	}
	topic := text
	if l.Redactor != nil {
		topic = l.Redactor.Summary(text)
	}
	t := map[string]any{"turn": id, "task": task, "topic": topic, "at": time.Now().UTC().Format(time.RFC3339Nano)}
	if prio, perDay := l.turnBudget(ctx); perDay > 0 {
		t["priority"], t["tokens_per_day"] = prio, perDay
	}
	raw, _ := json.Marshal(t)
	if err := os.WriteFile(l.GateTurnFile, raw, 0o640); err != nil {
		l.logf("gate turn file: %v", err)
		_ = os.Remove(l.GateTurnFile) // never leave the last turn's start and priority behind
	}
}
