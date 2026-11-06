// E6: every change in the Ledger is a commit, so every change can be seen in
// the history and undone by a new commit. The writer owns the repo token, so
// it is also the place that decides who may revert what: a hireling undoes
// changes inside its own ledger.write areas, a planner (the Innkeeper) curates
// any page of the brain, and nothing outside brain/ is ever touched.
package ledgerwriter

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Play-Hireling/hireling/runtime/internal/ledger"
	"github.com/Play-Hireling/hireling/runtime/profile"
)

const (
	// DefaultHistory and MaxHistory bound GET /history.
	DefaultHistory = 20
	MaxHistory     = 100
)

// Commit is one Ledger change as GET /history returns it.
type Commit struct {
	Commit  string   `json:"commit"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Paths   []string `json:"paths"`
	// Hidden counts the files of the commit outside the caller's ledger
	// areas: a non-planner sees that they exist, not their names.
	Hidden int `json:"hidden,omitempty"`
}

// Error kinds the handler maps to HTTP codes (E6).
var (
	ErrUnknownCommit  = errors.New("ledger writer: unknown commit")
	ErrNotRevertable  = errors.New("ledger writer: this change cannot be reverted")
	ErrRevertConflict = errors.New("ledger writer: the revert does not apply cleanly")
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// profileOf loads the caller's profile; the id comes from the network.
func (w *Writer) profileOf(id string) (*profile.Profile, error) {
	if !idRe.MatchString(id) {
		return nil, ErrUnknownAgent
	}
	p, err := profile.Load(filepath.Join(w.agentsDir(), id))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	return p, nil
}

func (w *Writer) agentsDir() string {
	if w.AgentsDir == "" {
		return "agents"
	}
	return w.AgentsDir
}

// pagePath normalizes a page from the body or the query to a brain-relative
// path ("learnings/dev/x.md"); "" means "no page filter".
func pagePath(page string) string {
	rel := strings.TrimSpace(page)
	rel = strings.TrimPrefix(rel, "brain/")
	if rel != "" && !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	return rel
}

// historyPaths is the git pathspec a caller may list: one page it reads, or —
// without a page — the whole brain for a planner and its own area for anyone
// else.
func historyPaths(p *profile.Profile, rel string) ([]string, error) {
	if rel != "" {
		if !ledger.InArea(p.Ledger.Read, rel) && !ledger.InArea(p.Ledger.Write, rel) {
			return nil, fmt.Errorf("%w: %s may read %s, not %q", ErrNotYourArea, p.ID, strings.Join(p.Ledger.Read, ", "), rel)
		}
		return []string{"brain/" + rel}, nil
	}
	if p.Planner {
		return []string{"brain/"}, nil
	}
	return []string{"brain/learnings/" + p.ID + "/"}, nil
}

// History lists the commits that changed a page (or the caller's area).
// Each commit lists the files it touched that the caller may read or write
// (a planner sees all), so a move into the area shows where the page came
// from (and why a revert of it may be refused); the other files are only
// counted (Hidden).
func (w *Writer) History(ctx context.Context, id, page string, limit int) ([]Commit, error) {
	p, err := w.profileOf(id)
	if err != nil {
		return nil, err
	}
	rel := pagePath(page)
	paths, err := historyPaths(p, rel)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultHistory
	}
	if limit > MaxHistory {
		limit = MaxHistory
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensure(ctx); err != nil {
		return nil, err
	}
	args := []string{"log", fmt.Sprintf("-n%d", limit), "--name-only", "--no-renames", "--full-diff", "--format=%x1e%H%x1f%an%x1f%aI%x1f%s", "--"}
	args = append(args, paths...)
	out, err := w.git(ctx, args...)
	if err != nil {
		return nil, err
	}
	commits := parseLog(out)
	if !p.Planner {
		for i := range commits {
			commits[i].Paths, commits[i].Hidden = visiblePaths(p, commits[i].Paths)
		}
	}
	return commits, nil
}

// visiblePaths keeps the files a non-planner may read or write and counts the
// rest: the history of its own area must not list the names of pages it
// cannot read.
func visiblePaths(p *profile.Profile, paths []string) ([]string, int) {
	keep, hidden := []string{}, 0
	for _, f := range paths {
		rel, ok := strings.CutPrefix(f, "brain/")
		if ok && (ledger.InArea(p.Ledger.Read, rel) || ledger.InArea(p.Ledger.Write, rel)) {
			keep = append(keep, f)
			continue
		}
		hidden++
	}
	return keep, hidden
}

// parseLog reads `git log --name-only --no-renames --full-diff` (every file of
// each listed commit, both sides of a rename) with \x1e between commits and
// \x1f between fields (the same separators the command asks for).
func parseLog(out string) []Commit {
	commits := []Commit{}
	for _, block := range strings.Split(out, "\x1e") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		fields := strings.SplitN(lines[0], "\x1f", 4)
		if len(fields) != 4 {
			continue
		}
		c := Commit{Commit: fields[0], Author: fields[1], Date: fields[2], Subject: fields[3], Paths: []string{}}
		for _, l := range lines[1:] {
			if l = strings.TrimSpace(l); l != "" {
				c.Paths = append(c.Paths, l)
			}
		}
		commits = append(commits, c)
	}
	return commits
}

// commitPaths is the list of files one commit touched. --no-renames lists
// both sides of a rename (a move is a deletion plus an addition), so the area
// check sees the path a revert would recreate, not only the destination.
func (w *Writer) commitPaths(ctx context.Context, sha string) ([]string, error) {
	out, err := w.git(ctx, "show", "--name-only", "--no-renames", "--format=", sha)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// Revert undoes one commit with a new commit authored by the asking hireling,
// then pushes. It refuses merges, commits that touch anything outside brain/
// and pages outside the caller's write areas (unless it plans).
func (w *Writer) Revert(ctx context.Context, id, commit, reason string) (*Result, error) {
	p, err := w.profileOf(id)
	if err != nil {
		return nil, err
	}
	commit = strings.TrimSpace(commit)
	if !shaRe.MatchString(commit) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownCommit, commit)
	}
	reason = oneLine(reason, 160)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensure(ctx); err != nil {
		return nil, err
	}
	sha, err := w.git(ctx, "rev-parse", "--verify", commit+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownCommit, commit)
	}
	sha = strings.TrimSpace(sha)
	if _, err := w.git(ctx, "merge-base", "--is-ancestor", sha, "HEAD"); err != nil {
		return nil, fmt.Errorf("%w: %s is not on this branch", ErrUnknownCommit, commit)
	}
	line, err := w.git(ctx, "rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return nil, err
	}
	if parents := strings.Fields(line); len(parents) > 2 {
		return nil, fmt.Errorf("%w: %s is a merge", ErrNotRevertable, commit)
	}
	files, err := w.commitPaths(ctx, sha)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: %s changes no file", ErrNotRevertable, commit)
	}
	for _, f := range files {
		if !strings.HasPrefix(f, "brain/") {
			return nil, fmt.Errorf("%w: %s also changes %s, outside the Ledger", ErrNotRevertable, commit, f)
		}
		rel := strings.TrimPrefix(f, "brain/")
		if !p.Planner && !ledger.InArea(p.Ledger.Write, rel) {
			return nil, fmt.Errorf("%w: %s may write %s, not %q", ErrNotYourArea, id, strings.Join(p.Ledger.Write, ", "), rel)
		}
	}

	if _, err := w.git(ctx, "revert", "--no-commit", sha); err != nil {
		_, _ = w.git(ctx, "revert", "--abort")
		return nil, fmt.Errorf("%w: %s", ErrRevertConflict, oneLine(err.Error(), 200))
	}
	if _, err := w.git(ctx, "diff", "--cached", "--quiet"); err == nil {
		// The tree already looks like the revert (it was reverted before):
		// nothing to commit, leave the checkout clean.
		_, _ = w.git(ctx, "reset", "--quiet", "--hard", "HEAD")
		return nil, fmt.Errorf("%w: %s has already been reverted", ErrRevertConflict, commit)
	}

	subject, _ := w.git(ctx, "log", "-1", "--format=%s", sha)
	domain := w.Domain
	if domain == "" {
		domain = "hireling.tavern"
	}
	author := fmt.Sprintf("%s <%s@%s>", id, id, domain)
	msg := fmt.Sprintf("revert(%s): undo %s\n\nReverts: %s\nReverted by %s through the ledger writer (E6).", id, oneLine(subject, 60), sha, id)
	if reason != "" {
		msg += "\nReason: " + reason
	}
	if _, err := w.git(ctx, "-c", "user.name=ledger-writer", "-c", "user.email=ledger-writer@"+domain,
		"commit", "--quiet", "--author", author, "-m", msg); err != nil {
		_, _ = w.git(ctx, "reset", "--quiet", "--hard", "HEAD")
		return nil, err
	}
	if err := w.push(ctx); err != nil {
		return nil, err
	}
	newSha, _ := w.git(ctx, "rev-parse", "--short", "HEAD")
	w.logf("%s reverted %s (%s)", id, sha[:7], strings.TrimSpace(newSha))
	return &Result{Path: files[0], Commit: strings.TrimSpace(newSha), Author: id}, nil
}
