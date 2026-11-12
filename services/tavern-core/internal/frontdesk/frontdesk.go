// Package frontdesk is the reference Front Desk: the place where the owner
// talks to the tavern and the tavern reports back (docs/api/front-desk.md).
//
// Hirelings (with their bearer token):
//
//	POST /api/turn                        start | step | end of a turn
//	GET  /api/commands/{agent}[?wait=25]  pending commands, oldest first (long poll)
//	POST /api/commands/{agent}/ack        received | done | rejected
//	GET  /api/interactions                recent hireling-to-hireling conversations
//	POST /api/interaction                 open one
//	POST /api/interaction/{id}/reply      close one
//	POST /api/decisions                   open a "Waiting on you" card
//
// The owner (with the owner token):
//
//	GET  /api/status                      every hireling's last turn, open decisions, conversations, and the quest (null when unset)
//	GET  /api/turn/stream[?agent=]        follows the turns as hirelings report them (SSE)
//	POST /api/command                     {agent, text} — an order for a hireling
//	GET  /api/decisions                   open decisions
//	GET  /api/decisions/{id}              one decision, open or answered (chosen set)
//	POST /api/decisions/{id}/choose       {option} — the answer goes back as a command
//
// tavern-core mounts the owner's read-only task board and quest next to these
// (GET /api/tasks, /api/tasks/{id}, /api/quest), guarded by IsOwner.
//
// State lives in memory and, when a Persister is set, is written after every
// change (one JSON document; tavern-core runs as a single instance per tavern).
package frontdesk

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxWait caps the long poll of GET /api/commands/{agent}?wait=.
const MaxWait = 25 * time.Second

// StreamPing is the keepalive comment interval of GET /api/turn/stream.
const StreamPing = 15 * time.Second

// streamBuffer is how many turn events one stream may fall behind before the
// desk closes it (a turn never waits for a reader).
const streamBuffer = 32

// Turn is the body of POST /api/turn (same shape the runtime sends).
type Turn struct {
	Agent          string `json:"agent"`
	Phase          string `json:"phase"`
	With           string `json:"with,omitempty"`
	Topic          string `json:"topic,omitempty"`
	Step           string `json:"step,omitempty"`
	Result         string `json:"result,omitempty"`
	Screen         string `json:"screen,omitempty"`
	WaitingOnOwner string `json:"waitingOnOwner,omitempty"`
	Server         string `json:"server,omitempty"`
	Region         string `json:"region,omitempty"`
	At             string `json:"at"`
}

// Command is one order waiting for a hireling.
type Command struct {
	ID         string `json:"id"`
	Agent      string `json:"agent"`
	Text       string `json:"text"`
	DecisionID string `json:"decisionId,omitempty"`
	At         string `json:"at"`
	Received   bool   `json:"received,omitempty"`
	Result     string `json:"result,omitempty"`
	Note       string `json:"note,omitempty"`
	Answer     string `json:"answer,omitempty"`
}

// Interaction is a conversation between two hirelings, shown at the table.
type Interaction struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Text  string `json:"text"`
	Reply string `json:"reply,omitempty"`
	At    string `json:"at"`
}

// Decision is a "Waiting on you" card.
type Decision struct {
	ID       string   `json:"id"`
	From     string   `json:"from"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Context  string   `json:"context,omitempty"`
	Urgency  string   `json:"urgency,omitempty"`
	At       string   `json:"at"`
	Chosen   string   `json:"chosen,omitempty"`
	// Card is what a yes grants, written by tavern-core when a gate card is
	// bound to the decision (D1); never taken from the hireling.
	Card string `json:"card,omitempty"`
	// CardYes is the option that grants the card (the first plain yes).
	CardYes string `json:"card_yes,omitempty"`
}

// State is everything the desk remembers.
type State struct {
	Turns        map[string]Turn `json:"turns"`
	Commands     []Command       `json:"commands"`
	Interactions []Interaction   `json:"interactions"`
	Decisions    []Decision      `json:"decisions"`
}

// Persister saves and loads the desk state (Redis in production, nil in tests).
type Persister interface {
	Load(ctx context.Context) ([]byte, error)
	Save(ctx context.Context, data []byte) error
}

// Desk is the Front Desk.
type Desk struct {
	// Agents lists the hireling ids the desk accepts.
	Agents []string
	// AgentTokens maps a hireling id to its own token; SharedToken, when set,
	// is accepted for any hireling (one box, hirelings trust each other).
	AgentTokens map[string]string
	SharedToken string
	// OwnerToken guards the owner's endpoints. Empty = owner endpoints off.
	OwnerToken string
	Persist    Persister
	Now        func() time.Time
	// Quest, when set, is included in GET /api/status under "quest".
	// Return nil when no quest is set. The owner panel reads this field
	// (HTTP 200) so a missing quest is an empty state. GET /api/quest stays
	// 404 until a quest exists, for API clients.
	Quest func(context.Context) any

	mu      sync.Mutex
	st      State
	waiters map[string][]chan struct{}
	streams map[*turnStream]struct{}
}

// turnStream is one open GET /api/turn/stream.
type turnStream struct {
	agent string // "" = every hireling
	ch    chan Turn
	err   string // why the desk closed it; valid once ch is closed
}

// Load restores the state from the Persister (call once at start).
func (d *Desk) Load(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st = State{Turns: map[string]Turn{}}
	if d.Persist == nil {
		return nil
	}
	data, err := d.Persist.Load(ctx)
	if err != nil || len(data) == 0 {
		return err
	}
	if err := json.Unmarshal(data, &d.st); err != nil {
		return fmt.Errorf("frontdesk: stored state: %w", err)
	}
	if d.st.Turns == nil {
		d.st.Turns = map[string]Turn{}
	}
	return nil
}

func (d *Desk) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Desk) stamp() string { return d.now().UTC().Format(time.RFC3339Nano) }

func newID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// save must be called with d.mu held.
func (d *Desk) save(ctx context.Context) {
	if d.st.Turns == nil {
		d.st.Turns = map[string]Turn{}
	}
	// Keep the document small: the last 200 finished commands, 200 interactions, 200 decisions.
	d.st.Commands = trimDone(d.st.Commands, 200)
	if n := len(d.st.Interactions); n > 200 {
		d.st.Interactions = d.st.Interactions[n-200:]
	}
	if n := len(d.st.Decisions); n > 200 {
		d.st.Decisions = d.st.Decisions[n-200:]
	}
	if d.Persist == nil {
		return
	}
	if data, err := json.Marshal(d.st); err == nil {
		_ = d.Persist.Save(ctx, data)
	}
}

func trimDone(cs []Command, keep int) []Command {
	done := 0
	for _, c := range cs {
		if c.Result != "" {
			done++
		}
	}
	if done <= keep {
		return cs
	}
	drop := done - keep
	out := cs[:0]
	for _, c := range cs {
		if c.Result != "" && drop > 0 {
			drop--
			continue
		}
		out = append(out, c)
	}
	return out
}

func (d *Desk) known(agent string) bool {
	for _, a := range d.Agents {
		if a == agent {
			return true
		}
	}
	return false
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func same(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// hireling reports whether the request carries a valid token for agent.
func (d *Desk) hireling(r *http.Request, agent string) bool {
	if !d.known(agent) {
		return false
	}
	t := bearer(r)
	return same(t, d.AgentTokens[agent]) || same(t, d.SharedToken)
}

// anyHireling accepts any hireling token (for endpoints without an agent in the path).
func (d *Desk) anyHireling(r *http.Request) bool {
	t := bearer(r)
	if same(t, d.SharedToken) {
		return true
	}
	for _, a := range d.Agents {
		if same(t, d.AgentTokens[a]) {
			return true
		}
	}
	return false
}

func (d *Desk) owner(r *http.Request) bool {
	return same(bearer(r), d.OwnerToken) && !d.ownerShared()
}

// MinOwnerToken is the shortest owner token tavern-core accepts
// (scripts/init-env.sh writes 48 hex characters).
const MinOwnerToken = 32

// ownerShared reports whether the owner token is also a hireling's token
// (the shared one or any hireling's own): then every such hireling would be
// the owner, so the owner's endpoints stay closed.
func (d *Desk) ownerShared() bool {
	if same(d.OwnerToken, d.SharedToken) {
		return true
	}
	for _, t := range d.AgentTokens {
		if same(d.OwnerToken, t) {
			return true
		}
	}
	return false
}

// Check validates the tokens at start. An owner token that is also a
// hireling's token would let that hireling order the party and answer its
// own decisions; a short one can be guessed. The errors never carry a token.
func (d *Desk) Check() error {
	if d.OwnerToken == "" {
		return nil
	}
	if n := len(d.OwnerToken); n < MinOwnerToken {
		return fmt.Errorf("FRONTDESK_OWNER_TOKEN has %d characters, at least %d are required (./scripts/init-env.sh writes a random one)", n, MinOwnerToken)
	}
	if same(d.OwnerToken, d.SharedToken) {
		return errors.New("FRONTDESK_OWNER_TOKEN is the same as FRONTDESK_AGENT_TOKEN: every hireling would be the owner; give the owner its own token")
	}
	ids := make([]string, 0, len(d.AgentTokens))
	for id := range d.AgentTokens {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if same(d.OwnerToken, d.AgentTokens[id]) {
			return fmt.Errorf("FRONTDESK_OWNER_TOKEN is the same as hireling %s's own token: that hireling would be the owner; give the owner its own token", id)
		}
	}
	return nil
}

// IsOwner reports whether r carries the owner token (constant-time compare;
// false when OwnerToken is empty). tavern-core uses it to guard the owner's
// routes it serves outside this package.
func (d *Desk) IsOwner(r *http.Request) bool { return d.owner(r) }

// Register adds the desk's routes to mux.
func (d *Desk) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/turn", d.turn)
	mux.HandleFunc("GET /api/commands/{agent}", d.commands)
	mux.HandleFunc("POST /api/commands/{agent}/ack", d.ack)
	mux.HandleFunc("GET /api/interactions", d.interactions)
	mux.HandleFunc("POST /api/interaction", d.newInteraction)
	mux.HandleFunc("POST /api/interaction/{id}/reply", d.reply)
	mux.HandleFunc("POST /api/decisions", d.newDecision)
	mux.HandleFunc("GET /api/decisions", d.decisions)
	mux.HandleFunc("GET /api/decisions/{id}", d.decision)
	mux.HandleFunc("POST /api/decisions/{id}/choose", d.choose)
	mux.HandleFunc("POST /api/command", d.order)
	mux.HandleFunc("GET /api/status", d.status)
	mux.HandleFunc("GET /api/turn/stream", d.turnStream)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	return dec.Decode(v)
}

func cut(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return s
}

func (d *Desk) turn(w http.ResponseWriter, r *http.Request) {
	var t Turn
	if err := decode(r, &t); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	if !d.hireling(r, t.Agent) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch t.Phase {
	case "start", "step", "end":
	default:
		errJSON(w, http.StatusBadRequest, "phase must be start, step or end")
		return
	}
	t.Topic, t.Step, t.Result, t.WaitingOnOwner = cut(t.Topic, 200), cut(t.Step, 200), cut(t.Result, 500), cut(t.WaitingOnOwner, 300)
	t.At = d.stamp()
	d.mu.Lock()
	prev := d.st.Turns[t.Agent]
	if t.Phase == "step" { // a step keeps the topic of its turn
		t.Topic, t.With = prev.Topic, prev.With
	}
	d.st.Turns[t.Agent] = t
	d.save(r.Context())
	d.publish(t)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pending returns the commands not yet received by agent (d.mu held).
func (d *Desk) pending(agent string) []Command {
	out := []Command{}
	for _, c := range d.st.Commands {
		if c.Agent == agent && !c.Received && c.Result == "" {
			out = append(out, c)
		}
	}
	return out
}

func (d *Desk) commands(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !d.hireling(r, agent) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	wait := time.Duration(0)
	if s := r.URL.Query().Get("wait"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			wait = min(time.Duration(n)*time.Second, MaxWait)
		}
	}
	d.mu.Lock()
	cmds := d.pending(agent)
	if len(cmds) > 0 || wait == 0 {
		d.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"commands": cmds, "waited": wait > 0})
		return
	}
	ch := make(chan struct{}, 1)
	if d.waiters == nil {
		d.waiters = map[string][]chan struct{}{}
	}
	d.waiters[agent] = append(d.waiters[agent], ch)
	d.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ch:
	case <-timer.C:
	case <-r.Context().Done():
	}
	d.mu.Lock()
	cmds = d.pending(agent)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"commands": cmds, "waited": true})
}

// wake must be called with d.mu held.
func (d *Desk) wake(agent string) {
	for _, ch := range d.waiters[agent] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	delete(d.waiters, agent)
}

func (d *Desk) ack(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if !d.hireling(r, agent) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		ID, Result, Note, Answer string
	}
	if err := decode(r, &body); err != nil || body.ID == "" {
		errJSON(w, http.StatusBadRequest, "id required")
		return
	}
	switch body.Result {
	case "", "done", "rejected":
	default:
		errJSON(w, http.StatusBadRequest, "result must be done or rejected")
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.st.Commands {
		c := &d.st.Commands[i]
		if c.ID != body.ID || c.Agent != agent {
			continue
		}
		c.Received = true
		if body.Result != "" {
			c.Result, c.Note, c.Answer = body.Result, cut(body.Note, 200), cut(body.Answer, 4000)
		}
		d.save(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	errJSON(w, http.StatusNotFound, "no such command")
}

func (d *Desk) interactions(w http.ResponseWriter, r *http.Request) {
	if !d.anyHireling(r) && !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	d.mu.Lock()
	out := append([]Interaction{}, d.st.Interactions...)
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"interactions": out})
}

func (d *Desk) newInteraction(w http.ResponseWriter, r *http.Request) {
	var body struct{ From, To, Text string }
	if err := decode(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	if !d.hireling(r, body.From) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !d.known(body.To) || body.To == body.From {
		errJSON(w, http.StatusBadRequest, "unknown recipient")
		return
	}
	it := Interaction{ID: newID("i-"), From: body.From, To: body.To, Text: cut(body.Text, 300), At: d.stamp()}
	d.mu.Lock()
	d.st.Interactions = append(d.st.Interactions, it)
	d.save(r.Context())
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"interaction": it})
}

func (d *Desk) reply(w http.ResponseWriter, r *http.Request) {
	var body struct{ Agent, Reply string }
	if err := decode(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	if !d.hireling(r, body.Agent) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.st.Interactions {
		it := &d.st.Interactions[i]
		if it.ID != id {
			continue
		}
		if it.To != body.Agent {
			errJSON(w, http.StatusForbidden, "only the recipient replies")
			return
		}
		if it.Reply != "" {
			errJSON(w, http.StatusConflict, "already answered")
			return
		}
		it.Reply = cut(body.Reply, 300)
		d.save(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"interaction": *it})
		return
	}
	errJSON(w, http.StatusNotFound, "no such interaction")
}

func (d *Desk) newDecision(w http.ResponseWriter, r *http.Request) {
	var body Decision
	if err := decode(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	if !d.hireling(r, body.From) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if strings.TrimSpace(body.Question) == "" || len(body.Options) < 2 || len(body.Options) > 6 {
		errJSON(w, http.StatusBadRequest, "a question and 2–6 options are required")
		return
	}
	for i, o := range body.Options {
		body.Options[i] = cut(strings.TrimSpace(o), 80)
	}
	if body.Urgency == "" {
		body.Urgency = "normal"
	}
	body.ID, body.At, body.Chosen, body.Card, body.CardYes = newID("d-"), d.stamp(), "", "", ""
	body.Question, body.Context = cut(body.Question, 300), cut(body.Context, 1000)
	d.mu.Lock()
	d.st.Decisions = append(d.st.Decisions, body)
	d.save(r.Context())
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"decision": body})
}

func (d *Desk) openDecisions() []Decision {
	out := []Decision{}
	for _, x := range d.st.Decisions {
		if x.Chosen == "" {
			out = append(out, x)
		}
	}
	return out
}

func (d *Desk) decisions(w http.ResponseWriter, r *http.Request) {
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	d.mu.Lock()
	out := d.openDecisions()
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"decisions": out})
}

// decision is GET /api/decisions/{id}: one decision, open or already answered
// (the desk keeps the last 200; an older one is 404).
func (d *Desk) decision(w http.ResponseWriter, r *http.Request) {
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := r.PathValue("id")
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.st.Decisions {
		if x.ID == id {
			writeJSON(w, http.StatusOK, map[string]any{"decision": x})
			return
		}
	}
	errJSON(w, http.StatusNotFound, "no such decision")
}

// ChoiceText is the command text a hireling receives when the owner answers;
// the runtime parses the option after "chosen:".
func ChoiceText(question, option, id string) string {
	return fmt.Sprintf("Decision: %s → chosen: %s (decision %s)", question, option, id)
}

func (d *Desk) choose(w http.ResponseWriter, r *http.Request) {
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct{ Option string }
	if err := decode(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	id := r.PathValue("id")
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.st.Decisions {
		x := &d.st.Decisions[i]
		if x.ID != id {
			continue
		}
		if x.Chosen != "" {
			errJSON(w, http.StatusConflict, "already decided")
			return
		}
		ok := false
		for _, o := range x.Options {
			if strings.EqualFold(o, strings.TrimSpace(body.Option)) {
				x.Chosen, ok = o, true
			}
		}
		if !ok {
			errJSON(w, http.StatusBadRequest, "option not offered")
			return
		}
		c := Command{ID: newID("c-"), Agent: x.From, Text: ChoiceText(x.Question, x.Chosen, x.ID), DecisionID: x.ID, At: d.stamp()}
		d.st.Commands = append(d.st.Commands, c)
		d.wake(x.From)
		d.save(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"decision": *x, "command": c})
		return
	}
	errJSON(w, http.StatusNotFound, "no such decision")
}

func (d *Desk) order(w http.ResponseWriter, r *http.Request) {
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct{ Agent, Text string }
	if err := decode(r, &body); err != nil {
		errJSON(w, http.StatusBadRequest, "bad json")
		return
	}
	if !d.known(body.Agent) || strings.TrimSpace(body.Text) == "" {
		errJSON(w, http.StatusBadRequest, "a known agent and a text are required")
		return
	}
	c := Command{ID: newID("c-"), Agent: body.Agent, Text: cut(strings.TrimSpace(body.Text), 4000), At: d.stamp()}
	d.mu.Lock()
	d.st.Commands = append(d.st.Commands, c)
	d.wake(body.Agent)
	d.save(r.Context())
	d.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"command": c})
}

// AgentStatus is one row of GET /api/status.
type AgentStatus struct {
	ID       string   `json:"id"`
	Turn     *Turn    `json:"turn,omitempty"`
	Pending  int      `json:"pending"`
	LastDone *Command `json:"lastDone,omitempty"`
}

// Status is the body of GET /api/status and the first event of
// GET /api/turn/stream.
type Status struct {
	Agents       []AgentStatus `json:"agents"`
	Decisions    []Decision    `json:"decisions"`
	Interactions []Interaction `json:"interactions"`
	Quest        any           `json:"quest"`
	At           string        `json:"at"`
}

// snapshot assembles the desk's current view. The quest callback runs outside
// the lock: it reads its own store, not the desk.
func (d *Desk) snapshot(ctx context.Context) Status {
	var quest any
	if d.Quest != nil {
		quest = d.Quest(ctx)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows := make([]AgentStatus, 0, len(d.Agents))
	ids := append([]string{}, d.Agents...)
	sort.Strings(ids)
	for _, a := range ids {
		row := AgentStatus{ID: a, Pending: len(d.pending(a))}
		if t, ok := d.st.Turns[a]; ok {
			t := t
			row.Turn = &t
		}
		for i := len(d.st.Commands) - 1; i >= 0; i-- {
			if c := d.st.Commands[i]; c.Agent == a && c.Result != "" {
				row.LastDone = &c
				break
			}
		}
		rows = append(rows, row)
	}
	return Status{
		Agents:       rows,
		Decisions:    d.openDecisions(),
		Interactions: append([]Interaction{}, d.st.Interactions...),
		Quest:        quest,
		At:           d.stamp(),
	}
}

func (d *Desk) status(w http.ResponseWriter, r *http.Request) {
	// The owner panel polls this. A cached 200 would keep a stale party or a
	// quest that has since been set.
	w.Header().Set("Cache-Control", "no-store")
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, d.snapshot(r.Context()))
}

// subscribe registers one turn stream (mu not held).
func (d *Desk) subscribe(agent string) *turnStream {
	s := &turnStream{agent: agent, ch: make(chan Turn, streamBuffer)}
	d.mu.Lock()
	if d.streams == nil {
		d.streams = map[*turnStream]struct{}{}
	}
	d.streams[s] = struct{}{}
	d.mu.Unlock()
	return s
}

func (d *Desk) unsubscribe(s *turnStream) {
	d.mu.Lock()
	delete(d.streams, s)
	d.mu.Unlock()
}

// publish hands a reported turn to every open stream. It never blocks a
// hireling: a reader that cannot keep up is closed with an error and dropped.
// d.mu must be held.
func (d *Desk) publish(t Turn) {
	for s := range d.streams {
		if s.agent != "" && s.agent != t.Agent {
			continue
		}
		select {
		case s.ch <- t:
		default:
			s.err = "stream too slow"
			close(s.ch)
			delete(d.streams, s)
		}
	}
}

// Close ends every open turn stream with an error event instead of a silent
// EOF; the rest of the desk keeps serving. tavern-core calls it on shutdown.
func (d *Desk) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for s := range d.streams {
		s.err = "desk closed"
		close(s.ch)
		delete(d.streams, s)
	}
}

// sse writes one Server-Sent Event and flushes it. The flush goes through the
// ResponseController: ServeMux wraps the ResponseWriter (Go 1.22+), so a
// plain http.Flusher assertion would fail on the real server.
func sse(w io.Writer, rc *http.ResponseController, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
		return err
	}
	return rc.Flush()
}

// canFlush reports whether w (or the writer it wraps) flushes, without
// writing anything: a middleware may hide http.Flusher while exposing Unwrap.
func canFlush(w http.ResponseWriter) bool {
	for {
		if _, ok := w.(http.Flusher); ok {
			return true
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return false
		}
		w = u.Unwrap()
	}
}

// turnStream is GET /api/turn/stream: Server-Sent Events of the turns as the
// hirelings report them (A13). The first event is the same snapshot as
// GET /api/status, so a client connects once and stops polling; every
// POST /api/turn follows as a "turn" event, which is how the first words of an
// answer reach the owner before the turn ends. Errors are explicit: a status
// before the first byte, or an "error" event and the end of the stream.
func (d *Desk) turnStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !d.owner(r) {
		errJSON(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	agent := r.URL.Query().Get("agent")
	if agent != "" && !d.known(agent) {
		errJSON(w, http.StatusBadRequest, "unknown agent")
		return
	}
	// A writer that cannot flush gets an explicit status before the first
	// byte instead of a stream that would buffer to the end.
	if !canFlush(w) {
		errJSON(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	s := d.subscribe(agent)
	defer d.unsubscribe(s)

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := sse(w, rc, "status", d.snapshot(r.Context())); err != nil {
		return
	}
	ping := time.NewTicker(StreamPing)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		case t, open := <-s.ch:
			if !open {
				d.mu.Lock()
				reason := s.err
				d.mu.Unlock()
				_ = sse(w, rc, "error", map[string]string{"error": reason})
				return
			}
			if err := sse(w, rc, "turn", t); err != nil {
				return
			}
		}
	}
}
