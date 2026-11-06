package ledgerwriter

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=seed", "GIT_AUTHOR_EMAIL=seed@x", "GIT_COMMITTER_NAME=seed", "GIT_COMMITTER_EMAIL=seed@x")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A bare "remote" with one seed commit, and a writer pointed at it.
func setup(t *testing.T) (*Writer, string) {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	run(t, root, "init", "--quiet", "--bare", "--initial-branch=main", bare)
	seed := filepath.Join(root, "seed")
	run(t, root, "clone", "--quiet", bare, seed)
	_ = os.MkdirAll(filepath.Join(seed, "brain", "learnings", "dev"), 0o755)
	_ = os.WriteFile(filepath.Join(seed, "brain", "learnings", "dev", "README.md"), []byte("# Dev learnings\n"), 0o644)
	run(t, seed, "add", "-A")
	run(t, seed, "commit", "--quiet", "-m", "seed")
	run(t, seed, "push", "--quiet", "origin", "HEAD:main")
	return &Writer{Dir: filepath.Join(root, "writer", "clone"), RepoURL: bare, AgentsDir: "../../../agents"}, bare
}

func learning(owner, signer string) string {
	return `---
title: Retry Canva on 502
type: learning
owner: ` + owner + `
tags: [learning, canva]
---

# Retry Canva on 502

> Canva's export returns 502 under load; one retry after 10 s works.

## State
- **Seen:** 3 times on carousel exports.

---

## Timeline
- **2026-11-04** | ` + signer + ` — Learned that one retry after 10 s fixes Canva 502s.
`
}

// E3 — a page in the hireling's own area becomes a commit authored by it.
func TestWriteCommitsAsTheHireling(t *testing.T) {
	w, bare := setup(t)
	res, err := w.Write(context.Background(), "dev", "learnings/dev/retry-canva-502.md", []byte(learning("dev", "dev")))
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != "brain/learnings/dev/retry-canva-502.md" || res.Author != "dev" || res.Commit == "" {
		t.Fatalf("result = %+v", res)
	}
	log := run(t, bare, "log", "-1", "--format=%an <%ae>|%cn|%s")
	if log != "dev <dev@hireling.tavern>|ledger-writer|learning(dev): Learned that one retry after 10 s fixes Canva 502s." {
		t.Fatalf("remote head = %q", log)
	}
	if got := run(t, bare, "show", "main:brain/learnings/dev/retry-canva-502.md"); !strings.Contains(got, "Retry Canva on 502") {
		t.Fatalf("page not in the remote: %q", got)
	}
	// Same content again: no new commit.
	again, err := w.Write(context.Background(), "dev", "learnings/dev/retry-canva-502", []byte(learning("dev", "dev")))
	if err != nil || again.Commit != res.Commit {
		t.Fatalf("rewrite made a commit: %+v %v", again, err)
	}
}

// E3 — someone else pushed first: the writer replays on the new tip.
func TestWriteRebasesOnANewerRemote(t *testing.T) {
	w, bare := setup(t)
	if _, err := w.Write(context.Background(), "dev", "learnings/dev/a.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "clone", "--quiet", bare, other)
	_ = os.WriteFile(filepath.Join(other, "brain", "goal.md"), []byte("# goal\n"), 0o644)
	run(t, other, "add", "-A")
	run(t, other, "commit", "--quiet", "-m", "owner edits the goal")
	run(t, other, "push", "--quiet", "origin", "HEAD:main")
	if _, err := w.Write(context.Background(), "dev", "learnings/dev/b.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatal(err)
	}
	if subjects := run(t, bare, "log", "--format=%s", "-3"); !strings.Contains(subjects, "owner edits the goal") || strings.Count(subjects, "learning(dev)") != 2 {
		t.Fatalf("history = %q", subjects)
	}
}

// E3 — someone pushes between the writer's fetch and its push: the write is
// replayed once on the new tip instead of failing. A pre-push hook in the
// writer's clone plays the other writer, once.
func TestWriteReplaysARacingPush(t *testing.T) {
	w, bare := setup(t)
	ctx := context.Background()
	if _, err := w.Write(ctx, "dev", "learnings/dev/a.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "clone", "--quiet", bare, other)
	mark := filepath.Join(t.TempDir(), "raced")
	hook := "#!/bin/sh\n" +
		"[ -f '" + filepath.ToSlash(mark) + "' ] && exit 0\n" +
		"touch '" + filepath.ToSlash(mark) + "'\n" +
		"unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX\n" +
		"cd '" + filepath.ToSlash(other) + "' || exit 1\n" +
		"git -c user.name=racer -c user.email=racer@x commit --quiet --allow-empty -m 'racing push' && git push --quiet origin HEAD:main\n"
	if err := os.WriteFile(filepath.Join(w.Dir, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(ctx, "dev", "learnings/dev/b.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatalf("write after a racing push: %v", err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("the racing push never ran")
	}
	subjects := run(t, bare, "log", "--format=%s", "-3")
	if !strings.Contains(subjects, "racing push") || strings.Count(subjects, "learning(dev)") != 2 {
		t.Fatalf("history = %q", subjects)
	}
}

// E3 — the repo moves between organisations: an existing clone follows RepoURL
// instead of keeping a stale origin and failing to fetch.
func TestEnsureFollowsAMovedRepo(t *testing.T) {
	w, first := setup(t)
	if _, err := w.Write(context.Background(), "dev", "learnings/dev/a.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(t.TempDir(), "moved.git")
	run(t, filepath.Dir(moved), "clone", "--quiet", "--bare", first, moved)
	w.RepoURL = moved
	if _, err := w.Write(context.Background(), "dev", "learnings/dev/b.md", []byte(learning("dev", "dev"))); err != nil {
		t.Fatal(err)
	}
	if origin := run(t, w.Dir, "remote", "get-url", "origin"); origin != moved {
		t.Fatalf("origin = %q, want %q", origin, moved)
	}
	if got := run(t, moved, "show", "main:brain/learnings/dev/b.md"); !strings.Contains(got, "Retry Canva on 502") {
		t.Fatalf("the page did not reach the moved repo: %q", got)
	}
}

// E2/E3 — the checks that keep the Ledger honest.
func TestWriteRefusals(t *testing.T) {
	w, _ := setup(t)
	ctx := context.Background()
	cases := []struct {
		id, path, content string
		want              error
	}{
		{"dev", "learnings/scriptwriter/x.md", learning("dev", "dev"), ErrNotYourArea},
		{"dev", "goal.md", learning("dev", "dev"), ErrNotYourArea},
		{"dev", "learnings/dev/../scriptwriter/x.md", learning("dev", "dev"), ErrNotYourArea},
		{"dev", "learnings/dev/README.md", learning("dev", "dev"), ErrNotYourArea},
		{"dev", "learnings/dev/x.md", learning("scriptwriter", "dev"), ErrInvalidPage},
		{"dev", "learnings/dev/x.md", learning("dev", "scriptwriter"), ErrInvalidPage},
		{"dev", "learnings/dev/x.md", strings.Replace(learning("dev", "dev"), "[learning, canva]", "[learning, kyc]", 1), ErrInvalidPage},
		{"dev", "learnings/dev/x.md", "# no frontmatter\n", ErrInvalidPage},
		{"nobody", "learnings/nobody/x.md", learning("nobody", "nobody"), ErrUnknownAgent},
		{"../dev", "learnings/dev/x.md", learning("dev", "dev"), ErrUnknownAgent},
	}
	for _, c := range cases {
		if _, err := w.Write(ctx, c.id, c.path, []byte(c.content)); err == nil || !strings.Contains(err.Error(), c.want.Error()) {
			t.Errorf("%s %s: err = %v, want %v", c.id, c.path, err, c.want)
		}
	}
}

// HTTP: the caller's identity comes from the network, never from the body.
func TestHandler(t *testing.T) {
	w, _ := setup(t)
	who := "dev"
	w.Identify = func(context.Context, string) string { return who }
	s := httptest.NewServer(w.Handler())
	defer s.Close()
	post := func(body any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		res, err := http.Post(s.URL+"/pages", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, out := post(map[string]string{"path": "learnings/dev/h.md", "content": learning("dev", "dev")}); code != 200 || out["author"] != "dev" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := post(map[string]string{"path": "learnings/dev/h.md", "content": learning("dev", "dev"), "as": "innkeeper"}); code != 400 {
		t.Fatalf("unknown fields must be refused, got %d", code)
	}
	who = "scriptwriter"
	if code, _ := post(map[string]string{"path": "learnings/dev/h2.md", "content": learning("dev", "dev")}); code != 403 {
		t.Fatalf("another hireling wrote dev's area: %d", code)
	}
	who = ""
	if code, _ := post(map[string]string{"path": "learnings/dev/h3.md", "content": learning("dev", "dev")}); code != 403 {
		t.Fatalf("unidentified caller: %d", code)
	}
}

func TestAgentFromName(t *testing.T) {
	for in, want := range map[string]string{
		"example-agent-dev-1.example_tavern.":          "dev",
		"example-agent-social-media-1.example_tavern.": "social-media",
		"example-gbrain-1.example_tavern.":             "",
	} {
		if got := AgentFromName(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
