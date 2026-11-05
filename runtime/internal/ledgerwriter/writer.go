// Package ledgerwriter is E3: the only path by which a hireling's page enters
// the Tavern Ledger. A hireling's runtime posts the page; the writer finds out
// which hireling is calling from the network (the container name, as the
// egress proxy does — a runtime cannot claim to be another hireling), checks
// the page against that hireling's profile (ledger.write areas, owner, the
// page grammar and the sensitive-data rule) and commits it to the brain repo
// with the hireling as the author, then pushes. GBrain indexes the commit on
// its next sync. Only this service holds the repo token.
package ledgerwriter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/ledger"
	"github.com/Play-Hireling/hireling/runtime/profile"
)

// MaxPage bounds one page (a learning is a page, not a document dump).
const MaxPage = 64 * 1024

// Writer commits hirelings' pages to a clone of the brain repo.
type Writer struct {
	Dir       string // the clone (created on first use)
	RepoURL   string // https://github.com/<owner>/hireling-ledger.git
	Token     string // fine-grained token, Contents on that repo only; never logged
	Branch    string // default "main"
	AgentsDir string // profiles (agents/<id>/profile.yaml)
	Domain    string // author e-mail domain, default "hireling.tavern"
	// Identify returns the hireling id behind a remote address ("" = unknown).
	Identify func(ctx context.Context, remoteAddr string) string
	Log      *log.Logger
	Now      func() time.Time

	mu sync.Mutex
}

// Result is what a write returns to the hireling.
type Result struct {
	Path   string `json:"path"`
	Commit string `json:"commit"`
	Author string `json:"author"`
}

// Error kinds the handler maps to HTTP codes.
var (
	ErrUnknownAgent = errors.New("ledger writer: caller is not a known hireling")
	ErrNotYourArea  = errors.New("ledger writer: outside this hireling's ledger.write areas")
	ErrInvalidPage  = errors.New("ledger writer: invalid page")
)

func (w *Writer) logf(format string, a ...any) {
	if w.Log != nil {
		w.Log.Printf(format, a...)
	}
}

func (w *Writer) branch() string {
	if w.Branch == "" {
		return "main"
	}
	return w.Branch
}

// git runs git in the clone with the token from memory only (credential
// helper reading the environment of this one command).
func (w *Writer) git(ctx context.Context, args ...string) (string, error) {
	return w.gitIn(ctx, w.Dir, args...)
}

func (w *Writer) gitIn(ctx context.Context, dir string, args ...string) (string, error) {
	full := []string{"-c", "credential.helper="}
	if w.Token != "" {
		full = append(full, "-c", "credential.helper=!f() { echo username=x-access-token; echo \"password=$LEDGER_GIT_TOKEN\"; }; f")
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LEDGER_GIT_TOKEN="+w.Token)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if w.Token != "" {
		s = strings.ReplaceAll(s, w.Token, "[hidden]")
	}
	if err != nil {
		return s, fmt.Errorf("git %s: %v: %s", args[0], err, tail(s, 300))
	}
	return s, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// ensure clones the repo on first use, keeps the clone's origin in step with
// RepoURL (the repo may move between organisations) and brings the clone to
// the remote tip.
func (w *Writer) ensure(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(w.Dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(w.Dir), 0o755); err != nil {
			return err
		}
		if _, err := w.gitIn(ctx, filepath.Dir(w.Dir), "clone", "--quiet", "--branch", w.branch(), w.RepoURL, w.Dir); err != nil {
			return err
		}
	} else if w.RepoURL != "" {
		origin, err := w.git(ctx, "remote", "get-url", "origin")
		if err != nil {
			return err
		}
		if strings.TrimSpace(origin) != w.RepoURL {
			w.logf("origin moved from %s to %s", strings.TrimSpace(origin), w.RepoURL)
			if _, err := w.git(ctx, "remote", "set-url", "origin", w.RepoURL); err != nil {
				return err
			}
		}
	}
	if _, err := w.git(ctx, "fetch", "--quiet", "origin", w.branch()); err != nil {
		return err
	}
	_, err := w.git(ctx, "reset", "--quiet", "--hard", "origin/"+w.branch())
	return err
}

var (
	pageName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*\.md$`)
	idRe     = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Check is everything but git: the page may be written by this hireling.
func (w *Writer) Check(id, page string, content []byte) (*profile.Profile, string, error) {
	if !idRe.MatchString(id) {
		return nil, "", ErrUnknownAgent
	}
	p, err := profile.Load(filepath.Join(w.AgentsDir, id))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %s", ErrUnknownAgent, id)
	}
	rel := strings.TrimPrefix(strings.TrimSpace(page), "brain/")
	if !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	if !ledger.InArea(p.Ledger.Write, rel) || !pageName.MatchString(filepath.Base(rel)) || strings.EqualFold(filepath.Base(rel), "readme.md") {
		return nil, "", fmt.Errorf("%w: %s may write %s, not %q", ErrNotYourArea, id, strings.Join(p.Ledger.Write, ", "), page)
	}
	if len(content) == 0 || len(content) > MaxPage {
		return nil, "", fmt.Errorf("%w: a page is 1 byte to %d KB", ErrInvalidPage, MaxPage/1024)
	}
	pg, err := ledger.Parse(content)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidPage, err)
	}
	if !pg.IsPage() {
		return nil, "", fmt.Errorf("%w: frontmatter needs a type (an index is not a page)", ErrInvalidPage)
	}
	if err := pg.Validate(); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalidPage, err)
	}
	if pg.Owner != id {
		return nil, "", fmt.Errorf("%w: owner must be %q (the hireling writing it), not %q", ErrInvalidPage, id, pg.Owner)
	}
	if pg.Timeline[0].Source != id {
		return nil, "", fmt.Errorf("%w: the newest timeline line must be signed by %q, not %q", ErrInvalidPage, id, pg.Timeline[0].Source)
	}
	return p, rel, nil
}

// Write checks and commits one page as the hireling id, then pushes.
func (w *Writer) Write(ctx context.Context, id, page string, content []byte) (*Result, error) {
	_, rel, err := w.Check(id, page, content)
	if err != nil {
		return nil, err
	}
	pg, _ := ledger.Parse(content)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensure(ctx); err != nil {
		return nil, err
	}
	dst := filepath.Join(w.Dir, "brain", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	if old, err := os.ReadFile(dst); err == nil && string(old) == string(content) {
		sha, _ := w.git(ctx, "rev-parse", "--short", "HEAD")
		return &Result{Path: "brain/" + rel, Commit: sha, Author: id}, nil
	}
	if err := os.WriteFile(dst, content, 0o644); err != nil {
		return nil, err
	}
	domain := w.Domain
	if domain == "" {
		domain = "hireling.tavern"
	}
	author := fmt.Sprintf("%s <%s@%s>", id, id, domain)
	msg := fmt.Sprintf("%s(%s): %s\n\nPage: brain/%s\nWritten by the hireling %s through the ledger writer (E3).",
		pg.Type, id, oneLine(pg.Timeline[0].Summary, 72), rel, id)
	if _, err := w.git(ctx, "add", "--", filepath.ToSlash(filepath.Join("brain", rel))); err != nil {
		return nil, err
	}
	if _, err := w.git(ctx, "-c", "user.name=ledger-writer", "-c", "user.email=ledger-writer@"+domain,
		"commit", "--quiet", "--author", author, "-m", msg); err != nil {
		return nil, err
	}
	if _, err := w.git(ctx, "push", "--quiet", "origin", "HEAD:"+w.branch()); err != nil {
		// Someone (the owner, another writer) pushed first: replay once on the new tip.
		if _, ferr := w.git(ctx, "pull", "--quiet", "--rebase", "origin", w.branch()); ferr != nil {
			_, _ = w.git(ctx, "rebase", "--abort")
			return nil, err
		}
		if _, err := w.git(ctx, "push", "--quiet", "origin", "HEAD:"+w.branch()); err != nil {
			return nil, err
		}
	}
	sha, _ := w.git(ctx, "rev-parse", "--short", "HEAD")
	w.logf("%s wrote brain/%s (%s)", id, rel, sha)
	return &Result{Path: "brain/" + rel, Commit: sha, Author: id}, nil
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Handler serves POST /pages {path, content} and GET /health.
func (w *Writer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("POST /pages", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(rw, r.Body, MaxPage+4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeJSON(rw, http.StatusBadRequest, map[string]string{"error": "body must be {path, content}"})
			return
		}
		id := ""
		if w.Identify != nil {
			id = w.Identify(r.Context(), r.RemoteAddr)
		}
		if id == "" {
			writeJSON(rw, http.StatusForbidden, map[string]string{"error": ErrUnknownAgent.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		res, err := w.Write(ctx, id, in.Path, []byte(in.Content))
		switch {
		case errors.Is(err, ErrUnknownAgent):
			writeJSON(rw, http.StatusForbidden, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrNotYourArea):
			writeJSON(rw, http.StatusForbidden, map[string]string{"error": err.Error()})
		case errors.Is(err, ErrInvalidPage):
			writeJSON(rw, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		case err != nil:
			w.logf("write by %s failed: %v", id, err)
			writeJSON(rw, http.StatusBadGateway, map[string]string{"error": "could not commit the page: " + tail(err.Error(), 200)})
		default:
			writeJSON(rw, http.StatusOK, res)
		}
	})
	return mux
}

func writeJSON(rw http.ResponseWriter, code int, v any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	_ = json.NewEncoder(rw).Encode(v)
}

// agentRe finds `agent-<id>-<n>` in a Docker container (PTR) name such as
// `example-agent-dev-1.example_tavern.` (same rule as
// the egress proxy, A8).
var agentRe = regexp.MustCompile(`(?:^|[-_.])agent-([a-z][a-z0-9-]*?)-[0-9]+(?:[._]|$)`)

// AgentFromName extracts the hireling id from a container name.
func AgentFromName(name string) string {
	m := agentRe.FindStringSubmatch(strings.ToLower(name))
	if m == nil {
		return ""
	}
	return m[1]
}

// DockerIdentify resolves the caller through Docker's embedded DNS.
func DockerIdentify(ctx context.Context, remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, host)
	if err != nil {
		return ""
	}
	for _, n := range names {
		if id := AgentFromName(n); id != "" {
			return id
		}
	}
	return ""
}
