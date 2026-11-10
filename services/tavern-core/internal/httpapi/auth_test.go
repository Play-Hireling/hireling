package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/asks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// Every /tasks route, PUT /goal and every /actions/* route answers 401 with
// no bearer and with the wrong bearer, and 2xx with the owner token. A token
// in the query does not count. X-Hireling does not replace the bearer.
func TestOwnerTokenOnInternalRoutes(t *testing.T) {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	approver := func(a string) bool { return a == "owner" }
	s := &Server{
		Gate:      policy.New(p),
		Tasks:     &tasks.Service{Store: tasks.NewMemory(), Approver: approver},
		Usage:     usage.NewMemory(),
		Goal:      &goal.Service{Store: goal.NewMemory(), Approver: approver},
		Grants:    policy.NewMemoryGrants(),
		Approvals: fakeApprovals{},
		Asks:      asks.NewMemory(),
	}
	srv := listen(s, testOwnerToken)
	defer srv.Close()

	type route struct {
		method, path, body string
		want               int
	}
	// 401 probes use a stable path. The success flow below uses real ids.
	probes := []route{
		{"GET", "/tasks", "", 0},
		{"GET", "/tasks/T-1", "", 0},
		{"POST", "/tasks", `{"title":"x","goal_contribution":{"kind":"leads","expected":1}}`, 0},
		{"POST", "/tasks/T-1/lease", `{}`, 0},
		{"POST", "/tasks/T-1/renew", `{}`, 0},
		{"POST", "/tasks/T-1/handoff", `{"to":"test"}`, 0},
		{"POST", "/tasks/T-1/finish", `{"status":"done"}`, 0},
		{"POST", "/tasks/T-1/release", `{"status":"queued"}`, 0},
		{"POST", "/tasks/T-1/fail", `{"error":"x","max":1}`, 0},
		{"POST", "/tasks/T-1/unblock", `{"action":"retry"}`, 0},
		{"POST", "/tasks/T-1/approve", `{}`, 0},
		{"POST", "/tasks/T-1/deny", `{}`, 0},
		{"PUT", "/goal", `{"title":"Net","target":1}`, 0},
		{"POST", "/actions/check", `{"action":"draft"}`, 0},
		{"POST", "/actions/pending", `{"decision":"d","agent":"test","actions":[{"action":"draft"}]}`, 0},
		{"POST", "/actions/answer", `{"decision":"d","choice":"Approve","agent":"test"}`, 0},
		{"POST", "/asks/answer", `{"decision":"d-1","choice":"Yes"}`, 0},
	}
	for _, r := range probes {
		for _, tok := range []struct{ label, value string }{{"none", ""}, {"wrong", "wrong-token"}, {"wrong2", "owner-tok-x"}} {
			code := rawCall(t, srv, r.method, r.path, tok.value, r.body)
			t.Logf("%s %s bearer=%s -> %d", r.method, r.path, tok.label, code)
			if code != http.StatusUnauthorized {
				t.Errorf("%s %s bearer=%s: %d, want 401", r.method, r.path, tok.label, code)
			}
		}
		q := r.path
		if strings.Contains(q, "?") {
			q += "&token=" + testOwnerToken
		} else {
			q += "?token=" + testOwnerToken
		}
		code := rawCall(t, srv, r.method, q, "", r.body)
		t.Logf("%s %s bearer=none query-token -> %d", r.method, r.path, code)
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s query token: %d, want 401", r.method, q, code)
		}
	}

	// The right token, and only that token, reaches the handler.
	if code := call(t, srv, "POST", "/actions/check", "test", `{"action":"draft"}`, nil); code != http.StatusOK {
		t.Fatalf("POST /actions/check: %d", code)
	}
	if code := call(t, srv, "POST", "/actions/pending", "test", `{"decision":"d-1","actions":[{"action":"draft"}]}`, nil); code != http.StatusOK {
		t.Fatalf("POST /actions/pending: %d", code)
	}
	if code := call(t, srv, "POST", "/actions/answer", "test", `{"decision":"d-1","choice":"Approve","agent":"test"}`, nil); code != http.StatusOK {
		t.Fatalf("POST /actions/answer: %d", code)
	}
	if code := call(t, srv, "PUT", "/goal", "owner", `{"title":"Net revenue","target":10}`, nil); code != http.StatusOK {
		t.Fatalf("PUT /goal: %d", code)
	}
	if code := call(t, srv, "GET", "/tasks", "", "", nil); code != http.StatusOK {
		t.Fatalf("GET /tasks: %d", code)
	}

	var created tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"own work","owner":"test","priority":"P2","goal_contribution":{"kind":"leads","expected":1}}`, &created); code != http.StatusCreated {
		t.Fatalf("POST /tasks: %d", code)
	}
	if code := call(t, srv, "GET", "/tasks/"+created.ID, "", "", nil); code != http.StatusOK {
		t.Fatalf("GET /tasks/{id}: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+created.ID+"/lease", "test", `{}`, nil); code != http.StatusOK {
		t.Fatalf("lease: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+created.ID+"/renew", "test", `{}`, nil); code != http.StatusOK {
		t.Fatalf("renew: %d", code)
	}

	var handed tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"pass","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`, &handed); code != http.StatusCreated {
		t.Fatalf("POST /tasks handoff: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+handed.ID+"/handoff", "test", `{"to":"test"}`, nil); code != http.StatusOK {
		t.Fatalf("handoff: %d", code)
	}

	var released tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"let go","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`, &released); code != http.StatusCreated {
		t.Fatalf("POST /tasks release: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+released.ID+"/lease", "test", `{}`, nil); code != http.StatusOK {
		t.Fatalf("lease for release: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+released.ID+"/release", "test", `{"status":"queued"}`, nil); code != http.StatusOK {
		t.Fatalf("release: %d", code)
	}

	var failed tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"boom","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`, &failed); code != http.StatusCreated {
		t.Fatalf("POST /tasks fail: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+failed.ID+"/lease", "test", `{}`, nil); code != http.StatusOK {
		t.Fatalf("lease for fail: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+failed.ID+"/fail", "test", `{"error":"boom","max":1}`, nil); code != http.StatusOK {
		t.Fatalf("fail: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+failed.ID+"/unblock", "test", `{"action":"retry"}`, nil); code != http.StatusOK {
		t.Fatalf("unblock: %d", code)
	}

	var done tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"finish","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`, &done); code != http.StatusCreated {
		t.Fatalf("POST /tasks finish: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+done.ID+"/lease", "test", `{}`, nil); code != http.StatusOK {
		t.Fatalf("lease for finish: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+done.ID+"/finish", "test", `{"status":"done"}`, nil); code != http.StatusOK {
		t.Fatalf("finish: %d", code)
	}

	var ask tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"please","owner":"dev","priority":"P2","goal_contribution":{"kind":"leads","expected":1}}`, &ask); code != http.StatusCreated || !ask.NeedsApproval {
		t.Fatalf("request for approval: %d %+v", code, ask)
	}
	if code := call(t, srv, "POST", "/tasks/"+ask.ID+"/approve", "owner", `{"note":"ok"}`, nil); code != http.StatusOK {
		t.Fatalf("approve: %d", code)
	}
	var deny tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"no","owner":"dev","priority":"P2","goal_contribution":{"kind":"leads","expected":1}}`, &deny); code != http.StatusCreated {
		t.Fatalf("request to deny: %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+deny.ID+"/deny", "owner", `{}`, nil); code != http.StatusOK {
		t.Fatalf("deny: %d", code)
	}
	if code := call(t, srv, "POST", "/asks/opened", "test", `{"decision":"d-ask","question":"Ship it?","recommended":"Yes"}`, nil); code != http.StatusOK {
		t.Fatalf("open ask: %d", code)
	}
	var answered struct {
		Found bool `json:"found"`
	}
	if code := call(t, srv, "POST", "/asks/answer", "test", `{"decision":"d-ask","choice":"Yes"}`, &answered); code != http.StatusOK || !answered.Found {
		t.Fatalf("POST /asks/answer: %d found=%v", code, answered.Found)
	}
}

// secretTitle is planted on a task. A bypass that returns the board would contain it.
const secretTitle = "ZZ-SECRET-TASK-TITLE"

// protectedCalls are every route that requires the owner bearer.
func protectedCalls(taskID string) []struct{ method, path, body string } {
	task := `{"title":"other","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`
	return []struct{ method, path, body string }{
		{"GET", "/tasks", ""},
		{"GET", "/tasks/" + taskID, ""},
		{"POST", "/tasks", task},
		{"POST", "/tasks/" + taskID + "/lease", `{}`},
		{"POST", "/tasks/" + taskID + "/renew", `{}`},
		{"POST", "/tasks/" + taskID + "/handoff", `{"to":"test"}`},
		{"POST", "/tasks/" + taskID + "/finish", `{"status":"done"}`},
		{"POST", "/tasks/" + taskID + "/release", `{"status":"queued"}`},
		{"POST", "/tasks/" + taskID + "/fail", `{"error":"x","max":1}`},
		{"POST", "/tasks/" + taskID + "/unblock", `{"action":"retry"}`},
		{"POST", "/tasks/" + taskID + "/approve", `{}`},
		{"POST", "/tasks/" + taskID + "/deny", `{}`},
		{"PUT", "/goal", `{"title":"Net","target":1}`},
		{"POST", "/actions/check", `{"action":"draft"}`},
		{"POST", "/actions/pending", `{"decision":"d","agent":"test","actions":[{"action":"draft"}]}`},
		{"POST", "/actions/answer", `{"decision":"d","choice":"Approve","agent":"test"}`},
		{"POST", "/asks/answer", `{"decision":"d-ask","choice":"Yes"}`},
	}
}

// TestProtectedRoutesDoNotBypass: HEAD and OPTIONS without a token do not
// succeed and do not return the board. A trailing slash does not skip the
// check. "Authorization: Bearer " (empty token) is 401 while a real owner
// token is configured.
func TestProtectedRoutesDoNotBypass(t *testing.T) {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft"}
	approver := func(a string) bool { return a == "owner" }
	s := &Server{
		Gate:   policy.New(p),
		Tasks:  &tasks.Service{Store: tasks.NewMemory(), Approver: approver},
		Usage:  usage.NewMemory(),
		Goal:   &goal.Service{Store: goal.NewMemory(), Approver: approver},
		Grants: policy.NewMemoryGrants(),
		Asks:   asks.NewMemory(),
	}
	srv := listen(s, testOwnerToken)
	defer srv.Close()
	var created tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"`+secretTitle+`","owner":"test","priority":"P3","goal_contribution":{"kind":"leads","expected":1}}`, &created); code != http.StatusCreated {
		t.Fatalf("plant: %d", code)
	}
	// Do not follow a slash redirect: the status we see is the first response.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, r := range protectedCalls(created.ID) {
		for _, probe := range []struct{ method, path, auth string }{
			{http.MethodHead, r.path, ""},
			{http.MethodOptions, r.path, ""},
			{r.method, r.path + "/", ""},
			{r.method, r.path, "Bearer "},
		} {
			code, body := doRaw(t, client, srv, probe.method, probe.path, probe.auth, r.body)
			label := probe.method + " " + probe.path
			if probe.auth != "" {
				label += " empty-bearer"
			}
			t.Logf("%s -> %d", label, code)
			if code/100 == 2 {
				t.Errorf("%s passed without a token: %d body %q", label, code, body)
			}
			if strings.Contains(body, secretTitle) {
				t.Errorf("%s leaked the task: %q", label, body)
			}
		}
	}
}

// TestEmptyOwnerTokenFailsClosed: FRONTDESK_OWNER_TOKEN empty means IsOwner
// never matches, including an empty bearer. An empty string does not authenticate.
func TestEmptyOwnerTokenFailsClosed(t *testing.T) {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft"}
	s := &Server{
		Gate:  policy.New(p),
		Tasks: &tasks.Service{Store: tasks.NewMemory()},
		Usage: usage.NewMemory(),
		Goal:  &goal.Service{Store: goal.NewMemory()},
		Asks:  asks.NewMemory(),
	}
	desk := &frontdesk.Desk{Agents: []string{"test"}, SharedToken: strings.Repeat("s", 40), OwnerToken: ""}
	if err := desk.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := desk.Check(); err != nil {
		t.Fatal(err)
	}
	s.Owner = desk.IsOwner
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, r := range protectedCalls("T-1") {
		for _, auth := range []string{"", "Bearer ", "Bearer", "Bearer " + testOwnerToken} {
			code, body := doRaw(t, client, srv, r.method, r.path, auth, r.body)
			label := auth
			if label == "" {
				label = "none"
			} else if label == "Bearer " || label == "Bearer" {
				label = "empty"
			} else {
				label = "some-token"
			}
			t.Logf("empty-owner %s %s auth=%s -> %d", r.method, r.path, label, code)
			if code != http.StatusUnauthorized {
				t.Errorf("%s %s auth=%s: %d, want 401 (body %q)", r.method, r.path, label, code, body)
			}
			if strings.Contains(body, secretTitle) || strings.Contains(body, `"title"`) && strings.Contains(body, "tasks") {
				t.Errorf("%s %s leaked data: %q", r.method, r.path, body)
			}
		}
		q := r.path + "?token="
		code, body := doRaw(t, client, srv, r.method, q, "", r.body)
		t.Logf("empty-owner %s %s query-empty -> %d", r.method, r.path, code)
		if code != http.StatusUnauthorized {
			t.Errorf("query token %s %s: %d body %q", r.method, q, code, body)
		}
	}
}

func doRaw(t *testing.T, client *http.Client, srv *httptest.Server, method, path, auth, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	req.Header.Set("X-Hireling", "test")
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func rawCall(t *testing.T, srv *httptest.Server, method, path, token, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Hireling", "test")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}
