package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
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
