package loop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/redact"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
	"github.com/Play-Hireling/hireling/runtime/profile"
)

type fakeTurner struct {
	out   string
	err   error
	delay time.Duration
	seen  string
}

func (f *fakeTurner) Run(ctx context.Context, text string) (string, error) {
	f.seen = text
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
	}
	return f.out, f.err
}

type server struct {
	mu           sync.Mutex
	turns        []frontdesk.Turn
	acks         []map[string]string
	queue        []frontdesk.Command
	tokens       []string
	interactions []map[string]string // B8: {id, from, to, text, reply, at}
	decisions    []map[string]any    // D3: bodies of POST /api/decisions
	longPoll     bool                // A13: answer ?wait= with waited:true
	waits        int                 // A13: GETs that carried ?wait=
	sticky       bool                // A13: keep returning the queue (unacked commands)
}

func newServer(t *testing.T) (*server, *httptest.Server) {
	s := &server{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.tokens = append(s.tokens, r.Header.Get("Authorization"))
		switch {
		case r.URL.Path == "/api/turn":
			var tt frontdesk.Turn
			_ = json.NewDecoder(r.Body).Decode(&tt)
			s.turns = append(s.turns, tt)
			w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/interactions":
			items := []map[string]any{}
			for _, it := range s.interactions {
				items = append(items, map[string]any{"id": it["id"], "from": it["from"], "to": it["to"], "text": it["text"], "reply": it["reply"], "at": it["at"]})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"interactions": items})
		case r.URL.Path == "/api/decisions":
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			s.decisions = append(s.decisions, b)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "decision": map[string]string{"id": "d-" + strconvItoa(len(s.decisions))}})
		case r.URL.Path == "/api/interaction":
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			it := map[string]string{"id": "i-" + strconvItoa(len(s.interactions)+1), "from": b["from"], "to": b["to"], "text": b["text"], "reply": "", "at": time.Now().UTC().Format(time.RFC3339Nano)}
			s.interactions = append(s.interactions, it)
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "interaction": map[string]string{"id": it["id"]}})
		case strings.HasPrefix(r.URL.Path, "/api/interaction/") && strings.HasSuffix(r.URL.Path, "/reply"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/interaction/"), "/reply")
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			for _, it := range s.interactions {
				if it["id"] == id {
					it["reply"] = b["reply"]
				}
			}
			w.Write([]byte(`{"ok":true}`))
		case strings.HasSuffix(r.URL.Path, "/ack"):
			var a map[string]string
			_ = json.NewDecoder(r.Body).Decode(&a)
			s.acks = append(s.acks, a)
			w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/api/commands/"):
			if !strings.HasPrefix(r.URL.Path, "/api/commands/tester") {
				w.WriteHeader(401)
				return
			}
			q := s.queue
			if !s.sticky {
				s.queue = nil
			}
			out := map[string]any{"commands": q}
			if r.URL.Query().Get("wait") != "" {
				s.waits++
				if s.longPoll {
					out["waited"] = true
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(h.Close)
	return s, h
}

func newLoop(s *httptest.Server, tr Turner) *Loop {
	p := &profile.Profile{ID: "test", DeskID: "tester", Model: "deepseek-v4-flash"}
	p.Runtime.PollSeconds, p.Runtime.TurnTTLMinutes, p.Runtime.Screen = 1, 15, "code"
	return &Loop{
		Profile:   p,
		Client:    &frontdesk.Client{BaseURL: s.URL, Token: "tok-123456"},
		Turner:    tr,
		Redactor:  redact.New("tok-123456"),
		Server:    "box-1",
		Region:    "CL",
		Keepalive: 30 * time.Millisecond,
	}
}

func TestExtractAndTranscript(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c3", Text: "Hi"}}
	l := newLoop(h, &fakeTurner{out: "Query: Hi\n\nthinking tok-123456\n────────\nFinal answer"})
	l.Extract = func(tr string) string { return tr[strings.LastIndex(tr, "\n")+1:] }
	l.TurnsDir = t.TempDir()
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := s.acks[len(s.acks)-1]
	if last["answer"] != "Final answer" || last["note"] != "Final answer" {
		t.Fatalf("ack = %v", last)
	}
	files, _ := os.ReadDir(l.TurnsDir)
	if len(files) != 1 || !strings.Contains(files[0].Name(), "c3") {
		t.Fatalf("transcript not saved: %v", files)
	}
	raw, _ := os.ReadFile(filepath.Join(l.TurnsDir, files[0].Name()))
	if !strings.Contains(string(raw), "thinking") || strings.Contains(string(raw), "tok-123456") {
		t.Fatalf("transcript = %q", raw)
	}
}

func TestHappyPath(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c1", Text: "Say hi", Instructions: "Keep it short"}}
	tr := &fakeTurner{out: "Hi! My token is tok-123456", delay: 80 * time.Millisecond}
	l := newLoop(h, tr)
	l.Client.BaseURL = h.URL
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.seen, "Keep it short") {
		t.Fatalf("instructions not passed: %q", tr.seen)
	}
	phases := []string{}
	for _, tt := range s.turns {
		phases = append(phases, string(tt.Phase))
		if tt.Agent != "tester" || tt.Server != "box-1" {
			t.Fatalf("turn missing agent/server: %+v", tt)
		}
	}
	if phases[0] != "start" || phases[len(phases)-1] != "end" || !contains(phases, "step") {
		t.Fatalf("phases = %v", phases)
	}
	if s.turns[0].Topic != "Say hi" || s.turns[0].With != "owner" {
		t.Fatalf("start turn = %+v", s.turns[0])
	}
	if len(s.acks) != 2 || s.acks[0]["result"] != "" || s.acks[1]["result"] != "done" {
		t.Fatalf("acks = %v", s.acks)
	}
	if strings.Contains(s.acks[1]["answer"], "tok-123456") || strings.Contains(s.turns[len(s.turns)-1].Result, "tok-123456") {
		t.Fatal("secret leaked to the front desk")
	}
}

func TestFailureIsRejected(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c2", Text: "Break"}}
	l := newLoop(h, &fakeTurner{err: errors.New("hermes: exit status 1: boom")})
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := s.acks[len(s.acks)-1]
	if last["result"] != "rejected" || !strings.Contains(last["note"], "boom") {
		t.Fatalf("ack = %v", last)
	}
	if !strings.HasPrefix(s.turns[len(s.turns)-1].Result, "Failed") {
		t.Fatalf("end turn = %+v", s.turns[len(s.turns)-1])
	}
}

func TestUnconfiguredFrontDeskDoesNotCrash(t *testing.T) {
	l := newLoop(httptest.NewUnstartedServer(nil), &fakeTurner{})
	l.Client = &frontdesk.Client{}
	if err := l.Once(context.Background()); !errors.Is(err, frontdesk.ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnContext(t *testing.T) {
	_, h := newServer(t)
	l := newLoop(h, &fakeTurner{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.Run(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func strconvItoa(n int) string { return fmt.Sprintf("%d", n) }

// D3 — an `ask` block opens a decision card and the turn ends "waiting on you".
func TestAskOpensDecision(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c9", Text: "Publish the Monday post"}}
	answer := "The post is ready.\n\n```ask\n{\"question\": \"Publish the post on Instagram now?\", \"options\": [\"Yes, publish now\", \"Wait for Monday 9h\"], \"context\": \"Draft is at /tavern/shared/posts/monday.md\", \"urgency\": \"high\"}\n```\n"
	l := newLoop(h, &fakeTurner{out: answer})
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.decisions) != 1 {
		t.Fatalf("decisions = %v", s.decisions)
	}
	d := s.decisions[0]
	if d["from"] != "tester" || d["question"] != "Publish the post on Instagram now?" || d["urgency"] != "high" {
		t.Fatalf("decision body = %v", d)
	}
	if opts, _ := d["options"].([]any); len(opts) != 2 || opts[1] != "Wait for Monday 9h" {
		t.Fatalf("options = %v", d["options"])
	}
	end := s.turns[len(s.turns)-1]
	if end.Phase != frontdesk.End || end.WaitingOnOwner != "Publish the post on Instagram now?" {
		t.Fatalf("end turn = %+v", end)
	}
	last := s.acks[len(s.acks)-1]
	if strings.Contains(last["answer"], "```ask") || !strings.Contains(last["answer"], "Waiting on you: Publish the post on Instagram now?") || !strings.Contains(last["answer"], "decision d-1") {
		t.Fatalf("answer = %q", last["answer"])
	}
}

// D3 — a malformed block opens nothing and says why.
func TestAskInvalidOpensNothing(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c10", Text: "x"}}
	l := newLoop(h, &fakeTurner{out: "```ask\n{\"question\": \"Go?\", \"options\": [\"only one\"]}\n```"})
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.decisions) != 0 {
		t.Fatalf("decision opened from invalid block: %v", s.decisions)
	}
	if !strings.Contains(s.acks[len(s.acks)-1]["answer"], "decision not opened") {
		t.Fatalf("answer = %q", s.acks[len(s.acks)-1]["answer"])
	}
}

type fakeQuest struct {
	q   *tavern.Quest
	err error
}

func (f *fakeQuest) Quest(context.Context) (*tavern.Quest, error) { return f.q, f.err }

// C1 — the quest bar opens every turn; no quest, no line.
func TestQuestLinePrefixesTurn(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c11", Text: "Plan the week"}}
	tr := &fakeTurner{out: "ok"}
	l := newLoop(h, tr)
	l.Quest = &fakeQuest{q: &tavern.Quest{Bar: "Quest: Net revenue — 12.5% [█░░░░░░░░░] 15,000/120,000 USD", Kinds: []string{"leads", "revenue"}}}
	if err := l.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tr.seen, "Quest: Net revenue — 12.5%") || !strings.Contains(tr.seen, "contribution kinds: leads, revenue") || !strings.HasSuffix(tr.seen, "Plan the week") {
		t.Fatalf("prompt = %q", tr.seen)
	}
	s.queue = []frontdesk.Command{{ID: "c12", Text: "Again"}}
	l.Quest = &fakeQuest{err: tavern.ErrNoQuest}
	_ = l.Once(context.Background())
	if tr.seen != "Again" {
		t.Fatalf("prompt without quest = %q", tr.seen)
	}
}

type fakeRanker struct{ q []tavern.Task }

func (f *fakeRanker) Queue(context.Context) ([]tavern.Task, error) { return f.q, nil }

// B3 — a batch is handled in queue order; messages without a task count as P2.
func TestOrderByQueue(t *testing.T) {
	l := &Loop{Ranker: &fakeRanker{q: []tavern.Task{{ID: "T-9", Priority: "P0"}, {ID: "T-4", Priority: "P1"}, {ID: "T-2", Priority: "P2"}, {ID: "T-7", Priority: "P3"}}}}
	msgs := []bar.Message{{ID: "a", Task: "T-7"}, {ID: "b"}, {ID: "c", Task: "T-2"}, {ID: "d", Task: "T-9"}, {ID: "e", Task: "T-4"}, {ID: "f", Task: "T-99"}}
	var got []string
	for _, m := range l.order(context.Background(), msgs) {
		got = append(got, m.ID)
	}
	if strings.Join(got, "") != "decbfa" {
		t.Fatalf("order = %v, want d e c b f a", got)
	}
	l.Ranker = nil
	if out := l.order(context.Background(), msgs); out[0].ID != "a" {
		t.Fatal("without tavern-core the arrival order stands")
	}
}
