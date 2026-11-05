package loop

import (
	"context"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
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
	notified  string          // UTC day the "over budget" event was sent
	essential map[string]bool // message id → runs over budget (read once per message)
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

// essential is work that runs over budget: P0/P1 tasks and alerts. A message
// without a task, or about a task tavern-core cannot find, is P2.
func (l *Loop) essential(ctx context.Context, m bar.Message) bool {
	if m.Type == "alert" {
		return true
	}
	if m.Task == "" || l.Budget == nil {
		return false
	}
	if v, ok := l.budget.essential[m.ID]; ok {
		return v
	}
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	t, err := l.Budget.GetTask(tctx, m.Task)
	if err != nil {
		return false // unknown is P2; asked again on the next tick
	}
	if l.budget.essential == nil {
		l.budget.essential = map[string]bool{}
	}
	l.budget.essential[m.ID] = prioRank(t.Priority) <= 1
	return l.budget.essential[m.ID]
}

// handleBatch runs a batch of bar messages in queue order (B3). Over the
// token budget (D7) only P0/P1 work runs; the rest is held — not acked, so a
// restart finds it pending — and retried on the next batch or idle tick,
// which runs it once the UTC day (and with it the budget) turns over.
func (l *Loop) handleBatch(ctx context.Context, msgs []bar.Message) {
	if len(msgs) == 0 && len(l.held) > 0 {
		if over, _ := l.overBudget(ctx); over {
			return // idle tick, still over: nothing new to decide
		}
	}
	batch := append(l.held, msgs...)
	l.held = nil
	for _, m := range l.order(ctx, batch) {
		if over, b := l.overBudget(ctx); over && !l.essential(ctx, m) {
			l.held = append(l.held, m)
			l.noteOverBudget(ctx, b)
			continue
		}
		delete(l.budget.essential, m.ID)
		l.HandleMessage(ctx, m)
		l.spentTurn()
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
