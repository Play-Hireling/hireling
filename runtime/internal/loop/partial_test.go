package loop

import (
	"context"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
)

type partialTurner struct {
	fn func(context.Context, string)
}

func (p *partialTurner) SetOnPartial(fn func(context.Context, string)) { p.fn = fn }

func (p *partialTurner) Run(ctx context.Context, text string) (string, error) {
	if p.fn != nil {
		p.fn(ctx, "the first words of the answer")
	}
	return "the first words of the answer", nil
}

// A13: partial answer text reaches the front desk as a turn step.
func TestPartialAnswerReachesTurnAPI(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c1", Text: "hi"}}
	l := newLoop(h, &partialTurner{})
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.turns {
		if turn.Phase == frontdesk.Step && strings.Contains(turn.Step, "first words") {
			return
		}
	}
	t.Fatalf("no partial step in %+v", s.turns)
}
