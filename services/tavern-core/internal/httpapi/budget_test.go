package httpapi

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/autonomy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/policy"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/usage"
)

// D7 inside a turn: the hook sends the running turn's spend with the
// `tokens` action; today's recorded tokens plus it, against tokens_per_day,
// decide free or budget, and only the refusal lands in the Autonomy Log.
func TestTokenBudgetCheck(t *testing.T) {
	p := &profile.Profile{ID: "social-media", Model: "deepseek-v4-flash"}
	p.Budget.TokensPerDay = 150000
	p.Autonomy.OwnerDecides = profile.HardDeny
	now := time.Date(2026, 11, 5, 15, 0, 0, 0, time.UTC)
	spent := usage.NewMemory()
	log := autonomy.NewMemory()
	s := &Server{Gate: policy.New(p), Tasks: &tasks.Service{Store: tasks.NewMemory()}, Usage: spent,
		Grants: policy.NewMemoryGrants(), Log: log, Now: func() time.Time { return now }}
	srv := listen(s, testOwnerToken)
	defer srv.Close()
	if err := spent.Add(context.Background(), usage.Turn{Agent: "social-media", CommandID: "msg-0", At: now.Add(-time.Hour),
		Totals: usage.Totals{InputTokens: 90000, OutputTokens: 10000}}); err != nil {
		t.Fatal(err)
	}
	check := func(turn int) policy.Decision {
		t.Helper()
		var d policy.Decision
		body := `{"action":"tokens","target":"browser_navigate","use":true,"turn":"msg-1","task":"T-9","turn_tokens":` + strconv.Itoa(turn) + `}`
		if code := call(t, srv, "POST", "/actions/check", "social-media", body, &d); code != 200 {
			t.Fatalf("status %d", code)
		}
		return d
	}
	if d := check(20000); d.Level != policy.Free || d.Used != 120000 || d.Limit != 150000 {
		t.Fatalf("under the budget: %+v", d)
	}
	d := check(50000)
	if d.Level != policy.Budget || d.Used != 150000 || !strings.Contains(d.Reason, "150000/150000") || !strings.Contains(d.Reason, "50000 of them in this turn") {
		t.Fatalf("crossing the budget mid-turn: %+v", d)
	}
	entries, _ := log.List(context.Background(), "2026-11-05", "", 10)
	if len(entries) != 1 {
		t.Fatalf("log: %+v (only the refusal is logged)", entries)
	}
	if e := entries[0]; e.Action != "tokens" || e.Outcome != "blocked" || e.Level != "budget" || e.Target != "browser_navigate" ||
		e.Turn != "msg-1" || e.Task != "T-9" || e.Used != 150000 || e.Limit != 150000 || e.Period != "day" {
		t.Fatalf("log entry: %+v", e)
	}
	// No token budget in the profile: always free.
	p.Budget.TokensPerDay = 0
	if d := check(10_000_000); d.Level != policy.Free {
		t.Fatalf("no budget: %+v", d)
	}
}
