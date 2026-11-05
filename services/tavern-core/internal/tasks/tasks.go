// Package tasks is the task queue with owners and leases (docs/architecture.md,
// "Task"). This ships create / lease / renew / handoff / complete on top of
// a Store; priorities, leases, interrupts and approvals live here.
package tasks

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// LeaseWindow is the 15-minute lease from docs/architecture.md, the same
// window as the front desk turn protocol.
const LeaseWindow = 15 * time.Minute

type Status string

const (
	Queued       Status = "queued"
	Leased       Status = "leased"
	Blocked      Status = "blocked"
	WaitingOwner Status = "waiting_owner"
	Done         Status = "done"
	Failed       Status = "failed"
	Cancelled    Status = "cancelled"
)

// Contribution is how a task moves the quest (C3): a kind from the goal's
// list, the expected units, and a short note on the mechanism.
type Contribution struct {
	Kind     string `json:"kind,omitempty"`
	Expected int    `json:"expected,omitempty"`
	Note     string `json:"note,omitempty"`
}

// ErrNoContribution is returned when a task does not declare how it moves the goal.
var ErrNoContribution = errors.New("goal_contribution.kind is required: every task declares how it moves the quest")

type Task struct {
	ID               string       `json:"id"`
	Brief            string       `json:"brief,omitempty"`
	NeedsApproval    bool         `json:"needs_approval,omitempty"` // B6: blocked until an approver decides
	DecidedBy        string       `json:"decided_by,omitempty"`
	DecisionNote     string       `json:"decision_note,omitempty"`
	Title            string       `json:"title"`
	Owner            string       `json:"owner"`
	RequestedBy      string       `json:"requested_by"`
	Priority         string       `json:"priority"` // P0..P3
	Status           Status       `json:"status"`
	LeaseExpiresAt   *time.Time   `json:"lease_expires_at,omitempty"`
	GoalContribution Contribution `json:"goal_contribution"`
	AutonomyLevel    string       `json:"autonomy_level,omitempty"`
	Attachments      []string     `json:"attachments,omitempty"`
	Parent           string       `json:"parent,omitempty"`
	// Progress is what the last holder saved when it let the task go (B4
	// release, B5 interrupt): a path on the common table or a short note.
	Progress string `json:"progress,omitempty"`
	// Failures counts failed turns in a row on this task (B11); LastError is
	// the newest. BlockedReason says why a blocked task waits: "needs_human"
	// after the escalation threshold (B6 approvals use NeedsApproval instead).
	Failures      int       `json:"failures,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	BlockedReason string    `json:"blocked_reason,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Event is one entry of the `events` stream.
type Event struct {
	Type  string    `json:"type"` // created | leased | renewed | handoff | done | failed | expired | released | waiting_owner | interrupted | failed_turn | escalated | unblocked
	Task  string    `json:"task"`
	Agent string    `json:"agent,omitempty"`
	To    string    `json:"to,omitempty"`
	At    time.Time `json:"at"`
}

var (
	ErrNotFound  = errors.New("task not found")
	ErrNotOwner  = errors.New("not the lease owner")
	ErrNotLeased = errors.New("task is not leased")
	ErrFinished  = errors.New("task already finished")
)

// Store persists tasks and emits events; Memory and Redis implement it.
type Store interface {
	Get(ctx context.Context, id string) (*Task, error)
	Put(ctx context.Context, t *Task) error
	List(ctx context.Context) ([]*Task, error)
	NextID(ctx context.Context) (string, error)
	Emit(ctx context.Context, e Event) error
}

// Service applies the rules on top of a Store.
type Service struct {
	Store    Store
	Now      func() time.Time
	Approver Approver
	// Kinds returns the quest's contribution kinds (C3). When set, every task
	// must declare a kind from the list; nil = contributions are not checked.
	Kinds func(ctx context.Context) []string
	mu    sync.Mutex // one mutation at a time (Redis WATCH later)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

type CreateInput struct {
	Title            string       `json:"title"`
	RequestedBy      string       `json:"requested_by"`
	Owner            string       `json:"owner,omitempty"`
	Priority         string       `json:"priority,omitempty"`
	GoalContribution Contribution `json:"goal_contribution"`
	Attachments      []string     `json:"attachments,omitempty"`
	Parent           string       `json:"parent,omitempty"`
	Brief            string       `json:"brief,omitempty"` // B6: why the requester needs it (shown to the approver)
}

// Approver decides who may assign work to others without approval: the owner and
// the planner(s). nil = everyone (tests, dev). Handoffs (B6): a task that one
// hireling requests from another is created `blocked` until an approver
// approves it (→ queued) or denies it (→ cancelled); both land on the timeline.
type Approver func(agent string) bool

func (s *Service) Create(ctx context.Context, in CreateInput) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("title is required")
	}
	if in.Priority == "" {
		in.Priority = "P3"
	}
	if !validPriority(in.Priority) {
		return nil, fmt.Errorf("priority must be P0..P3")
	}
	if err := s.checkContribution(ctx, &in.GoalContribution); err != nil {
		return nil, err
	}
	id, err := s.Store.NextID(ctx)
	if err != nil {
		return nil, err
	}
	n := s.now()
	t := &Task{ID: id, Title: in.Title, Owner: in.Owner, RequestedBy: in.RequestedBy, Priority: in.Priority,
		Status: Queued, GoalContribution: in.GoalContribution, Attachments: in.Attachments, Parent: in.Parent,
		Brief: in.Brief, CreatedAt: n, UpdatedAt: n}
	// B6: work one hireling asks of another waits for the Innkeeper (P2 autonomy).
	if in.Owner != "" && in.Owner != in.RequestedBy && s.Approver != nil && !s.Approver(in.RequestedBy) {
		t.Status, t.NeedsApproval, t.AutonomyLevel = Blocked, true, "p2"
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	kind := "created"
	if t.NeedsApproval {
		kind = "requested"
	}
	return t, s.Store.Emit(ctx, Event{Type: kind, Task: id, Agent: in.RequestedBy, To: in.Owner, At: n})
}

// ErrNotApprover is returned when a non-approver tries to decide a handoff.
var ErrNotApprover = errors.New("only an approver (the Innkeeper or the owner) can decide")

// ErrNotPending is returned when the task is not waiting for approval.
var ErrNotPending = errors.New("task is not waiting for approval")

// Decide approves (→ queued for the owner) or denies (→ cancelled) a task that
// waits for approval. Either way the change is on the timeline with who decided.
func (s *Service) Decide(ctx context.Context, id, agent string, approve bool, note string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Approver != nil && !s.Approver(agent) {
		return nil, ErrNotApprover
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !t.NeedsApproval || t.Status != Blocked {
		return nil, ErrNotPending
	}
	n := s.now()
	t.NeedsApproval, t.DecidedBy, t.DecisionNote, t.UpdatedAt = false, agent, strings.TrimSpace(note), n
	kind := "approved"
	if approve {
		t.Status = Queued
	} else {
		t.Status, kind = Cancelled, "denied"
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: kind, Task: id, Agent: agent, To: t.Owner, At: n})
}

// Lease gives the task to agent for LeaseWindow. A queued task, or a task whose
// lease expired, can be leased; a live lease by someone else cannot.
func (s *Service) Lease(ctx context.Context, id, agent string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	switch t.Status {
	case Done, Failed, Cancelled:
		return nil, ErrFinished
	case Leased:
		if t.Owner != agent {
			return nil, fmt.Errorf("%w: leased by %s until %s", ErrNotOwner, t.Owner, t.LeaseExpiresAt.Format(time.RFC3339))
		}
	}
	exp := n.Add(LeaseWindow)
	t.Owner, t.Status, t.LeaseExpiresAt, t.UpdatedAt = agent, Leased, &exp, n
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: "leased", Task: id, Agent: agent, At: n})
}

// Renew extends a live lease held by agent (called on every step).
func (s *Service) Renew(ctx context.Context, id, agent string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	if t.Status != Leased {
		return nil, ErrNotLeased
	}
	if t.Owner != agent {
		return nil, ErrNotOwner
	}
	exp := n.Add(LeaseWindow)
	t.LeaseExpiresAt, t.UpdatedAt = &exp, n
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: "renewed", Task: id, Agent: agent, At: n})
}

// Handoff changes the owner (docs/architecture.md: handing off = changing the
// owner in the queue, logged on the timeline). The task goes back to queued
// for the new owner to lease.
func (s *Service) Handoff(ctx context.Context, id, from, to string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	switch t.Status {
	case Done, Failed, Cancelled:
		return nil, ErrFinished
	case Leased:
		if t.Owner != from {
			return nil, ErrNotOwner
		}
	}
	if strings.TrimSpace(to) == "" {
		return nil, fmt.Errorf("handoff target is required")
	}
	t.Owner, t.Status, t.LeaseExpiresAt, t.UpdatedAt = to, Queued, nil, n
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: "handoff", Task: id, Agent: from, To: to, At: n})
}

// Finish closes the task as done or failed; only the lease owner may.
func (s *Service) Finish(ctx context.Context, id, agent string, status Status) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status != Done && status != Failed {
		return nil, fmt.Errorf("status must be done or failed")
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	if t.Status != Leased || t.Owner != agent {
		return nil, ErrNotOwner
	}
	t.Status, t.LeaseExpiresAt, t.UpdatedAt = status, nil, n
	if status == Done {
		t.Failures, t.LastError = 0, ""
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: string(status), Task: id, Agent: agent, At: n})
}

// NeedsHuman is the BlockedReason of a task escalated after repeated failures (B11).
const NeedsHuman = "needs_human"

// DefaultMaxFailures is the escalation threshold when the caller sends none (B11).
const DefaultMaxFailures = 3

// Fail records a failed turn on a leased task (B11). Below the threshold the
// task goes back to the queue (`failed_turn`) for another try; at the
// threshold it is blocked with reason needs_human (`escalated`) until the owner
// answers the decision the runtime opens. Only the holder may.
func (s *Service) Fail(ctx context.Context, id, agent, lastError string, max int) (*Task, error) {
	if max <= 0 {
		max = DefaultMaxFailures
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	if t.Status != Leased || t.Owner != agent {
		return nil, ErrNotOwner
	}
	t.Failures++
	if e := strings.TrimSpace(lastError); len(e) > 300 {
		t.LastError = e[:300]
	} else {
		t.LastError = e
	}
	kind := "failed_turn"
	t.Status, t.LeaseExpiresAt, t.UpdatedAt = Queued, nil, n
	if t.Failures >= max {
		t.Status, t.BlockedReason, kind = Blocked, NeedsHuman, "escalated"
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: kind, Task: id, Agent: agent, At: n})
}

// Unblock applies the owner's answer to an escalated task (B11): `drop` cancels
// it; any other action (retry, reassign, change brief) puts it back in the
// queue with the failure count reset. Only the owner (who opened the
// decision) may.
func (s *Service) Unblock(ctx context.Context, id, agent, action string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Status != Blocked || t.BlockedReason != NeedsHuman {
		return nil, fmt.Errorf("task is not waiting on a human after repeated failures")
	}
	if t.Owner != agent {
		return nil, ErrNotOwner
	}
	n := s.now()
	t.Status, t.BlockedReason, t.Failures, t.UpdatedAt = Queued, "", 0, n
	if strings.EqualFold(strings.TrimSpace(action), "drop") {
		t.Status = Cancelled
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	return t, s.Store.Emit(ctx, Event{Type: "unblocked", Task: id, Agent: agent, To: string(t.Status), At: n})
}

// List returns every task, expiring stale leases on the way, newest first.
func (s *Service) List(ctx context.Context) ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	n := s.now()
	for _, t := range ts {
		if s.expire(t, n) {
			_ = s.Store.Put(ctx, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].CreatedAt.After(ts[j].CreatedAt) })
	return ts, nil
}

// expire returns a lease to the queue when its window passed (no renewal → back
// to the queue).
func (s *Service) expire(t *Task, now time.Time) bool {
	if t.Status == Leased && t.LeaseExpiresAt != nil && !now.Before(*t.LeaseExpiresAt) {
		t.Status, t.LeaseExpiresAt, t.UpdatedAt = Queued, nil, now
		_ = s.Store.Emit(context.Background(), Event{Type: "expired", Task: t.ID, Agent: t.Owner, At: now})
		return true
	}
	return false
}

func validPriority(p string) bool {
	return p == "P0" || p == "P1" || p == "P2" || p == "P3"
}

// checkContribution enforces C3 when the service knows the quest's kinds.
func (s *Service) checkContribution(ctx context.Context, c *Contribution) error {
	c.Kind = strings.ToLower(strings.TrimSpace(c.Kind))
	c.Note = strings.TrimSpace(c.Note)
	if c.Expected < 0 {
		return fmt.Errorf("goal_contribution.expected must be ≥ 0")
	}
	if len([]rune(c.Note)) > 140 {
		return fmt.Errorf("goal_contribution.note over 140 characters")
	}
	if s.Kinds == nil {
		return nil
	}
	kinds := s.Kinds(ctx)
	if c.Kind == "" {
		return fmt.Errorf("%w (one of %v)", ErrNoContribution, kinds)
	}
	for _, k := range kinds {
		if k == c.Kind {
			return nil
		}
	}
	return fmt.Errorf("goal_contribution.kind %q is not one of the quest's kinds %v", c.Kind, kinds)
}

// Expected sums the declared contributions by kind: open (queued, leased,
// blocked, waiting) and done. What the quest bar shows as "in the pipeline".
func Expected(all []*Task) (open, done map[string]int) {
	open, done = map[string]int{}, map[string]int{}
	for _, t := range all {
		if t.GoalContribution.Kind == "" {
			continue
		}
		switch t.Status {
		case Done:
			done[t.GoalContribution.Kind] += t.GoalContribution.Expected
		case Failed, Cancelled:
		default:
			open[t.GoalContribution.Kind] += t.GoalContribution.Expected
		}
	}
	return open, done
}

// ---- Priorities (B3) --------------------------------------------------------

// PriorityRank is P0 → 0 … P3 → 3 (unknown → 4).
func PriorityRank(p string) int {
	switch p {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	}
	return 4
}

// Rank orders tasks the way a hireling takes them (docs/architecture.md):
// P0 > P1 > P2 > P3; at the same level the one that moves the shared goal more
// wins — first the contribution kind closest to the goal (its position in
// kinds), then more expected units — and then the oldest. Stable.
func Rank(ts []*Task, kinds []string) {
	pos := map[string]int{}
	for i, k := range kinds {
		pos[k] = i
	}
	kindRank := func(t *Task) int {
		if i, ok := pos[t.GoalContribution.Kind]; ok {
			return i
		}
		return len(kinds)
	}
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if pa, pb := PriorityRank(a.Priority), PriorityRank(b.Priority); pa != pb {
			return pa < pb
		}
		if ka, kb := kindRank(a), kindRank(b); ka != kb {
			return ka < kb
		}
		if a.GoalContribution.Expected != b.GoalContribution.Expected {
			return a.GoalContribution.Expected > b.GoalContribution.Expected
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
}

// Queue is an owner's work in the order it should be taken: their queued tasks
// (not waiting for approval), ranked. Stale leases are expired first, so a task
// whose lease ran out is back in line.
func (s *Service) Queue(ctx context.Context, owner string) ([]*Task, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, t := range all {
		if t.Owner == owner && t.Status == Queued && !t.NeedsApproval {
			out = append(out, t)
		}
	}
	var kinds []string
	if s.Kinds != nil {
		kinds = s.Kinds(ctx)
	}
	Rank(out, kinds)
	return out, nil
}

// Get returns one task (stale lease expired on the way).
func (s *Service) Get(ctx context.Context, id string) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.expire(t, s.now()) {
		_ = s.Store.Put(ctx, t)
	}
	return t, nil
}

// ---- Leases (B4) ------------------------------------------------------------

// Sweep returns every lease whose window passed to the queue (logged as an
// `expired` event) and reports which tasks it freed. tavern-core runs it on a
// timer, so an expired lease is back in line even when nobody asks.
func (s *Service) Sweep(ctx context.Context) ([]*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts, err := s.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	n := s.now()
	var freed []*Task
	for _, t := range ts {
		owner := t.Owner
		if s.expire(t, n) {
			if err := s.Store.Put(ctx, t); err != nil {
				return freed, err
			}
			c := *t
			c.Owner = owner
			freed = append(freed, &c)
		}
	}
	return freed, nil
}

// Release lets go of a live lease without finishing the task: back to the
// queue (`queued`, e.g. interrupted by a P0 — B5) or parked until the owner
// answers (`waiting_owner`, the D3 "Waiting on you" card). Only the holder may.
func (s *Service) Release(ctx context.Context, id, agent string, status Status, progress string) (*Task, error) {
	return s.release(ctx, id, agent, status, progress, false)
}

// Interrupt is Release back to the queue because a P0 cut the turn short (B5):
// `interrupted` on the timeline, progress kept so the next lease resumes.
func (s *Service) Interrupt(ctx context.Context, id, agent, progress string) (*Task, error) {
	return s.release(ctx, id, agent, Queued, progress, true)
}

func (s *Service) release(ctx context.Context, id, agent string, status Status, progress string, interrupt bool) (*Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status != Queued && status != WaitingOwner {
		return nil, fmt.Errorf("status must be queued or waiting_owner")
	}
	t, err := s.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	n := s.now()
	s.expire(t, n)
	if t.Status != Leased || t.Owner != agent {
		return nil, ErrNotOwner
	}
	t.Status, t.LeaseExpiresAt, t.UpdatedAt = status, nil, n
	if p := strings.TrimSpace(progress); p != "" {
		if len(p) > 500 {
			p = p[:500]
		}
		t.Progress = p
	}
	if err := s.Store.Put(ctx, t); err != nil {
		return nil, err
	}
	kind := "released"
	switch {
	case interrupt:
		kind = "interrupted"
	case status == WaitingOwner:
		kind = "waiting_owner"
	}
	return t, s.Store.Emit(ctx, Event{Type: kind, Task: id, Agent: agent, At: n})
}
