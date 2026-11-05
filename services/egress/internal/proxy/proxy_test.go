package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestAgentFromName(t *testing.T) {
	cases := map[string]string{
		"hireling-abc123-agent-test-1.hireling-abc123_tavern.": "test",
		"hireling-abc123-agent-social-media-1":                 "social-media",
		"agent-dev-12":                                         "dev",
		"hireling-abc123-tavern-core-1":                        "",
		"my-agent-tester":                                      "",
		"":                                                     "",
	}
	for in, want := range cases {
		if got := AgentFromName(in); got != want {
			t.Errorf("AgentFromName(%q) = %q, want %q", in, got, want)
		}
	}
}

func newProxy(t *testing.T, port string) (*Proxy, *httptest.Server) {
	t.Helper()
	p := &Proxy{
		Allow: map[string][]string{"test": {"api.deepseek.com", "localhost"}, "dev": {"github.com"}},
		Identify: func(_ context.Context, ip string) string {
			return map[string]string{"127.0.0.1": "test", "::1": "test"}[ip]
		},
		Log:   log.New(os.Stderr, "proxy-test ", 0),
		Ports: map[string]bool{"443": true, "80": true, port: true},
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv
}

func TestDecide(t *testing.T) {
	p, _ := newProxy(t, "0")
	ctx := context.Background()
	cases := []struct {
		remote, target string
		ok             bool
		why            string
	}{
		{"127.0.0.1:5000", "api.deepseek.com:443", true, ""},
		{"127.0.0.1:5000", "Api.DeepSeek.com.:443", true, ""},
		{"127.0.0.1:5000", "pypi.org:443", false, "not on allowlist"},
		{"127.0.0.1:5000", "api.deepseek.com:8443", false, "port 8443"},
		{"127.0.0.1:5000", "1.1.1.1:443", false, "ip literal"},
		{"10.9.9.9:5000", "api.deepseek.com:443", false, "unknown client"},
	}
	for _, c := range cases {
		agent, ok, why := p.Decide(ctx, c.remote, c.target)
		if ok != c.ok || why != c.why {
			t.Errorf("Decide(%s → %s) = %s %v %q, want %v %q", c.remote, c.target, agent, ok, why, c.ok, c.why)
		}
	}
	// D6: an allowed suffix does not open a sensitive subdomain
	p.Allow["test"] = append(p.Allow["test"], "example.com")
	if _, ok, why := p.Decide(ctx, "127.0.0.1:5000", "docs.example.com:443"); !ok {
		t.Fatalf("docs.example.com refused: %s", why)
	}
	if _, ok, why := p.Decide(ctx, "127.0.0.1:5000", "bank.example.com:443"); ok || !strings.HasPrefix(why, "data rule (D6)") {
		t.Fatalf("bank.example.com allowed: %v %q", ok, why)
	}
	// a hireling without a profile is refused even if resolved
	p.Identify = func(context.Context, string) string { return "ghost" }
	if _, ok, why := p.Decide(ctx, "127.0.0.1:1", "api.deepseek.com:443"); ok || why != "no profile" {
		t.Fatal(ok, why)
	}
}

func TestConnectTunnel(t *testing.T) {
	// an HTTPS target on localhost
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	_, srv := newProxy(t, port)

	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	res, err := client.Get("https://localhost:" + port + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "hello /ok" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
	// a host that is not on the list is refused at CONNECT time
	_, err = client.Get("https://pypi.org/simple/")
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("expected a 403 from the proxy, got %v", err)
	}
}

func TestPlainHTTPForward(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "plain "+r.URL.Path)
	}))
	defer target.Close()
	u, _ := url.Parse(target.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	_, srv := newProxy(t, port)
	proxyURL, _ := url.Parse(srv.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	res, err := client.Get("http://localhost:" + port + "/x")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "plain /x" {
		t.Fatalf("%d %q", res.StatusCode, body)
	}
	res, err = client.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("example.com must be refused, got %d", res.StatusCode)
	}
}
