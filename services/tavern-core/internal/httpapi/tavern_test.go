package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

func TestTavernFilesAndCSP(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), `<!doctype html><html><head><link rel="stylesheet" href="play.css"></head><body><div id="player" style="left:0"></div><script src="play.js"></script></body></html>`)
	writeFile(t, filepath.Join(dir, "play.js"), "/* play */\n")
	writeFile(t, filepath.Join(dir, "play.css"), "#player{position:absolute}\n")
	writeFile(t, filepath.Join(dir, "docs", "provenance", "note.txt"), "secret")
	writeFile(t, filepath.Join(dir, "packages", "assets", "withheld", "secret.txt"), "secret")
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), ClientDir: dir}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	noredirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noredirect.Get(srv.URL + "/tavern")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusPermanentRedirect || res.Header.Get("Location") != "/tavern/" {
		t.Fatalf("/tavern: %d location %q", res.StatusCode, res.Header.Get("Location"))
	}
	page, err := http.Get(srv.URL + "/tavern/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	if page.StatusCode != 200 || !strings.Contains(string(body), "play.js") {
		t.Fatalf("index: %d %s", page.StatusCode, body)
	}
	if page.Header.Get("Content-Security-Policy") != ContentSecurityPolicy {
		t.Fatalf("CSP = %q", page.Header.Get("Content-Security-Policy"))
	}
	if strings.Contains(page.Header.Get("Content-Security-Policy"), "unsafe-eval") {
		t.Fatal("CSP allows unsafe-eval")
	}
	js, err := http.Get(srv.URL + "/tavern/play.js")
	if err != nil {
		t.Fatal(err)
	}
	js.Body.Close()
	if js.StatusCode != 200 || js.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("play.js: %d nosniff %q", js.StatusCode, js.Header.Get("X-Content-Type-Options"))
	}
	if _, ok := inside(dir, "../client.js"); ok {
		t.Fatal("client path escaped the root")
	}
	for _, p := range []string{"/tavern/docs/provenance/note.txt", "/tavern/packages/assets/withheld/secret.txt"} {
		got, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		got.Body.Close()
		if got.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: %d, want 404", p, got.StatusCode)
		}
	}
	empty := &Server{Gate: s.Gate, Tasks: s.Tasks, Usage: s.Usage}
	off := httptest.NewServer(empty.Handler())
	defer off.Close()
	miss, err := http.Get(off.URL + "/tavern/")
	if err != nil {
		t.Fatal(err)
	}
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("unmounted client: %d", miss.StatusCode)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
