package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/asks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/autonomy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/frontdesk"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// fakeApprovals stands in for the Front Desk: the owner chose "Approve"
// (or chosen) on every decision, unless err is set.
type fakeApprovals struct {
	chosen string
	err    error
}

func (f fakeApprovals) Bind(context.Context, string, string, string, []string) (string, error) {
	return "Approve", f.err
}

func (f fakeApprovals) Confirm(context.Context, string, string, string) (string, string, error) {
	if f.chosen == "" {
		return "Approve", "Approve", f.err
	}
	return f.chosen, "Approve", f.err
}

func newServer() *httptest.Server {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	p.Budget.TokensPerDay = 50000
	approver := func(a string) bool { return a == "owner" }
	quests := &goal.Service{Store: goal.NewMemory(), Approver: approver}
	kinds := func(ctx context.Context) []string { // C3, as main.go wires it
		if g, err := quests.Get(ctx); err == nil {
			return g.AllowedKinds()
		}
		return goal.DefaultKinds
	}
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory(), Kinds: kinds}, Usage: usage.NewMemory(),
		Goal: quests,
		Now:  func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }}
	return httptest.NewServer(s.Handler())
}

func call(t *testing.T, srv *httptest.Server, method, path, agent, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json") // as the runtimes send it
	}
	if agent != "" {
		req.Header.Set("X-Hireling", agent)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func TestHealthAndBudget(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var h map[string]any
	if code := call(t, srv, "GET", "/healthz", "", "", &h); code != 200 || h["status"] != "ok" {
		t.Fatal(code, h)
	}
	var b map[string]any
	if code := call(t, srv, "GET", "/agents/test/budget", "", "", &b); code != 200 || b["tokens_per_day"].(float64) != 50000 {
		t.Fatal(code, b)
	}
	if code := call(t, srv, "GET", "/agents/ghost/budget", "", "", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestPolicyGate(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var d policy.Decision
	call(t, srv, "POST", "/actions/check", "test", `{"action":"draft"}`, &d)
	if d.Level != policy.Free {
		t.Fatal(d)
	}
	call(t, srv, "POST", "/actions/check", "test", `{"action":"payment"}`, &d)
	if d.Level != policy.Owner {
		t.Fatal(d)
	}
	if code := call(t, srv, "POST", "/actions/check", "test", `{"action":"draft","bogus":1}`, nil); code != 400 {
		t.Fatal("unknown fields must be rejected", code)
	}
}

func TestTaskFlow(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var task tasks.Task
	if code := call(t, srv, "POST", "/tasks", "social-media", `{"title":"3 hooks","priority":"P2","goal_contribution":{"kind":"leads","expected":3}}`, &task); code != 201 || task.RequestedBy != "social-media" || task.GoalContribution.Kind != "leads" {
		t.Fatal(code, task)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/lease", "scriptwriter", "", &task); code != 200 || task.Status != tasks.Leased {
		t.Fatal(code, task)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/lease", "dev", "", nil); code != 409 {
		t.Fatal("conflict expected", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/renew", "scriptwriter", "", nil); code != 200 {
		t.Fatal(code)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/handoff", "scriptwriter", `{"to":"dev"}`, &task); code != 200 || task.Owner != "dev" {
		t.Fatal(code, task)
	}
	call(t, srv, "POST", "/tasks/"+task.ID+"/lease", "dev", "", nil)
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/finish", "dev", `{"status":"done"}`, &task); code != 200 || task.Status != tasks.Done {
		t.Fatal(code, task)
	}
	var list []tasks.Task
	if code := call(t, srv, "GET", "/tasks", "", "", &list); code != 200 || len(list) != 1 {
		t.Fatal(code, list)
	}
	if code := call(t, srv, "POST", "/tasks/T-404/lease", "dev", "", nil); code != 404 {
		t.Fatal(code)
	}
}

func TestUsage(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	turn := `{"command_id":"c1","at":"2026-11-03T19:41:00Z","sessions":1,"totals":{"calls":7,"input_tokens":14924,"cache_read_tokens":68096,"output_tokens":1017,"reasoning_tokens":313,"cost_usd":0.0032,"priced":true}}`
	var day usage.Day
	if code := call(t, srv, "POST", "/agents/test/usage", "test", turn, &day); code != 201 || day.Turns != 1 || day.Totals.Tokens() != 84350 {
		t.Fatal(code, day)
	}
	// header must match the path; unknown agents are refused
	if code := call(t, srv, "POST", "/agents/test/usage", "other", turn, nil); code != 403 {
		t.Fatal(code)
	}
	if code := call(t, srv, "POST", "/agents/ghost/usage", "ghost", turn, nil); code != 404 {
		t.Fatal(code)
	}
	if code := call(t, srv, "POST", "/agents/test/usage", "test", `{"totals":{}}`, nil); code != 400 {
		t.Fatal(code)
	}
	var u struct {
		Today  usage.Day    `json:"today"`
		Recent []usage.Turn `json:"recent"`
	}
	if code := call(t, srv, "GET", "/agents/test/usage", "", "", &u); code != 200 || len(u.Recent) != 1 || u.Recent[0].CommandID != "c1" {
		t.Fatal(code, u)
	}
	// the budget shows what was spent today (the server clock decides the day)
	var b map[string]any
	call(t, srv, "GET", "/agents/test/budget", "", "", &b)
	if b["tokens_used"].(float64) != 0 { // the turn above is dated 2026-11-03; the server clock says 2026-11-10
		t.Fatalf("tokens_used today = %v", b["tokens_used"])
	}
	today := `{"command_id":"c2","totals":{"calls":1,"input_tokens":100,"output_tokens":50,"cost_usd":0.0001,"priced":true}}`
	call(t, srv, "POST", "/agents/test/usage", "test", today, nil)
	call(t, srv, "GET", "/agents/test/budget", "", "", &b)
	if b["tokens_used"].(float64) != 150 || b["turns"].(float64) != 1 || b["over_budget"] != false || b["resets_at"] != "2026-11-11T00:00:00Z" {
		t.Fatalf("budget after a turn today: %v", b)
	}
	// D7: reaching tokens_per_day (50 000) flips over_budget.
	big := `{"command_id":"c3","totals":{"calls":1,"input_tokens":49850,"output_tokens":0,"cost_usd":0.01,"priced":true}}`
	call(t, srv, "POST", "/agents/test/usage", "test", big, nil)
	call(t, srv, "GET", "/agents/test/budget", "", "", &b)
	if b["tokens_used"].(float64) != 50000 || b["over_budget"] != true {
		t.Fatalf("budget at the limit: %v", b)
	}
}

// The exact JSON the runtime posts (runtime/internal/usage.Turn, with `model`
// inside totals and per-record fields) must be accepted: found after
// the first deploy of A2 (`json: unknown field "model"` → budget stayed at 0).
func TestUsageAcceptsRuntimeShape(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	body := `{"agent":"test","command_id":"f1cd3342","at":"2026-11-10T12:00:00Z","sessions":1,` +
		`"records":[{"model":"deepseek-v4-flash","provider":"custom","base_url":"https://api.deepseek.com/v1","calls":4,"input_tokens":1004,"cache_read_tokens":53376,"output_tokens":641,"reasoning_tokens":278,"cost_usd":0.000862,"priced":true}],` +
		`"totals":{"model":"","calls":5,"input_tokens":1217,"cache_read_tokens":53632,"output_tokens":654,"reasoning_tokens":278,"cost_usd":0.0009,"priced":true}}`
	var day usage.Day
	if code := call(t, srv, "POST", "/agents/test/usage", "test", body, &day); code != 201 || day.Totals.Tokens() != 55781 {
		t.Fatal(code, day)
	}
	var b map[string]any
	call(t, srv, "GET", "/agents/test/budget", "", "", &b)
	if b["tokens_used"].(float64) != 55781 {
		t.Fatalf("budget: %v", b)
	}
}

// B6 over HTTP: a non-approver's request is blocked; only the approver's
// /approve or /deny moves it; the header identifies the caller.
func TestHandoffApproval(t *testing.T) {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	ink := &profile.Profile{ID: "innkeeper", Model: "deepseek-v4-pro", Planner: true}
	ink.Autonomy.OwnerDecides = profile.HardDeny
	gate := policy.New(p, ink)
	approver := func(a string) bool { pr := gate.Profile(a); return a == "owner" || (pr != nil && pr.Planner) }
	s := &Server{Gate: gate, Tasks: &tasks.Service{Store: tasks.NewMemory(), Approver: approver}, Usage: usage.NewMemory()}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var task tasks.Task
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"hooks","owner":"innkeeper","brief":"please"}`, &task); code != 201 || task.Status != tasks.Blocked || !task.NeedsApproval {
		t.Fatalf("%d %+v", code, task)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/approve", "test", `{}`, nil); code != 403 {
		t.Fatalf("non-approver got %d", code)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/approve", "innkeeper", `{"note":"ok"}`, &task); code != 200 || task.Status != tasks.Queued || task.DecidedBy != "innkeeper" {
		t.Fatalf("%d %+v", code, task)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/deny", "innkeeper", `{}`, nil); code != 409 {
		t.Fatalf("second decision got %d", code)
	}
}

// C1 — one quest record, defined by the owner, moved only by its source, shown with %.
func TestQuest(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var e map[string]any
	if code := call(t, srv, "GET", "/goal", "", "", &e); code != 404 {
		t.Fatal(code, e)
	}
	if code := call(t, srv, "PUT", "/goal", "test", `{"title":"Net revenue","target":120000}`, &e); code != 403 {
		t.Fatalf("hireling defined the quest: %d %v", code, e)
	}
	var q map[string]any
	if code := call(t, srv, "PUT", "/goal", "owner", `{"title":"Net revenue","target":120000,"currency":"brl","period":"2026-Q4"}`, &q); code != 200 || q["source"] != "owner" || q["currency"] != "BRL" {
		t.Fatal(code, q)
	}
	if code := call(t, srv, "POST", "/goal/progress", "test", `{"current":15000}`, &e); code != 403 {
		t.Fatalf("hireling moved the bar: %d %v", code, e)
	}
	if code := call(t, srv, "POST", "/goal/progress", "", `{"from":"owner","current":15000}`, &q); code != 200 || q["percent"] != 12.5 {
		t.Fatal(code, q)
	}
	if code := call(t, srv, "GET", "/goal", "", "", &q); code != 200 || q["current"] != 15000.0 || !strings.HasPrefix(q["bar"].(string), "Quest: Net revenue — 12.5% [█░░░░░░░░░]") {
		t.Fatal(code, q)
	}
	if kinds, _ := q["kinds"].([]any); len(kinds) != len(goal.DefaultKinds) {
		t.Fatal(q["kinds"])
	}
}

// C3 — GET /goal shows what open and done tasks declare, by kind.
func TestQuestExpected(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var q map[string]any
	call(t, srv, "PUT", "/goal", "owner", `{"title":"Net revenue","target":120000,"kinds":["leads","posts"]}`, &q)
	var e map[string]any
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"hooks","requested_by":"test","goal_contribution":{"kind":"revenue","expected":1}}`, &e); code != 400 {
		t.Fatalf("kind outside the quest accepted: %d %v", code, e)
	}
	if code := call(t, srv, "POST", "/tasks", "test", `{"title":"hooks","requested_by":"test","goal_contribution":{"kind":"leads","expected":10,"note":"warm-up"}}`, &e); code != 201 && code != 200 {
		t.Fatal(code, e)
	}
	if code := call(t, srv, "GET", "/goal", "", "", &q); code != 200 {
		t.Fatal(code, q)
	}
	if open, _ := q["expected_open"].(map[string]any); open["leads"] != 10.0 {
		t.Fatalf("expected_open = %v", q["expected_open"])
	}
}

// B3 — GET /agents/{id}/queue returns the owner's queued tasks, P0 first.
func TestQueueEndpoint(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	for _, b := range []string{
		`{"title":"routine","owner":"test","priority":"P3","goal_contribution":{"kind":"posts","expected":1}}`,
		`{"title":"order","owner":"test","priority":"P0","goal_contribution":{"kind":"support"}}`,
		`{"title":"handoff","owner":"test","priority":"P2","goal_contribution":{"kind":"leads","expected":3}}`,
	} {
		if code := call(t, srv, "POST", "/tasks", "test", b, nil); code != 201 {
			t.Fatal(code, b)
		}
	}
	var q struct {
		Tasks []tasks.Task `json:"tasks"`
	}
	if code := call(t, srv, "GET", "/agents/test/queue", "", "", &q); code != 200 || len(q.Tasks) != 3 || q.Tasks[0].Title != "order" || q.Tasks[1].Title != "handoff" || q.Tasks[2].Title != "routine" {
		t.Fatalf("%d %+v", code, q.Tasks)
	}
	var one tasks.Task
	if code := call(t, srv, "GET", "/tasks/"+q.Tasks[0].ID, "", "", &one); code != 200 || one.Title != "order" {
		t.Fatal(code, one)
	}
	if code := call(t, srv, "GET", "/agents/ghost/queue", "", "", nil); code != 404 {
		t.Fatal(code)
	}
}

// B11 over HTTP: fail twice with max 2 → blocked needs_human; unblock retry → queued.
func TestFailAndUnblock(t *testing.T) {
	srv := newServer()
	defer srv.Close()
	var task tasks.Task
	call(t, srv, "POST", "/tasks", "innkeeper", `{"title":"hooks","owner":"dev","priority":"P1","goal_contribution":{"kind":"leads","expected":1}}`, &task)
	var got map[string]any
	for i := 1; i <= 2; i++ {
		call(t, srv, "POST", "/tasks/"+task.ID+"/lease", "dev", "", nil)
		got = nil
		if code := call(t, srv, "POST", "/tasks/"+task.ID+"/fail", "dev", `{"error":"boom","max":2}`, &got); code != 200 || got["failures"] != float64(i) {
			t.Fatal(code, got)
		}
	}
	if got["status"] != "blocked" || got["blocked_reason"] != tasks.NeedsHuman {
		t.Fatal(got)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/unblock", "scriptwriter", `{"action":"Retry"}`, nil); code != 409 {
		t.Fatal("only the owner unblocks", code)
	}
	got = nil
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/unblock", "dev", `{"action":"Retry"}`, &got); code != 200 || got["status"] != "queued" || got["failures"] != nil {
		t.Fatal(code, got)
	}
	if code := call(t, srv, "POST", "/tasks/"+task.ID+"/unblock", "dev", `{"action":"Retry"}`, nil); code != 400 {
		t.Fatal("unblock of a queued task must fail", code)
	}
}

// D1/D3 over HTTP: the hook's check (use) remembers the block, the runtime
// attaches it to the owner's card, the owner's yes grants exactly one use.
func TestGateGrantsOverHTTP(t *testing.T) {
	p := &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"open_pr"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	g := policy.NewMemoryGrants()
	g.Now = func() time.Time { return now }
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(),
		Grants: g, Approvals: fakeApprovals{}, Now: func() time.Time { return now }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	var d policy.Decision
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"open_pr","use":true}`, &d)
	if d.Level != policy.Free {
		t.Fatalf("free action: %+v", d)
	}
	// A dry check (no "use") neither records nor consumes.
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"merge_pr","target":"gh pr merge 7"}`, &d)
	var acts []policy.Act
	call(t, srv, "GET", "/agents/dev/blocked?since=2026-11-05T08:59:00Z", "", "", &acts)
	if d.Level != policy.Owner || len(acts) != 0 {
		t.Fatalf("dry check: %+v, recorded %v", d, acts)
	}
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"merge_pr","target":"gh pr merge 7","use":true}`, &d)
	call(t, srv, "GET", "/agents/dev/blocked?since=2026-11-05T08:59:00Z", "", "", &acts)
	if d.Level != policy.Owner || len(acts) != 1 || acts[0].Action != "merge_pr" {
		t.Fatalf("blocked: %+v, recorded %v", d, acts)
	}
	if code := call(t, srv, "POST", "/actions/pending", "dev", `{"decision":"dec-1","actions":[{"action":"merge_pr","target":"gh pr merge 7"}],"approve":"Approve"}`, nil); code != 200 {
		t.Fatalf("pending: %d", code)
	}
	var ans struct {
		Granted []policy.Act `json:"granted"`
	}
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"unknown","choice":"Approve","agent":"dev"}`, &ans)
	if len(ans.Granted) != 0 {
		t.Fatalf("a card without gate actions granted %v", ans.Granted)
	}
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"dec-1","choice":"Approve","agent":"dev"}`, &ans)
	if len(ans.Granted) != 1 {
		t.Fatalf("approve: %v", ans.Granted)
	}
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"merge_pr","use":true}`, &d)
	if d.Level != policy.Free || !strings.Contains(d.Reason, "the owner approved") {
		t.Fatalf("granted use: %+v", d)
	}
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"merge_pr","use":true}`, &d)
	if d.Level != policy.Owner {
		t.Fatalf("second use without a new approval: %+v", d)
	}
}

// D2: a capped action counts per day (UTC) or ISO week; past the cap it is
// the owner's, his yes allows one more, and the next day starts again.
func TestCappedLimitsOverHTTP(t *testing.T) {
	p := &profile.Profile{ID: "social-media", Model: "deepseek-v4-flash"}
	p.Autonomy.Capped = map[string]map[string]int{
		"publish_owned_channel": {"per_day": 2},
		"propose_hire":          {"per_week": 1},
		"ad_spend_usd":          {"per_day": 50},
	}
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC)
	g := policy.NewMemoryGrants()
	g.Now = func() time.Time { return now }
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(),
		Grants: g, Approvals: fakeApprovals{}, Now: func() time.Time { return now }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	check := func(action string) policy.Decision {
		var d policy.Decision
		call(t, srv, "POST", "/actions/check", "social-media", `{"action":"`+action+`","target":"instagram","use":true}`, &d)
		return d
	}
	for i := 1; i <= 2; i++ {
		if d := check("publish_owned_channel"); d.Level != policy.Capped || !d.Counted || d.Used != i || d.Limit != 2 || d.Period != "day" {
			t.Fatalf("use %d: %+v", i, d)
		}
	}
	d := check("publish_owned_channel")
	if d.Level != policy.Owner || !strings.Contains(d.Reason, "cap reached (D2): 2/2 this day") {
		t.Fatalf("third use: %+v", d)
	}
	// The block rides on the owner's card; his yes allows exactly one more.
	var acts []policy.Act
	call(t, srv, "GET", "/agents/social-media/blocked", "", "", &acts)
	if len(acts) != 1 || acts[0].Action != "publish_owned_channel" {
		t.Fatalf("blocked = %v", acts)
	}
	call(t, srv, "POST", "/actions/pending", "social-media", `{"decision":"d-9","actions":[{"action":"publish_owned_channel"}],"approve":"Approve"}`, nil)
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"d-9","choice":"Approve","agent":"social-media"}`, nil)
	if d := check("publish_owned_channel"); d.Level != policy.Free {
		t.Fatalf("approved extra use: %+v", d)
	}
	if d := check("publish_owned_channel"); d.Level != policy.Owner {
		t.Fatalf("one yes, one extra use: %+v", d)
	}
	// Next UTC day: the count starts again.
	now = now.Add(2 * time.Hour)
	if d := check("publish_owned_channel"); d.Level != policy.Capped || d.Used != 1 {
		t.Fatalf("new day: %+v", d)
	}
	// Weekly cap.
	if d := check("propose_hire"); d.Level != policy.Capped || d.Period != "week" {
		t.Fatalf("weekly: %+v", d)
	}
	if d := check("propose_hire"); d.Level != policy.Owner {
		t.Fatalf("weekly cap not enforced: %+v", d)
	}
	// A cap in dollars cannot be counted per call.
	if d := check("ad_spend_usd"); d.Level != policy.Owner || !strings.Contains(d.Reason, "USD") {
		t.Fatalf("usd cap: %+v", d)
	}
	// A dry check (no "use") reports the cap without counting.
	var dry policy.Decision
	call(t, srv, "POST", "/actions/check", "social-media", `{"action":"publish_owned_channel"}`, &dry)
	if dry.Level != policy.Capped || dry.Counted {
		t.Fatalf("dry: %+v", dry)
	}
}

// D4: every "use" check lands in the Autonomy Log with who, what, why and the
// cost of its turn; dry checks do not.
func TestAutonomyLogOverHTTP(t *testing.T) {
	p := &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"open_pr"}
	p.Autonomy.Capped = map[string]map[string]int{"deploy_non_critical": {"per_day": 1}}
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	spent := usage.NewMemory()
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: spent,
		Grants: policy.NewMemoryGrants(), Log: autonomy.NewMemory(), Now: func() time.Time { return now }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	call(t, srv, "POST", "/actions/check", "dev", `{"action":"open_pr","target":"gh pr create --fill","use":true,"why":"ship the D4 fix","turn":"c-42","task":"T-7"}`, nil)
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"deploy_non_critical","use":true,"turn":"c-42"}`, nil)
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"deploy_non_critical","use":true,"turn":"c-42"}`, nil)
	call(t, srv, "POST", "/actions/check", "dev", `{"action":"open_pr"}`, nil) // dry: not logged
	if err := spent.Add(context.Background(), usage.Turn{Agent: "dev", CommandID: "c-42", At: now,
		Totals: usage.Totals{InputTokens: 1000, OutputTokens: 200, CostUSD: 0.0042, Priced: true}}); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Day     string           `json:"day"`
		Done    int              `json:"done"`
		Blocked int              `json:"blocked"`
		Entries []autonomy.Entry `json:"entries"`
	}
	if code := call(t, srv, "GET", "/autonomy?agent=dev", "", "", &out); code != 200 {
		t.Fatal(code)
	}
	if out.Day != "2026-11-05" || len(out.Entries) != 3 || out.Done != 2 || out.Blocked != 1 {
		t.Fatalf("log = %+v", out)
	}
	pr := out.Entries[2] // oldest
	if pr.Agent != "dev" || pr.Action != "open_pr" || pr.Outcome != "done" || pr.Level != "free" ||
		pr.Why != "ship the D4 fix" || pr.Task != "T-7" || pr.Target != "gh pr create --fill" {
		t.Fatalf("entry = %+v", pr)
	}
	if pr.TurnCostUSD == nil || *pr.TurnCostUSD != 0.0042 || pr.TurnTokens != 1200 {
		t.Fatalf("turn cost not joined: %+v", pr)
	}
	capped, over := out.Entries[1], out.Entries[0]
	if capped.Outcome != "done" || capped.Level != "capped" || capped.Used != 1 || capped.Limit != 1 {
		t.Fatalf("capped entry = %+v", capped)
	}
	if over.Outcome != "blocked" || over.Level != "owner" || !strings.Contains(over.Reason, "cap reached") {
		t.Fatalf("over-cap entry = %+v", over)
	}
	if code := call(t, srv, "GET", "/autonomy?day=yesterday", "", "", nil); code != 400 {
		t.Fatalf("bad day: %d", code)
	}
}

// D11: an ask about the hireling's own action goes back to it (once per task
// and action); one card per task; the owner's choice closes it with the follow-ups
// and counts whether he picked the recommendation.
func TestAutonomyFirstAsks(t *testing.T) {
	p := &profile.Profile{ID: "social-media", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft"}
	p.Autonomy.Capped = map[string]map[string]int{"publish_owned_channel": {"per_day": 1}, "ad_spend_usd": {"per_day": 50}}
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	g := policy.NewMemoryGrants()
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(),
		Grants: g, Asks: asks.NewMemory(), Now: func() time.Time { return now }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	pre := func(body string) AskVerdict {
		var v AskVerdict
		if code := call(t, srv, "POST", "/asks", "social-media", body, &v); code != 200 {
			t.Fatalf("POST /asks %s: %d", body, code)
		}
		return v
	}
	if v := pre(`{"task":"T-1","action":"draft","question":"Can I draft the post?"}`); v.Verdict != "yours" || v.Level != "free" {
		t.Fatalf("free: %+v", v)
	}
	if v := pre(`{"task":"T-1","action":"draft","question":"Really?"}`); v.Verdict != "ask" || !strings.Contains(v.Reason, "asked again") {
		t.Fatalf("asked again: %+v", v)
	}
	if v := pre(`{"task":"T-2","action":"publish_owned_channel"}`); v.Verdict != "yours" || !strings.Contains(v.Reason, "0/1 used this day") {
		t.Fatalf("within the cap: %+v", v)
	}
	call(t, srv, "POST", "/actions/check", "social-media", `{"action":"publish_owned_channel","use":true}`, nil)
	if v := pre(`{"task":"T-3","action":"publish_owned_channel"}`); v.Verdict != "ask" {
		t.Fatalf("over the cap is the owner's: %+v", v)
	}
	if v := pre(`{"task":"T-4","action":"ad_spend_usd"}`); v.Verdict != "ask" {
		t.Fatalf("a spend is the owner's: %+v", v)
	}
	if v := pre(`{"task":"T-5","action":"draft","blocked":true}`); v.Verdict != "ask" {
		t.Fatalf("the gate blocked something in the turn: %+v", v)
	}
	if v := pre(`{"task":"T-6","action":"payment"}`); v.Verdict != "ask" || v.Level != "owner" {
		t.Fatalf("payment: %+v", v)
	}
	call(t, srv, "POST", "/asks/opened", "social-media", `{"decision":"d-1","task":"T-6","action":"payment","question":"Pay the invoice?","recommended":"Pay it"}`, nil)
	if v := pre(`{"task":"T-6","action":"draft","question":"And post the receipt?"}`); v.Verdict != "joined" || v.Decision != "d-1" || len(v.FollowUps) != 1 {
		t.Fatalf("one card per task: %+v", v)
	}
	var ans struct {
		Found bool          `json:"found"`
		Card  asks.Answered `json:"card"`
	}
	call(t, srv, "POST", "/asks/answer", "", `{"decision":"d-1","choice":"Pay it"}`, &ans)
	if !ans.Found || !ans.Card.PickedRecommended || ans.Card.FollowUps[0] != "And post the receipt?" {
		t.Fatalf("answer: %+v", ans)
	}
	call(t, srv, "POST", "/asks/answer", "", `{"decision":"nope","choice":"x"}`, &ans)
	if ans.Found {
		t.Fatal("unknown card found")
	}
	var list struct {
		Open  []asks.Open  `json:"open"`
		Stats []asks.Stats `json:"stats"`
	}
	call(t, srv, "GET", "/asks?agent=social-media", "", "", &list)
	if len(list.Open) != 0 || len(list.Stats) != 1 {
		t.Fatalf("list: %+v", list)
	}
	st := list.Stats[0]
	if st.Bounced != 2 || st.Opened != 1 || st.Joined != 1 || st.Answered != 1 || st.CouldHaveDecidedAlone != 1 {
		t.Fatalf("stats: %+v", st)
	}
}

// D8: web_call is counted per UTC day against web_calls_per_day; past it the
// level is "budget" (not the owner's) and the block lands in the Autonomy Log.
func TestWebCallBudget(t *testing.T) {
	p := &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}
	p.Budget.WebCallsPerDay = 2
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC)
	log := autonomy.NewMemory()
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(),
		Grants: policy.NewMemoryGrants(), Log: log, Now: func() time.Time { return now }}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	web := func(use bool) policy.Decision {
		var d policy.Decision
		body := `{"action":"web_call","target":"web_search canva"}`
		if use {
			body = `{"action":"web_call","target":"web_search canva","use":true,"turn":"c-1"}`
		}
		call(t, srv, "POST", "/actions/check", "dev", body, &d)
		return d
	}
	if d := web(false); d.Level != policy.Free || d.Counted || d.Used != 0 {
		t.Fatalf("dry check: %+v", d)
	}
	for i := 1; i <= 2; i++ {
		if d := web(true); d.Level != policy.Free || !d.Counted || d.Used != i || d.Limit != 2 {
			t.Fatalf("call %d: %+v", i, d)
		}
	}
	d := web(true)
	if d.Level != policy.Budget || !strings.Contains(d.Reason, "2/2") {
		t.Fatalf("third call: %+v", d)
	}
	var b map[string]any
	call(t, srv, "GET", "/agents/dev/budget", "", "", &b)
	if b["web_calls_used"].(float64) != 2 {
		t.Fatalf("budget: %v", b)
	}
	entries, _ := log.List(context.Background(), "2026-11-05", "", 10)
	if len(entries) != 1 || entries[0].Outcome != "blocked" || entries[0].Level != "budget" {
		t.Fatalf("log: %+v (only the blocked call is logged)", entries)
	}
	// The next UTC day starts again.
	now = now.Add(2 * time.Hour)
	if d := web(true); d.Level != policy.Free || d.Used != 1 {
		t.Fatalf("next day: %+v", d)
	}
}

// D1/D3: the body of /actions/answer proves nothing. A hireling that reaches
// tavern-core cannot grant itself a use: its card is tied to an open decision
// of its own, once, before the owner answers, and the desk writes on the
// decision what a yes grants; the grant waits for the Front Desk's record of
// the owner's answer (only the owner token can answer) and only the
// decision's first option is a yes.
func TestAnswerNeedsTheOwnersAnswer(t *testing.T) {
	dev := &profile.Profile{ID: "dev", DeskID: "builder", Model: "deepseek-v4-flash"}
	dev.Autonomy.OwnerDecides = profile.HardDeny
	other := &profile.Profile{ID: "writer", DeskID: "writer", Model: "deepseek-v4-flash"}
	other.Autonomy.OwnerDecides = profile.HardDeny
	desk := &frontdesk.Desk{Agents: []string{"builder", "writer"}, SharedToken: strings.Repeat("s", 40), OwnerToken: strings.Repeat("o", 40)}
	g := policy.NewMemoryGrants()
	s := &Server{Gate: policy.New(dev, other), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(),
		Grants: g, Approvals: desk, Extra: desk.Register, Owner: desk.IsOwner}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	deskCall := func(path, token, body string, out any) int {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			_ = json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	open := func(from string) string {
		t.Helper()
		var out struct{ Decision frontdesk.Decision }
		if code := deskCall("/api/decisions", desk.SharedToken, `{"from":"`+from+`","question":"Merge the PR?","options":["Approve","No"]}`, &out); code != 200 || out.Decision.ID == "" {
			t.Fatalf("open decision: %d %+v", code, out)
		}
		return out.Decision.ID
	}
	choose := func(id, option string) {
		t.Helper()
		if code := deskCall("/api/decisions/"+id+"/choose", desk.OwnerToken, `{"option":"`+option+`"}`, nil); code != 200 {
			t.Fatalf("choose %s: %d", option, code)
		}
	}
	type answer struct {
		Granted []policy.Act `json:"granted"`
		Error   string       `json:"error"`
	}
	ask := func(id, agent string) (int, answer) {
		t.Helper()
		var ans answer
		body := `{"decision":"` + id + `","choice":"Approve"`
		if agent != "" {
			body += `,"agent":"` + agent + `"`
		}
		code := call(t, srv, "POST", "/actions/answer", agent, body+"}", &ans)
		return code, ans
	}
	pendAs := func(id, agent, action, approve string) int {
		t.Helper()
		return call(t, srv, "POST", "/actions/pending", agent, `{"decision":"`+id+`","actions":[{"action":"`+action+`"}],"approve":"`+approve+`"}`, nil)
	}
	pend := func(id, agent string) {
		t.Helper()
		if code := pendAs(id, agent, "merge_pr", "Approve"); code != 200 {
			t.Fatalf("pending: %d", code)
		}
	}
	used := func(agent, action string) policy.Level {
		t.Helper()
		var d policy.Decision
		call(t, srv, "POST", "/actions/check", agent, `{"action":"`+action+`","use":true}`, &d)
		return d.Level
	}
	get := func(id string) frontdesk.Decision {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/decisions/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+desk.OwnerToken)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out struct{ Decision frontdesk.Decision }
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out.Decision
	}

	d1 := open("builder")
	pend(d1, "dev")
	// The owner reads, in the desk's words, what a yes grants.
	if x := get(d1); !strings.Contains(x.Context, `Choosing "Approve" grants dev one use of: merge_pr.`) {
		t.Fatalf("the decision does not say what a yes grants: %q", x.Context)
	}
	// Still open: the hireling's own "Approve" is not enough.
	if code, ans := ask(d1, "dev"); code != 403 || len(ans.Granted) != 0 {
		t.Fatalf("self-approval: %d %+v", code, ans)
	}
	if code, _ := ask(d1, "ghost"); code != 403 {
		t.Fatalf("unknown hireling: %d", code)
	}
	if code, _ := ask(d1, ""); code != 400 {
		t.Fatalf("missing agent: %d", code)
	}
	if code, _ := ask("d-unknown", "dev"); code != 403 {
		t.Fatalf("unknown decision: %d", code)
	}
	// One card per decision: its actions cannot be swapped before the answer.
	if code := pendAs(d1, "dev", "payment", "Approve"); code != 409 {
		t.Fatalf("second card on an open decision: %d", code)
	}
	// Another hireling's answered decision does not grant dev anything.
	d2 := open("writer")
	choose(d2, "Approve")
	if code, ans := ask(d2, "dev"); code != 403 || len(ans.Granted) != 0 {
		t.Fatalf("someone else's decision: %d %+v", code, ans)
	}
	// The owner answers: the card survived the refusals, one use is granted.
	choose(d1, "Approve")
	if code, ans := ask(d1, "dev"); code != 200 || len(ans.Granted) != 1 || ans.Granted[0].Action != "merge_pr" {
		t.Fatalf("owner approved: %d %+v", code, ans)
	}
	if l := used("dev", "merge_pr"); l != policy.Free {
		t.Fatalf("granted use: %s", l)
	}
	// Replay: an answered decision takes no new card, and answering it again
	// grants nothing more.
	if code := pendAs(d1, "dev", "send_email", "Approve"); code != 409 {
		t.Fatalf("card on an answered decision: %d", code)
	}
	if code, ans := ask(d1, "dev"); code != 200 || len(ans.Granted) != 0 {
		t.Fatalf("second answer: %d %+v", code, ans)
	}
	if l := used("dev", "send_email"); l == policy.Free {
		t.Fatal("an old yes granted a new action")
	}

	// The owner said no: the body's "Approve" does not count.
	d3 := open("builder")
	pend(d3, "dev")
	choose(d3, "No")
	if code, ans := ask(d3, "dev"); code != 200 || len(ans.Granted) != 0 {
		t.Fatalf("owner said no: %d %+v", code, ans)
	}
	// The hireling cannot name the option that grants: a card that says
	// approve:"No" still grants only on the decision's first option.
	d4 := open("builder")
	if code := pendAs(d4, "dev", "merge_pr", "No"); code != 200 {
		t.Fatalf("pending: %d", code)
	}
	choose(d4, "No")
	if code, ans := ask(d4, "dev"); code != 200 || len(ans.Granted) != 0 {
		t.Fatalf("the owner's no became a grant: %d %+v", code, ans)
	}
	// A card pended after the answer is refused, so the answer finds none.
	d5 := open("builder")
	choose(d5, "Approve")
	if code := pendAs(d5, "dev", "merge_pr", "Approve"); code != 409 {
		t.Fatalf("card after the answer: %d", code)
	}
	if code, ans := ask(d5, "dev"); code != 403 || len(ans.Granted) != 0 {
		t.Fatalf("answer without a card: %d %+v", code, ans)
	}
	// Another hireling cannot hang its card on dev's decision.
	d6 := open("builder")
	if code := pendAs(d6, "writer", "merge_pr", "Approve"); code != 403 {
		t.Fatalf("card of another hireling: %d", code)
	}
	choose(d6, "Approve")
	if l := used("writer", "merge_pr"); l == policy.Free {
		t.Fatal("writer got a use from dev's decision")
	}
	// A card whose line would be cut is refused: the line the owner reads
	// names every action a yes grants, or there is no card.
	d7 := open("builder")
	many := make([]string, 0, 41)
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf(`{"action":"read_docs_%02d"}`, i))
	}
	many = append(many, `{"action":"payment"}`)
	if code := call(t, srv, "POST", "/actions/pending", "dev", `{"decision":"`+d7+`","actions":[`+strings.Join(many, ",")+`],"approve":"Approve"}`, nil); code != 400 {
		t.Fatalf("a card longer than the decision can show: %d", code)
	}
	choose(d7, "Approve")
	if code, ans := ask(d7, "dev"); code != 403 || len(ans.Granted) != 0 {
		t.Fatalf("a refused card granted: %d %+v", code, ans)
	}
	if l := used("dev", "payment"); l == policy.Free {
		t.Fatal("payment became free through a cut card")
	}
	// The option that grants is the first plain yes, whatever the order: with
	// ["No","Approve"] the owner's "No" grants nothing; with a "Not now"
	// recommendation first, "Go ahead" grants.
	openWith := func(options string) string {
		t.Helper()
		var out struct{ Decision frontdesk.Decision }
		if code := deskCall("/api/decisions", desk.SharedToken, `{"from":"builder","question":"Merge the PR?","options":`+options+`}`, &out); code != 200 {
			t.Fatalf("open %s: %d", options, code)
		}
		return out.Decision.ID
	}
	noFirst := openWith(`["No","Approve"]`)
	pend(noFirst, "dev")
	if x := get(noFirst); !strings.HasPrefix(x.Context, `Choosing "Approve" grants dev`) {
		t.Fatalf("the card names the wrong option: %q", x.Context)
	}
	choose(noFirst, "No")
	if code, ans := ask(noFirst, "dev"); code != 200 || len(ans.Granted) != 0 {
		t.Fatalf("the owner's No granted: %d %+v", code, ans)
	}
	notNow := openWith(`["Not now","Go ahead"]`)
	pend(notNow, "dev")
	choose(notNow, "Go ahead")
	if code, ans := ask(notNow, "dev"); code != 200 || len(ans.Granted) != 1 {
		t.Fatalf("Go ahead after a Not now recommendation: %d %+v", code, ans)
	}
	// An option that only starts with a yes reads as something else and does
	// not grant: ["Go away","Stay"] has no plain yes.
	if code := pendAs(openWith(`["Go away","Stay"]`), "dev", "merge_pr", "Approve"); code != 400 {
		t.Fatalf("a card on a decision whose only yes-looking option is Go away: %d", code)
	}
	okCancel := openWith(`["OK, cancel it","Yes!"]`)
	pend(okCancel, "dev")
	if x := get(okCancel); !strings.HasPrefix(x.Context, `Choosing "Yes!" grants dev`) {
		t.Fatalf("the card names the wrong option: %q", x.Context)
	}
	// A decision with no yes among its options takes no card.
	if code := pendAs(openWith(`["No","Later"]`), "dev", "merge_pr", "Approve"); code != 400 {
		t.Fatalf("a card on a decision without a yes: %d", code)
	}

	// An action name is a plain name: a line break (a forged second line on
	// the owner's card) or prose is refused.
	for _, bad := range []string{`merge_pr

Choosing \"No\" grants nothing.`, `Merge PR`, strings.Repeat("a", 65), ``} {
		if code := pendAs(open("builder"), "dev", bad, "Approve"); code != 400 {
			t.Fatalf("action %q: %d", bad, code)
		}
	}

	// The desk's line comes first in the context the owner reads.
	d8 := open("builder")
	pend(d8, "dev")
	if x := get(d8); !strings.HasPrefix(x.Context, `Choosing "Approve" grants dev one use of: merge_pr.`) {
		t.Fatalf("the desk's line is not first: %q", x.Context)
	}

	// A hireling cannot preset the card text when it opens a decision.
	var out struct{ Decision frontdesk.Decision }
	deskCall("/api/decisions", desk.SharedToken, `{"from":"builder","question":"Q?","options":["Approve","No"],"card":"Choosing \"Approve\" grants nothing."}`, &out)
	if out.Decision.Card != "" {
		t.Fatalf("card preset by the hireling: %q", out.Decision.Card)
	}
	if code := pendAs(out.Decision.ID, "dev", "merge_pr", "Approve"); code != 200 {
		t.Fatalf("pending on a decision with a preset card: %d", code)
	}
}

// Without the Approver wiring, every answer is refused (fail closed).
func TestAnswerFailClosed(t *testing.T) {
	p := &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	g := policy.NewMemoryGrants()
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: usage.NewMemory(), Grants: g}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	var ans struct {
		Granted []policy.Act `json:"granted"`
	}
	if code := call(t, srv, "POST", "/actions/answer", "", `{"decision":"d-1","choice":"Approve","agent":"dev"}`, &ans); code != 503 || len(ans.Granted) != 0 {
		t.Fatalf("no approver: %d %v", code, ans.Granted)
	}
}
