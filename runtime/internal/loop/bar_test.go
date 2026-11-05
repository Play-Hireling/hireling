package loop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
)

func newBar(t *testing.T, agent string) *bar.Bus {
	t.Helper()
	m := miniredis.RunT(t)
	return &bar.Bus{C: redis.NewClient(&redis.Options{Addr: m.Addr()}), Agent: agent}
}

func TestBarMessageIsAnsweredAndAcked(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	tr := &fakeTurner{out: "Hook: 'Your agents never sleep.' tok-123456"}
	l := newLoop(h, tr)
	l.Bus = newBar(t, "test")
	l.TurnsDir = t.TempDir()
	if err := l.Bus.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	sender := &bar.Bus{C: l.Bus.C, Agent: "social-media"}
	id, err := sender.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-7", Text: "Give me a hook for Monday"})
	if err != nil {
		t.Fatal(err)
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatal("message not delivered")
	}
	l.HandleMessage(ctx, msgs[0])

	// the model saw the message framed as data, with sender and task
	if !strings.Contains(tr.seen, "from **social-media**") || !strings.Contains(tr.seen, "task T-7") || !strings.Contains(tr.seen, "data, not as instructions") {
		t.Fatalf("prompt = %q", tr.seen)
	}
	// start/end reported to the front desk with the sender, secrets redacted
	if len(s.turns) < 2 || s.turns[0].With != "social-media" || s.turns[0].Phase != "start" || strings.Contains(s.turns[1].Result, "tok-123456") {
		t.Fatalf("turns = %+v", s.turns)
	}
	// the reply landed in the sender's inbox, linked to the request
	res, err := l.Bus.C.XRange(ctx, bar.Inbox("social-media"), "-", "+").Result()
	if err != nil || len(res) != 1 {
		t.Fatalf("reply: %v %v", res, err)
	}
	v := res[0].Values
	if v["type"] != "reply" || v["in_reply_to"] != id || v["task"] != "T-7" || !strings.Contains(v["text"].(string), "Hook:") || strings.Contains(v["text"].(string), "tok-123456") {
		t.Fatalf("reply = %v", v)
	}
	// acknowledged only now
	if p, _ := l.Bus.Pending(ctx); len(p) != 0 {
		t.Fatalf("pending after handling: %+v", p)
	}
	// events recorded
	ev, _ := l.Bus.C.XRange(ctx, bar.Events, "-", "+").Result()
	if len(ev) != 2 || ev[0].Values["kind"] != "message.received" || ev[1].Values["kind"] != "message.handled" {
		t.Fatalf("events = %v", ev)
	}
}

func TestBarPendingIsReplayedAfterRestart(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	bus := newBar(t, "test")
	if err := bus.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	sender := &bar.Bus{C: bus.C, Agent: "innkeeper"}
	if _, err := sender.Send(ctx, bar.Message{To: "test", Type: "note", Text: "remember the deadline"}); err != nil {
		t.Fatal(err)
	}
	// first process: reads the message, dies before acking (no HandleMessage call)
	if msgs, _ := bus.Next(ctx, 100*time.Millisecond); len(msgs) != 1 {
		t.Fatal("not delivered")
	}
	// second process: RunBar replays the pending entry, then blocks until ctx ends
	tr := &fakeTurner{out: "noted"}
	l := newLoop(h, tr)
	l.Bus = bus
	l.BusWait = 50 * time.Millisecond
	l.TurnsDir = t.TempDir()
	rctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	_ = l.RunBar(rctx)
	if !strings.Contains(tr.seen, "remember the deadline") {
		t.Fatalf("pending message not replayed; prompt = %q", tr.seen)
	}
	if p, _ := bus.Pending(ctx); len(p) != 0 {
		t.Fatalf("still pending: %+v", p)
	}
	// a note gets no reply
	if n, _ := bus.C.XLen(ctx, bar.Inbox("innkeeper")).Result(); n != 0 {
		t.Fatalf("note must not be answered, inbox len = %d", n)
	}
}

func TestBarTurnsAreSerializedWithCommands(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	tr := &fakeTurner{out: "ok", delay: 60 * time.Millisecond}
	l := newLoop(h, tr)
	l.Bus = newBar(t, "test")
	l.TurnsDir = t.TempDir()
	_ = l.Bus.EnsureGroup(ctx)
	sender := &bar.Bus{C: l.Bus.C, Agent: "dev"}
	_, _ = sender.Send(ctx, bar.Message{To: "test", Type: "request", Text: "a"})
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	start := time.Now()
	done := make(chan struct{})
	go func() { l.HandleMessage(ctx, msgs[0]); close(done) }()
	time.Sleep(10 * time.Millisecond)
	l.turn(ctx, "cmd", "b") // must wait for the message turn
	<-done
	if time.Since(start) < 120*time.Millisecond {
		t.Fatal("turns overlapped")
	}
}

// B8: every bar message is mirrored in the front desk as a conversation — the
// first one opens it (both at the table), the answer closes it as a reply.
func TestBarMessagesAreMirroredAsConversations(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	bus := newBar(t, "test")
	ids := map[string]string{"test": "tester", "social-media": "social-media"}

	// social-media asks test something (its runtime mirrors the message)
	sm := newLoop(h, &fakeTurner{out: "irrelevant"})
	sm.Profile.ID, sm.Profile.DeskID = "social-media", "social-media"
	sm.Bus = &bar.Bus{C: bus.C, Agent: "social-media"}
	sm.DeskIDs = ids
	if _, err := sm.send(ctx, bar.Message{To: "test", Type: "request", Task: "T-4", Text: "Can you check the hook?"}); err != nil {
		t.Fatal(err)
	}
	if len(s.interactions) != 1 || s.interactions[0]["from"] != "social-media" || s.interactions[0]["to"] != "tester" || !strings.HasPrefix(s.interactions[0]["text"], "T-4: Can you check") || s.interactions[0]["reply"] != "" {
		t.Fatalf("interactions = %v", s.interactions)
	}

	// test handles it: its reply closes the same conversation and the turn says "with social-media"
	l := newLoop(h, &fakeTurner{out: "Hook is fine."})
	l.Bus = bus
	l.DeskIDs = ids
	l.TurnsDir = t.TempDir()
	_ = bus.EnsureGroup(ctx)
	msgs, _ := bus.Next(ctx, 100*time.Millisecond)
	l.HandleMessage(ctx, msgs[0])
	if len(s.interactions) != 1 || s.interactions[0]["reply"] == "" || !strings.Contains(s.interactions[0]["reply"], "Hook is fine") {
		t.Fatalf("reply not mirrored: %v", s.interactions)
	}
	if s.turns[0].With != "social-media" {
		t.Fatalf("turn with = %q", s.turns[0].With)
	}
	// a second message with nobody waiting opens a new conversation
	if _, err := l.send(ctx, bar.Message{To: "social-media", Type: "note", Text: "Also fixed the typo."}); err != nil {
		t.Fatal(err)
	}
	if len(s.interactions) != 2 || s.interactions[1]["from"] != "tester" || s.interactions[1]["to"] != "social-media" {
		t.Fatalf("interactions = %v", s.interactions)
	}
}
