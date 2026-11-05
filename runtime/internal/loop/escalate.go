package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
)

// B11 — repeated failure → automatic "Waiting on you".
//
// A failed turn on a task this hireling holds is recorded in tavern-core
// (POST /tasks/{id}/fail). Below the profile's threshold the task goes back
// to the queue and the request is put back on this hireling's inbox for
// another try; at the threshold tavern-core blocks the task (needs_human) and
// the runtime opens the decision itself through the D3 path — the model never
// has to decide that it is stuck. The owner's answer comes back as a command with
// the decision id: Drop cancels the task; any other option re-queues it (count
// reset) and the request goes back on the inbox with the owner's choice attached,
// so it runs as an ordinary turn.

// EscalationOptions are the choices of an automatic escalation card.
var EscalationOptions = []string{"Retry", "Reassign", "Drop", "Change brief"}

// escalation is what the owner's answer needs to put the task back to work.
type escalation struct {
	Task    string      `json:"task"`
	Request bar.Message `json:"request"`
}

// failTurn records the failure; re-queues below the threshold, escalates at
// it. Returns the question of the decision it opened ("" = none).
func (l *Loop) failTurn(ctx context.Context, m bar.Message, runErr error) string {
	note := l.Redactor.Summary(runErr.Error())
	t, err := l.Leases.Fail(ctx, m.Task, note, l.Profile.Runtime.EscalateAfter)
	if err != nil {
		l.logf("fail %s: %v", m.Task, err)
		return ""
	}
	if t.Status != "blocked" {
		l.logf("task %s failed (%d/%d): %s — back in the queue", m.Task, t.Failures, l.Profile.Runtime.EscalateAfter, note)
		if l.Bus != nil {
			_ = l.Bus.Event(ctx, "task.failed_turn", map[string]any{"task": m.Task, "failures": t.Failures, "error": note})
			retry := m
			retry.ID, retry.TS = "", ""
			retry.Text = clip(m.Text, 3500)
			if !strings.Contains(retry.Text, "(Retry ") {
				retry.Text += fmt.Sprintf("\n\n(Retry after a failed turn: %s. Try a different approach.)", clip(note, 200))
			}
			if _, err := l.Bus.Send(ctx, retry); err != nil {
				l.logf("retry %s: %v", m.Task, err)
			}
		}
		return ""
	}

	question := fmt.Sprintf("%s failed %d times in a row: %s", clip(t.Title, 120), t.Failures, clip(t.LastError, 200))
	urgency := "normal"
	if t.Priority == "P0" || t.Priority == "P1" {
		urgency = "high"
	}
	ctxText := l.Redactor.Text(fmt.Sprintf("Task %s (%s), requested by %s. Retry = try again as it is; Reassign = I hand it to someone better placed; Drop = cancel the task; Change brief = tell me what to do differently.", t.ID, t.Priority, t.RequestedBy))
	id, err := l.Client.RequestDecision(ctx, frontdesk.DecisionRequest{From: l.Profile.DeskID, Question: question, Options: EscalationOptions, Context: ctxText, Urgency: urgency})
	if err != nil {
		l.logf("escalate %s: decision not opened: %v", t.ID, l.Redactor.Summary(err.Error()))
		return ""
	}
	l.putEscalation(id, escalation{Task: t.ID, Request: m})
	l.logf("task %s escalated after %d failed turns: decision %s", t.ID, t.Failures, id)
	if l.Bus != nil {
		_ = l.Bus.Event(ctx, "task.escalated", map[string]any{"task": t.ID, "decision": id, "failures": t.Failures, "error": t.LastError})
	}
	return question
}

// resolveEscalation applies the owner's answer to an automatic escalation.
func (l *Loop) resolveEscalation(ctx context.Context, c frontdesk.Command, e escalation) {
	choice := chosenOption(c.Text)
	action := "retry"
	switch strings.ToLower(choice) {
	case "drop":
		action = "drop"
	case "reassign":
		action = "reassign"
	case "change brief":
		action = "change brief"
	}
	t, err := l.Leases.Unblock(ctx, e.Task, action)
	result := ""
	switch {
	case err != nil:
		result = "Could not apply the answer to " + e.Task + ": " + l.Redactor.Summary(err.Error())
		l.logf("unblock %s: %v", e.Task, err)
	case action == "drop":
		result = "Task " + e.Task + " dropped (the owner's answer)."
	default:
		result = fmt.Sprintf("Task %s back in the queue: the owner chose %s.", e.Task, choice)
		if l.Bus != nil {
			r := e.Request
			r.ID, r.TS = "", ""
			r.Text = clip(e.Request.Text, 3000) + "\n\n(the owner answered the escalation of this task: " + choice + "."
			if extra := strings.TrimSpace(c.Instructions); extra != "" {
				r.Text += " His note: " + clip(extra, 500)
			}
			r.Text += " Apply it now: " + actionHint(action) + ")"
			if _, err := l.Bus.Send(ctx, r); err != nil {
				l.logf("requeue %s: %v", e.Task, err)
			}
		}
	}
	if err == nil && l.Bus != nil {
		_ = l.Bus.Event(ctx, "task.unblocked", map[string]any{"task": e.Task, "decision": c.DecisionID, "choice": choice, "status": t.Status})
	}
	l.logf("decision %s on %s: %s", c.DecisionID, e.Task, result)
	l.report(ctx, frontdesk.Turn{Phase: frontdesk.End, Result: result})
	if ackErr := l.Client.Ack(ctx, l.Profile.DeskID, c.ID, "done", result, result); ackErr != nil {
		l.logf("ack done %s: %v", c.ID, ackErr)
	}
}

func actionHint(action string) string {
	switch action {
	case "reassign":
		return "hand the task to the hireling best placed for it with a request block, and say why."
	case "change brief":
		return "follow the owner's note instead of the original approach."
	default:
		return "try again, differently from the attempts that failed."
	}
}

// chosenOption reads the option out of the front desk's answer text
// "Decision: <question> → chosen: <option> (decision <id>)".
func chosenOption(text string) string {
	s := text
	if i := strings.LastIndex(s, "chosen:"); i >= 0 {
		s = s[i+len("chosen:"):]
	}
	if i := strings.LastIndex(s, "(decision"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	for _, o := range EscalationOptions {
		if strings.EqualFold(s, o) {
			return o
		}
	}
	if s == "" {
		return "Retry"
	}
	return s
}

func (l *Loop) putEscalation(decision string, e escalation) {
	l.escMu.Lock()
	defer l.escMu.Unlock()
	l.loadEscalations()
	l.escalations[decision] = e
	l.saveEscalations()
}

func (l *Loop) takeEscalation(decision string) (escalation, bool) {
	l.escMu.Lock()
	defer l.escMu.Unlock()
	l.loadEscalations()
	e, ok := l.escalations[decision]
	if ok {
		delete(l.escalations, decision)
		l.saveEscalations()
	}
	return e, ok
}

// loadEscalations reads the file once; callers hold escMu.
func (l *Loop) loadEscalations() {
	if l.escalations != nil {
		return
	}
	l.escalations = map[string]escalation{}
	if l.EscalationsFile == "" {
		return
	}
	raw, err := os.ReadFile(l.EscalationsFile)
	if err != nil {
		return
	}
	if err := json.Unmarshal(raw, &l.escalations); err != nil {
		l.logf("escalations: %v", err)
		l.escalations = map[string]escalation{}
	}
}

// saveEscalations writes the file atomically; callers hold escMu.
func (l *Loop) saveEscalations() {
	if l.EscalationsFile == "" {
		return
	}
	raw, _ := json.Marshal(l.escalations)
	tmp := l.EscalationsFile + ".tmp"
	if err := os.MkdirAll(filepath.Dir(l.EscalationsFile), 0o700); err == nil {
		if err := os.WriteFile(tmp, raw, 0o600); err == nil {
			err = os.Rename(tmp, l.EscalationsFile)
			if err == nil {
				return
			}
		}
	}
	l.logf("escalations: could not save %s", l.EscalationsFile)
}
