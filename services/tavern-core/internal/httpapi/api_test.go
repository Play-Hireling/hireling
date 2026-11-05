package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/asks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/autonomy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

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
	if b["tokens_used"].(float64) != 150 || b["turns"].(float64) != 1 {
		t.Fatalf("budget after a turn today: %v", b)
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
		Grants: g, Now: func() time.Time { return now }}
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
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"unknown","choice":"Approve"}`, &ans)
	if len(ans.Granted) != 0 {
		t.Fatalf("a card without gate actions granted %v", ans.Granted)
	}
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"dec-1","choice":"Approve"}`, &ans)
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
		Grants: g, Now: func() time.Time { return now }}
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
	call(t, srv, "POST", "/actions/answer", "", `{"decision":"d-9","choice":"Approve"}`, nil)
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
