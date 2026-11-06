package httpapi

// Second security review of PR #21: the 415 (jsonOnly), the
// owner token's Check() and the corners of the /api/ prefix the middleware exempts.

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

func qa3Desk(t *testing.T, shared, owner string) (*frontdesk.Desk, *tasks.Service, *httptest.Server) {
	t.Helper()
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	approver := func(a string) bool { return a == "owner" }
	tasksSvc := &tasks.Service{Store: tasks.NewMemory(), Approver: approver}
	s := &Server{Gate: policy.New(p), Tasks: tasksSvc, Usage: usage.NewMemory(),
		Goal: &goal.Service{Store: goal.NewMemory(), Approver: approver}}
	d := &frontdesk.Desk{Agents: []string{"test"}, AgentTokens: map[string]string{"test": "test-tok"},
		SharedToken: shared, OwnerToken: owner}
	if err := d.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Extra, s.Owner = d.Register, d.IsOwner
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return d, tasksSvc, srv
}

func qa3Do(t *testing.T, srv *httptest.Server, method, path, ctype, body, token string) *http.Response {
	t.Helper()
	var r *http.Request
	if body == "" {
		r, _ = http.NewRequest(method, srv.URL+path, nil)
	} else {
		r, _ = http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	}
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Origin", "https://evil.example")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res
}

// The middleware exempts by path prefix: /api/../tasks must not reach the
// internal handler without JSON (CSRF with text/plain).
func TestQAJsonOnlyPathExemption(t *testing.T) {
	_, svc, srv := qa3Desk(t, "shared-tok", strings.Repeat("o", 48))
	for _, path := range []string{"/api/../tasks", "/api/%2e%2e/tasks", "/api/..%2ftasks", "//api/../tasks", "/api/../actions/answer", "/api/%2E%2E/tasks"} {
		res := qa3Do(t, srv, "POST", path, "text/plain", `{"title":"csrf","priority":"P2","requested_by":"owner"}`, "")
		ts, _ := svc.List(context.Background())
		if len(ts) > 0 {
			t.Fatalf("BYPASS CSRF: %s created a task (%d) with status %d", path, len(ts), res.StatusCode)
		}
		t.Logf("%-24s -> %d location=%q", path, res.StatusCode, res.Header.Get("Location"))
	}
}

// A raw client (curl --path-as-is) sends /api/../tasks without Go cleaning the
// path: the middleware exempts by prefix, so the mux has to stop it.
func TestQAJsonOnlyRawPath(t *testing.T) {
	_, svc, srv := qa3Desk(t, "shared-tok", strings.Repeat("o", 48))
	addr := strings.TrimPrefix(srv.URL, "http://")
	body := `{"title":"csrf","priority":"P2","requested_by":"owner"}`
	for _, path := range []string{"/api/../tasks", "/api/..%2ftasks", "//api/../tasks", "/api/%2e%2e/tasks"} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		req := "POST " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nContent-Type: text/plain\r\nContent-Length: " +
			strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n" + body
		if _, err := conn.Write([]byte(req)); err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(conn)
		conn.Close()
		line := strings.SplitN(string(raw), "\r\n", 2)[0]
		if strings.Contains(line, "201") || strings.Contains(line, "200") {
			t.Errorf("raw BYPASS: %s -> %s", path, line)
		}
		ts, _ := svc.List(context.Background())
		if len(ts) > 0 {
			t.Fatalf("raw BYPASS: %s created a task (%s)", path, line)
		}
		t.Logf("%-22s -> %s", path, line)
	}
}

func TestQAJsonOnlyContentTypes(t *testing.T) {
	_, svc, srv := qa3Desk(t, "shared-tok", strings.Repeat("o", 48))
	body := `{"title":"x","priority":"P2","requested_by":"owner"}`
	deny := []string{"", "text/plain", "text/plain;charset=utf-8", "application/x-www-form-urlencoded",
		"multipart/form-data; boundary=b", "application/jsonx", "application/json-patch+json", "text/json"}
	for _, ct := range deny {
		if res := qa3Do(t, srv, "POST", "/tasks", ct, body, ""); res.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: want 415, got %d", ct, res.StatusCode)
		}
	}
	allow := []string{"application/json", "application/json; charset=utf-8", "Application/JSON", " application/json "}
	for _, ct := range allow {
		if res := qa3Do(t, srv, "POST", "/tasks", ct, body, ""); res.StatusCode/100 != 2 {
			t.Errorf("Content-Type %q: want it accepted, got %d", ct, res.StatusCode)
		}
	}
	ts, _ := svc.List(context.Background())
	if len(ts) != len(allow) {
		t.Errorf("tasks created = %d, want %d", len(ts), len(allow))
	}
	// GET and OPTIONS are not affected.
	if res := qa3Do(t, srv, "GET", "/tasks", "", "", ""); res.StatusCode != http.StatusOK {
		t.Errorf("GET /tasks: %d", res.StatusCode)
	}
	res := qa3Do(t, srv, "OPTIONS", "/tasks", "", "", "")
	if res.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("OPTIONS answered with CORS: %q", res.Header.Get("Access-Control-Allow-Origin"))
	}
	t.Logf("OPTIONS /tasks -> %d", res.StatusCode)
}

// Check(): short, equal to the shared token, equal to a hireling's token.
func TestQACheckRefusesWeakOrShared(t *testing.T) {
	cases := map[string]struct{ owner, shared string }{
		"curto":         {"abc", "shared-tok"},
		"compartilhado": {strings.Repeat("s", 40), strings.Repeat("s", 40)},
		"de hireling":   {"test-tok-1234567890123456789012345", "shared-tok"},
	}
	long := strings.Repeat("o", 48)
	cases["ok"] = struct{ owner, shared string }{long, "shared-tok"}

	for name, c := range cases {
		d := &frontdesk.Desk{Agents: []string{"test"}, AgentTokens: map[string]string{"test": "test-tok-1234567890123456789012345"},
			SharedToken: c.shared, OwnerToken: c.owner}
		err := d.Check()
		if name == "ok" {
			if err != nil {
				t.Errorf("a valid token must pass: %v", err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: Check() accepted it", name)
			continue
		}
		if len(c.owner) > 4 && strings.Contains(err.Error(), c.owner) {
			t.Errorf("%s: the message carries the token", name)
		}
		t.Logf("%s: %v", name, err)
	}
}

// Without Check, a collision must keep the owner's routes closed (defence in depth).
func TestQACollisionClosedWithoutCheck(t *testing.T) {
	_, _, srv := qa3Desk(t, "shared-tok", "shared-tok")
	if res := qa3Do(t, srv, "GET", "/api/tasks", "", "", "shared-tok"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("collision with the shared token opened /api/tasks: %d", res.StatusCode)
	}
	_, _, srv2 := qa3Desk(t, "shared-tok", "test-tok")
	if res := qa3Do(t, srv2, "GET", "/api/tasks", "", "", "test-tok"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("collision with a hireling's token opened /api/tasks: %d", res.StatusCode)
	}
}
