package policy

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestApproves(t *testing.T) {
	for choice, want := range map[string]bool{
		"Approve": true, "approve": true, "Yes, go ahead": true, "OK": true,
		"Not now": false, "No": false, "Deny": false, "": false, "Send a new link": false,
	} {
		if got := Approves(choice, "Approve"); got != want {
			t.Errorf("Approves(%q) = %v, want %v", choice, got, want)
		}
	}
	if !Approves("Merge it", "Merge it") {
		t.Error("the card's own first option must count as yes")
	}
}

// The whole D1/D3 loop on one store: block → card → the owner → one use → gone.
func grantsFlow(t *testing.T, g Grants, now func() time.Time) {
	ctx := context.Background()
	start := now()
	if err := g.Blocked(ctx, "dev", Act{Action: "merge_pr", Target: "gh pr merge 7", At: start}); err != nil {
		t.Fatal(err)
	}
	if err := g.Blocked(ctx, "dev", Act{Action: "http_write", Target: "old", At: start.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	got, err := g.RecentBlocked(ctx, "dev", start.Add(-time.Minute))
	if err != nil || len(got) != 1 || got[0].Action != "merge_pr" {
		t.Fatalf("recent = %v, %v (only this turn's block)", got, err)
	}
	if acts, _ := g.RecentBlocked(ctx, "social-media", start.Add(-time.Minute)); len(acts) != 0 {
		t.Fatalf("another hireling's blocks leaked: %v", acts)
	}
	if ok, _ := g.Consume(ctx, "dev", "merge_pr"); ok {
		t.Fatal("a grant before the owner answered")
	}
	if err := g.Pend(ctx, Pending{Decision: "d-1", Agent: "dev", Actions: got, Approve: "Approve"}); err != nil {
		t.Fatal(err)
	}
	// "Not now" grants nothing and closes the card.
	if err := g.Pend(ctx, Pending{Decision: "d-2", Agent: "dev", Actions: got, Approve: "Approve"}); err != nil {
		t.Fatal(err)
	}
	if acts, err := Answer(ctx, g, "d-2", "Not now"); err != nil || len(acts) != 0 {
		t.Fatalf("not now: %v %v", acts, err)
	}
	if _, err := Answer(ctx, g, "d-2", "Approve"); !errors.Is(err, ErrNoPending) {
		t.Fatalf("a card is answered once: %v", err)
	}
	acts, err := Answer(ctx, g, "d-1", "Approve")
	if err != nil || len(acts) != 1 || acts[0].Action != "merge_pr" {
		t.Fatalf("approve: %v %v", acts, err)
	}
	if ok, _ := g.Consume(ctx, "social-media", "merge_pr"); ok {
		t.Fatal("the grant is the asking hireling's only")
	}
	if ok, err := g.Consume(ctx, "dev", "merge_pr"); !ok || err != nil {
		t.Fatalf("approved action not granted: %v", err)
	}
	if ok, _ := g.Consume(ctx, "dev", "merge_pr"); ok {
		t.Fatal("one approval, one use")
	}
}

func TestMemoryGrants(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	m := NewMemoryGrants()
	m.Now = func() time.Time { return now }
	grantsFlow(t, m, m.Now)
	// A grant lapses after a day.
	_ = m.Grant(context.Background(), "dev", "open_pr", 1)
	now = now.Add(GrantTTL + time.Minute)
	if ok, _ := m.Consume(context.Background(), "dev", "open_pr"); ok {
		t.Fatal("expired grant used")
	}
}

// Runs against a real Redis when REDIS_TEST_URL is set (CI sets it; run it
// locally with `redis-server` → REDIS_TEST_URL=redis://127.0.0.1:6379).
func TestRedisGrants(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL unset")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	// Each package's Redis test owns a DB: go test runs packages in
	// parallel and every one of them flushes its DB first.
	opt.DB = 3
	c := redis.NewClient(opt)
	defer c.Close()
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	r := &RedisGrants{C: c, Prefix: "test"}
	grantsFlow(t, r, time.Now)
	if ttl := c.TTL(context.Background(), "test:gate:blocked:dev").Val(); ttl <= 0 || ttl > BlockedTTL {
		t.Fatalf("blocked list TTL = %v", ttl)
	}
	for i := 1; i <= 2; i++ {
		if n, ok, err := r.Use(context.Background(), "a", "x", "2026-11-05", 2, time.Hour); !ok || n != i || err != nil {
			t.Fatal(i, n, ok, err)
		}
	}
	if n, ok, err := r.Use(context.Background(), "a", "x", "2026-11-05", 2, time.Hour); ok || n != 2 || err != nil {
		t.Fatal("over the cap", n, ok, err)
	}
	if ttl := c.TTL(context.Background(), "test:gate:count:a:x:2026-11-05").Val(); ttl <= 0 || ttl > time.Hour {
		t.Fatalf("count TTL = %v", ttl)
	}
}

func TestCapAndPeriod(t *testing.T) {
	if n, p := Cap(map[string]int{"per_day": 3}); n != 3 || p != "day" {
		t.Fatal(n, p)
	}
	if n, p := Cap(map[string]int{"per_week": 1}); n != 1 || p != "week" {
		t.Fatal(n, p)
	}
	if n, _ := Cap(map[string]int{}); n != 0 {
		t.Fatal("no limit given must be a cap of zero, not unlimited")
	}
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) // ISO week 1 of 2026
	if PeriodKey("day", at) != "2026-01-01" || PeriodKey("week", at) != "2026-W01" {
		t.Fatal(PeriodKey("day", at), PeriodKey("week", at))
	}
}

func TestMemoryUse(t *testing.T) {
	m := NewMemoryGrants()
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		if n, ok, _ := m.Use(ctx, "a", "x", "2026-11-05", 2, time.Hour); !ok || n != i {
			t.Fatal(i, n, ok)
		}
	}
	if n, ok, _ := m.Use(ctx, "a", "x", "2026-11-05", 2, time.Hour); ok || n != 2 {
		t.Fatal("over the cap", n, ok)
	}
	if _, ok, _ := m.Use(ctx, "a", "x", "2026-11-05", 0, time.Hour); ok {
		t.Fatal("a cap of zero allowed a use")
	}
}
