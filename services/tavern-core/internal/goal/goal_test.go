package goal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestQuest(t *testing.T) {
	ctx := context.Background()
	s := &Service{Store: NewMemory(), Now: func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) },
		Approver: func(a string) bool { return a == "owner" || a == "innkeeper" }}
	if _, err := s.Get(ctx); !errors.Is(err, ErrNotSet) {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, "scriptwriter", Define{Title: "Net revenue", Target: 120000}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-approver defined the quest: %v", err)
	}
	if _, err := s.Set(ctx, "owner", Define{Title: "Net revenue", Target: 0}); err == nil {
		t.Fatal("target 0 accepted")
	}
	g, err := s.Set(ctx, "owner", Define{Title: "Net revenue", Target: 120000, Kinds: []string{"Revenue", "leads", "revenue"}})
	if err != nil || g.Currency != "USD" || g.Source != "owner" || len(g.Kinds) != 2 || g.Percent() != 0 {
		t.Fatalf("%v %+v", err, g)
	}
	if _, err := s.Report(ctx, "innkeeper", 15000); !errors.Is(err, ErrNotSource) {
		t.Fatalf("hireling moved the bar: %v", err)
	}
	if _, err := s.Report(ctx, "Owner", 15000); !errors.Is(err, ErrNotSource) {
		t.Fatalf("X-Hireling Owner matched source owner: %v", err)
	}
	if _, err := s.Set(ctx, "Owner", Define{Title: "Net revenue", Target: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("X-Hireling Owner defined the quest: %v", err)
	}
	g, err = s.Report(ctx, "owner", 15000)
	if err != nil || g.Current != 15000 || g.Percent() != 12.5 || g.UpdatedBy != "owner" {
		t.Fatalf("%v %+v", err, g)
	}
	if want := "Quest: Net revenue — 12.5% [█░░░░░░░░░] 15,000/120,000 USD"; g.Bar() != want {
		t.Fatalf("bar = %q", g.Bar())
	}
	// Redefining keeps the number unless told otherwise.
	g, _ = s.Set(ctx, "innkeeper", Define{Title: "Net revenue Q4", Target: 150000})
	if g.Current != 15000 || g.Percent() != 10 {
		t.Fatalf("%+v", g)
	}
	if !g.KindAllowed("revenue") || g.KindAllowed("coffee") {
		t.Fatal("kinds")
	}
	if (Goal{}).KindAllowed("support") != true {
		t.Fatal("default kinds")
	}
	if (Goal{Target: 10, Current: 25}).Percent() != 100 {
		t.Fatal("cap")
	}
}
