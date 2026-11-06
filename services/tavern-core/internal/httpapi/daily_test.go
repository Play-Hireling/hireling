package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/daily"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// dailyServer is newServer with the B10 daily plan on and a clock the test moves.
func dailyServer(now *time.Time) *httptest.Server {
	p := &profile.Profile{ID: "test", Model: "deepseek-v4-flash"}
	p.Autonomy.OwnerDecides = profile.HardDeny
	clock := func() time.Time { return *now }
	approver := func(a string) bool { return a == "owner" || a == "innkeeper" }
	quests := &goal.Service{Store: goal.NewMemory(), Approver: approver, Now: clock}
	queue := &tasks.Service{Store: tasks.NewMemory(), Approver: approver, Now: clock}
	s := &Server{Gate: policy.New(p), Tasks: queue, Usage: usage.NewMemory(), Goal: quests,
		Daily: &daily.Service{Store: daily.NewMemory(), Tasks: queue, Goal: quests, Approver: approver, Now: clock},
		Now:   clock}
	return httptest.NewServer(s.Handler())
}

// B10 — the Innkeeper posts the day's plan once; the quest board (GET /goal)
// shows it under the bar.
func TestDailyPlanOverHTTP(t *testing.T) {
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	srv := dailyServer(&now)
	defer srv.Close()
	call(t, srv, "PUT", "/goal", "owner", `{"title":"Net revenue","target":120000}`, nil)
	call(t, srv, "POST", "/tasks", "innkeeper", `{"title":"Fix the signup form","owner":"dev","priority":"P0","goal_contribution":{"kind":"revenue"}}`, nil)
	call(t, srv, "POST", "/tasks", "innkeeper", `{"title":"3 hooks","owner":"scriptwriter","priority":"P2","goal_contribution":{"kind":"posts","expected":3}}`, nil)

	var e map[string]any
	if code := call(t, srv, "GET", "/plans/daily", "", "", &e); code != 404 {
		t.Fatal(code, e)
	}
	if code := call(t, srv, "POST", "/plans/daily", "dev", `{"day":"2026-11-05","tz":"America/Montevideo"}`, &e); code != 403 {
		t.Fatalf("a hireling posted the plan: %d %v", code, e)
	}
	if code := call(t, srv, "POST", "/plans/daily", "innkeeper", `{"day":"tomorrow"}`, &e); code != 400 {
		t.Fatal(code, e)
	}
	var out struct {
		Created bool        `json:"created"`
		Plan    *daily.Plan `json:"plan"`
	}
	if code := call(t, srv, "POST", "/plans/daily", "innkeeper", `{"day":"2026-11-05","tz":"America/Montevideo"}`, &out); code != 201 || !out.Created {
		t.Fatal(code, out)
	}
	if out.Plan.Summary != "2 tasks: dev 1, scriptwriter 1 · quest 0.0%" || !strings.Contains(out.Plan.Text, "- T-1 P0 Fix the signup form · 0 revenue") {
		t.Fatalf("%q\n%s", out.Plan.Summary, out.Plan.Text)
	}
	first := out.Plan.PostedAt

	// Same day, later (the runtime restarted): 200, created false, same record.
	now = now.Add(2 * time.Hour)
	out.Plan = nil
	if code := call(t, srv, "POST", "/plans/daily", "innkeeper", `{"day":"2026-11-05","tz":"America/Montevideo"}`, &out); code != 200 || out.Created || !out.Plan.PostedAt.Equal(first) {
		t.Fatal(code, out)
	}

	// The quest board carries it.
	var q struct {
		Bar       string      `json:"bar"`
		DailyPlan *daily.Plan `json:"daily_plan"`
	}
	if code := call(t, srv, "GET", "/goal", "", "", &q); code != 200 || q.DailyPlan == nil || q.DailyPlan.Day != "2026-11-05" || !strings.HasPrefix(q.DailyPlan.Text, "Daily plan · 2026-11-05") {
		t.Fatal(code, q)
	}
	var p daily.Plan
	if code := call(t, srv, "GET", "/plans/daily?day=2026-11-05", "", "", &p); code != 200 || p.PostedBy != "innkeeper" {
		t.Fatal(code, p)
	}

	// Next day: a new plan.
	now = now.Add(24 * time.Hour)
	if code := call(t, srv, "POST", "/plans/daily", "innkeeper", `{"day":"2026-11-06","tz":"America/Montevideo"}`, &out); code != 201 || !out.Created {
		t.Fatal(code, out)
	}
	if code := call(t, srv, "GET", "/goal", "", "", &q); code != 200 || q.DailyPlan.Day != "2026-11-06" {
		t.Fatal(code, q.DailyPlan)
	}
	// The Innkeeper's note and the log mark, shown on /plans/daily and /goal.
	if code := call(t, srv, "POST", "/plans/daily/2026-11-06/commentary", "dev", `{"text":"x"}`, &e); code != 403 {
		t.Fatal(code, e)
	}
	if code := call(t, srv, "POST", "/plans/daily/2026-10-01/commentary", "innkeeper", `{"text":"x"}`, &e); code != 404 {
		t.Fatal(code, e)
	}
	if code := call(t, srv, "POST", "/plans/daily/2026-11-06/commentary", "innkeeper", `{"text":"Signup fix first; the hooks can wait."}`, &p); code != 200 || p.Commentary == "" {
		t.Fatal(code, p)
	}
	if code := call(t, srv, "POST", "/plans/daily/2026-11-06/logged", "innkeeper", ``, &p); code != 200 || p.LoggedAt == nil {
		t.Fatal(code, p)
	}
	if code := call(t, srv, "GET", "/plans/daily?day=2026-11-06", "", "", &p); code != 200 || p.Commentary != "Signup fix first; the hooks can wait." || p.LoggedAt == nil {
		t.Fatal(code, p)
	}
	q.DailyPlan = nil
	if code := call(t, srv, "GET", "/goal", "", "", &q); code != 200 || q.DailyPlan.Commentary != "Signup fix first; the hooks can wait." || !strings.Contains(q.DailyPlan.Text, "Innkeeper's note") {
		t.Fatal(code, q.DailyPlan)
	}
}
