package tasks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newSvc() (*Service, *Memory, *time.Time) {
	m := NewMemory()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s := &Service{Store: m, Now: func() time.Time { return now }}
	return s, m, &now
}

func TestLeaseRenewFinish(t *testing.T) {
	ctx := context.Background()
	s, m, now := newSvc()
	task, err := s.Create(ctx, CreateInput{Title: "3 hooks", RequestedBy: "social-media"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "T-1" || task.Status != Queued || task.Priority != "P3" {
		t.Fatalf("%+v", task)
	}
	if _, err := s.Lease(ctx, task.ID, "scriptwriter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lease(ctx, task.ID, "dev"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("second lease must fail: %v", err)
	}
	*now = now.Add(10 * time.Minute)
	got, err := s.Renew(ctx, task.ID, "scriptwriter")
	if err != nil || got.LeaseExpiresAt.Sub(*now) != LeaseWindow {
		t.Fatalf("renew: %v %+v", err, got)
	}
	if _, err := s.Finish(ctx, task.ID, "dev", Done); !errors.Is(err, ErrNotOwner) {
		t.Fatal("only the owner finishes")
	}
	if _, err := s.Finish(ctx, task.ID, "scriptwriter", Done); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lease(ctx, task.ID, "scriptwriter"); !errors.Is(err, ErrFinished) {
		t.Fatal("finished tasks cannot be leased")
	}
	types := []string{}
	for _, e := range m.Events {
		types = append(types, e.Type)
	}
	want := "created leased renewed done"
	if got := join(types); got != want {
		t.Fatalf("events = %s", got)
	}
}

func TestLeaseExpiresBackToQueue(t *testing.T) {
	ctx := context.Background()
	s, _, now := newSvc()
	task, _ := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper"})
	if _, err := s.Lease(ctx, task.ID, "dev"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(LeaseWindow + time.Second)
	if _, err := s.Renew(ctx, task.ID, "dev"); !errors.Is(err, ErrNotLeased) {
		t.Fatalf("expired lease must not renew: %v", err)
	}
	got, err := s.Lease(ctx, task.ID, "scriptwriter")
	if err != nil || got.Owner != "scriptwriter" {
		t.Fatalf("anyone may lease after expiry: %v %+v", err, got)
	}
}

func TestHandoff(t *testing.T) {
	ctx := context.Background()
	s, m, _ := newSvc()
	task, _ := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper"})
	s.Lease(ctx, task.ID, "social-media")
	got, err := s.Handoff(ctx, task.ID, "social-media", "scriptwriter")
	if err != nil || got.Owner != "scriptwriter" || got.Status != Queued {
		t.Fatalf("%v %+v", err, got)
	}
	last := m.Events[len(m.Events)-1]
	if last.Type != "handoff" || last.To != "scriptwriter" {
		t.Fatalf("%+v", last)
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newSvc()
	if _, err := s.Create(ctx, CreateInput{Title: " "}); err == nil {
		t.Fatal("empty title")
	}
	if _, err := s.Create(ctx, CreateInput{Title: "x", Priority: "P9"}); err == nil {
		t.Fatal("bad priority")
	}
	if _, err := s.Lease(ctx, "T-404", "dev"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func join(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}

// B6: a hireling asking work of another waits for the Innkeeper; approving
// puts it in the owner's queue, denying cancels it; both are on the timeline.
func TestRequestedHandoffNeedsApproval(t *testing.T) {
	ctx := context.Background()
	m := NewMemory()
	s := &Service{Store: m, Approver: func(a string) bool { return a == "innkeeper" || a == "owner" }}

	// the Innkeeper and the owner assign directly
	direct, _ := s.Create(ctx, CreateInput{Title: "hooks", RequestedBy: "innkeeper", Owner: "scriptwriter"})
	if direct.Status != Queued || direct.NeedsApproval {
		t.Fatalf("innkeeper's task = %+v", direct)
	}
	// another hireling's request is blocked until decided
	req, _ := s.Create(ctx, CreateInput{Title: "carousel", RequestedBy: "scriptwriter", Owner: "social-media", Brief: "need visuals"})
	if req.Status != Blocked || !req.NeedsApproval || req.AutonomyLevel != "p2" {
		t.Fatalf("requested task = %+v", req)
	}
	// a task for oneself needs nothing
	own, _ := s.Create(ctx, CreateInput{Title: "notes", RequestedBy: "scriptwriter", Owner: "scriptwriter"})
	if own.Status != Queued {
		t.Fatalf("own task = %+v", own)
	}
	// only an approver decides
	if _, err := s.Decide(ctx, req.ID, "dev", true, ""); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("dev decided: %v", err)
	}
	got, err := s.Decide(ctx, req.ID, "innkeeper", true, "go")
	if err != nil || got.Status != Queued || got.NeedsApproval || got.DecidedBy != "innkeeper" || got.Owner != "social-media" {
		t.Fatalf("approved = %+v %v", got, err)
	}
	if _, err := s.Decide(ctx, req.ID, "innkeeper", true, ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second decision: %v", err)
	}
	den, _ := s.Create(ctx, CreateInput{Title: "ads", RequestedBy: "dev", Owner: "paid-traffic"})
	got, err = s.Decide(ctx, den.ID, "owner", false, "not now")
	if err != nil || got.Status != Cancelled || got.DecisionNote != "not now" {
		t.Fatalf("denied = %+v %v", got, err)
	}
	// timeline
	var kinds []string
	for _, e := range m.Events {
		kinds = append(kinds, e.Type)
	}
	want := "created,requested,created,approved,requested,denied"
	if strings.Join(kinds, ",") != want {
		t.Fatalf("events = %v", kinds)
	}
	// The owner can lease once approved
	if _, err := s.Lease(ctx, req.ID, "social-media"); err != nil {
		t.Fatal(err)
	}
}

// C3 — with the quest's kinds known, every task declares how it moves the goal.
func TestContributionRequired(t *testing.T) {
	ctx := context.Background()
	s := &Service{Store: NewMemory(), Kinds: func(context.Context) []string { return []string{"leads", "posts", "support"} }}
	if _, err := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper"}); !errors.Is(err, ErrNoContribution) {
		t.Fatalf("task without contribution created: %v", err)
	}
	if _, err := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper", GoalContribution: Contribution{Kind: "coffee"}}); err == nil {
		t.Fatal("foreign kind accepted")
	}
	if _, err := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper", GoalContribution: Contribution{Kind: "leads", Expected: -1}}); err == nil {
		t.Fatal("negative expected accepted")
	}
	a, err := s.Create(ctx, CreateInput{Title: "hooks", RequestedBy: "innkeeper", GoalContribution: Contribution{Kind: " Leads ", Expected: 10, Note: "warm-up"}})
	if err != nil || a.GoalContribution.Kind != "leads" {
		t.Fatal(err, a)
	}
	b, _ := s.Create(ctx, CreateInput{Title: "post", RequestedBy: "innkeeper", Owner: "social-media", GoalContribution: Contribution{Kind: "posts", Expected: 1}})
	c, _ := s.Create(ctx, CreateInput{Title: "more", RequestedBy: "innkeeper", GoalContribution: Contribution{Kind: "leads", Expected: 5}})
	if _, err := s.Lease(ctx, b.ID, "social-media"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(ctx, b.ID, "social-media", Done); err != nil {
		t.Fatal(err)
	}
	all, _ := s.Store.List(ctx)
	open, done := Expected(all)
	if open["leads"] != 15 || done["posts"] != 1 || open["posts"] != 0 {
		t.Fatalf("open=%v done=%v (c=%s)", open, done, c.ID)
	}
}

// B3 — P0 > P1 > P2 > P3; ties by the kind closest to the goal, then expected units, then oldest.
func TestQueueOrder(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	s := &Service{Store: NewMemory(), Now: func() time.Time { clock = clock.Add(time.Second); return clock },
		Kinds: func(context.Context) []string { return []string{"revenue", "leads", "posts", "support"} }}
	mk := func(title, prio, kind string, exp int) {
		if _, err := s.Create(ctx, CreateInput{Title: title, RequestedBy: "innkeeper", Owner: "scriptwriter", Priority: prio, GoalContribution: Contribution{Kind: kind, Expected: exp}}); err != nil {
			t.Fatal(err)
		}
	}
	mk("p3 routine", "P3", "revenue", 100)
	mk("p2 posts", "P2", "posts", 1)
	mk("p2 leads small", "P2", "leads", 5)
	mk("p0 order", "P0", "support", 0)
	mk("p2 leads big", "P2", "leads", 20)
	mk("p1 unblocked", "P1", "posts", 1)
	mk("p2 leads big later", "P2", "leads", 20)
	if _, err := s.Create(ctx, CreateInput{Title: "someone else's", RequestedBy: "innkeeper", Owner: "dev", Priority: "P0", GoalContribution: Contribution{Kind: "support"}}); err != nil {
		t.Fatal(err)
	}
	q, err := s.Queue(ctx, "scriptwriter")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range q {
		got = append(got, x.Title)
	}
	want := []string{"p0 order", "p1 unblocked", "p2 leads big", "p2 leads big later", "p2 leads small", "p2 posts", "p3 routine"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("queue =\n%v\nwant\n%v", got, want)
	}
	// a leased task leaves the queue; an expired lease puts it back in line
	if _, err := s.Lease(ctx, q[0].ID, "scriptwriter"); err != nil {
		t.Fatal(err)
	}
	if q2, _ := s.Queue(ctx, "scriptwriter"); len(q2) != 6 || q2[0].Title != "p1 unblocked" {
		t.Fatalf("after lease: %v", q2)
	}
	clock = clock.Add(LeaseWindow + time.Minute)
	if q3, _ := s.Queue(ctx, "scriptwriter"); len(q3) != 7 || q3[0].Title != "p0 order" {
		t.Fatalf("after expiry: %v", q3)
	}
}

// B4 — a lease renewed on each step stays; without renewal the sweeper returns
// it to the queue and logs `expired`; release parks or re-queues it.
func TestLeaseRenewSweepRelease(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	mem := NewMemory()
	s := &Service{Store: mem, Now: func() time.Time { return clock }}
	a, _ := s.Create(ctx, CreateInput{Title: "hooks", RequestedBy: "innkeeper", Owner: "scriptwriter"})
	b, _ := s.Create(ctx, CreateInput{Title: "carousel", RequestedBy: "innkeeper", Owner: "social-media"})
	if _, err := s.Lease(ctx, a.ID, "scriptwriter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lease(ctx, b.ID, "social-media"); err != nil {
		t.Fatal(err)
	}
	// steps every 5 minutes keep a alive past the 15-minute window; b is never renewed
	for i := 0; i < 4; i++ {
		clock = clock.Add(5 * time.Minute)
		if _, err := s.Renew(ctx, a.ID, "scriptwriter"); err != nil {
			t.Fatalf("renew %d: %v", i, err)
		}
	}
	freed, err := s.Sweep(ctx)
	if err != nil || len(freed) != 1 || freed[0].ID != b.ID || freed[0].Owner != "social-media" {
		t.Fatalf("sweep = %v %v", freed, err)
	}
	if got, _ := s.Store.Get(ctx, b.ID); got.Status != Queued {
		t.Fatalf("expired task status %s", got.Status)
	}
	if got, _ := s.Store.Get(ctx, a.ID); got.Status != Leased {
		t.Fatalf("renewed task status %s", got.Status)
	}
	expired := 0
	for _, e := range mem.Events {
		if e.Type == "expired" && e.Task == b.ID {
			expired++
		}
	}
	if expired != 1 {
		t.Fatalf("expired events = %d", expired)
	}
	// release: someone else cannot; the holder parks it for the owner
	if _, err := s.Release(ctx, a.ID, "dev", WaitingOwner, ""); !errors.Is(err, ErrNotOwner) {
		t.Fatal(err)
	}
	r, err := s.Release(ctx, a.ID, "scriptwriter", WaitingOwner, "decision d-3")
	if err != nil || r.Status != WaitingOwner || r.LeaseExpiresAt != nil || r.Progress != "decision d-3" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := s.Release(ctx, a.ID, "scriptwriter", Done, ""); err == nil {
		t.Fatal("release to done must fail")
	}
}

// B5 — an interrupted task is back in the queue with its progress and an
// `interrupted` event; the next lease sees the progress.
func TestInterruptRequeuesWithProgress(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	s := &Service{Store: mem}
	a, _ := s.Create(ctx, CreateInput{Title: "hooks", RequestedBy: "innkeeper", Owner: "scriptwriter", Priority: "P2"})
	if _, err := s.Lease(ctx, a.ID, "scriptwriter"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Interrupt(ctx, a.ID, "dev", "x"); !errors.Is(err, ErrNotOwner) {
		t.Fatal(err)
	}
	r, err := s.Interrupt(ctx, a.ID, "scriptwriter", "interrupted by the owner's command c1; progress at /tavern/shared/T-1/progress-1.md")
	if err != nil || r.Status != Queued || !strings.Contains(r.Progress, "progress-1.md") {
		t.Fatalf("%+v %v", r, err)
	}
	if q, _ := s.Queue(ctx, "scriptwriter"); len(q) != 1 || q[0].ID != a.ID {
		t.Fatalf("queue = %v", q)
	}
	if last := mem.Events[len(mem.Events)-1]; last.Type != "interrupted" || last.Task != a.ID {
		t.Fatalf("last event = %+v", last)
	}
	if again, _ := s.Lease(ctx, a.ID, "scriptwriter"); !strings.Contains(again.Progress, "progress-1.md") {
		t.Fatalf("progress lost on re-lease: %+v", again)
	}
}

// B11 — failed turns re-queue until the threshold, then block needs_human;
// The owner's answer re-queues (count reset) or drops; success resets the count.
func TestRepeatedFailureEscalates(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory()
	s := &Service{Store: mem}
	a, _ := s.Create(ctx, CreateInput{Title: "carousel", RequestedBy: "innkeeper", Owner: "social-media", Priority: "P1"})
	for i := 1; i <= 3; i++ {
		if _, err := s.Lease(ctx, a.ID, "social-media"); err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		if i == 1 {
			if _, err := s.Fail(ctx, a.ID, "dev", "x", 3); !errors.Is(err, ErrNotOwner) {
				t.Fatalf("not holder: %v", err)
			}
		}
		r, err := s.Fail(ctx, a.ID, "social-media", "canva: 502 bad gateway", 3)
		if err != nil {
			t.Fatal(err)
		}
		want, kind := Queued, "failed_turn"
		if i == 3 {
			want, kind = Blocked, "escalated"
		}
		if r.Status != want || r.Failures != i || r.LastError != "canva: 502 bad gateway" {
			t.Fatalf("after %d failures: %+v", i, r)
		}
		if last := mem.Events[len(mem.Events)-1]; last.Type != kind {
			t.Fatalf("event %d = %+v", i, last)
		}
	}
	if q, _ := s.Queue(ctx, "social-media"); len(q) != 0 {
		t.Fatalf("blocked task still queued: %v", q)
	}
	b, _ := s.Get(ctx, a.ID)
	if b.BlockedReason != NeedsHuman {
		t.Fatalf("reason = %q", b.BlockedReason)
	}
	if _, err := s.Unblock(ctx, a.ID, "dev", "Retry"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("unblock by another: %v", err)
	}
	r, err := s.Unblock(ctx, a.ID, "social-media", "Retry")
	if err != nil || r.Status != Queued || r.Failures != 0 || r.BlockedReason != "" {
		t.Fatalf("retry: %+v %v", r, err)
	}
	if _, err := s.Unblock(ctx, a.ID, "social-media", "Retry"); err == nil {
		t.Fatal("unblocked a task that was not escalated")
	}
	// One more failure, then a success: the count goes back to zero.
	_, _ = s.Lease(ctx, a.ID, "social-media")
	if r, _ := s.Fail(ctx, a.ID, "social-media", "timeout", 3); r.Failures != 1 {
		t.Fatalf("failures = %d", r.Failures)
	}
	_, _ = s.Lease(ctx, a.ID, "social-media")
	if r, _ := s.Finish(ctx, a.ID, "social-media", Done); r.Failures != 0 || r.LastError != "" {
		t.Fatalf("done kept the count: %+v", r)
	}
}

// B11 — threshold 1 escalates at once; Drop cancels; default threshold is 3.
func TestEscalateThresholdAndDrop(t *testing.T) {
	ctx := context.Background()
	s := &Service{Store: NewMemory()}
	a, _ := s.Create(ctx, CreateInput{Title: "x", RequestedBy: "innkeeper", Owner: "dev", Priority: "P2"})
	_, _ = s.Lease(ctx, a.ID, "dev")
	if r, _ := s.Fail(ctx, a.ID, "dev", "boom", 1); r.Status != Blocked {
		t.Fatalf("threshold 1: %+v", r)
	}
	if r, err := s.Unblock(ctx, a.ID, "dev", "drop"); err != nil || r.Status != Cancelled {
		t.Fatalf("drop: %+v %v", r, err)
	}
	b, _ := s.Create(ctx, CreateInput{Title: "y", RequestedBy: "innkeeper", Owner: "dev", Priority: "P2"})
	for i := 0; i < 2; i++ {
		_, _ = s.Lease(ctx, b.ID, "dev")
		if r, _ := s.Fail(ctx, b.ID, "dev", "boom", 0); r.Status != Queued {
			t.Fatalf("default threshold escalated early: %+v", r)
		}
	}
	_, _ = s.Lease(ctx, b.ID, "dev")
	if r, _ := s.Fail(ctx, b.ID, "dev", "boom", 0); r.Status != Blocked {
		t.Fatalf("default threshold: %+v", r)
	}
}
