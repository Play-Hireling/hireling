package tasks

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// store is one Store under test plus a way to read back the events it emitted.
type store struct {
	Store
	events func(t *testing.T) []Event
}

func (s store) last(t *testing.T) Event {
	t.Helper()
	es := s.events(t)
	if len(es) == 0 {
		t.Fatal("no events")
	}
	return es[len(es)-1]
}

// eachStore runs a case on Memory and, when REDIS_TEST_URL is set, on Redis.
func eachStore(t *testing.T, f func(t *testing.T, st store)) {
	t.Run("memory", func(t *testing.T) {
		m := NewMemory()
		f(t, store{Store: m, events: func(*testing.T) []Event { return m.Events }})
	})
	t.Run("redis", func(t *testing.T) {
		r := newRedis(t, "test")
		f(t, store{Store: r, events: func(t *testing.T) []Event { return streamEvents(t, r.C) }})
	})
}

// newRedis connects to REDIS_TEST_URL (skips when unset) on a DB of its own
// (go test runs packages in parallel and each flushes its DB first) and
// flushes it.
func newRedis(t *testing.T, prefix string) *Redis {
	t.Helper()
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL unset")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	opt.DB = 4
	c := redis.NewClient(opt)
	t.Cleanup(func() { c.Close() })
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return &Redis{C: c, Prefix: prefix}
}

// streamEvents reads the whole `events` stream back as Events.
func streamEvents(t *testing.T, c *redis.Client) []Event {
	t.Helper()
	msgs, err := c.XRange(context.Background(), "events", "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]Event, 0, len(msgs))
	for _, m := range msgs {
		str := func(k string) string { s, _ := m.Values[k].(string); return s }
		at, err := time.Parse(time.RFC3339, str("at"))
		if err != nil {
			t.Fatalf("event %s: at %q: %v", m.ID, str("at"), err)
		}
		out = append(out, Event{Type: str("type"), Task: str("task"), Agent: str("agent"), To: str("to"), At: at})
	}
	return out
}

// Every field of a task survives the JSON round trip through the hash.
func TestRedisRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newRedis(t, "test")
	exp := time.Date(2026, 11, 5, 12, 15, 0, 0, time.UTC)
	in := &Task{
		ID: "T-7", Brief: "need visuals", NeedsApproval: true, DecidedBy: "innkeeper", DecisionNote: "go",
		Title: "carousel", Owner: "social-media", RequestedBy: "scriptwriter", Priority: "P1", Status: Leased,
		LeaseExpiresAt: &exp, GoalContribution: Contribution{Kind: "posts", Expected: 3, Note: "launch"},
		AutonomyLevel: "p2", Attachments: []string{"/tavern/shared/a.png"}, Parent: "T-1", Progress: "half",
		Failures: 2, LastError: "canva: 502", BlockedReason: NeedsHuman,
		CreatedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 11, 5, 12, 1, 0, 0, time.UTC),
	}
	if err := r.Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "T-7")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, in) {
		t.Fatalf("round trip\n got %+v\nwant %+v", got, in)
	}
	// Put overwrites; List returns copies of every task.
	in.Status = Done
	in.LeaseExpiresAt = nil
	if err := r.Put(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := r.Put(ctx, &Task{ID: "T-8", Title: "other"}); err != nil {
		t.Fatal(err)
	}
	all, err := r.List(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %v %v", all, err)
	}
	for _, x := range all {
		if x.ID == "T-7" && (x.Status != Done || x.LeaseExpiresAt != nil) {
			t.Fatalf("overwrite lost: %+v", x)
		}
	}
}

func TestRedisNotFoundAndEmpty(t *testing.T) {
	ctx := context.Background()
	r := newRedis(t, "test")
	if _, err := r.Get(ctx, "T-404"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task: %v", err)
	}
	if all, err := r.List(ctx); err != nil || len(all) != 0 {
		t.Fatalf("empty list = %v %v", all, err)
	}
}

// Keys live under the prefix ("tavern" by default); ids count up per prefix.
func TestRedisKeysAndIDs(t *testing.T) {
	ctx := context.Background()
	r := newRedis(t, "")
	for i, want := range []string{"T-1", "T-2", "T-3"} {
		if id, err := r.NextID(ctx); err != nil || id != want {
			t.Fatalf("id %d = %q %v", i, id, err)
		}
	}
	if err := r.Put(ctx, &Task{ID: "T-1", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if n := r.C.HLen(ctx, "tavern:tasks").Val(); n != 1 {
		t.Fatalf("tavern:tasks has %d fields", n)
	}
	if v := r.C.Get(ctx, "tavern:tasks:seq").Val(); v != "3" {
		t.Fatalf("tavern:tasks:seq = %q", v)
	}
	other := &Redis{C: r.C, Prefix: "other"}
	if id, _ := other.NextID(ctx); id != "T-1" {
		t.Fatalf("other prefix id = %q", id)
	}
	if _, err := other.Get(ctx, "T-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("prefixes leak: %v", err)
	}
}

// A corrupt entry is an error, not a zero task.
func TestRedisCorruptEntry(t *testing.T) {
	ctx := context.Background()
	r := newRedis(t, "test")
	if err := r.C.HSet(ctx, "test:tasks", "T-1", "{not json").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, "T-1"); err == nil {
		t.Fatal("corrupt task decoded")
	}
	if _, err := r.List(ctx); err == nil {
		t.Fatal("corrupt list decoded")
	}
}

// Events land on the shared `events` stream with every field, time in UTC.
func TestRedisEmit(t *testing.T) {
	ctx := context.Background()
	r := newRedis(t, "test")
	at := time.Date(2026, 11, 5, 9, 30, 15, 0, time.FixedZone("BRT", -3*3600))
	if err := r.Emit(ctx, Event{Type: "handoff", Task: "T-1", Agent: "social-media", To: "scriptwriter", At: at}); err != nil {
		t.Fatal(err)
	}
	msgs, err := r.C.XRange(ctx, "events", "-", "+").Result()
	if err != nil || len(msgs) != 1 {
		t.Fatalf("stream = %v %v", msgs, err)
	}
	want := map[string]any{"type": "handoff", "task": "T-1", "agent": "social-media", "to": "scriptwriter", "at": "2026-11-05T12:30:15Z"}
	if !reflect.DeepEqual(msgs[0].Values, want) {
		t.Fatalf("event = %v", msgs[0].Values)
	}
}

// A dead Redis surfaces as an error from every call.
func TestRedisDown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	defer c.Close()
	r := &Redis{C: c}
	if _, err := r.Get(ctx, "T-1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("get: %v", err)
	}
	if err := r.Put(ctx, &Task{ID: "T-1"}); err == nil {
		t.Fatal("put succeeded")
	}
	if _, err := r.List(ctx); err == nil {
		t.Fatal("list succeeded")
	}
	if _, err := r.NextID(ctx); err == nil {
		t.Fatal("next id succeeded")
	}
	if err := r.Emit(ctx, Event{Type: "created", Task: "T-1"}); err == nil {
		t.Fatal("emit succeeded")
	}
}
