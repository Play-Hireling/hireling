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
	desk.Quest = s.QuestSnapshot
	s.Extra, s.Owner = desk.Register, desk.IsOwner
	srv := listen(s, desk.OwnerToken)
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

	// /tasks takes the same owner token. GET /goal stays open for the runtime's quest bar.
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

// ---- security review of PR #21 (QA Segurança) ---------------------------------

const ownerTok = "0123456789abcdef0123456789abcdef0123456789abcdef" // 48, as init-env.sh writes

// qaServer wires a desk with the given shared and owner tokens; hireling
// "test" has its own token "test-tok-0123456789abcdef0123456789ab".
func qaServer(t *testing.T, shared, owner string) (*httptest.Server, *frontdesk.Desk) {
	t.Helper()
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	approver := func(a string) bool { return a == "owner" }
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory(), Approver: approver},
		Usage: usage.NewMemory(), Goal: &goal.Service{Store: goal.NewMemory(), Approver: approver}}
	desk := &frontdesk.Desk{Agents: []string{"test"}, AgentTokens: map[string]string{"test": "test-tok-0123456789abcdef0123456789ab"},
		SharedToken: shared, OwnerToken: owner}
	if err := desk.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	desk.Quest = s.QuestSnapshot
	s.Extra, s.Owner = desk.Register, desk.IsOwner
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, desk
}

func qaCall(t *testing.T, srv *httptest.Server, method, path, token, ctype, body string) int {
	t.Helper()
	r, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if ctype != "" {
		r.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("X-Hireling", "owner") // the internal header must not open the owner's routes
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

// An owner token that is also a hireling's token is refused at start, and the
// owner's endpoints stay closed even if a desk is built without the check.
func TestOwnerTokenCollision(t *testing.T) {
	const js = "application/json"
	for name, c := range map[string]struct{ shared, owner, hireling string }{
		"shared token":   {"shared-tok-0123456789abcdef0123456789ab", "shared-tok-0123456789abcdef0123456789ab", "shared-tok-0123456789abcdef0123456789ab"},
		"hireling token": {"shared-tok-0123456789abcdef0123456789ab", "test-tok-0123456789abcdef0123456789ab", "test-tok-0123456789abcdef0123456789ab"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, desk := qaServer(t, c.shared, c.owner)
			err := desk.Check()
			if err == nil || strings.Contains(err.Error(), c.owner) {
				t.Fatalf("Check: %v (must refuse, without the token)", err)
			}
			for _, x := range []struct{ method, path, body string }{
				{"GET", "/api/tasks", ""}, {"GET", "/api/status", ""}, {"GET", "/api/decisions", ""},
				{"POST", "/api/command", `{"agent":"test","text":"x"}`},
				{"POST", "/api/decisions/d-1/choose", `{"option":"yes"}`},
			} {
				if got := qaCall(t, srv, x.method, x.path, c.hireling, js, x.body); got != http.StatusUnauthorized {
					t.Errorf("%s %s with the colliding token: %d, want 401", x.method, x.path, got)
				}
			}
		})
	}
}

func TestOwnerTokenCheck(t *testing.T) {
	d := &frontdesk.Desk{SharedToken: "shared-tok-0123456789abcdef0123456789ab", AgentTokens: map[string]string{"test": "test-tok-0123456789abcdef0123456789ab"}}
	if err := d.Check(); err != nil {
		t.Fatalf("no owner token is allowed (owner endpoints closed): %v", err)
	}
	for _, short := range []string{"abc", strings.Repeat("x", frontdesk.MinOwnerToken-1)} {
		d.OwnerToken = short
		if err := d.Check(); err == nil || strings.Contains(err.Error(), short) {
			t.Fatalf("%d characters: %v", len(short), err)
		}
	}
	d.OwnerToken = ownerTok
	if err := d.Check(); err != nil {
		t.Fatalf("a 48-character token of its own: %v", err)
	}
}

// No variant of the path opens the owner's routes without the token.
func TestOwnerNoPathBypass(t *testing.T) {
	srv, _ := qaServer(t, "shared-tok-0123456789abcdef0123456789ab", ownerTok)
	for _, path := range []string{
		"/api/tasks", "/api/tasks/", "/api//tasks", "/api/%74asks", "/API/tasks", "/api/tasks?x=1",
		"/api/tasks?token=" + ownerTok, "/api/tasks/T-1", "/api/tasks/..%2F..%2Ftasks", "/api/quest", "/api/decisions/d-1",
	} {
		if got := qaCall(t, srv, "GET", path, "", "", ""); got == http.StatusOK {
			t.Errorf("%s without a token: 200", path)
		}
	}
	if got := qaCall(t, srv, "GET", "/api/tasks", ownerTok, "", ""); got != http.StatusOK {
		t.Errorf("owner: %d", got)
	}
	if got := qaCall(t, srv, "GET", "/api/quest", ownerTok, "", ""); got == http.StatusUnauthorized {
		t.Errorf("owner, no quest yet: %d (404 expected, never 401)", got)
	}
}

// A page the owner opens can send a "simple" cross-site POST to 127.0.0.1
// (text/plain, no preflight). The internal write routes refuse anything that
// is not declared JSON; JSON itself needs a preflight this server never answers.
func TestInternalWritesNeedJSON(t *testing.T) {
	srv, _ := qaServer(t, "shared-tok-0123456789abcdef0123456789ab", ownerTok)
	task := `{"title":"csrf","owner":"test","priority":"P2","requested_by":"owner","goal_contribution":{"kind":"posts","expected":1}}`
	for _, x := range []struct{ method, path, body string }{
		{"POST", "/tasks", task},
		{"PUT", "/goal", `{"title":"csrf","target":1}`},
		{"POST", "/actions/answer", `{"agent":"test","decision":"d-1","choice":"yes"}`},
		{"POST", "/tasks/T-1/approve", `{"agent":"owner"}`},
		{"POST", "/tasks/T-1/lease", ``},
	} {
		for _, ct := range []string{"", "text/plain", "text/plain;charset=UTF-8", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonx"} {
			if got := qaCall(t, srv, x.method, x.path, "", ct, x.body); got != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s as %q: %d, want 415", x.method, x.path, ct, got)
			}
		}
	}
	if got := qaCall(t, srv, "POST", "/tasks", "", "application/json; charset=utf-8", task); got != http.StatusUnauthorized {
		t.Errorf("JSON without the owner token: %d, want 401", got)
	}
	if got := qaCall(t, srv, "POST", "/tasks", ownerTok, "application/json; charset=utf-8", task); got != http.StatusCreated {
		t.Errorf("JSON with the owner token: %d, want 201", got)
	}
	if got := qaCall(t, srv, "GET", "/tasks", "", "", ""); got != http.StatusUnauthorized {
		t.Errorf("GET /tasks without the owner token: %d, want 401", got)
	}
	if got := qaCall(t, srv, "GET", "/tasks", ownerTok, "", ""); got != http.StatusOK {
		t.Errorf("GET /tasks with the owner token: %d", got)
	}
	if got := qaCall(t, srv, "POST", "/api/command", ownerTok, "application/json", `{"agent":"test","text":"hi"}`); got != http.StatusOK {
		t.Errorf("owner order: %d", got)
	}
}

// The owner panel reads the quest from GET /api/status. With no quest, that
// field is null and the page stays on a 200; GET /api/quest is still 404.
func TestPanelQuestComesFromStatus(t *testing.T) {
	srv := newOwnerServer(t, true)
	var st struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
		Decisions []any           `json:"decisions"`
		Quest     json.RawMessage `json:"quest"`
	}
	if code := bearerCall(t, srv, "/api/status", "owner-tok", &st); code != 200 || string(st.Quest) != "null" || len(st.Agents) != 1 || st.Decisions == nil {
		t.Fatalf("status without a quest: %d agents=%d decisions=%v quest=%s", code, len(st.Agents), st.Decisions, st.Quest)
	}
	if code := bearerCall(t, srv, "/api/quest", "owner-tok", nil); code != 404 {
		t.Fatalf("/api/quest without a quest: %d, want 404", code)
	}
	if code := call(t, srv, "PUT", "/goal", "owner", `{"title":"Monthly revenue","target":5000}`, nil); code != 200 {
		t.Fatalf("set quest: %d", code)
	}
	if code := bearerCall(t, srv, "/api/status", "owner-tok", &st); code != 200 || !strings.Contains(string(st.Quest), `"title":"Monthly revenue"`) {
		t.Fatalf("status with a quest: %d %s", code, st.Quest)
	}
}
