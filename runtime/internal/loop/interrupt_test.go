package loop

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
)

// waitRunning blocks until an interruptible turn is in flight.
func waitRunning(t *testing.T, l *Loop) {
	t.Helper()
	for i := 0; i < 200; i++ {
		l.intMu.Lock()
		r := l.running
		l.intMu.Unlock()
		if r != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no interruptible turn started")
}

// B5 — the owner's P0 cuts a P2 turn short: progress saved on the common table,
// the task back in the queue, the request back in the inbox, nothing finished.
func TestOwnerInterruptsP2(t *testing.T) {
	ctx := context.Background()
	l, f := leaseLoop(t, &fakeTurner{out: "Hook 1: ...\nHook 2: ... (half done)", delay: 5 * time.Second})
	l.SharedDir = t.TempDir()
	sender := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	if _, err := sender.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-1", Text: "3 hooks for Monday"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	done := make(chan struct{})
	go func() { l.HandleMessage(ctx, msgs[0]); close(done) }()
	waitRunning(t, l)
	if !l.Interrupt("the owner's command c1") {
		t.Fatal("interrupt refused")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("interrupted turn did not stop")
	}
	got := f.seen()
	if !strings.HasPrefix(got, "lease T-1 | interrupt T-1 interrupted by the owner's command c1; progress at ") || strings.Contains(got, "done T-1") {
		t.Fatalf("calls = %s", got)
	}
	files, _ := filepath.Glob(filepath.Join(l.SharedDir, "T-1", "progress-*.md"))
	if len(files) != 1 {
		t.Fatalf("progress files = %v", files)
	}
	body, _ := os.ReadFile(files[0])
	if !strings.Contains(string(body), "half done") || !strings.Contains(string(body), "Interrupted by the owner's command c1") {
		t.Fatalf("progress = %q", body)
	}
	// the request is back in the inbox, pointing at the progress; the original is acked
	inbox, _ := l.Bus.C.XRange(ctx, bar.Inbox("test"), "-", "+").Result()
	last := inbox[len(inbox)-1].Values
	if len(inbox) != 2 || last["task"] != "T-1" || last["from"] != "innkeeper" || !strings.Contains(last["text"].(string), "Resumed after an interrupt") || !strings.Contains(last["text"].(string), files[0]) {
		t.Fatalf("inbox = %v", inbox)
	}
	if p, _ := l.Bus.Pending(ctx); len(p) != 0 {
		t.Fatalf("original not acked: %v", p)
	}
	if l.Interrupt("again") {
		t.Fatal("nothing should be running now")
	}
}

// B5 — a P1 turn is not interruptible.
func TestP1IsNotInterruptible(t *testing.T) {
	l, f := leaseLoop(t, &fakeTurner{out: "ok", delay: 150 * time.Millisecond})
	done := make(chan struct{})
	go func() {
		l.HandleMessage(context.Background(), bar.Message{ID: "1-0", From: "innkeeper", To: "test", Type: "request", Task: "T-4", Text: "x"})
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	if l.Interrupt("the owner's command c2") {
		t.Fatal("a P1 turn was interrupted")
	}
	<-done
	if got := f.seen(); !strings.HasPrefix(got, "lease T-4") || !strings.HasSuffix(got, "done T-4") || strings.Contains(got, "interrupt") {
		t.Fatalf("calls = %s", got)
	}
}

// B5 — a P0 task from the owner on the bar interrupts; a P0 from a Flash hireling does not.
func TestBarP0Interrupts(t *testing.T) {
	ctx := context.Background()
	l, f := leaseLoop(t, &fakeTurner{out: "partial", delay: 2 * time.Second})
	l.SharedDir, l.Approver, l.InterruptPoll = t.TempDir(), "innkeeper", 20*time.Millisecond
	inn := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	sw := &bar.Bus{C: l.Bus.C, Agent: "scriptwriter"}
	if _, err := inn.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-1", Text: "routine"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	done := make(chan struct{})
	go func() { l.HandleMessage(ctx, msgs[0]); close(done) }()
	waitRunning(t, l)
	_, _ = sw.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-8", Text: "jump the queue"})
	time.Sleep(100 * time.Millisecond)
	l.intMu.Lock()
	cut := l.running != nil && l.running.interrupted != ""
	l.intMu.Unlock()
	if cut {
		t.Fatal("a P0 from a Flash hireling interrupted the turn")
	}
	_, _ = inn.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-9", Text: "now"})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("P0 on the bar did not interrupt")
	}
	if got := f.seen(); !strings.Contains(got, "interrupt T-1 interrupted by P0 task T-9 from owner") {
		t.Fatalf("calls = %s", got)
	}
}
