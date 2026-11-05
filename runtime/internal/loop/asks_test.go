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
)

// fakeAsks is tavern-core's D11 book in memory.
type fakeAsks struct {
	verdict tavern.AskVerdict
	err     error
	checks  []tavern.AskCheck
	opened  []tavern.OpenAsk
	answer  *tavern.AnsweredAsk
	closed  []string
}

func (f *fakeAsks) PreAsk(_ context.Context, in tavern.AskCheck) (tavern.AskVerdict, error) {
	f.checks = append(f.checks, in)
	return f.verdict, f.err
}
func (f *fakeAsks) AskOpened(_ context.Context, o tavern.OpenAsk) error {
	f.opened = append(f.opened, o)
	return nil
}
func (f *fakeAsks) AskAnswer(_ context.Context, decision, choice string) (*tavern.AnsweredAsk, error) {
	f.closed = append(f.closed, decision+"="+choice)
	return f.answer, nil
}

const draftAsk = "Draft is ready.\n\n```ask\n{\"question\": \"Can I write the launch post draft?\", \"options\": [\"Write it\", \"Wait\"], \"why\": \"the brief asks for it\", \"action\": \"draft\"}\n```"

// D11: an ask about the hireling's own action goes back to it — no card,
// a note on its own inbox so the next turn decides it.
func TestAskAboutOwnActionGoesBack(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	tr := &fakeTurner{out: draftAsk}
	l := newLoop(h, tr)
	l.Bus = newBar(t, "test")
	f := &fakeAsks{verdict: tavern.AskVerdict{Verdict: "yours", Level: "free", Reason: "free action"}}
	l.Asks = f
	l.Handle(ctx, frontdesk.Command{ID: "c1", Text: "prepare the launch post"})

	if len(s.decisions) != 0 || len(f.opened) != 0 {
		t.Fatalf("a card was opened: %v %v", s.decisions, f.opened)
	}
	if len(f.checks) != 1 || f.checks[0].Action != "draft" || f.checks[0].Turn != "c1" || f.checks[0].Blocked {
		t.Fatalf("checks = %+v", f.checks)
	}
	last := s.acks[len(s.acks)-1]
	if !strings.Contains(last["answer"], "Not sent to the owner: `draft` is test's own to decide") || strings.Contains(last["answer"], "```ask") {
		t.Fatalf("answer = %q", last["answer"])
	}
	res, err := l.Bus.C.XRange(ctx, bar.Inbox("test"), "-", "+").Result()
	if err != nil || len(res) != 1 {
		t.Fatalf("note to self: %v %v", res, err)
	}
	if txt, _ := res[0].Values["text"].(string); !strings.Contains(txt, "`draft` is yours to decide (free action)") || !strings.Contains(txt, "(Write it)") {
		t.Fatalf("note = %v", res[0].Values)
	}
}

// The card leads with the recommendation and is recorded for the counters.
func TestCardLeadsWithTheRecommendation(t *testing.T) {
	s, h := newServer(t)
	tr := &fakeTurner{out: "```ask\n{\"question\": \"Merge PR #7?\", \"options\": [\"Merge it\", \"Not now\"], \"why\": \"CI green, reviewed\", \"action\": \"merge_pr\", \"context\": \"2 files.\"}\n```"}
	l := newLoop(h, tr)
	f := &fakeAsks{verdict: tavern.AskVerdict{Verdict: "ask", Level: "owner"}}
	l.Asks = f
	l.Handle(context.Background(), frontdesk.Command{ID: "c1", Text: "ship PR 7"})
	if len(s.decisions) != 1 {
		t.Fatalf("decisions = %v", s.decisions)
	}
	if ctxText, _ := s.decisions[0]["context"].(string); ctxText != "Recommended: Merge it — CI green, reviewed\n\n2 files." {
		t.Fatalf("context = %q", ctxText)
	}
	if len(f.opened) != 1 || f.opened[0] != (tavern.OpenAsk{Decision: "d-1", Action: "merge_pr", Question: "Merge PR #7?", Recommended: "Merge it"}) {
		t.Fatalf("opened = %+v", f.opened)
	}
}

// One card per task: a second question on the task joins the open card.
func TestSecondAskOnTaskJoinsTheCard(t *testing.T) {
	s, h := newServer(t)
	l := newLoop(h, &fakeTurner{})
	f := &fakeAsks{verdict: tavern.AskVerdict{Verdict: "joined", Decision: "d-9"}}
	l.Asks = f
	out, waiting := l.applyAsk(context.Background(), "T-1", "msg-1", "```ask\n{\"question\": \"Also tag it?\", \"options\": [\"Tag\", \"Skip\"], \"action\": \"merge_pr\"}\n```", time.Now())
	if len(s.decisions) != 0 || waiting != "Also tag it?" || !strings.Contains(out, "task T-1 already has card d-9") {
		t.Fatalf("out %q waiting %q decisions %v", out, waiting, s.decisions)
	}
}

// tavern-core down: the card opens as before (when in doubt, the owner sees it).
func TestAskCheckErrorOpensTheCard(t *testing.T) {
	s, h := newServer(t)
	l := newLoop(h, &fakeTurner{out: draftAsk})
	l.Asks = &fakeAsks{err: errors.New("connection refused")}
	l.Handle(context.Background(), frontdesk.Command{ID: "c1", Text: "x"})
	if len(s.decisions) != 1 {
		t.Fatalf("decisions = %v", s.decisions)
	}
}

// the owner's answer closes the card and hands back the questions that joined it.
func TestAnswerCarriesTheFollowUps(t *testing.T) {
	_, h := newServer(t)
	tr := &fakeTurner{out: "done"}
	l := newLoop(h, tr)
	f := &fakeAsks{answer: &tavern.AnsweredAsk{Task: "T-1", FollowUps: []string{"Also tag it?"}, PickedRecommended: true}}
	l.Asks = f
	l.Handle(context.Background(), frontdesk.Command{ID: "c2", Text: "Merge it", DecisionID: "d-9"})
	if len(f.closed) != 1 || f.closed[0] != "d-9=Merge it" {
		t.Fatalf("closed = %v", f.closed)
	}
	if !strings.Contains(tr.seen, "you also asked, on the same task:\n- Also tag it?") {
		t.Fatalf("turn = %q", tr.seen)
	}
}
