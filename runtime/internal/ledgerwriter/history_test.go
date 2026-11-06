package ledgerwriter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E6 — a change is in the history and a revert undoes it with a new commit.
func TestHistoryAndRevert(t *testing.T) {
	w, bare := setup(t)
	ctx := context.Background()
	first := learning("dev", "dev")
	one, err := w.Write(ctx, "dev", "learnings/dev/retry-canva-502.md", []byte(first))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.Replace(first, "one retry after 10 s works", "two retries after 30 s work", 1)
	two, err := w.Write(ctx, "dev", "learnings/dev/retry-canva-502.md", []byte(second))
	if err != nil {
		t.Fatal(err)
	}

	hist, err := w.History(ctx, "dev", "learnings/dev/retry-canva-502.md", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history = %d commits, want 2: %+v", len(hist), hist)
	}
	if !strings.HasPrefix(hist[0].Commit, two.Commit) || !strings.HasPrefix(hist[1].Commit, one.Commit) {
		t.Fatalf("history order = %s, %s (want %s, %s)", hist[0].Commit, hist[1].Commit, two.Commit, one.Commit)
	}
	if hist[0].Author != "dev" || !strings.HasPrefix(hist[0].Subject, "learning(dev):") {
		t.Fatalf("newest commit = %+v", hist[0])
	}
	if len(hist[0].Paths) != 1 || hist[0].Paths[0] != "brain/learnings/dev/retry-canva-502.md" {
		t.Fatalf("paths = %v", hist[0].Paths)
	}

	// No page filter: a hireling sees its own area (the two writes and the
	// seed's README).
	if mine, err := w.History(ctx, "dev", "", 0); err != nil || len(mine) != 3 {
		t.Fatalf("own history = %d, %v", len(mine), err)
	}

	res, err := w.Revert(ctx, "dev", two.Commit, "the retry window was wrong")
	if err != nil {
		t.Fatal(err)
	}
	if res.Author != "dev" || res.Path != "brain/learnings/dev/retry-canva-502.md" || res.Commit == "" {
		t.Fatalf("revert result = %+v", res)
	}
	got := run(t, bare, "show", "main:brain/learnings/dev/retry-canva-502.md")
	if !strings.Contains(got, "one retry after 10 s works") || strings.Contains(got, "two retries after 30 s work") {
		t.Fatalf("page after revert = %q", got)
	}
	log := run(t, bare, "log", "-1", "--format=%an|%cn|%s")
	if !strings.HasPrefix(log, "dev|ledger-writer|revert(dev): undo learning(dev)") {
		t.Fatalf("revert commit = %q", log)
	}
	if body := run(t, bare, "log", "-1", "--format=%B"); !strings.Contains(body, "Reverts: ") || !strings.Contains(body, "Reason: the retry window was wrong") {
		t.Fatalf("revert message = %q", body)
	}
	if after, err := w.History(ctx, "dev", "learnings/dev/retry-canva-502.md", 10); err != nil || len(after) != 3 {
		t.Fatalf("history after revert = %d, %v", len(after), err)
	}
}

// E6 — a hireling undoes only its own area; a planner curates the brain;
// nothing outside brain/ is ever reverted.
func TestRevertPermissions(t *testing.T) {
	w, bare := setup(t)
	ctx := context.Background()
	mine, err := w.Write(ctx, "dev", "learnings/dev/a.md", []byte(learning("dev", "dev")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Revert(ctx, "scriptwriter", mine.Commit, ""); !errors.Is(err, ErrNotYourArea) {
		t.Fatalf("another hireling reverted dev's page: %v", err)
	}

	// The planner may revert anyone's change.
	if _, err := w.Revert(ctx, "innkeeper", mine.Commit, "curating the brain"); err != nil {
		t.Fatal(err)
	}
	if out := run(t, bare, "ls-tree", "-r", "--name-only", "main"); strings.Contains(out, "brain/learnings/dev/a.md") {
		t.Fatalf("page survived the revert: %q", out)
	}

	// A commit outside brain/ (a rogue push) is never reverted by the writer.
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "clone", "--quiet", bare, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# brain\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, other, "add", "-A")
	run(t, other, "commit", "--quiet", "-m", "rogue edit")
	run(t, other, "push", "--quiet", "origin", "HEAD:main")
	rogue := run(t, other, "rev-parse", "HEAD")
	if _, err := w.Revert(ctx, "innkeeper", rogue, ""); !errors.Is(err, ErrNotRevertable) {
		t.Fatalf("reverted a commit outside brain/: %v", err)
	}
}

// E6 — unknown commits are refused, and a revert of a revert is reported as a
// conflict instead of a silent empty commit.
func TestRevertErrors(t *testing.T) {
	w, _ := setup(t)
	ctx := context.Background()
	if _, err := w.Revert(ctx, "dev", "deadbeef", ""); !errors.Is(err, ErrUnknownCommit) {
		t.Fatalf("unknown commit: %v", err)
	}
	if _, err := w.Revert(ctx, "dev", "not-a-sha", ""); !errors.Is(err, ErrUnknownCommit) {
		t.Fatalf("malformed sha: %v", err)
	}
	res, err := w.Write(ctx, "dev", "learnings/dev/b.md", []byte(learning("dev", "dev")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Revert(ctx, "dev", res.Commit, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Revert(ctx, "dev", res.Commit, ""); !errors.Is(err, ErrRevertConflict) {
		t.Fatalf("second revert: %v", err)
	}
}

// HTTP: GET /history and POST /revert, with the caller from the network.
func TestHistoryAndRevertHandler(t *testing.T) {
	w, _ := setup(t)
	who := "dev"
	w.Identify = func(context.Context, string) string { return who }
	s := httptest.NewServer(w.Handler())
	defer s.Close()

	post := func(path string, body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		res, err := http.Post(s.URL+path, "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, out := post("/pages", map[string]string{"path": "learnings/dev/h.md", "content": learning("dev", "dev")}); code != 200 {
		t.Fatalf("write: %d %v", code, out)
	}
	if code, out := post("/pages", map[string]string{"path": "learnings/dev/h.md", "content": strings.Replace(learning("dev", "dev"), "one retry", "two retries", 1)}); code != 200 {
		t.Fatalf("second write: %d %v", code, out)
	}

	get := func(q string) (int, map[string]any) {
		res, err := http.Get(s.URL + "/history?" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	code, out := get("path=" + url.QueryEscape("learnings/dev/h.md"))
	if code != 200 {
		t.Fatalf("history: %d %v", code, out)
	}
	commits, _ := out["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("history = %v", out)
	}
	last := commits[0].(map[string]any)["commit"].(string)

	code, out = post("/revert", map[string]string{"commit": last, "reason": "wrong"})
	if code != 200 || out["author"] != "dev" {
		t.Fatalf("revert: %d %v", code, out)
	}
	if code, _ := post("/revert", map[string]string{"commit": "deadbeef"}); code != 404 {
		t.Fatalf("unknown commit: %d", code)
	}
	if code, _ := post("/revert", map[string]string{"commit": last, "as": "innkeeper"}); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	who = ""
	if code, _ := get(""); code != 403 {
		t.Fatalf("unidentified history: %d", code)
	}
	if code, _ := post("/revert", map[string]string{"commit": last}); code != 403 {
		t.Fatalf("unidentified revert: %d", code)
	}
}

// A revert considers both sides of a rename: a commit that moved a page from
// another hireling's area into the caller's is not the caller's to undo, since
// the undo would recreate the page outside its area.
func TestRevertRenameCannotEscapeArea(t *testing.T) {
	w, bare := setup(t)
	ctx := context.Background()
	seed := filepath.Join(t.TempDir(), "s")
	run(t, filepath.Dir(seed), "clone", "--quiet", bare, seed)
	if err := os.MkdirAll(filepath.Join(seed, "brain", "learnings", "social-media"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(seed, "brain", "learnings", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(seed, "brain", "learnings", "social-media", "x.md")
	if err := os.WriteFile(page, []byte(learning("social-media", "social-media")), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "add", "-A")
	run(t, seed, "commit", "--quiet", "-m", "add page")
	run(t, seed, "mv", "brain/learnings/social-media/x.md", "brain/learnings/dev/x.md")
	run(t, seed, "commit", "--quiet", "-m", "move to dev")
	run(t, seed, "push", "--quiet", "origin", "HEAD:main")

	hist, err := w.History(ctx, "dev", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	var mv *Commit
	for i := range hist {
		if hist[i].Subject == "move to dev" {
			mv = &hist[i]
		}
	}
	if mv == nil {
		t.Fatalf("the move is not in dev's history: %+v", hist)
	}
	// The history shows both sides of the move.
	want := map[string]bool{"brain/learnings/social-media/x.md": true, "brain/learnings/dev/x.md": true}
	for _, p := range mv.Paths {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("history paths = %v, missing %v", mv.Paths, want)
	}

	if _, err := w.Revert(ctx, "dev", mv.Commit, "undo the move"); !errors.Is(err, ErrNotYourArea) {
		t.Fatalf("dev reverted a move out of social-media's area: err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Dir, "brain", "learnings", "social-media", "x.md")); err == nil {
		t.Fatal("the refused revert recreated social-media's page")
	}
	if out := run(t, w.Dir, "status", "--porcelain"); strings.TrimSpace(out) != "" {
		t.Fatalf("clone left dirty: %q", out)
	}
	// A planner curates any page of the brain, so it may undo the move.
	if _, err := w.Revert(ctx, "innkeeper", mv.Commit, "undo the move"); err != nil {
		t.Fatalf("planner revert: %v", err)
	}
}

// A non-planner's history names only the files it may read or write; the
// others of the same commit are counted, not named. A planner sees them all.
func TestHistoryHidesOtherAreas(t *testing.T) {
	w, bare := setup(t)
	ctx := context.Background()
	seed := filepath.Join(t.TempDir(), "s")
	run(t, filepath.Dir(seed), "clone", "--quiet", bare, seed)
	for _, d := range []string{"learnings/dev", "private"} {
		if err := os.MkdirAll(filepath.Join(seed, "brain", filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(seed, "brain", "learnings", "dev", "y.md"), []byte(learning("dev", "dev")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "brain", "private", "salary-negotiation.md"), []byte("# notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, seed, "add", "-A")
	run(t, seed, "commit", "--quiet", "-m", "two areas")
	run(t, seed, "push", "--quiet", "origin", "HEAD:main")

	find := func(hist []Commit) Commit {
		t.Helper()
		for _, c := range hist {
			if c.Subject == "two areas" {
				return c
			}
		}
		t.Fatalf("commit not listed: %+v", hist)
		return Commit{}
	}
	hist, err := w.History(ctx, "dev", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if c := find(hist); len(c.Paths) != 1 || c.Paths[0] != "brain/learnings/dev/y.md" || c.Hidden != 1 {
		t.Fatalf("dev sees %v (hidden %d)", c.Paths, c.Hidden)
	}
	hist, err = w.History(ctx, "innkeeper", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if c := find(hist); len(c.Paths) != 2 || c.Hidden != 0 {
		t.Fatalf("the planner sees %v (hidden %d)", c.Paths, c.Hidden)
	}
}
