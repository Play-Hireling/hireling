package asks

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestPicked(t *testing.T) {
	for choice, want := range map[string]bool{
		"Approve": true, "approve": true, "Approve: go ahead": true, "  approve  ": true,
		"Not now": false, "": false, "Approved later": false,
	} {
		if got := Picked(choice, "Approve"); got != want {
			t.Errorf("Picked(%q) = %v, want %v", choice, got, want)
		}
	}
	if Picked("Approve", "") {
		t.Error("no recommendation, nothing picked")
	}
}

// The whole D11 book on one store: one card per task, follow-ups, a bounce
// only once, the answer and the counters.
func bookFlow(t *testing.T, s Store) {
	ctx := context.Background()
	if o, err := s.OpenFor(ctx, "dev", "T-1"); o != nil || err != nil {
		t.Fatalf("nothing open yet: %v %v", o, err)
	}
	if o, err := s.Join(ctx, "dev", "T-1", "anything?"); o != nil || err != nil {
		t.Fatalf("no card to join: %v %v", o, err)
	}
	if err := s.Opened(ctx, Open{Decision: "d-1", Agent: "dev", Task: "T-1", Action: "merge_pr", Question: "Merge PR 7?", Recommended: "Merge it"}); err != nil {
		t.Fatal(err)
	}
	o, err := s.Join(ctx, "dev", "T-1", "Also tag v0.1.0?")
	if err != nil || o == nil || o.Decision != "d-1" || len(o.FollowUps) != 1 {
		t.Fatalf("join: %+v %v", o, err)
	}
	if o, _ := s.Join(ctx, "dev", "T-1", "also tag   v0.1.0?"); len(o.FollowUps) != 1 {
		t.Fatalf("the same follow-up twice: %v", o.FollowUps)
	}
	if o, _ := s.Join(ctx, "social-media", "T-1", "mine?"); o != nil {
		t.Fatal("another hireling's card was joined")
	}
	if first, err := s.Bounce(ctx, "dev", "T-1", "open_pr"); !first || err != nil {
		t.Fatalf("first bounce: %v %v", first, err)
	}
	if first, _ := s.Bounce(ctx, "dev", "T-1", "open_pr"); first {
		t.Fatal("a second ask about the same action must reach the owner")
	}
	if first, _ := s.Bounce(ctx, "dev", "T-2", "open_pr"); !first {
		t.Fatal("another task bounces again")
	}
	// A card without a task stands alone.
	if err := s.Opened(ctx, Open{Decision: "d-2", Agent: "dev", Question: "Rename the repo?", Recommended: "Keep"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Answer(ctx, "d-1", "Merge it")
	if err != nil || !a.PickedRecommended || a.Task != "T-1" || len(a.FollowUps) != 1 {
		t.Fatalf("answer: %+v %v", a, err)
	}
	if _, err := s.Answer(ctx, "d-1", "Merge it"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a card is answered once: %v", err)
	}
	if o, _ := s.OpenFor(ctx, "dev", "T-1"); o != nil {
		t.Fatal("answered card still open")
	}
	if a, _ := s.Answer(ctx, "d-2", "Rename"); a.PickedRecommended {
		t.Fatal("picked the other option")
	}
	st, err := s.Stats(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if st.Opened != 2 || st.Joined != 2 || st.Bounced != 2 || st.Answered != 2 || st.Picked != 1 || st.Tasks != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if st.AsksPerTask != 4 || st.CouldHaveDecidedAlone != 0.5 {
		t.Fatalf("derived: %+v", st)
	}
	open, stats, err := s.List(ctx, "")
	if err != nil || len(open) != 0 || len(stats) != 1 || stats[0].Agent != "dev" {
		t.Fatalf("list: %v %v %v", open, stats, err)
	}
}

func TestMemoryBook(t *testing.T) {
	now := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	m := NewMemory()
	m.Now = func() time.Time { return now }
	bookFlow(t, m)
	// A card unanswered for a week is forgotten.
	_ = m.Opened(context.Background(), Open{Decision: "d-9", Agent: "dev", Task: "T-9", Question: "?"})
	now = now.Add(TTL + time.Minute)
	if o, _ := m.OpenFor(context.Background(), "dev", "T-9"); o != nil {
		t.Fatal("stale card still open")
	}
}

// Runs against a real Redis when REDIS_TEST_URL is set.
func TestRedisBook(t *testing.T) {
	url := os.Getenv("REDIS_TEST_URL")
	if url == "" {
		t.Skip("REDIS_TEST_URL unset")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	defer c.Close()
	if err := c.FlushDB(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	bookFlow(t, &Redis{C: c, Prefix: "test"})
	if ttl := c.TTL(context.Background(), "test:asks:bounce:dev:T-1:open_pr").Val(); ttl <= 0 || ttl > TTL {
		t.Fatalf("bounce TTL = %v", ttl)
	}
}
