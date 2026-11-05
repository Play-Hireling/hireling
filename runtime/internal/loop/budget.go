package loop

import (
	"context"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
	"github.com/Play-Hireling/hireling/runtime/internal/usage"
)

// BudgetReader is tavern-core's side of D7: today's tokens against the
// profile's tokens_per_day, and the priority of the task a message is about.
type BudgetReader interface {
	Budget(ctx context.Context) (*tavern.Budget, error)
	GetTask(ctx context.Context, id string) (*tavern.Task, error)
}

// budgetTTL is how long one budget read is trusted between turns.
const budgetTTL = 30 * time.Second

type budgetState struct {
	read      time.Time
	b         *tavern.Budget
	notified  string         // UTC day the "over budget" event was sent
	peakNoted string         // peak window (its end) the "routines wait" event was sent for
	rank      map[string]int // message id → priority rank (read once per message)
}

// overBudget reports whether today's tokens reached tokens_per_day (D7).
// No budget in the profile, no tavern-core or a failed read → not over: the
// usage is still recorded and the next read decides (the owner's commands never
// wait on this either way).
func (l *Loop) overBudget(ctx context.Context) (bool, *tavern.Budget) {
	if l.Budget == nil || l.Profile.Budget.TokensPerDay <= 0 {
		return false, nil
	}
	now := l.clock()
	if l.budget.b == nil || now.Sub(l.budget.read) > budgetTTL {
		bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		b, err := l.Budget.Budget(bctx)
		cancel()
		if err != nil {
			l.logf("budget: %v (not holding work)", err)
			return false, nil
		}
		l.budget.b, l.budget.read = b, now
	}
	b := l.budget.b
	return b.TokensPerDay > 0 && b.TokensUsed >= int64(b.TokensPerDay), b
}

func (l *Loop) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// spentTurn forgets the cached budget after a turn, so the next message is
// decided on fresh numbers.
func (l *Loop) spentTurn() { l.budget.b = nil }

// priority is the rank (0 = P0 … 3 = P3) of the work a bar message carries:
// alerts are 0, a message without a task (or about a task tavern-core cannot
// find) is 2, otherwise the task's priority, read once per message.
func (l *Loop) priority(ctx context.Context, m bar.Message) int {
	if m.Type == "alert" {
		return 0
	}
	if m.Task == "" || l.Budget == nil {
		return 2
	}
	if v, ok := l.budget.rank[m.ID]; ok {
		return v
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	t, err := l.Budget.GetTask(tctx, m.Task)
	if err != nil {
		return 2 // unknown is P2; asked again on the next tick
	}
	if l.budget.rank == nil {
		l.budget.rank = map[string]int{}
	}
	l.budget.rank[m.ID] = prioRank(t.Priority)
	return l.budget.rank[m.ID]
}

// holdReason says why a bar message waits instead of running now:
//   - "budget" (D7): over today's tokens, only P0/P1 run;
//   - "peak" (D9): routines (P3) wait for DeepSeek's off-peak hours, when
//     every token costs half, if the hireling runs on DeepSeek.
//
// "" means run it.
func (l *Loop) holdReason(ctx context.Context, m bar.Message) string {
	over, _ := l.overBudget(ctx)
	peak := usage.Discounted(l.Profile.Model) && usage.IsPeak(l.clock())
	if !over && !peak {
		return ""
	}
	p := l.priority(ctx, m)
	switch {
	case over && p >= 2:
		return "budget"
	case peak && p >= 3:
		return "peak"
	}
	return ""
}

// handleBatch runs a batch of bar messages in queue order (B3). Work that
// has to wait (holdReason) is held — not acked, so a restart finds it
// pending — and retried on the next batch or idle tick: once the UTC day
// turns (budget) or the peak window ends (routines).
func (l *Loop) handleBatch(ctx context.Context, msgs []bar.Message) {
	if len(msgs) == 0 {
		runnable := false
		for _, m := range l.held {
			if l.holdReason(ctx, m) == "" {
				runnable = true
				break
			}
		}
		if !runnable {
			return // idle tick: everything held still has to wait
		}
	}
	batch := append(l.held, msgs...)
	l.held = nil
	for _, m := range l.order(ctx, batch) {
		switch l.holdReason(ctx, m) {
		case "budget":
			l.held = append(l.held, m)
			_, b := l.overBudget(ctx)
			l.noteOverBudget(ctx, b)
			continue
		case "peak":
			l.held = append(l.held, m)
			l.notePeak(ctx)
			continue
		}
		delete(l.budget.rank, m.ID)
		l.HandleMessage(ctx, m)
		l.spentTurn()
	}
}

// notePeak tells the tavern once per peak window that routines wait (D9).
func (l *Loop) notePeak(ctx context.Context) {
	until := usage.OffPeakAt(l.clock())
	key := until.Format(time.RFC3339)
	if l.budget.peakNoted == key {
		return
	}
	l.budget.peakNoted = key
	l.logf("off-peak: DeepSeek peak hours, routines (P3) wait until %s", key)
	if l.Bus != nil {
		_ = l.Bus.Event(ctx, "offpeak.deferred", map[string]any{"until": key, "held": len(l.held)})
	}
}

// noteOverBudget tells the tavern once per UTC day that this hireling is
// holding its P2/P3 work.
func (l *Loop) noteOverBudget(ctx context.Context, b *tavern.Budget) {
	if b == nil || l.budget.notified == b.Day {
		return
	}
	l.budget.notified = b.Day
	l.logf("budget: %d/%d tokens today (%s): only P0/P1 work until %s", b.TokensUsed, b.TokensPerDay, b.Day, b.ResetsAt)
	if l.Bus != nil {
		_ = l.Bus.Event(ctx, "budget.exceeded", map[string]any{"day": b.Day, "tokens_used": b.TokensUsed,
			"tokens_per_day": b.TokensPerDay, "resets_at": b.ResetsAt, "held": len(l.held)})
	}
}
