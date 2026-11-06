package loop

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/hermes"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
	"github.com/Play-Hireling/hireling/runtime/internal/usage"
)

type fakeTasks struct {
	created  []tavern.CreateTask
	tasks    map[string]*tavern.Task
	decided  []string
	n        int
	approver string // requests from anyone else are blocked (tavern-core's rule)
}

func (f *fakeTasks) NewTask(_ context.Context, in tavern.CreateTask) (*tavern.Task, error) {
	f.n++
	f.created = append(f.created, in)
	t := &tavern.Task{ID: fmt.Sprintf("T-%d", f.n), Title: in.Title, Owner: in.Owner, RequestedBy: in.RequestedBy, Priority: in.Priority, Status: "queued", Parent: in.Parent, Brief: in.Brief, Attachments: in.Attachments}
	if in.Owner != "" && in.Owner != in.RequestedBy && f.approver != "" && in.RequestedBy != f.approver {
		t.Status, t.NeedsApproval = "blocked", true
	}
	if f.tasks == nil {
		f.tasks = map[string]*tavern.Task{}
	}
	f.tasks[t.ID] = t
	return t, nil
}

func (f *fakeTasks) Decide(_ context.Context, id string, approve bool, note string) (*tavern.Task, error) {
	t, ok := f.tasks[id]
	if !ok || !t.NeedsApproval {
		return nil, fmt.Errorf("task %s is not waiting for approval", id)
	}
	t.NeedsApproval, t.DecisionNote = false, note
	if approve {
		t.Status = "queued"
	} else {
		t.Status = "cancelled"
	}
	f.decided = append(f.decided, id)
	return t, nil
}

const innkeeperAnswer = "Two pieces for Monday: hooks first, then the carousel.\n\n```plan\n{\"summary\": \"Monday launch post\", \"tasks\": [\n {\"title\": \"3 hooks for the launch post\", \"owner\": \"scriptwriter\", \"priority\": \"P1\", \"goal\": {\"kind\": \"leads\", \"expected\": 10}, \"brief\": \"Audience: SMB owners in Brazil. Tone: direct.\"},\n {\"title\": \"Carousel from the best hook\", \"owner\": \"social-media\", \"goal\": {\"kind\": \"posts\", \"expected\": 1, \"note\": \"the Monday post itself\"}, \"depends_on\": [0]}\n]}\n```"

func TestPlannerCreatesTasksAndTellsOwners(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c9", Text: "Prepare Monday's launch post"}}
	tr := &fakeTurner{out: innkeeperAnswer}
	l := newLoop(h, tr)
	l.Profile.Planner = true
	l.Party = []string{"innkeeper", "scriptwriter", "social-media", "dev", "test"}
	ft := &fakeTasks{}
	l.Tasks = ft
	l.Bus = newBar(t, "test")
	l.SharedDir = t.TempDir()
	l.TurnsDir = t.TempDir()
	if err := l.Once(ctx); err != nil {
		t.Fatal(err)
	}
	// parent + 2 children, with owner, priority, goal and parent
	if len(ft.created) != 3 {
		t.Fatalf("tasks created = %+v", ft.created)
	}
	parent, a, b := ft.created[0], ft.created[1], ft.created[2]
	if parent.Title != "Monday launch post" || parent.RequestedBy != "owner" || parent.Owner != "test" || parent.Priority != "P1" || parent.GoalContribution.Kind != "support" {
		t.Fatalf("parent = %+v", parent)
	}
	if a.Owner != "scriptwriter" || a.Priority != "P1" || a.GoalContribution.Expected != 10 || a.Parent != "T-1" || len(a.Attachments) != 1 {
		t.Fatalf("child a = %+v", a)
	}
	if b.Owner != "social-media" || b.Priority != "P2" || b.Parent != "T-1" {
		t.Fatalf("child b = %+v", b)
	}
	// briefs on the common table
	raw, err := os.ReadFile(a.Attachments[0])
	if err != nil || !strings.Contains(string(raw), "Audience: SMB owners") || !strings.Contains(string(raw), "Parent: T-1") {
		t.Fatalf("brief: %v %q", err, raw)
	}
	if !strings.HasPrefix(a.Attachments[0], filepath.Join(l.SharedDir, "T-1")) {
		t.Fatalf("brief path = %s", a.Attachments[0])
	}
	// each owner got a request on the bar with the task id and the brief path
	for owner, id := range map[string]string{"scriptwriter": "T-2", "social-media": "T-3"} {
		res, _ := l.Bus.C.XRange(ctx, bar.Inbox(owner), "-", "+").Result()
		if len(res) != 1 || res[0].Values["task"] != id || res[0].Values["type"] != "request" || !strings.Contains(res[0].Values["text"].(string), "New task "+id) || !strings.Contains(res[0].Values["text"].(string), "Moves the quest: ") {
			t.Fatalf("%s inbox = %v", owner, res)
		}
	}
	// the answer to the owner lists the created tasks instead of the JSON block
	last := s.acks[len(s.acks)-1]
	if strings.Contains(last["answer"], "```") || !strings.Contains(last["answer"], "Plan T-1:") || !strings.Contains(last["answer"], "- T-2 3 hooks") {
		t.Fatalf("answer = %q", last["answer"])
	}
	// the model's prompt carried the plan format only through the Runner prompt (main); here we only check the party was enforced
	ev, _ := l.Bus.C.XRange(ctx, bar.Events, "-", "+").Result()
	if len(ev) == 0 || ev[len(ev)-1].Values["kind"] != "plan.created" {
		t.Fatalf("events = %v", ev)
	}
}

func TestPlannerRejectsUnknownOwner(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c10", Text: "x"}}
	l := newLoop(h, &fakeTurner{out: "ok\n\n```plan\n{\"tasks\": [{\"title\": \"y\", \"owner\": \"ghost\"}]}\n```"})
	l.Profile.Planner = true
	l.Party = []string{"test", "dev"}
	ft := &fakeTasks{}
	l.Tasks = ft
	l.TurnsDir = t.TempDir()
	_ = l.Once(context.Background())
	if len(ft.created) != 0 {
		t.Fatal("no task may be created for an unknown owner")
	}
	last := s.acks[len(s.acks)-1]
	if !strings.Contains(last["answer"], "plan not applied") || !strings.Contains(last["answer"], "ghost") {
		t.Fatalf("answer = %q", last["answer"])
	}
}

func TestNonPlannerIgnoresPlanBlocks(t *testing.T) {
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c11", Text: "x"}}
	l := newLoop(h, &fakeTurner{out: innkeeperAnswer})
	ft := &fakeTasks{}
	l.Tasks = ft
	l.TurnsDir = t.TempDir()
	_ = l.Once(context.Background())
	if len(ft.created) != 0 {
		t.Fatal("only a planner acts on plan blocks")
	}
	_ = time.Second
	_ = s
}

// B6: scriptwriter asks social-media for work → task blocked, handoff to the
// Innkeeper; the Innkeeper approves → owner gets the request, requester a note.
func TestHandoffRequestAndApproval(t *testing.T) {
	ctx := context.Background()
	_, h := newServer(t)
	bus := newBar(t, "scriptwriter")
	ft := &fakeTasks{approver: "innkeeper"}
	party := []string{"innkeeper", "scriptwriter", "social-media", "test"}

	// requester
	req := newLoop(h, &fakeTurner{out: "Copy is drafted at /tavern/shared/T-2/copy.md.\n\n```request\n{\"to\": \"social-media\", \"title\": \"Carousel for the launch post\", \"brief\": \"Use the draft on the table\", \"goal\": {\"kind\": \"posts\", \"expected\": 1}}\n```"})
	req.Profile.ID = "scriptwriter"
	req.Party, req.Tasks, req.Bus, req.Approver = party, ft, bus, "innkeeper"
	req.TurnsDir = t.TempDir()
	_ = bus.EnsureGroup(ctx)
	sender := &bar.Bus{C: bus.C, Agent: "innkeeper"}
	_, _ = sender.Send(ctx, bar.Message{To: "scriptwriter", Type: "request", Task: "T-2", Text: "Write the copy"})
	msgs, _ := bus.Next(ctx, 100*time.Millisecond)
	req.HandleMessage(ctx, msgs[0])

	if len(ft.created) != 1 || ft.created[0].Owner != "social-media" || ft.created[0].RequestedBy != "scriptwriter" || ft.created[0].Parent != "T-2" || ft.created[0].Priority != "P2" {
		t.Fatalf("created = %+v", ft.created)
	}
	// The owner was NOT told yet; the Innkeeper got the handoff
	if n, _ := bus.C.XLen(ctx, bar.Inbox("social-media")).Result(); n != 0 {
		t.Fatal("owner must not be told before approval")
	}
	ink, _ := bus.C.XRange(ctx, bar.Inbox("innkeeper"), "-", "+").Result()
	var handoffs, replies int
	for _, e := range ink {
		switch e.Values["type"] {
		case "handoff":
			handoffs++
			if e.Values["task"] != "T-1" {
				t.Fatalf("handoff = %v", e.Values)
			}
		case "reply":
			replies++
		}
	}
	if handoffs != 1 || replies != 1 {
		t.Fatalf("innkeeper inbox = %v", ink)
	}

	// approver handles the handoff and approves
	ap := newLoop(h, &fakeTurner{out: "Fits Monday's plan.\n\n```decision\n{\"task\": \"T-1\", \"approve\": true, \"note\": \"go, keep the tone\"}\n```"})
	ap.Profile.ID, ap.Profile.Planner = "innkeeper", true
	ap.Party, ap.Tasks, ap.Approver = party, ft, "innkeeper"
	ap.Bus = &bar.Bus{C: bus.C, Agent: "innkeeper"}
	ap.TurnsDir = t.TempDir()
	_ = ap.Bus.EnsureGroup(ctx)
	hm, _ := ap.Bus.Next(ctx, 100*time.Millisecond)
	var handoff bar.Message
	for _, m := range hm {
		if m.Type == "handoff" {
			handoff = m
		}
	}
	ap.HandleMessage(ctx, handoff)

	if len(ft.decided) != 1 || ft.tasks["T-1"].Status != "queued" {
		t.Fatalf("decided = %v task = %+v", ft.decided, ft.tasks["T-1"])
	}
	own, _ := bus.C.XRange(ctx, bar.Inbox("social-media"), "-", "+").Result()
	if len(own) != 1 || own[0].Values["task"] != "T-1" || own[0].Values["type"] != "request" {
		t.Fatalf("owner inbox = %v", own)
	}
	notes, _ := bus.C.XRange(ctx, bar.Inbox("scriptwriter"), "-", "+").Result()
	found := false
	for _, e := range notes {
		if e.Values["type"] == "note" && strings.Contains(e.Values["text"].(string), "approved by innkeeper: go, keep the tone") {
			found = true
		}
	}
	if !found {
		t.Fatalf("requester note missing: %v", notes)
	}
	ev, _ := bus.C.XRange(ctx, bar.Events, "-", "+").Result()
	var kinds []string
	for _, e := range ev {
		kinds = append(kinds, e.Values["kind"].(string))
	}
	if !strings.Contains(strings.Join(kinds, ","), "handoff.requested") || !strings.Contains(strings.Join(kinds, ","), "handoff.approved") {
		t.Fatalf("events = %v", kinds)
	}
}

func TestHandoffDenied(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	bus := newBar(t, "innkeeper")
	ft := &fakeTasks{approver: "innkeeper"}
	// a pending request already exists
	pending, _ := ft.NewTask(ctx, tavern.CreateTask{Title: "ads", RequestedBy: "dev", Owner: "paid-traffic"})
	s.queue = []frontdesk.Command{{ID: "c12", Text: "Decide the pending handoff T-1"}}
	ap := newLoop(h, &fakeTurner{out: "Not this week.\n\n```decision\n{\"task\": \"" + pending.ID + "\", \"approve\": false, \"note\": \"budget frozen\"}\n```"})
	ap.Profile.ID, ap.Profile.Planner = "innkeeper", true
	ap.Party, ap.Tasks, ap.Bus = []string{"innkeeper", "dev", "paid-traffic"}, ft, bus
	ap.TurnsDir = t.TempDir()
	_ = ap.Once(ctx)
	if ft.tasks[pending.ID].Status != "cancelled" {
		t.Fatalf("task = %+v", ft.tasks[pending.ID])
	}
	if n, _ := bus.C.XLen(ctx, bar.Inbox("paid-traffic")).Result(); n != 0 {
		t.Fatal("denied: owner must not be told")
	}
	dev, _ := bus.C.XRange(ctx, bar.Inbox("dev"), "-", "+").Result()
	if len(dev) != 1 || !strings.Contains(dev[0].Values["text"].(string), "denied by innkeeper: budget frozen") {
		t.Fatalf("requester note = %v", dev)
	}
	last := s.acks[len(s.acks)-1]
	if !strings.Contains(last["answer"], "Denied "+pending.ID) {
		t.Fatalf("answer = %q", last["answer"])
	}
}

// TestPlanReadFromSessionStore: the `hermes chat -q` stdout is a terminal
// render that drops code fences (2026-11-04: "plan{…}" and no task).
// The loop takes the raw reply from Hermes' session store instead.
func TestPlanReadFromSessionStore(t *testing.T) {
	ctx := context.Background()
	s, h := newServer(t)
	s.queue = []frontdesk.Command{{ID: "c10", Text: "Prepare Monday's launch post"}}
	rendered := strings.NewReplacer("```plan\n", "plan", "\n```", "").Replace(innkeeperAnswer)
	l := newLoop(h, &fakeTurner{out: "☤ Hermes\n" + rendered})
	l.Extract = hermes.Answer
	l.Profile.Planner = true
	l.Party = []string{"innkeeper", "scriptwriter", "social-media", "dev", "test"}
	ft := &fakeTasks{}
	l.Tasks = ft
	l.SharedDir = t.TempDir()
	l.TurnsDir = t.TempDir()
	db := filepath.Join(t.TempDir(), "state.db")
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE sessions (id TEXT PRIMARY KEY, source TEXT NOT NULL, started_at REAL NOT NULL)`,
		`CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, role TEXT NOT NULL, content TEXT, tool_calls TEXT)`,
	} {
		if _, err := conn.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// An hour ahead: the session must start after the turn whatever the setup
	// costs (the loop reads answers from sessions started since the turn).
	if _, err := conn.Exec(`INSERT INTO sessions VALUES ('local-x','oneshot',?)`, float64(time.Now().Add(time.Hour).UnixNano())/1e9); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO messages (session_id, role, content) VALUES ('local-x','assistant',?)`, innkeeperAnswer); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	l.Usage = &usage.Store{DB: db}
	if err := l.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(ft.created) != 3 {
		t.Fatalf("tasks created = %+v", ft.created)
	}
	if last := s.acks[len(s.acks)-1]; !strings.Contains(last["answer"], "Plan T-1:") {
		t.Fatalf("answer = %q", last["answer"])
	}
}
