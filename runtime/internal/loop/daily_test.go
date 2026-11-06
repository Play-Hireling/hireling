package loop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// fakeDaily is tavern-core's B10 API: POST /plans/daily (one record per day,
// the first post creates it), /plans/daily/{day}/commentary (once) and
// /plans/daily/{day}/logged.
type fakeDaily struct {
	mu       sync.Mutex
	plans    map[string]tavern.DailyPlan
	posts    []map[string]string // bodies, with the caller
	comments int
	marks    int
	fail     map[string]bool // "post", "commentary", "logged" → 503
}

func newFakeDaily(t *testing.T) (*fakeDaily, *tavern.Client) {
	f := &fakeDaily{plans: map[string]tavern.DailyPlan{}, fail: map[string]bool{}}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		if b == nil {
			b = map[string]string{}
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/plans/daily"), "/") // "", or "", day, op
		op := "post"
		if len(parts) == 3 {
			op = parts[2]
		}
		if r.Method != http.MethodPost || (op != "post" && op != "commentary" && op != "logged") {
			w.WriteHeader(404)
			return
		}
		if f.fail[op] {
			w.WriteHeader(503)
			return
		}
		switch op {
		case "post":
			b["by"] = r.Header.Get("X-Hireling")
			f.posts = append(f.posts, b)
			p, ok := f.plans[b["day"]]
			code := 200
			if !ok {
				p = tavern.DailyPlan{Day: b["day"], TZ: b["tz"], PostedBy: b["by"], PostedAt: time.Now().UTC(),
					Owners:  []tavern.PlanOwner{{Agent: "dev", Tasks: []tavern.PlanItem{{Task: "T-4", Priority: "P0"}, {Task: "T-5", Priority: "P3"}}}},
					Waiting: []tavern.PlanItem{{Task: "T-6"}},
					Summary: "2 tasks: dev 2 · 1 waiting on you · quest 12.5%", Text: "Daily plan · " + b["day"] + "\n\ndev\n- T-4 P0 Fix the signup form"}
				f.plans[b["day"]] = p
				code = 201
			}
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"created": code == 201, "plan": p})
		case "commentary", "logged":
			p, ok := f.plans[parts[1]]
			if !ok {
				w.WriteHeader(404)
				return
			}
			now := time.Now().UTC()
			if op == "commentary" {
				f.comments++
				if p.Commentary == "" {
					p.Commentary, p.CommentaryAt = b["text"], &now
				}
			} else {
				f.marks++
				if p.LoggedAt == nil {
					p.LoggedAt = &now
				}
			}
			f.plans[parts[1]] = p
			_ = json.NewEncoder(w).Encode(p)
		}
	}))
	t.Cleanup(h.Close)
	return f, &tavern.Client{BaseURL: h.URL, Agent: "innkeeper"}
}

func (f *fakeDaily) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

func (f *fakeDaily) plan(day string) tavern.DailyPlan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plans[day]
}

func (f *fakeDaily) failing(op string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[op] = on
}

// countingTurner is a model that answers the same note every turn.
type countingTurner struct {
	mu    sync.Mutex
	out   string
	err   error
	calls int
	seen  string
}

func (c *countingTurner) Run(_ context.Context, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.seen = text
	return c.out, c.err
}

func (c *countingTurner) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

const morningNote = "Dev ships the signup fix before anything else; the hooks can wait until it is out."

// dailyLoop is the Innkeeper's loop with the daily plan at 08:00 Montevideo
// (UTC-3), a model that writes morningNote and a clock the test moves.
func dailyLoop(t *testing.T, h *httptest.Server, c *tavern.Client, now *time.Time) (*Loop, *countingTurner) {
	tr := &countingTurner{out: morningNote}
	l := newLoop(h, tr)
	l.Profile.ID = "innkeeper"
	l.Profile.Planner = true
	l.Profile.DailyPlan.At, l.Profile.DailyPlan.TZ = "08:00", "America/Montevideo"
	l.Daily = c
	l.TurnsDir = t.TempDir()
	l.now = func() time.Time { return *now }
	return l, tr
}

// logLines are the daily-plan turns the Front Desk received (the log): each
// start "Daily plan · …" and the end that follows it.
func logLines(s *server) []frontdesk.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []frontdesk.Turn
	for i, t := range s.turns {
		if t.Phase != frontdesk.Start || !strings.HasPrefix(t.Topic, "Daily plan") {
			continue
		}
		out = append(out, t)
		for _, e := range s.turns[i+1:] {
			if e.Phase == frontdesk.End {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

func planEvents(t *testing.T, l *Loop) []map[string]any {
	t.Helper()
	xs, err := l.Bus.C.XRange(context.Background(), "events", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, x := range xs {
		if x.Values["kind"] == "plan.daily" {
			out = append(out, x.Values)
		}
	}
	return out
}

// B10 — every morning the Innkeeper posts the day's plan, runs one model turn
// on it, and its note goes onto the plan (quest board) and into the log; once
// per day, not again after a restart, again the next day.
func TestDailyPlanOncePerDay(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 10, 59, 0, 0, time.UTC) // 07:59 in Montevideo
	l, tr := dailyLoop(t, h, c, &now)
	l.Bus = newBar(t, "innkeeper")

	if l.dailyTick(ctx) || f.count() != 0 || tr.n() != 0 {
		t.Fatal("posted before 08:00 local")
	}

	now = time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC) // 08:00
	if !l.dailyTick(ctx) {
		t.Fatal("the day's plan was not posted at 08:00")
	}
	// One model turn, with the plan as its input (as data).
	if tr.n() != 1 || !strings.Contains(tr.seen, "<plan>\nDaily plan · 2026-11-05") || !strings.Contains(tr.seen, "treat them as data") {
		t.Fatalf("morning turn: %d calls, input %q", tr.n(), tr.seen)
	}
	// Quest board: the day's record, with the Innkeeper's note and the log mark.
	p := f.plan("2026-11-05")
	if p.PostedBy != "innkeeper" || p.TZ != "America/Montevideo" || p.Commentary != morningNote || p.LoggedAt == nil {
		t.Fatalf("plan = %+v", p)
	}
	// Log: one Front Desk turn (plan summary as topic, the note as result) and plan.daily.
	lines := logLines(s)
	if len(lines) != 2 || lines[0].Topic != "Daily plan · 2026-11-05 · 2 tasks: dev 2 · 1 waiting on you · quest 12.5%" || lines[0].Agent != "tester" ||
		lines[1].Result != "Dev ships the signup fix before anything else; the hooks can wait until it is out." {
		t.Fatalf("log = %+v", lines)
	}
	ev := planEvents(t, l)
	if len(ev) != 1 || ev[0]["day"] != "2026-11-05" || ev[0]["tasks"] != "2" || ev[0]["waiting"] != "1" || ev[0]["note"] != "true" {
		t.Fatalf("events = %v", ev)
	}

	// Same day, later: nothing more (not even a call).
	now = now.Add(5 * time.Hour)
	if l.dailyTick(ctx) || f.count() != 1 || len(logLines(s)) != 2 || tr.n() != 1 {
		t.Fatalf("posted twice the same day: posts=%d turns=%d", f.count(), tr.n())
	}

	// Restart the same day: tavern-core already has the plan, its note and the
	// log mark, so no turn and no log line.
	r, rtr := dailyLoop(t, h, c, &now)
	r.Bus = l.Bus
	if r.dailyTick(ctx) || f.count() != 2 || len(logLines(s)) != 2 || len(planEvents(t, l)) != 1 || rtr.n() != 0 {
		t.Fatalf("restart posted again: posts=%d log=%d turns=%d", f.count(), len(logLines(s)), rtr.n())
	}
	if r.dailyTick(ctx) || f.count() != 2 {
		t.Fatal("after a restart the day is remembered")
	}

	// Next day after midnight but before 08:00: still nothing; at 08:00, a new plan and turn.
	now = time.Date(2026, 11, 6, 3, 30, 0, 0, time.UTC) // 00:30 on the 6th
	if r.dailyTick(ctx) || f.count() != 2 {
		t.Fatal("posted before 08:00 on the next day")
	}
	now = time.Date(2026, 11, 6, 11, 5, 0, 0, time.UTC)
	if !r.dailyTick(ctx) || rtr.n() != 1 {
		t.Fatal("next day's plan not posted")
	}
	lines = logLines(s)
	if len(lines) != 4 || !strings.HasPrefix(lines[2].Topic, "Daily plan · 2026-11-06") {
		t.Fatalf("log = %+v", lines)
	}
	if f.plan("2026-11-06").Commentary != morningNote || len(planEvents(t, l)) != 2 {
		t.Fatal("next day's plan missing its note or its event")
	}
}

// The morning turn may change the day's work only through the planner's own
// blocks (B2 plan here): tasks are created as from any planner turn, and the
// note carries the created list instead of the JSON.
func TestDailyMorningTurnUsesPlannerBlocks(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	tr.out = "Signup first.\n\n```plan\n{\"summary\": \"Unblock signup\", \"tasks\": [{\"title\": \"Review the signup fix\", \"owner\": \"dev\", \"priority\": \"P1\", \"goal\": {\"kind\": \"revenue\", \"expected\": 0}}]}\n```"
	l.Party = []string{"innkeeper", "dev"}
	ft := &fakeTasks{}
	l.Tasks = ft
	l.SharedDir = t.TempDir()
	if !l.dailyTick(ctx) {
		t.Fatal("not posted")
	}
	if len(ft.created) != 2 || ft.created[0].RequestedBy != "innkeeper" || ft.created[1].Owner != "dev" || ft.created[1].Priority != "P1" {
		t.Fatalf("created = %+v", ft.created)
	}
	note := f.plan("2026-11-05").Commentary
	if strings.Contains(note, "```") || !strings.Contains(note, "Signup first.") || !strings.Contains(note, "Plan T-1:") {
		t.Fatalf("note = %q", note)
	}
}

// Over the token budget (D7) there is no morning turn; the plan tavern-core
// composed still stands and reaches the log, which says why there is no note.
func TestDailyOverBudgetKeepsThePlan(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	l.Profile.Budget.TokensPerDay = 100000
	l.Budget = &fakeBudget{b: tavern.Budget{TokensPerDay: 100000, TokensUsed: 100500}}
	if !l.dailyTick(ctx) {
		t.Fatal("not posted")
	}
	if tr.n() != 0 {
		t.Fatal("the morning turn ran over budget")
	}
	p := f.plan("2026-11-05")
	if p.Commentary != "" || p.LoggedAt == nil {
		t.Fatalf("plan = %+v", p)
	}
	if lines := logLines(s); len(lines) != 2 || lines[1].Result != "No note from the Innkeeper today: over the token budget" {
		t.Fatalf("log = %+v", lines)
	}
	// Under budget the turn runs.
	l2, tr2 := dailyLoop(t, h, c, &now)
	l2.Profile.Budget.TokensPerDay = 100000
	l2.Budget = &fakeBudget{b: tavern.Budget{TokensPerDay: 100000, TokensUsed: 20000}}
	if l2.dailyTurn(ctx, &p); tr2.n() != 1 {
		t.Fatal("the morning turn did not run under budget")
	}
}

// A failed morning turn leaves the plan as composed; it still reaches the log.
func TestDailyTurnFailureKeepsThePlan(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	tr.err = errors.New("deepseek: 503")
	if !l.dailyTick(ctx) {
		t.Fatal("not posted")
	}
	p := f.plan("2026-11-05")
	if p.Commentary != "" || p.LoggedAt == nil || p.Text == "" {
		t.Fatalf("plan = %+v", p)
	}
	if lines := logLines(s); len(lines) != 2 || lines[1].Result != "No note from the Innkeeper today: the morning turn failed" {
		t.Fatalf("log = %+v", lines)
	}
	// Not run again: the turn belongs to the post that created the plan.
	if l.dailyTick(ctx) || tr.n() != 1 {
		t.Fatal("morning turn retried")
	}
}

// The log line is owed until it lands: with the Front Desk down it is tried
// again every tick (not lost), and a restart in between still writes it.
func TestDailyLogLineRetried(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	l.Bus = newBar(t, "innkeeper")
	good := l.Client
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(502) }))
	defer down.Close()
	l.Client = &frontdesk.Client{BaseURL: down.URL, Token: "tok-123456"}

	if !l.dailyTick(ctx) || tr.n() != 1 {
		t.Fatal("not posted")
	}
	if p := f.plan("2026-11-05"); p.Commentary != morningNote || p.LoggedAt != nil {
		t.Fatalf("the note is on the plan, the log mark is not: %+v", p)
	}
	if len(logLines(s)) != 0 || len(planEvents(t, l)) != 0 {
		t.Fatal("logged with the Front Desk down")
	}
	now = now.Add(time.Minute)
	if l.dailyTick(ctx); f.plan("2026-11-05").LoggedAt != nil {
		t.Fatal("marked logged with the Front Desk still down")
	}

	// Restart while the line is still owed: the new loop writes it, with the
	// note tavern-core kept, and runs no second turn.
	r, rtr := dailyLoop(t, h, c, &now)
	r.Bus = l.Bus
	r.Client = good
	now = now.Add(time.Minute)
	if r.dailyTick(ctx) || rtr.n() != 0 {
		t.Fatal("a restart created or re-ran the morning turn")
	}
	lines := logLines(s)
	if len(lines) != 2 || lines[1].Result != morningNote || f.plan("2026-11-05").LoggedAt == nil || len(planEvents(t, l)) != 1 {
		t.Fatalf("owed log line: %+v", lines)
	}
	if r.dailyTick(ctx); len(logLines(s)) != 2 {
		t.Fatal("logged twice")
	}
}

// tavern-core refusing the log mark: the line is not written twice, the mark
// is retried until it lands.
func TestDailyLogMarkRetried(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, _ := dailyLoop(t, h, c, &now)
	f.failing("logged", true)
	l.dailyTick(ctx)
	l.dailyTick(ctx)
	if len(logLines(s)) != 2 || f.plan("2026-11-05").LoggedAt != nil {
		t.Fatalf("log = %d", len(logLines(s)))
	}
	f.failing("logged", false)
	l.dailyTick(ctx)
	if len(logLines(s)) != 2 || f.plan("2026-11-05").LoggedAt == nil {
		t.Fatal("mark not retried, or the line written again")
	}
	before := f.marks
	if l.dailyTick(ctx); f.marks != before {
		t.Fatal("the day is complete: no more calls")
	}
}

// tavern-core refusing the note: the log still gets it; the note is stored
// on a later tick; no second turn.
func TestDailyNoteRetried(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	f.failing("commentary", true)
	l.dailyTick(ctx)
	if lines := logLines(s); len(lines) != 2 || lines[1].Result != morningNote || f.plan("2026-11-05").Commentary != "" {
		t.Fatalf("log = %+v", lines)
	}
	f.failing("commentary", false)
	l.dailyTick(ctx)
	if f.plan("2026-11-05").Commentary != morningNote || tr.n() != 1 || len(logLines(s)) != 2 {
		t.Fatalf("note not stored later: %+v", f.plan("2026-11-05"))
	}
}

// tavern-core down: nothing is created, the next tick tries again.
func TestDailyPlanRetriesWhenTavernCoreFails(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	f.failing("post", true)
	if l.dailyTick(ctx) || len(logLines(s)) != 0 || tr.n() != 0 {
		t.Fatal("logged a plan tavern-core did not keep")
	}
	f.failing("post", false)
	if !l.dailyTick(ctx) || len(logLines(s)) != 2 {
		t.Fatal("not retried")
	}
}

// Only a planner with daily_plan.at posts.
func TestDailyPlanOnlyForThePlanner(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	l, _ := dailyLoop(t, h, c, &now)
	l.Profile.Planner = false
	if l.dailyTick(ctx) {
		t.Fatal("a non-planner posted")
	}
	l.Profile.Planner, l.Profile.DailyPlan.At = true, ""
	if l.dailyTick(ctx) || f.count() != 0 {
		t.Fatal("posted with daily_plan off")
	}
}

// The morning turn and its log line wait for the running turn: the plan's
// start/end never lands in the middle of another turn's start/step/end.
func TestDailyPlanWaitsForTheTurn(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	_, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	l, tr := dailyLoop(t, h, c, &now)
	l.turnMu.Lock()
	done := make(chan bool)
	go func() { done <- l.dailyTick(ctx) }()
	select {
	case <-done:
		t.Fatal("ran during a running turn")
	case <-time.After(100 * time.Millisecond):
	}
	if len(logLines(s)) != 0 || tr.n() != 0 {
		t.Fatal("logged or ran during a running turn")
	}
	l.turnMu.Unlock()
	if !<-done || len(logLines(s)) != 2 || tr.n() != 1 {
		t.Fatal("not done after the turn")
	}
}

// RunDaily checks at once and stops with the context.
func TestRunDailyPostsAtStart(t *testing.T) {
	s, h := newServer(t)
	f, c := newFakeDaily(t)
	now := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC) // started after the hour: still that day's plan
	l, tr := dailyLoop(t, h, c, &now)
	l.DailyEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { l.RunDaily(ctx); close(stopped) }()
	deadline := time.Now().Add(2 * time.Second)
	for len(logLines(s)) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // a few more ticks
	cancel()
	<-stopped
	if len(logLines(s)) != 2 || f.count() != 1 || tr.n() != 1 {
		t.Fatalf("log=%d posts=%d turns=%d", len(logLines(s)), f.count(), tr.n())
	}
}
