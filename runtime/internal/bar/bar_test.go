package bar

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newBus(t *testing.T, agent string) (*Bus, *miniredis.Miniredis) {
	t.Helper()
	m := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: m.Addr()})
	return &Bus{C: c, Agent: agent}, m
}

func TestSendReadAck(t *testing.T) {
	ctx := context.Background()
	b, m := newBus(t, "scriptwriter")
	sender := &Bus{C: b.C, Agent: "social-media"}
	if err := b.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := sender.Send(ctx, Message{To: "scriptwriter", Type: "request", Task: "T-1", Text: "Need a hook for Monday's post"})
	if err != nil {
		t.Fatal(err)
	}
	// mirrored on the bar
	if n, _ := b.C.XLen(ctx, Bar).Result(); n != 1 {
		t.Fatalf("bar len = %d", n)
	}
	_ = m
	msgs, err := b.Next(ctx, 100*time.Millisecond)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("next: %v %v", msgs, err)
	}
	got := msgs[0]
	if got.ID != id || got.From != "social-media" || got.Type != "request" || got.Task != "T-1" || got.TS == "" {
		t.Fatalf("message = %+v", got)
	}
	// not acked yet: a fresh consumer run finds it pending
	pend, _ := b.Pending(ctx)
	if len(pend) != 1 || pend[0].ID != id {
		t.Fatalf("pending = %+v", pend)
	}
	if err := b.Ack(ctx, id); err != nil {
		t.Fatal(err)
	}
	pend, _ = b.Pending(ctx)
	if len(pend) != 0 {
		t.Fatalf("still pending after ack: %+v", pend)
	}
	// nothing new: Next times out with nil, nil
	msgs, err = b.Next(ctx, 50*time.Millisecond)
	if err != nil || msgs != nil {
		t.Fatalf("expected timeout, got %v %v", msgs, err)
	}
}

func TestNothingLostWhileDown(t *testing.T) {
	ctx := context.Background()
	b, _ := newBus(t, "dev")
	sender := &Bus{C: b.C, Agent: "innkeeper"}
	// the group exists (first start), then the agent is "down" while two messages arrive
	if err := b.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"first", "second"} {
		if _, err := sender.Send(ctx, Message{To: "dev", Type: "note", Text: txt}); err != nil {
			t.Fatal(err)
		}
	}
	// agent comes back: EnsureGroup is idempotent, nothing pending, both delivered in order
	if err := b.EnsureGroup(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.Pending(ctx); len(p) != 0 {
		t.Fatalf("pending = %+v", p)
	}
	msgs, err := b.Next(ctx, 100*time.Millisecond)
	if err != nil || len(msgs) != 2 || msgs[0].Text != "first" || msgs[1].Text != "second" {
		t.Fatalf("got %+v %v", msgs, err)
	}
}

func TestValidate(t *testing.T) {
	bad := []Message{
		{To: "x", Type: "request", Text: "hi"},          // no from
		{From: "a", To: "b", Type: "shout", Text: "hi"}, // unknown type
		{From: "a", To: "b", Type: "note"},              // empty
		{From: "a", To: "b", Type: "note", Text: string(make([]byte, 4001))},
	}
	for i, m := range bad {
		if Validate(m) == nil {
			t.Errorf("case %d must fail: %+v", i, m)
		}
	}
	if err := Validate(Message{From: "a", To: "b", Type: "handoff", Attachment: "/tavern/shared/T-1/brief.md"}); err != nil {
		t.Fatal(err)
	}
}
