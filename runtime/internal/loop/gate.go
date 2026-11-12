package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/plan"
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

// yesWord is an option that is a plain yes and nothing else, as the Front
// Desk reads it (frontdesk.plainYes in tavern-core: keep the two lists the
// same): the card is granted on the first such option.
var yesWord = regexp.MustCompile(`(?i)^\s*(approve|approved|yes|ok|okay|go|go ahead|do it|allow|confirm)\s*[.!]?\s*$`)

// cardName is an action name the Front Desk accepts on a card
// (frontdesk.actionName in tavern-core: keep the two the same).
var cardName = regexp.MustCompile(`^[a-z0-9_.:-]{1,128}$`)

// cardable splits the blocked actions into those a card can carry and the
// names it cannot.
func cardable(acts []GateAct) (keep []GateAct, off []string) {
	for _, a := range acts {
		if cardName.MatchString(a.Action) {
			keep = append(keep, a)
		} else {
			off = append(off, a.Action)
		}
	}
	return keep, off
}

// withYes makes sure a card that carries blocked actions offers a plain yes:
// without one tavern-core refuses the card and the owner's answer could grant
// nothing. "Approve" is added (in place of the last option when all six are
// taken).
func withYes(options []string) []string {
	for _, o := range options {
		if yesWord.MatchString(o) {
			return options
		}
	}
	out := append([]string{}, options...)
	if len(out) >= plan.MaxOptions {
		out = out[:plan.MaxOptions-1]
	}
	return append(out, "Approve")
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
	err := os.WriteFile(l.GateTurnFile, raw, 0o640)
	if err != nil {
		// The file may be this process's but read-only (the owner can restore
		// the mode without writing the directory), or stale/foreign (remove and
		// rewrite). An unwritable directory is repaired by `hireling setup`'s
		// lock (root:hermes 1775) on the next boot, not here. Only a regular
		// file is chmodded: the path can be a directory left by a bad restore,
		// and 0640 would take away its traverse bit.
		if fi, statErr := os.Stat(l.GateTurnFile); statErr == nil && fi.Mode().IsRegular() {
			if os.Chmod(l.GateTurnFile, 0o640) == nil {
				err = os.WriteFile(l.GateTurnFile, raw, 0o640)
			}
		}
		if err != nil && os.Remove(l.GateTurnFile) == nil {
			err = os.WriteFile(l.GateTurnFile, raw, 0o640)
		}
	}
	if err != nil {
		// Never leave the last turn's start and priority behind: the hook would
		// read it as this turn and D7 would count the day twice (decision #113).
		_ = os.Remove(l.GateTurnFile)
		l.turnFileFailOnce.Do(func() {
			l.logf("gate turn file %s: %v (euid=%d; a deploy may have changed /opt/data's owner — the D4 turn link and the D7 in-turn cut are off until the next boot fixes it)",
				l.GateTurnFile, err, os.Geteuid())
		})
	}
}
