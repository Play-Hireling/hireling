package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/gate"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
)

// labelTurner runs every turn through the real gate hook (with tavern-core
// saying the day's tokens are spent) and records, per turn, the priority the
// hook read and whether it let a tool call through.
type labelTurner struct {
	mu   sync.Mutex
	hook *gate.Hook
	seen map[string][]turnLabel // turn text marker → labels
}

type turnLabel struct {
	prio    string
	allowed bool
}

func (lt *labelTurner) Run(ctx context.Context, text string) (string, error) {
	var tf struct {
		Priority string `json:"priority"`
	}
	raw, _ := os.ReadFile(filepath.Join(lt.hook.Home, gate.TurnFile))
	_ = json.Unmarshal(raw, &tf)
	o := lt.hook.Decide(ctx, gate.Payload{Tool: "read_file", Input: map[string]any{"path": "brief.md"}})
	key := "other"
	for _, k := range []string{"the owner's order", "weekly routine"} {
		if strings.Contains(text, k) {
			key = k
		}
	}
	lt.mu.Lock()
	lt.seen[key] = append(lt.seen[key], turnLabel{tf.Priority, o.Allow})
	lt.mu.Unlock()
	return "ok", nil
}

func (lt *labelTurner) labels(key string) []turnLabel {
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return append([]turnLabel(nil), lt.seen[key]...)
}

// prioLoop is a hireling with a token budget, under it by tavern-core's last
// recorded number (so the bar runs its P3 work), while the gate hook's
// in-turn check says the day's tokens are spent (D7 in-turn). Off-peak, so
// D9 holds nothing.
func prioLoop(t *testing.T) (*server, *Loop, *labelTurner) {
	t.Helper()
	s, h := newServer(t)
	home := t.TempDir()
	lt := &labelTurner{hook: &gate.Hook{Agent: "test", Home: home, Checker: &overTokens{}, Spend: spent(5000)}, seen: map[string][]turnLabel{}}
	l := newLoop(h, lt)
	l.Profile.Budget.TokensPerDay = 100000
	l.now = func() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }
	l.TurnsDir = t.TempDir()
	l.GateTurnFile = filepath.Join(home, gate.TurnFile)
	l.Bus = newBar(t, "test")
	if err := l.Bus.EnsureGroup(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Budget = &fakeBudget{b: tavern.Budget{TokensPerDay: 100000, TokensUsed: 1000}, prio: map[string]string{}}
	return s, l, lt
}

func (s *server) started(topic string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.turns {
		if t.Phase == frontdesk.Start && strings.Contains(t.Topic, topic) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The D7 in-turn race: the bar loop used to set a shared "priority of
// the running turn" before waiting for the turn lock, so the owner's command
// taking the lock first ran labelled P3 and was cut. The priority now rides
// on each turn's ctx: the command is P0 and runs on, the P3 message is cut.
func TestOwnerCommandWhileP3WaitsIsP0(t *testing.T) {
	ctx := context.Background()
	s, l, lt := prioLoop(t)
	l.Budget.(*fakeBudget).prio["T-3"] = "P3"
	sender := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	if _, err := sender.Send(ctx, bar.Message{To: "test", Type: "request", Task: "T-3", Text: "weekly routine"}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := l.Bus.Next(ctx, 100*time.Millisecond)
	if len(msgs) != 1 {
		t.Fatalf("read %d", len(msgs))
	}

	l.turnMu.Lock() // a turn is running
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); l.handleBatch(ctx, msgs) }()
	waitFor(t, "the P3 message waiting for the turn lock", func() bool { return s.started("weekly routine") })
	go func() { defer wg.Done(); l.Handle(ctx, frontdesk.Command{ID: "c-1", Text: "the owner's order"}) }()
	waitFor(t, "the owner's command waiting for the turn lock", func() bool { return s.started("the owner's order") })
	time.Sleep(20 * time.Millisecond) // both blocked on turnMu
	l.turnMu.Unlock()
	wg.Wait()

	if got := lt.labels("the owner's order"); len(got) != 1 || got[0] != (turnLabel{"P0", true}) {
		t.Fatalf("the owner's command = %+v, want P0 and not cut", got)
	}
	if got := lt.labels("weekly routine"); len(got) != 1 || got[0] != (turnLabel{"P3", false}) {
		t.Fatalf("P3 message = %+v, want P3 and cut", got)
	}
}

// Commands and bar messages at the same time, many rounds: every turn keeps
// its own label (run with -race: no shared field is written while a turn
// reads it).
func TestConcurrentCommandsAndBarKeepTheirPriority(t *testing.T) {
	ctx := context.Background()
	_, l, lt := prioLoop(t)
	sender := &bar.Bus{C: l.Bus.C, Agent: "innkeeper"}
	const rounds = 8
	for i := 0; i < rounds; i++ {
		l.Budget.(*fakeBudget).prio[fmt.Sprintf("T-%d", i)] = "P3"
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			l.Handle(ctx, frontdesk.Command{ID: fmt.Sprintf("c-%d", i), Text: "the owner's order"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := sender.Send(ctx, bar.Message{To: "test", Type: "request", Task: fmt.Sprintf("T-%d", i), Text: "weekly routine"}); err != nil {
				t.Error(err)
				return
			}
			msgs, err := l.Bus.Next(ctx, 100*time.Millisecond)
			if err != nil {
				t.Error(err)
				return
			}
			l.handleBatch(ctx, msgs)
		}
	}()
	wg.Wait()
	cmds, msgs := lt.labels("the owner's order"), lt.labels("weekly routine")
	if len(cmds) != rounds || len(msgs) != rounds {
		t.Fatalf("turns: %d commands, %d messages", len(cmds), len(msgs))
	}
	for _, c := range cmds {
		if c != (turnLabel{"P0", true}) {
			t.Fatalf("a command ran as %+v", c)
		}
	}
	for _, m := range msgs {
		if m != (turnLabel{"P3", false}) {
			t.Fatalf("a P3 message ran as %+v", m)
		}
	}
}
