package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
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
