package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// OS5: the owner panel is served at the root as self-contained HTML, and
// reads the owner's status with the owner token. It never names the owner
// or the private office panel, and it does not fetch GET /api/quest (404
// until a quest exists; the empty state comes from status).
func TestClientServed(t *testing.T) {
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}),
		Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	hz, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	hz.Body.Close()
	if !strings.HasPrefix(hz.Header.Get("Content-Type"), "application/json") || hz.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("JSON nosniff: %q %q", hz.Header.Get("Content-Type"), hz.Header.Get("X-Content-Type-Options"))
	}

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET / = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if got := res.Header.Get("Content-Security-Policy"); got != PanelContentSecurityPolicy {
		t.Fatalf("CSP = %q", got)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" || res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("security headers: nosniff=%q referrer=%q", res.Header.Get("X-Content-Type-Options"), res.Header.Get("Referrer-Policy"))
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{"Tavern", "FRONTDESK_OWNER_TOKEN", `rel="icon" href="data:,"`, `href="/client.css"`, `src="/client.js"`} {
		if !strings.Contains(body, want) {
			t.Errorf("client page missing %q", want)
		}
	}
	if strings.Contains(body, "<style") || strings.Contains(body, "unsafe-inline") || strings.Count(body, "<script") != 1 {
		t.Fatalf("panel still has an inline style or script:\n%s", body)
	}
	for _, asset := range []struct{ path, ct, snippet string }{
		{"/client.css", "text/css", ":root"},
		{"/client.js", "text/javascript", "/api/status"},
	} {
		ares, err := http.Get(srv.URL + asset.path)
		if err != nil {
			t.Fatal(err)
		}
		ab, _ := io.ReadAll(ares.Body)
		ares.Body.Close()
		if ares.StatusCode != http.StatusOK || !strings.HasPrefix(ares.Header.Get("Content-Type"), asset.ct) {
			t.Fatalf("%s: %d %q", asset.path, ares.StatusCode, ares.Header.Get("Content-Type"))
		}
		if ares.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s missing nosniff", asset.path)
		}
		if !strings.Contains(string(ab), asset.snippet) {
			t.Fatalf("%s missing %q", asset.path, asset.snippet)
		}
		if asset.path == "/client.js" && !strings.Contains(string(ab), "no quest set yet") {
			t.Fatal("client.js missing the empty quest state")
		}
		if asset.path == "/client.js" && (!strings.Contains(string(ab), "sessionStorage") || strings.Contains(string(ab), "localStorage.setItem")) {
			t.Fatal("client.js must keep the token in sessionStorage and must not remember it")
		}
	}
	if strings.Contains(body, `api("/api/quest")`) || strings.Contains(body, "Error(\"missing\")") {
		t.Fatalf("panel still fetches /api/quest or treats 404 as missing:\n%s", body)
	}
	for _, bad := range []string{"pedro", "escritorio", "unsafe-inline", "unsafe-eval"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("client page leaks %q", bad)
		}
	}
	// OS5: without a bundle the root is the panel, and the panel says how to
	// build the client instead of linking to a route that is not there.
	for _, want := range []string{"Plain-art client not built", "scripts/pack-client.sh", "docker compose --profile game"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel without a client missing %q", want)
		}
	}
	panel, err := http.Get(srv.URL + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := io.ReadAll(panel.Body)
	panel.Body.Close()
	if panel.StatusCode != http.StatusOK || !strings.Contains(string(pb), `src="/client.js"`) || panel.Header.Get("Content-Security-Policy") != PanelContentSecurityPolicy {
		t.Fatalf("GET /panel/ = %d CSP %q", panel.StatusCode, panel.Header.Get("Content-Security-Policy"))
	}
	noredirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	canon, err := noredirect.Get(srv.URL + "/panel")
	if err != nil {
		t.Fatal(err)
	}
	canon.Body.Close()
	if canon.StatusCode != http.StatusPermanentRedirect || canon.Header.Get("Location") != "/panel/" {
		t.Fatalf("/panel: %d location %q", canon.StatusCode, canon.Header.Get("Location"))
	}
}

// OS5: when the client bundle is mounted, the promised root opens the tavern
// and the panel keeps a stable route at /panel/.
func TestRootOpensTheTavernWhenTheClientIsMounted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html><html><body>GAME-INDEX</body></html>")
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}),
		Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), ClientDir: dir}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	noredirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	root, err := noredirect.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root.Body.Close()
	if root.StatusCode != http.StatusTemporaryRedirect || root.Header.Get("Location") != "/tavern/" {
		t.Fatalf("/: %d location %q", root.StatusCode, root.Header.Get("Location"))
	}
	if root.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("/ redirect Cache-Control = %q", root.Header.Get("Cache-Control"))
	}

	panel, err := http.Get(srv.URL + "/panel/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(panel.Body)
	panel.Body.Close()
	if panel.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/tavern/"`) {
		t.Fatalf("/panel/ = %d body %q", panel.StatusCode, body)
	}
	if strings.Contains(string(body), "Plain-art client not built") {
		t.Fatal("panel still says the client is not built")
	}
}

// OS5: the panel is also served when ClientDir exists but has no index.html
// (an empty volume), and the root keeps the build notice.
func TestRootStaysPanelWhenTheVolumeIsEmpty(t *testing.T) {
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}),
		Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), ClientDir: t.TempDir()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "Plain-art client not built") {
		t.Fatalf("/ with an empty volume = %d body %q", res.StatusCode, body)
	}
	miss, err := http.Get(srv.URL + "/tavern/")
	if err != nil {
		t.Fatal(err)
	}
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("/tavern/ with an empty volume = %d", miss.StatusCode)
	}
}

// HTMLSecurity is the one wrapper. A second HTML route, not only GET /,
// gets the same CSP and the two headers. JSON does not.
func TestHTMLSecurityOnEveryHTMLResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /other", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>other</body></html>"))
	})
	mux.HandleFunc("GET /plain", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(HTMLSecurity(mux))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Content-Security-Policy") != PanelContentSecurityPolicy ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" ||
		res.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("other HTML headers: %v", res.Header)
	}
	if strings.Contains(res.Header.Get("Content-Security-Policy"), "unsafe-") {
		t.Fatal(res.Header.Get("Content-Security-Policy"))
	}
	j, err := http.Get(srv.URL + "/plain")
	if err != nil {
		t.Fatal(err)
	}
	j.Body.Close()
	if j.Header.Get("Content-Security-Policy") != "" {
		t.Fatalf("JSON carried a CSP: %q", j.Header.Get("Content-Security-Policy"))
	}
}
