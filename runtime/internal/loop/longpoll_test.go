package loop

import (
	"context"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
)

// A13 — with a long-polling front desk the loop asks again at once; with an
// old one (no `waited`) it keeps sleeping poll_seconds.
func TestRunLongPollsAndFallsBack(t *testing.T) {
	for _, long := range []bool{true, false} {
		s, h := newServer(t)
		s.longPoll = long
		s.queue = []frontdesk.Command{{ID: "c1", Text: "oi"}}
		l := newLoop(h, &fakeTurner{out: "ok"})
		l.Profile.Runtime.PollSeconds = 1
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		_ = l.Run(ctx)
		cancel()
		s.mu.Lock()
		waits, acks := s.waits, len(s.acks)
		s.mu.Unlock()
		if acks < 2 { // received + done for c1
			t.Fatalf("long=%v: command not handled (acks %d)", long, acks)
		}
		if long && waits < 5 {
			t.Fatalf("long poll must ask again at once, got %d requests in 400 ms", waits)
		}
		if !long && waits != 1 {
			t.Fatalf("an old front desk must be polled every poll_seconds, got %d requests", waits)
		}
	}
}

// A13 — a command the front desk keeps returning (ack not landed) is not run
// twice and does not spin the loop.
func TestLongPollDoesNotSpinOnUnackedCommand(t *testing.T) {
	s, h := newServer(t)
	s.longPoll, s.sticky = true, true
	s.queue = []frontdesk.Command{{ID: "c1", Text: "oi"}}
	tr := &fakeTurner{out: "ok"}
	l := newLoop(h, tr)
	l.Profile.Runtime.PollSeconds = 1
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_ = l.Run(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waits > 2 {
		t.Fatalf("spinning on an unacked command: %d requests", s.waits)
	}
	dones := 0
	for _, a := range s.acks {
		if a["result"] == "done" {
			dones++
		}
	}
	if dones != 1 {
		t.Fatalf("command handled %d times", dones)
	}
}

// A13 — NoLongPoll keeps the old behaviour.
func TestNoLongPoll(t *testing.T) {
	s, h := newServer(t)
	s.longPoll = true
	l := newLoop(h, &fakeTurner{out: "ok"})
	l.NoLongPoll = true
	if waited, err := l.onceWait(context.Background()); waited || err != nil || s.waits != 0 {
		t.Fatalf("waited=%v err=%v waits=%d", waited, err, s.waits)
	}
}
