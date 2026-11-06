package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// newOwnerServer wires the Front Desk into the server as main.go does
// (Extra: desk.Register, Owner: desk.IsOwner).
func newOwnerServer(t *testing.T, withGoal bool) *httptest.Server {
	t.Helper()
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	approver := func(a string) bool { return a == "owner" }
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory(), Approver: approver}, Usage: usage.NewMemory()}
	if withGoal {
		s.Goal = &goal.Service{Store: goal.NewMemory(), Approver: approver}
	}
	desk := &frontdesk.Desk{Agents: []string{"test"}, AgentTokens: map[string]string{"test": "test-tok"}, SharedToken: "shared-tok", OwnerToken: "owner-tok"}
	if err := desk.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Extra, s.Owner = desk.Register, desk.IsOwner
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

func bearerCall(t *testing.T, srv *httptest.Server, path, token string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// X-Hireling is what the internal routes trust; it must not open these.
	req.Header.Set("X-Hireling", "owner")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return res.StatusCode
}

func TestOwnerRoutesNeedTheOwnerToken(t *testing.T) {
	srv := newOwnerServer(t, true)
	var task tasks.Task
	if code := call(t, srv, "POST", "/tasks", "owner", `{"title":"Launch post","owner":"test","priority":"P1","goal_contribution":{"kind":"posts","expected":1}}`, &task); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if code := call(t, srv, "PUT", "/goal", "owner", `{"title":"Net revenue","target":1000}`, nil); code != 200 {
		t.Fatalf("goal: %d", code)
	}
	paths := []string{"/api/tasks", "/api/tasks/" + task.ID, "/api/quest"}
	for _, path := range paths {
		for _, tok := range []string{"", "test-tok", "shared-tok", "owner-tok-x", "owner"} {
			if code := bearerCall(t, srv, path, tok, nil); code != 401 {
				t.Fatalf("%s with token %q: %d, want 401", path, tok, code)
			}
		}
	}

	var list struct{ Tasks []tasks.Task }
	if code := bearerCall(t, srv, "/api/tasks", "owner-tok", &list); code != 200 || len(list.Tasks) != 1 || list.Tasks[0].ID != task.ID || list.Tasks[0].Title != "Launch post" {
		t.Fatalf("/api/tasks: %d %+v", code, list)
	}
	var one struct{ Task tasks.Task }
	if code := bearerCall(t, srv, "/api/tasks/"+task.ID, "owner-tok", &one); code != 200 || one.Task.ID != task.ID || one.Task.Owner != "test" {
		t.Fatalf("/api/tasks/{id}: %d %+v", code, one)
	}
	if code := bearerCall(t, srv, "/api/tasks/T-404", "owner-tok", nil); code != 404 {
		t.Fatalf("unknown task: %d", code)
	}
	var q struct {
		Quest map[string]any `json:"quest"`
	}
	if code := bearerCall(t, srv, "/api/quest", "owner-tok", &q); code != 200 || q.Quest["title"] != "Net revenue" || q.Quest["bar"] == nil {
		t.Fatalf("/api/quest: %d %+v", code, q)
	}

	// The internal routes are unchanged (bin/tavern and the runtimes use them).
	var raw []tasks.Task
	if code := call(t, srv, "GET", "/tasks", "", "", &raw); code != 200 || len(raw) != 1 {
		t.Fatalf("/tasks: %d %+v", code, raw)
	}
	if code := call(t, srv, "GET", "/goal", "", "", nil); code != 200 {
		t.Fatalf("/goal: %d", code)
	}
}

func TestOwnerQuestNotSetAndOff(t *testing.T) {
	srv := newOwnerServer(t, true)
	if code := bearerCall(t, srv, "/api/quest", "owner-tok", nil); code != 404 {
		t.Fatalf("quest not set: %d", code)
	}
	off := newOwnerServer(t, false)
	if code := bearerCall(t, off, "/api/quest", "owner-tok", nil); code != 404 {
		t.Fatalf("quest off: %d", code)
	}
	if code := bearerCall(t, off, "/api/quest", "", nil); code != 401 {
		t.Fatalf("quest off, no token: %d", code)
	}
}

func TestOwnerRoutesClosedWithoutOwnerFunc(t *testing.T) {
	s := &Server{Gate: policy.New(&profile.Profile{ID: "test"}), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	for _, path := range []string{"/api/tasks", "/api/tasks/T-1", "/api/quest"} {
		if code := bearerCall(t, srv, path, "anything", nil); code != 401 {
			t.Fatalf("%s: %d, want 401", path, code)
		}
	}
	// And an owner token configured as empty never matches an empty bearer.
	desk := &frontdesk.Desk{Agents: []string{"test"}}
	_ = desk.Load(context.Background())
	s.Owner = desk.IsOwner
	srv2 := httptest.NewServer(s.Handler())
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/api/tasks", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer ")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("empty owner token: %d", res.StatusCode)
	}
}
