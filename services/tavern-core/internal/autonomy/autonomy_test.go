package autonomy

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func logFlow(t *testing.T, s Store) {
	ctx := context.Background()
	at := time.Date(2026, 11, 5, 23, 30, 0, 0, time.UTC)
	for i, a := range []Entry{
		{At: at, Agent: "dev", Action: "open_pr", Outcome: "done", Level: "free", Why: strings.Repeat("why ", 200), Turn: "c1"},
		{At: at.Add(time.Minute), Agent: "social-media", Action: "publish_owned_channel", Outcome: "done", Level: "capped", Used: 1, Limit: 3, Period: "day"},
		{At: at.Add(2 * time.Minute), Agent: "dev", Action: "merge_pr", Outcome: "blocked", Level: "owner"},
		{At: at.Add(time.Hour), Agent: "dev", Action: "open_branch", Outcome: "done", Level: "free"}, // next UTC day
	} {
		if err := s.Append(ctx, a); err != nil {
			t.Fatal(i, err)
		}
	}
	all, err := s.List(ctx, "2026-11-05", "", 0)
	if err != nil || len(all) != 3 || all[0].Action != "merge_pr" {
		t.Fatalf("day = %v %v (newest first, one UTC day)", all, err)
	}
	dev, _ := s.List(ctx, "2026-11-05", "dev", 0)
	if len(dev) != 2 {
		t.Fatalf("dev = %v", dev)
	}
	if r := []rune(dev[1].Why); len(r) > 300 {
		t.Fatalf("why not clipped: %d", len(r))
	}
	if one, _ := s.List(ctx, "2026-11-05", "", 1); len(one) != 1 {
		t.Fatal("limit ignored")
	}
	if next, _ := s.List(ctx, "2026-11-06", "", 0); len(next) != 1 || next[0].Action != "open_branch" {
		t.Fatalf("next day = %v", next)
	}
}

func TestMemoryLog(t *testing.T) { logFlow(t, NewMemory()) }

func TestRedisLog(t *testing.T) {
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
	opt.DB = 2
	c := redis.NewClient(opt)
	defer c.Close()
	_ = c.FlushDB(context.Background()).Err()
	logFlow(t, &Redis{C: c, Prefix: "test"})
	if ttl := c.TTL(context.Background(), "test:autonomy:2026-11-05").Val(); ttl <= 0 || ttl > Retention {
		t.Fatalf("TTL = %v", ttl)
	}
}
