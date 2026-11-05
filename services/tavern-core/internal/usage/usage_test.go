package usage

import (
	"context"
	"testing"
	"time"
)

func TestMemoryDays(t *testing.T) {
	m := NewMemory()
	ctx := context.Background()
	d1 := time.Date(2026, 11, 3, 23, 59, 0, 0, time.UTC)
	d2 := d1.Add(2 * time.Minute) // next UTC day
	_ = m.Add(ctx, Turn{Agent: "test", CommandID: "a", At: d1, Totals: Totals{Calls: 2, InputTokens: 10, OutputTokens: 5, CostUSD: 0.01, Priced: true}})
	_ = m.Add(ctx, Turn{Agent: "test", CommandID: "b", At: d1, Totals: Totals{Calls: 1, InputTokens: 1, Priced: false}})
	_ = m.Add(ctx, Turn{Agent: "test", CommandID: "c", At: d2, Totals: Totals{Calls: 1, OutputTokens: 7, Priced: true}})
	day, _ := m.Day(ctx, "test", "2026-11-03")
	if day.Turns != 2 || day.Totals.Calls != 3 || day.Totals.Tokens() != 16 || day.Unknown != 1 || day.Totals.Priced {
		t.Fatalf("day 1: %+v", day)
	}
	next, _ := m.Day(ctx, "test", "2026-11-04")
	if next.Turns != 1 || next.Totals.Tokens() != 7 || !next.Totals.Priced {
		t.Fatalf("day 2: %+v", next)
	}
	empty, _ := m.Day(ctx, "test", "2026-01-01")
	if empty.Turns != 0 || empty.Day != "2026-01-01" {
		t.Fatalf("empty: %+v", empty)
	}
	r, _ := m.Recent(ctx, "test", 2)
	if len(r) != 2 || r[0].CommandID != "c" || r[1].CommandID != "b" {
		t.Fatalf("recent: %+v", r)
	}
	if err := Validate(&Turn{Agent: "test"}, d1); err != ErrInvalid {
		t.Fatal(err)
	}
}
