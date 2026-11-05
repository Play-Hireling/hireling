package loop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// fakeLeaser records the lease calls a turn makes (B4).
type fakeLeaser struct {
	mu    sync.Mutex
	tasks map[string]*tavern.Task
	calls []string
}

func (f *fakeLeaser) log(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakeLeaser) GetTask(_ context.Context, id string) (*tavern.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return nil, errors.New("not found")
	}
	c := *t
	return &c, nil
}
func (f *fakeLeaser) Lease(_ context.Context, id string) (*tavern.Task, error) {
	f.log("lease " + id)
	return f.tasks[id], nil
}
func (f *fakeLeaser) Renew(_ context.Context, id string) error { f.log("renew " + id); return nil }
func (f *fakeLeaser) Finish(_ context.Context, id string, done bool) error {
	if done {
		f.log("done " + id)
	} else {
		f.log("failed " + id)
	}
	return nil
}
func (f *fakeLeaser) Release(_ context.Context, id, status, progress string) error {
	f.log("release " + id + " " + status + " " + progress)
	return nil
}

func (f *fakeLeaser) Interrupt(_ context.Context, id, progress string) error {
	f.log("interrupt " + id + " " + progress)
	return nil
}

func (f *fakeLeaser) Fail(_ context.Context, id, lastError string, max int) (*tavern.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tasks[id]
	t.Failures++
	t.LastError = lastError
	t.Status = "queued"
	if t.Failures >= max {
		t.Status, t.BlockedReason = "blocked", "needs_human"
	}
	f.calls = append(f.calls, fmt.Sprintf("fail %s %d", id, t.Failures))
	c := *t
	return &c, nil
}

func (f *fakeLeaser) Unblock(_ context.Context, id, action string) (*tavern.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tasks[id]
	t.Failures, t.BlockedReason, t.Status = 0, "", "queued"
	if action == "drop" {
		t.Status = "cancelled"
	}
	f.calls = append(f.calls, "unblock "+id+" "+action)
	c := *t
	return &c, nil
}

func (f *fakeLeaser) seen() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, " | ")
}

func leaseLoop(t *testing.T, tr *fakeTurner) (*Loop, *fakeLeaser) {
	t.Helper()
	_, h := newServer(t)
	l := newLoop(h, tr)
	l.Bus = newBar(t, "test")
	l.TurnsDir = t.TempDir()
	if err := l.Bus.EnsureGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := &fakeLeaser{tasks: map[string]*tavern.Task{
		"T-1": {ID: "T-1", Owner: "test", Status: "queued", Priority: "P2"},
		"T-2": {ID: "T-2", Owner: "scriptwriter", Status: "queued"},
		"T-3": {ID: "T-3", Owner: "test", Status: "done"},
		"T-4": {ID: "T-4", Owner: "test", Status: "queued", Priority: "P1"},
		"T-9": {ID: "T-9", Owner: "test", Status: "queued", Priority: "P0", RequestedBy: "owner"},
		"T-8": {ID: "T-8", Owner: "test", Status: "queued", Priority: "P0", RequestedBy: "scriptwriter"},
	}}
	l.Leases = f
	return l, f
}

// B4 — leased before the turn, renewed on every step, done after.
func TestLeaseRenewedOnEveryStepThenDone(t *testing.T) {
	tr := &fakeTurner{out: "Hooks are on the table.", delay: 100 * time.Millisecond}
	l, f := leaseLoop(t, tr)
	l.Keepalive = 30 * time.Millisecond
	l.HandleMessage(context.Background(), bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-1", Text: "3 hooks"})
	got := f.seen()
	if !strings.HasPrefix(got, "lease T-1 | renew T-1") || !strings.HasSuffix(got, "done T-1") {
		t.Fatalf("calls = %s", got)
	}
	if l.leasing != "" {
		t.Fatal("lease marker left behind")
	}
}

// B4 — a failed turn goes back to the queue (B11); an open decision parks it.
func TestLeaseFailedAndWaiting(t *testing.T) {
	l, f := leaseLoop(t, &fakeTurner{err: errors.New("boom")})
	l.HandleMessage(context.Background(), bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-1", Text: "x"})
	if got := f.seen(); got != "lease T-1 | fail T-1 1" { // B11: a failed turn re-queues (escalate.go)
		t.Fatalf("calls = %s", got)
	}
	ask := "Draft ready.\n\n```ask\n{\"question\": \"Publish it today?\", \"options\": [\"Yes\", \"Monday\"]}\n```"
	l2, f2 := leaseLoop(t, &fakeTurner{out: ask})
	l2.HandleMessage(context.Background(), bar.Message{ID: "2-0", From: "innkeeper", To: "test", Type: "request", Task: "T-1", Text: "x"})
	if got := f2.seen(); got != "lease T-1 | release T-1 waiting_owner Waiting on you: Publish it today?" {
		t.Fatalf("calls = %s", got)
	}
}

// B4 — no lease on someone else's task, a finished task, or a non-request.
func TestNoLeaseWhenNotMine(t *testing.T) {
	l, f := leaseLoop(t, &fakeTurner{out: "ok"})
	ctx := context.Background()
	l.HandleMessage(ctx, bar.Message{ID: "1-0", From: "scriptwriter", To: "test", Type: "request", Task: "T-2", Text: "x"})
	l.HandleMessage(ctx, bar.Message{ID: "2-0", From: "innkeeper", To: "test", Type: "request", Task: "T-3", Text: "x"})
	l.HandleMessage(ctx, bar.Message{ID: "3-0", From: "innkeeper", To: "test", Type: "note", Task: "T-1", Text: "fyi"})
	if got := f.seen(); got != "" {
		t.Fatalf("calls = %s", got)
	}
}
