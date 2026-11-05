package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
)

// fakeGrants is tavern-core's grants API in memory.
type fakeGrants struct {
	blocked []GateAct
	since   time.Time
	pending map[string][]GateAct
	approve map[string]string
	answers []string
}

func (f *fakeGrants) RecentBlocked(_ context.Context, since time.Time) ([]GateAct, error) {
	f.since = since
	var out []GateAct
	for _, a := range f.blocked {
		if !a.At.Before(since) {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeGrants) Pend(_ context.Context, decision string, acts []GateAct, approve string) error {
	f.pending[decision], f.approve[decision] = acts, approve
	return nil
}

func (f *fakeGrants) Answer(_ context.Context, decision, choice string) ([]GateAct, error) {
	f.answers = append(f.answers, decision+"="+choice)
	acts, ok := f.pending[decision]
	if !ok || choice != f.approve[decision] {
		return nil, nil
	}
	delete(f.pending, decision)
	return acts, nil
}

// D1 + D3: the gate blocks `merge_pr` during the turn, the hireling asks, the
// card says what a yes allows; the owner's yes comes back and the next turn is
// told it may now do exactly that.
func TestGateBlocksRideOnTheCard(t *testing.T) {
	s, h := newServer(t)
	ask := "PR #7 is green.\n\n```ask\n{\"question\": \"Merge PR #7 into main?\", \"options\": [\"Approve\", \"Not now\"], \"context\": \"CI green, 2 files.\"}\n```"
	tr := &fakeTurner{out: ask}
	l := newLoop(h, tr)
	g := &fakeGrants{pending: map[string][]GateAct{}, approve: map[string]string{}}
	l.Gate = g
	// Blocked one hour ago (another turn) and during this turn.
	g.blocked = []GateAct{
		{Action: "http_write", Target: "old", At: time.Now().Add(-time.Hour)},
		{Action: "merge_pr", Target: "gh pr merge 7 --squash", At: time.Now().Add(time.Second)},
	}
	l.Handle(context.Background(), frontdesk.Command{ID: "c1", Text: "merge PR 7 when CI is green"})

	if len(s.decisions) != 1 {
		t.Fatalf("decisions = %v", s.decisions)
	}
	ctxText, _ := s.decisions[0]["context"].(string)
	if !strings.Contains(ctxText, "Approving lets test do once: merge_pr (gh pr merge 7 --squash)") || strings.Contains(ctxText, "http_write") {
		t.Fatalf("card context = %q", ctxText)
	}
	if got := g.pending["d-1"]; len(got) != 1 || got[0].Action != "merge_pr" || g.approve["d-1"] != "Approve" {
		t.Fatalf("pending = %v approve %q", g.pending, g.approve["d-1"])
	}

	// the owner answers "Approve".
	tr.out = "Merged."
	l.Handle(context.Background(), frontdesk.Command{ID: "c2", Text: "Approve", DecisionID: "d-1"})
	if len(g.answers) != 1 || g.answers[0] != "d-1=Approve" {
		t.Fatalf("answers = %v", g.answers)
	}
	if !strings.Contains(tr.seen, "the autonomy gate now lets you do, once each: `merge_pr`") {
		t.Fatalf("the turn was not told about the grant: %q", tr.seen)
	}
}

// A card with no gate block is untouched, and "Not now" grants nothing.
func TestCardWithoutGateBlocks(t *testing.T) {
	s, h := newServer(t)
	tr := &fakeTurner{out: "```ask\n{\"question\": \"Publish Monday?\", \"options\": [\"Yes\", \"No\"], \"context\": \"draft ready\"}\n```"}
	l := newLoop(h, tr)
	g := &fakeGrants{pending: map[string][]GateAct{}, approve: map[string]string{}}
	l.Gate = g
	l.Handle(context.Background(), frontdesk.Command{ID: "c1", Text: "plan the post"})
	if ctxText, _ := s.decisions[0]["context"].(string); ctxText != "draft ready" || len(g.pending) != 0 {
		t.Fatalf("context %q, pending %v", ctxText, g.pending)
	}
	tr.out = "ok"
	l.Handle(context.Background(), frontdesk.Command{ID: "c2", Text: "No", DecisionID: "d-1"})
	if strings.Contains(tr.seen, "autonomy gate") {
		t.Fatalf("a no turned into a grant: %q", tr.seen)
	}
}

// D4: the runtime writes the running turn for the gate hook.
func TestTurnFileForTheGate(t *testing.T) {
	_, h := newServer(t)
	l := newLoop(h, &fakeTurner{out: "done"})
	l.GateTurnFile = filepath.Join(t.TempDir(), "gate-turn.json")
	l.Handle(context.Background(), frontdesk.Command{ID: "c-9", Text: "open a PR for the docs fix"})
	raw, err := os.ReadFile(l.GateTurnFile)
	if err != nil || !strings.Contains(string(raw), `"turn":"c-9"`) || !strings.Contains(string(raw), "open a PR for the docs fix") {
		t.Fatalf("turn file = %s (%v)", raw, err)
	}
}
