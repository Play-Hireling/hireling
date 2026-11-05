package ledger

import (
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

func TestInArea(t *testing.T) {
	dev := []string{"learnings/dev/*"}
	read := []string{"goal", "learnings/*", "projects/*"}
	cases := []struct {
		areas []string
		page  string
		want  bool
	}{
		{dev, "learnings/dev/retry-on-502.md", true},
		{dev, "brain/learnings/dev/retry-on-502", true},
		{dev, "learnings/dev/sub/deep.md", true},
		{dev, "learnings/scriptwriter/hooks.md", false},
		{dev, "learnings/devops/x.md", false},
		{dev, "learnings/dev/../scriptwriter/x.md", false},
		{dev, "/learnings/dev/x.md", false},
		{dev, "learnings\\dev\\x.md", false},
		{dev, "goal.md", false},
		{read, "goal.md", true},
		{read, "goal", true},
		{read, "projects/hireling-launch.md", true},
		{read, "agents/dev.md", false},
		{nil, "goal.md", false},
		{dev, "", false},
	}
	for _, c := range cases {
		if got := InArea(c.areas, c.page); got != c.want {
			t.Errorf("InArea(%v, %q) = %v, want %v", c.areas, c.page, got, c.want)
		}
	}
}

// E2: every profile writes only its own learnings and reads inside the brain.
func TestProfilesWriteOnlyTheirLearnings(t *testing.T) {
	ps, err := profile.List("../../../agents")
	if err != nil || len(ps) == 0 {
		t.Fatalf("profiles: %v", err)
	}
	for _, p := range ps {
		for _, w := range p.Ledger.Write {
			if w != "learnings/"+p.ID+"/*" {
				t.Errorf("%s writes %q: only learnings/%s/* is allowed", p.ID, w, p.ID)
			}
		}
		if InArea(p.Ledger.Write, "goal.md") || InArea(p.Ledger.Write, "decisions/x.md") {
			t.Errorf("%s could write shared pages", p.ID)
		}
	}
}
