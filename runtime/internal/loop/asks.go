package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/plan"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// AskBook is tavern-core's side of D11 ("autonomy first").
type AskBook interface {
	PreAsk(ctx context.Context, in tavern.AskCheck) (tavern.AskVerdict, error)
	AskOpened(ctx context.Context, o tavern.OpenAsk) error
	AskAnswer(ctx context.Context, decision, choice string) (*tavern.AnsweredAsk, error)
}

// preAsk runs before a card opens. done=true means no card: the ask was the
// hireling's own (sent back with a note on its own inbox, so the next turn
// decides it) or it joined the task's open card (the turn waits on that
// card). Any error opens the card as before: when in doubt, the owner sees it.
func (l *Loop) preAsk(ctx context.Context, a *plan.Ask, task, turn, rest string, blocked bool) (answer, waiting string, done bool) {
	if l.Asks == nil {
		return "", "", false
	}
	v, err := l.Asks.PreAsk(ctx, tavern.AskCheck{Task: task, Turn: turn, Action: a.Action, Question: a.Question, Blocked: blocked})
	if err != nil {
		l.logf("ask: autonomy check: %v (opening the card)", err)
		return "", "", false
	}
	switch v.Verdict {
	case "yours":
		note := fmt.Sprintf("Your ask \"%s\" did not go to the owner: `%s` is yours to decide (%s). "+
			"Decide it now — take your recommended option (%s) unless the brief says otherwise — do it, and say what you chose and why in your answer; the Autonomy Log records the action. "+
			"Ask the owner only if something you find makes it theirs (see \"Autonomy first\").",
			clip(a.Question, 200), a.Action, v.Reason, a.Options[0])
		l.logf("ask sent back: %s is %s (%s)", a.Action, v.Level, v.Reason)
		if l.Bus != nil {
			if _, err := l.Bus.Send(ctx, bar.Message{To: l.Profile.ID, Type: "note", Task: task, Text: clip(note, 3900)}); err != nil {
				l.logf("ask: note to self: %v", err)
			}
			_ = l.Bus.Event(ctx, "ask.returned", map[string]any{"task": task, "action": a.Action, "level": v.Level, "question": a.Question})
		}
		return strings.TrimSpace(rest + fmt.Sprintf("\n\n(Not sent to the owner: `%s` is %s's own to decide — %s.)", a.Action, l.Profile.ID, v.Reason)), "", true
	case "joined":
		l.logf("ask joined the open card %s on task %s: %s", v.Decision, task, a.Question)
		if l.Bus != nil {
			_ = l.Bus.Event(ctx, "ask.joined", map[string]any{"task": task, "decision": v.Decision, "question": a.Question})
		}
		return strings.TrimSpace(rest + fmt.Sprintf("\n\nWaiting on you (task %s already has card %s): this question joins it and comes back with your answer — %s", task, v.Decision, a.Question)), a.Question, true
	}
	return "", "", false
}

// answeredAsk closes the card in tavern-core when the owner's answer arrives and
// hands the hireling the questions that joined it while it was open.
func (l *Loop) answeredAsk(ctx context.Context, decision, choice, text string) string {
	if l.Asks == nil {
		return text
	}
	a, err := l.Asks.AskAnswer(ctx, decision, choice)
	if err != nil {
		l.logf("decision %s: close the ask: %v", decision, err)
		return text
	}
	if a == nil {
		return text
	}
	l.logf("decision %s answered (recommended picked: %v)", decision, a.PickedRecommended)
	if len(a.FollowUps) == 0 {
		return text
	}
	var sb strings.Builder
	sb.WriteString(text)
	sb.WriteString("\n\nWhile this card was open you also asked, on the same task:")
	for _, f := range a.FollowUps {
		sb.WriteString("\n- " + f)
	}
	sb.WriteString("\nDecide each one now under \"Autonomy first\"; ask the owner again only if it is still theirs.")
	return sb.String()
}
