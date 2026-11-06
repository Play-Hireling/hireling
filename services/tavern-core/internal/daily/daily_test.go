package daily

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
)

// fixture is a tavern with a quest and a queue: a plan parent (grouping), its
// children for two hirelings, one in progress, one waiting on the owner, one
// escalated, one handoff waiting for approval and one finished task.
func fixture(t *testing.T, store Store) (*Service, *time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	approver := func(a string) bool { return a == "owner" || a == "innkeeper" }
	quests := &goal.Service{Store: goal.NewMemory(), Now: clock}
	if _, err := quests.Set(ctx, "owner", goal.Define{Title: "Net revenue", Target: 120000, Currency: "BRL", Source: "finance"}); err != nil {
		t.Fatal(err)
	}
	if _, err := quests.Report(ctx, "finance", 15000); err != nil {
		t.Fatal(err)
	}
	ts := &tasks.Service{Store: tasks.NewMemory(), Now: clock, Approver: approver,
		Kinds: func(context.Context) []string { return goal.DefaultKinds }}
	mk := func(title, by, owner, prio, kind string, exp int, parent string) *tasks.Task {
		task, err := ts.Create(ctx, tasks.CreateInput{Title: title, RequestedBy: by, Owner: owner, Priority: prio,
			GoalContribution: tasks.Contribution{Kind: kind, Expected: exp}, Parent: parent})
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		return task
	}
	parent := mk("Monday remittances post", "owner", "innkeeper", "P1", "support", 0, "")   // T-1: groups
	mk("3 hooks for the post", "innkeeper", "scriptwriter", "P2", "posts", 1, parent.ID)    // T-2
	mk("Landing copy", "innkeeper", "scriptwriter", "P1", "leads", 10, parent.ID)           // T-3
	fix := mk("Fix the signup form", "owner", "dev", "P0", "revenue", 0, "")                // T-4
	mk("Refactor the footer", "innkeeper", "dev", "P3", "support", 0, "")                   // T-5
	wait := mk("Boost the launch post", "innkeeper", "paid-traffic", "P2", "leads", 50, "") // T-6
	stuck := mk("Schedule the carousel", "innkeeper", "social-media", "P2", "posts", 1, "") // T-7
	mk("Short video of the launch", "scriptwriter", "social-media", "P2", "videos", 1, "")  // T-8: handoff
	done := mk("Old task", "innkeeper", "dev", "P2", "support", 0, "")                      // T-9
	if _, err := ts.Lease(ctx, fix.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Lease(ctx, wait.ID, "paid-traffic"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Release(ctx, wait.ID, "paid-traffic", tasks.WaitingOwner, "Waiting on you: spend"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Lease(ctx, stuck.ID, "social-media"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Fail(ctx, stuck.ID, "social-media", "boom", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Lease(ctx, done.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Finish(ctx, done.ID, "dev", tasks.Done); err != nil {
		t.Fatal(err)
	}
	return &Service{Store: store, Tasks: ts, Goal: quests, Approver: approver, Now: clock}, &now
}

func TestComposeFromQueueAndQuest(t *testing.T) {
	s, _ := fixture(t, NewMemory())
	p, created, err := s.Post(context.Background(), "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || !created {
		t.Fatalf("post: %v created=%v", err, created)
	}
	if p.Day != "2026-11-05" || p.TZ != "America/Montevideo" || p.PostedBy != "innkeeper" {
		t.Fatalf("%+v", p)
	}
	if !strings.HasPrefix(p.Quest, "Quest: Net revenue — 12.5%") {
		t.Fatalf("quest = %q", p.Quest)
	}
	// B3 order across the party: dev's P0 first, then scriptwriter's P1.
	if len(p.Owners) != 2 || p.Owners[0].Agent != "dev" || p.Owners[1].Agent != "scriptwriter" {
		t.Fatalf("owners = %+v", p.Owners)
	}
	dev := p.Owners[0].Tasks
	if len(dev) != 2 || dev[0].Task != "T-4" || dev[0].Status != "leased" || dev[1].Task != "T-5" {
		t.Fatalf("dev = %+v (done T-9 must be out)", dev)
	}
	sw := p.Owners[1].Tasks
	if len(sw) != 2 || sw[0].Task != "T-3" || sw[0].Goal != "10 leads" || sw[1].Task != "T-2" {
		t.Fatalf("scriptwriter = %+v", sw)
	}
	for _, o := range p.Owners {
		if o.Agent == "innkeeper" {
			t.Fatal("the grouping parent T-1 is not work of the day")
		}
	}
	if len(p.Waiting) != 2 || p.Waiting[0].Task != "T-6" || p.Waiting[1].Task != "T-7" {
		t.Fatalf("waiting = %+v", p.Waiting)
	}
	if len(p.Approvals) != 1 || p.Approvals[0].Task != "T-8" {
		t.Fatalf("approvals = %+v", p.Approvals)
	}
	want := "4 tasks: dev 2, scriptwriter 2 · 2 waiting on you · 1 handoff to approve · quest 12.5%"
	if p.Summary != want {
		t.Fatalf("summary = %q\nwant      %q", p.Summary, want)
	}
	for _, s := range []string{"Daily plan · 2026-11-05 (America/Montevideo)", "\ndev\n- T-4 P0 Fix the signup form (in progress) · 0 revenue",
		"\nWaiting on you\n- T-6 P2 Boost the launch post · paid-traffic", "\nHandoffs to approve\n- T-8 P2 Short video of the launch · social-media"} {
		if !strings.Contains(p.Text, s) {
			t.Fatalf("text misses %q:\n%s", s, p.Text)
		}
	}
}

func TestOncePerDay(t *testing.T) {
	ctx := context.Background()
	s, now := fixture(t, NewMemory())
	first, created, err := s.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || !created {
		t.Fatal(err, created)
	}
	// Same day again (a restarted runtime): the record comes back untouched.
	*now = now.Add(3 * time.Hour)
	if _, err := s.Tasks.Create(ctx, tasks.CreateInput{Title: "late task", RequestedBy: "owner", Owner: "dev", Priority: "P0",
		GoalContribution: tasks.Contribution{Kind: "revenue"}}); err != nil {
		t.Fatal(err)
	}
	again, created, err := s.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || created {
		t.Fatalf("second post of the day: %v created=%v", err, created)
	}
	if !again.PostedAt.Equal(first.PostedAt) || again.Text != first.Text {
		t.Fatal("the day's plan must not be rewritten")
	}
	// Next day: a new plan, which sees the new task.
	*now = now.Add(24 * time.Hour)
	next, created, err := s.Post(ctx, "innkeeper", "2026-11-06", "America/Montevideo")
	if err != nil || !created || next.Day != "2026-11-06" || !strings.Contains(next.Text, "late task") {
		t.Fatalf("next day: %v created=%v %+v", err, created, next)
	}
	if l, err := s.Get(ctx, ""); err != nil || l.Day != "2026-11-06" {
		t.Fatalf("latest = %v %v", l, err)
	}
	if d, err := s.Get(ctx, "2026-11-05"); err != nil || d.Text != first.Text {
		t.Fatalf("by day = %v %v", d, err)
	}
	if _, err := s.Get(ctx, "2026-11-01"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPostRules(t *testing.T) {
	ctx := context.Background()
	s, _ := fixture(t, NewMemory())
	if _, _, err := s.Post(ctx, "dev", "2026-11-05", "UTC"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a non-planner posts: %v", err)
	}
	for _, day := range []string{"", "05/11/2026", "2026-11-08", "2026-10-30"} {
		if _, _, err := s.Post(ctx, "innkeeper", day, "UTC"); !errors.Is(err, ErrBadDay) {
			t.Fatalf("day %q: %v", day, err)
		}
	}
	if _, _, err := s.Post(ctx, "innkeeper", "2026-11-05", "not a zone; rm"); err == nil {
		t.Fatal("bad tz accepted")
	}
	// Either side of UTC's date is fine (Montevideo is behind, Tokyo ahead).
	for _, day := range []string{"2026-11-04", "2026-11-06"} {
		if _, created, err := s.Post(ctx, "owner", day, ""); err != nil || !created {
			t.Fatalf("day %s: %v %v", day, err, created)
		}
	}
}

func TestEmptyQueue(t *testing.T) {
	ts := &tasks.Service{Store: tasks.NewMemory()}
	s := &Service{Store: NewMemory(), Tasks: ts, Now: func() time.Time { return time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC) }}
	p, created, err := s.Post(context.Background(), "innkeeper", "2026-11-05", "UTC")
	if err != nil || !created {
		t.Fatal(err)
	}
	if p.Summary != "no open tasks" || !strings.Contains(p.Text, "No open tasks in the queue.") || p.Owners == nil {
		t.Fatalf("%+v", p)
	}
}

func TestPerOwnerCap(t *testing.T) {
	ctx := context.Background()
	ts := &tasks.Service{Store: tasks.NewMemory()}
	for i := 0; i < PerOwner+3; i++ {
		if _, err := ts.Create(ctx, tasks.CreateInput{Title: "t", RequestedBy: "innkeeper", Owner: "dev", Priority: "P2"}); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := ts.List(ctx)
	p := Compose("2026-11-05", "UTC", "innkeeper", time.Now(), all, nil)
	if len(p.Owners) != 1 || len(p.Owners[0].Tasks) != PerOwner || p.Owners[0].More != 3 || p.Tasks() != PerOwner+3 {
		t.Fatalf("%+v", p.Owners)
	}
	if !strings.Contains(p.Text, "- … and 3 more") || p.Summary != "8 tasks: dev 8" {
		t.Fatalf("%q\n%s", p.Summary, p.Text)
	}
}

func TestRedisOncePerDay(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL unset")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	// Its own logical database: other packages' Redis tests flush theirs
	// while this one runs (go test runs packages in parallel).
	opt.DB = 10
	c := redis.NewClient(opt)
	defer c.Close()
	ctx := context.Background()
	_ = c.FlushDB(ctx).Err()
	store := &Redis{C: c, Prefix: "test"}
	s, now := fixture(t, store)
	first, created, err := s.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || !created {
		t.Fatal(err, created)
	}
	// A second service on the same Redis (tavern-core restarted) sees the day taken.
	s2 := &Service{Store: &Redis{C: c, Prefix: "test"}, Tasks: s.Tasks, Goal: s.Goal, Now: s.Now}
	again, created, err := s2.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || created || again.Text != first.Text {
		t.Fatalf("restart: %v created=%v", err, created)
	}
	// Create itself is atomic: a racing second create of the day loses.
	if _, created, err := store.Create(ctx, &Plan{Day: "2026-11-05", Text: "other"}); err != nil || created {
		t.Fatalf("SET NX: %v %v", err, created)
	}
	// An older day posted late never moves "latest" back.
	if _, created, err := s.Post(ctx, "innkeeper", "2026-11-04", "America/Montevideo"); err != nil || !created {
		t.Fatal(err, created)
	}
	if l, err := s.Get(ctx, ""); err != nil || l.Day != "2026-11-05" {
		t.Fatalf("latest = %+v %v", l, err)
	}
	*now = now.Add(24 * time.Hour)
	if _, created, err := s.Post(ctx, "innkeeper", "2026-11-06", "America/Montevideo"); err != nil || !created {
		t.Fatal(err, created)
	}
	if l, err := s.Get(ctx, ""); err != nil || l.Day != "2026-11-06" {
		t.Fatalf("latest = %+v %v", l, err)
	}
	// The note and the log mark go through WATCH and keep the day's TTL.
	if p, err := s.Comment(ctx, "innkeeper", "2026-11-05", "Ship the signup fix first."); err != nil || p.Commentary == "" {
		t.Fatal(p, err)
	}
	if p, err := s.Logged(ctx, "innkeeper", "2026-11-05"); err != nil || p.LoggedAt == nil {
		t.Fatal(p, err)
	}
	if p, err := s.Get(ctx, "2026-11-05"); err != nil || p.Commentary != "Ship the signup fix first." || p.LoggedAt == nil || !strings.Contains(p.Text, "Innkeeper's note") {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := store.Update(ctx, "2026-10-01", func(*Plan) {}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if ttl := c.TTL(ctx, "test:plan:daily:2026-11-05").Val(); ttl <= 0 || ttl > Retention {
		t.Fatalf("TTL = %v", ttl)
	}
}

// The Innkeeper's note lands on the plan once; the log mark too.
func TestCommentAndLogged(t *testing.T) {
	ctx := context.Background()
	s, now := fixture(t, NewMemory())
	if _, err := s.Comment(ctx, "innkeeper", "2026-11-05", "note"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("note on a day without a plan: %v", err)
	}
	p, _, err := s.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo")
	if err != nil || p.Commentary != "" || p.LoggedAt != nil {
		t.Fatal(p, err)
	}
	if _, err := s.Comment(ctx, "dev", "2026-11-05", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := s.Comment(ctx, "innkeeper", "2026-11-05", "  "); err == nil {
		t.Fatal("empty note accepted")
	}
	if _, err := s.Comment(ctx, "innkeeper", "2026-11-05", strings.Repeat("x", MaxCommentary+1)); !errors.Is(err, ErrTooLong) {
		t.Fatal(err)
	}
	c, err := s.Comment(ctx, "innkeeper", "2026-11-05", "Dev ships the signup fix before anything else.")
	if err != nil || c.Commentary != "Dev ships the signup fix before anything else." || c.CommentaryAt == nil ||
		!strings.HasSuffix(c.Text, "\n\nInnkeeper's note\nDev ships the signup fix before anything else.") {
		t.Fatalf("%+v %v", c, err)
	}
	if !strings.HasPrefix(c.Text, p.Text) {
		t.Fatal("the note is added under the plan, the plan itself unchanged")
	}
	// A retry keeps the first note.
	if again, err := s.Comment(ctx, "innkeeper", "2026-11-05", "other"); err != nil || again.Commentary != c.Commentary {
		t.Fatal(again, err)
	}
	*now = now.Add(time.Minute)
	l1, err := s.Logged(ctx, "innkeeper", "2026-11-05")
	if err != nil || l1.LoggedAt == nil {
		t.Fatal(l1, err)
	}
	*now = now.Add(time.Minute)
	if l2, _ := s.Logged(ctx, "innkeeper", "2026-11-05"); !l2.LoggedAt.Equal(*l1.LoggedAt) {
		t.Fatal("logged_at moved")
	}
	if _, err := s.Logged(ctx, "dev", "2026-11-05"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	// A later post of the day returns the note and the mark.
	if got, created, _ := s.Post(ctx, "innkeeper", "2026-11-05", "America/Montevideo"); created || got.Commentary == "" || got.LoggedAt == nil {
		t.Fatalf("%+v", got)
	}
}
