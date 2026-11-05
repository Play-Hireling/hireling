package loop

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

func escalateLoop(t *testing.T, tr *fakeTurner) (*Loop, *server, *fakeLeaser) {
	t.Helper()
	s, h := newServer(t)
	l := newLoop(h, tr)
	l.Profile.Runtime.EscalateAfter = 3
	l.Bus = newBar(t, "test")
	l.TurnsDir = t.TempDir()
	l.EscalationsFile = filepath.Join(t.TempDir(), "escalations.json")
	if err := l.Bus.EnsureGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := &fakeLeaser{tasks: map[string]*tavern.Task{
		"T-4": {ID: "T-4", Title: "Carousel for Monday", Owner: "test", Status: "queued", Priority: "P1", RequestedBy: "innkeeper"},
		"T-5": {ID: "T-5", Title: "Ad copy", Owner: "test", Status: "queued", Priority: "P2", RequestedBy: "innkeeper"},
	}}
	l.Leases = f
	return l, s, f
}

func inboxLen(t *testing.T, l *Loop) int64 {
	n, err := l.Bus.C.XLen(context.Background(), bar.Inbox("test")).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// B11 — two failures re-queue the request; the third opens the decision itself
// (Retry · Reassign · Drop · Change brief, urgency high for a P1).
func TestRepeatedFailureOpensDecision(t *testing.T) {
	ctx := context.Background()
	l, s, f := escalateLoop(t, &fakeTurner{err: errors.New("canva: 502 bad gateway")})
	req := bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-4", Text: "Build the carousel"}
	for i := 1; i <= 2; i++ {
		l.HandleMessage(ctx, req)
		if n := inboxLen(t, l); n != int64(i) {
			t.Fatalf("after failure %d the request was not put back (inbox %d)", i, n)
		}
		if len(s.decisions) != 0 {
			t.Fatalf("escalated too early: %v", s.decisions)
		}
	}
	l.HandleMessage(ctx, req)
	if got := f.seen(); !strings.HasSuffix(got, "lease T-4 | fail T-4 3") {
		t.Fatalf("calls = %s", got)
	}
	if inboxLen(t, l) != 2 {
		t.Fatal("the escalated task must not be retried again")
	}
	if len(s.decisions) != 1 {
		t.Fatalf("decisions = %v", s.decisions)
	}
	d := s.decisions[0]
	q, _ := d["question"].(string)
	if !strings.Contains(q, "Carousel for Monday") || !strings.Contains(q, "3 times") || !strings.Contains(q, "502") {
		t.Fatalf("question = %q", q)
	}
	if d["urgency"] != "high" || len(d["options"].([]any)) != 4 || d["from"] != "tester" {
		t.Fatalf("decision = %v", d)
	}
	if last := s.turns[len(s.turns)-1]; last.WaitingOnOwner == "" {
		t.Fatalf("the turn report must show Waiting on you: %+v", last)
	}
	raw, err := os.ReadFile(l.EscalationsFile)
	if err != nil || !strings.Contains(string(raw), `"task":"T-4"`) {
		t.Fatalf("escalations file = %s %v", raw, err)
	}

	// The owner chooses Retry: count reset, request back on the inbox with his
	// choice, no model turn spent on the answer itself.
	tr := l.Turner.(*fakeTurner)
	tr.seen = ""
	l.Handle(ctx, frontdesk.Command{ID: "c9", DecisionID: "d-1", Text: "Decision: " + q + " → chosen: Retry (decision d-1)"})
	if !strings.HasSuffix(f.seen(), "unblock T-4 retry") {
		t.Fatalf("calls = %s", f.seen())
	}
	if tr.seen != "" {
		t.Fatal("the answer must not run a model turn by itself")
	}
	msgs, err := l.Bus.C.XRevRangeN(ctx, bar.Inbox("test"), "+", "-", 1).Result()
	if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Values["text"].(string), "the owner answered the escalation of this task: Retry") {
		t.Fatalf("requeued = %v %v", msgs, err)
	}
	if ack := s.acks[len(s.acks)-1]; ack["result"] != "done" || !strings.Contains(ack["note"], "back in the queue") {
		t.Fatalf("ack = %v", ack)
	}
	if raw, _ := os.ReadFile(l.EscalationsFile); strings.Contains(string(raw), "T-4") {
		t.Fatal("answered escalation left in the file")
	}
}

// B11 — Drop cancels; an empty answer counts as a failure; threshold from the profile.
func TestEscalationDropAndEmptyAnswer(t *testing.T) {
	ctx := context.Background()
	l, s, f := escalateLoop(t, &fakeTurner{out: ""})
	l.Profile.Runtime.EscalateAfter = 1
	l.HandleMessage(ctx, bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-5", Text: "Write ad copy"})
	if len(s.decisions) != 1 || s.decisions[0]["urgency"] != "normal" {
		t.Fatalf("decisions = %v", s.decisions)
	}
	l.Handle(ctx, frontdesk.Command{ID: "c1", DecisionID: "d-1", Text: "Decision: x → chosen: Drop (decision d-1)"})
	if !strings.HasSuffix(f.seen(), "unblock T-5 drop") || f.tasks["T-5"].Status != "cancelled" {
		t.Fatalf("calls = %s, task %+v", f.seen(), f.tasks["T-5"])
	}
	// A decision that is not an escalation still runs as an ordinary command.
	tr := l.Turner.(*fakeTurner)
	tr.out = "Ok, publishing Monday."
	l.Handle(ctx, frontdesk.Command{ID: "c2", DecisionID: "d-77", Text: "Decision: publish? → chosen: Yes (decision d-77)"})
	if !strings.Contains(tr.seen, "publish?") {
		t.Fatalf("ordinary decision answer not turned: %q", tr.seen)
	}
}

func TestChosenOption(t *testing.T) {
	for in, want := range map[string]string{
		"Decision: X failed 3 times → chosen: Change brief (decision d-1)": "Change brief",
		"Decision: a → chosen: drop (decision 9)":                          "Drop",
		"": "Retry",
	} {
		if got := chosenOption(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
