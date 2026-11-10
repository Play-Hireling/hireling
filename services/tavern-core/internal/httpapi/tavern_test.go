package httpapi

import (
	"bufio"
	"fmt"
	"io"
	"net"
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
	if page.Header.Get("Content-Security-Policy") != TavernContentSecurityPolicy {
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
	if js.Header.Get("Content-Security-Policy") != TavernContentSecurityPolicy {
		t.Fatalf("play.js CSP = %q", js.Header.Get("Content-Security-Policy"))
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

// The game policy is selected only by the URL path prefix "/tavern/".
// The panel and every other route, including an unknown one, keep the
// stricter panel policy. Query, Referer and Accept cannot select the game.
func TestCSPDependsOnPathPrefixOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html><html><body>game</body></html>")
	writeFile(t, filepath.Join(dir, "play.js"), "/* asset */\n")
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), ClientDir: dir}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	check := func(rawURL, referer, accept, want string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Content-Security-Policy"); got != want {
			t.Errorf("%s referer %q accept %q: CSP = %q, want %q", rawURL, referer, accept, got, want)
		}
		if strings.Contains(res.Header.Get("Content-Security-Policy"), "unsafe-eval") {
			t.Errorf("%s allows unsafe-eval", rawURL)
		}
	}

	check(srv.URL+"/tavern/", "", "", TavernContentSecurityPolicy)
	check(srv.URL+"/tavern/play.js", "", "", TavernContentSecurityPolicy)
	check(srv.URL+"/", "", "", PanelContentSecurityPolicy)
	check(srv.URL+"/no-such-route", "", "", PanelContentSecurityPolicy)
	check(srv.URL+"/tavernX", "", "", PanelContentSecurityPolicy)
	check(srv.URL+"/?x=/tavern/", "", "", PanelContentSecurityPolicy)
	check(srv.URL+"/", srv.URL+"/tavern/", "", PanelContentSecurityPolicy)
	check(srv.URL+"/", "", "text/html, /tavern/", PanelContentSecurityPolicy)

	if strings.Contains(PanelContentSecurityPolicy, "unsafe-") {
		t.Fatal(PanelContentSecurityPolicy)
	}
	if !strings.Contains(TavernContentSecurityPolicy, "style-src 'self' 'unsafe-inline'") {
		t.Fatal(TavernContentSecurityPolicy)
	}
}

// The game policy is set by serveTavern when it serves a file. The mux's
// redirect and every other handler keep the panel policy. Each case is one
// request against the real Handler.
func TestCSPMatchesTheHandler(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "<!doctype html><html><body>GAME-INDEX</body></html>")
	writeFile(t, filepath.Join(dir, "play.js"), "GAME-ASSET\n")
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), ClientDir: dir}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// /tavern/../ is not a clean path. The mux redirects; serveTavern does not run.
	redir := rawRequest(t, srv, "/tavern/../")
	if redir.csp == TavernContentSecurityPolicy {
		t.Fatalf("/tavern/../ got the game CSP (status %d location %q handler %s)", redir.status, redir.location, redir.handler)
	}
	if redir.status/100 != 3 && redir.csp != PanelContentSecurityPolicy {
		t.Fatalf("/tavern/../ status %d CSP %q, want a mux redirect or the panel policy", redir.status, redir.csp)
	}
	t.Logf("route /tavern/../ → %s → %s", redir.handler, policyName(redir.csp))

	// Encoded slash. The decoded path cleans out of /tavern/, so this is not a game file.
	enc := rawRequest(t, srv, "/tavern/..%2f")
	if enc.csp == TavernContentSecurityPolicy {
		t.Fatalf("/tavern/..%%2f got the game CSP (status %d body %q handler %s)", enc.status, enc.body, enc.handler)
	}
	if enc.csp != PanelContentSecurityPolicy && enc.status/100 != 3 {
		t.Fatalf("/tavern/..%%2f status %d CSP %q, want the panel policy or a mux redirect", enc.status, enc.csp)
	}
	t.Logf("route /tavern/..%%2f → %s → %s", enc.handler, policyName(enc.csp))

	// %74 is 't'. The server decodes it and the handler that matches serves the response.
	pct := rawRequest(t, srv, "/%74avern/")
	if pct.handler == "serveTavern" && pct.csp != TavernContentSecurityPolicy {
		t.Fatalf("%%74avern served by serveTavern with CSP %q", pct.csp)
	}
	if pct.handler != "serveTavern" && pct.csp == TavernContentSecurityPolicy {
		t.Fatalf("%%74avern got the game CSP from %s", pct.handler)
	}
	if pct.handler != "serveTavern" && pct.csp != PanelContentSecurityPolicy && pct.status/100 != 3 {
		t.Fatalf("%%74avern handler %s status %d CSP %q", pct.handler, pct.status, pct.csp)
	}
	t.Logf("route /%%74avern/ → %s → %s", pct.handler, policyName(pct.csp))

	panel := rawRequest(t, srv, "/")
	if panel.handler != "client" || panel.csp != PanelContentSecurityPolicy {
		t.Fatalf("/ handler %s CSP %q", panel.handler, panel.csp)
	}
	t.Logf("route / → %s → %s", panel.handler, policyName(panel.csp))

	other := rawRequest(t, srv, "/tavernX/")
	if other.csp != PanelContentSecurityPolicy || other.handler == "serveTavern" {
		t.Fatalf("/tavernX/ handler %s CSP %q", other.handler, other.csp)
	}
	t.Logf("route /tavernX/ → %s → %s", other.handler, policyName(other.csp))

	game := rawRequest(t, srv, "/tavern/")
	if game.handler != "serveTavern" || game.csp != TavernContentSecurityPolicy || !strings.Contains(game.body, "GAME-INDEX") {
		t.Fatalf("/tavern/ handler %s CSP %q body %q", game.handler, game.csp, game.body)
	}
	t.Logf("route /tavern/ → %s → %s", game.handler, policyName(game.csp))

	asset := rawRequest(t, srv, "/tavern/play.js")
	if asset.handler != "serveTavern" || asset.csp != TavernContentSecurityPolicy || !strings.Contains(asset.body, "GAME-ASSET") {
		t.Fatalf("/tavern/play.js handler %s CSP %q body %q", asset.handler, asset.csp, asset.body)
	}
	t.Logf("route /tavern/play.js → %s → %s", asset.handler, policyName(asset.csp))
}

// JSON from the panel API, outside /tavern/, keeps the headers its handler
// wrote. The middleware adds nosniff and does not add a CSP.
func TestPanelAPIJSONKeepsHandlerHeaders(t *testing.T) {
	srv := newOwnerServer(t, true)
	check := func(path string, want map[string]string) {
		t.Helper()
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		t.Logf("after %s status %d", path, res.StatusCode)
		for k, v := range res.Header {
			t.Logf("  %s: %s", k, strings.Join(v, ", "))
		}
		if res.Header.Get("Content-Security-Policy") != "" {
			t.Fatalf("%s gained a CSP: %q", path, res.Header.Get("Content-Security-Policy"))
		}
		for k, v := range want {
			if got := res.Header.Get(k); got != v {
				t.Fatalf("%s %s = %q, want %q", path, k, got, v)
			}
		}
	}
	// health writes Content-Type only. The wrapper adds nosniff.
	t.Log("before /healthz (handler): Content-Type application/json")
	check("/healthz", map[string]string{
		"Content-Type":           "application/json",
		"X-Content-Type-Options": "nosniff",
	})
	// status sets Cache-Control: no-store before the 401 body, which sets Content-Type.
	t.Log("before /api/status (handler): Content-Type application/json, Cache-Control no-store")
	check("/api/status", map[string]string{
		"Content-Type":           "application/json",
		"Cache-Control":          "no-store",
		"X-Content-Type-Options": "nosniff",
	})
}

func policyName(csp string) string {
	switch csp {
	case TavernContentSecurityPolicy:
		return "game"
	case PanelContentSecurityPolicy:
		return "panel"
	case "":
		return "none"
	default:
		return "other"
	}
}

type rawResponse struct {
	status   int
	location string
	csp      string
	body     string
	handler  string
}

// rawRequest sends the target as the request line, so the client does not
// clean ".." or decode the escapes before the server sees them.
func rawRequest(t *testing.T, srv *httptest.Server, target string) rawResponse {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target, srv.Listener.Addr().String())
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	body := string(b)
	return rawResponse{
		status:   res.StatusCode,
		location: res.Header.Get("Location"),
		csp:      res.Header.Get("Content-Security-Policy"),
		body:     body,
		handler:  handlerName(res.Header.Get("X-Hireling-Handler"), res.StatusCode, res.Header.Get("Location")),
	}
}

func handlerName(header string, status int, location string) string {
	if header != "" {
		return header
	}
	if status/100 == 3 {
		return "mux redirect to " + location
	}
	return "mux not found"
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
