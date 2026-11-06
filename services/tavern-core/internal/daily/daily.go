// Package daily is the Innkeeper's daily plan (feature B10): every morning the
// planner's runtime asks tavern-core for the day's plan, tavern-core composes
// it from the queue (B3 order) and the quest (C1), and keeps one record per
// local day. The first post of a day creates it; any later post of the same
// day gets the record back with created=false, so a restarted runtime never
// posts twice. The record is what the quest board shows (GET /goal
// `daily_plan`, GET /plans/daily).
package daily

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/Play-Hireling/hireling/services/tavern-core/internal/goal"
	"github.com/Play-Hireling/hireling/services/tavern-core/internal/tasks"
)

// PerOwner is how many tasks the plan lists per hireling; the rest is counted.
const PerOwner = 5

// Retention is how long a day's plan is kept on Redis.
const Retention = 90 * 24 * time.Hour

// Item is one task line of the plan.
type Item struct {
	Task     string `json:"task"`
	Title    string `json:"title"`
	Owner    string `json:"owner"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
	Goal     string `json:"goal,omitempty"` // "10 leads" (C3)
}

// Owner is one hireling's part of the day, in the order it takes its work.
type Owner struct {
	Agent string `json:"agent"`
	Tasks []Item `json:"tasks"`
	More  int    `json:"more,omitempty"` // open tasks beyond PerOwner
}

// Plan is one day's record.
type Plan struct {
	Day      string    `json:"day"` // local date, 2006-01-02
	TZ       string    `json:"tz"`  // the planner's time zone (IANA)
	PostedBy string    `json:"posted_by"`
	PostedAt time.Time `json:"posted_at"`
	Quest    string    `json:"quest,omitempty"` // the quest bar at posting time
	Owners   []Owner   `json:"owners"`
	// Waiting are tasks parked on the owner: a "Waiting on you" card (D3) or an
	// escalation after repeated failures (B11).
	Waiting []Item `json:"waiting,omitempty"`
	// Approvals are handoffs waiting for the Innkeeper (B6).
	Approvals []Item `json:"approvals,omitempty"`
	// Summary is the one line for the log (≤ 120 characters, like a turn result).
	Summary string `json:"summary"`
	// Text is the whole plan in markdown, as the quest board shows it.
	Text string `json:"text"`
	// Commentary is the Innkeeper's note on the day, written by its morning
	// model turn after the plan was created (≤ MaxCommentary characters;
	// empty when the turn did not run, failed or was over budget).
	Commentary   string     `json:"commentary,omitempty"`
	CommentaryAt *time.Time `json:"commentary_at,omitempty"`
	// LoggedAt is when the runtime put the plan in the log (Front Desk
	// timeline + events). Unset = the log line is still owed: the runtime
	// retries it until it lands.
	LoggedAt *time.Time `json:"logged_at,omitempty"`
}

// MaxCommentary bounds the Innkeeper's note on the plan.
const MaxCommentary = 2000

// Tasks counts the open tasks the plan schedules.
func (p *Plan) Tasks() int {
	n := 0
	for _, o := range p.Owners {
		n += len(o.Tasks) + o.More
	}
	return n
}

var (
	ErrForbidden = errors.New("daily: only an approver (the Innkeeper or the owner) posts the daily plan")
	ErrNotFound  = errors.New("daily: no plan for that day")
	ErrBadDay    = errors.New("daily: day must be a date (2006-01-02) within a day of today")
	ErrTooLong   = fmt.Errorf("daily: commentary over %d characters", MaxCommentary)
)

var tzRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+\-/]{0,63}$`)

// Store keeps one plan per day; Memory and Redis implement it.
type Store interface {
	// Create stores p unless the day already has a plan; it returns the stored
	// plan and whether this call created it. Atomic: two posts of the same day
	// create one record.
	Create(ctx context.Context, p *Plan) (*Plan, bool, error)
	Get(ctx context.Context, day string) (*Plan, error) // ErrNotFound
	Latest(ctx context.Context) (*Plan, error)          // ErrNotFound
	// Update changes a stored plan atomically (ErrNotFound when the day has none).
	Update(ctx context.Context, day string, fn func(p *Plan)) (*Plan, error)
}

// Service composes and keeps the plans.
type Service struct {
	Store Store
	Tasks *tasks.Service
	Goal  *goal.Service // nil = no quest line
	// Approver says who may post (the owner, planners). nil = everyone (dev, tests).
	Approver func(agent string) bool
	Now      func() time.Time
	mu       sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// Post composes the plan for day and keeps it, once per day: the first call
// of a day creates the record (created=true), later calls return it untouched.
func (s *Service) Post(ctx context.Context, by, day, tz string) (*Plan, bool, error) {
	by = strings.TrimSpace(by)
	if s.Approver != nil && !s.Approver(by) {
		return nil, false, ErrForbidden
	}
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return nil, false, ErrBadDay
	}
	now := s.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	// Every time zone is within a day of UTC, so any planner's "today" is too.
	if diff := d.Sub(today); diff < -24*time.Hour || diff > 24*time.Hour {
		return nil, false, ErrBadDay
	}
	tz = strings.TrimSpace(tz)
	if tz == "" {
		tz = "UTC"
	}
	if !tzRe.MatchString(tz) {
		return nil, false, fmt.Errorf("daily: tz %q is not a time zone name", tz)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, err := s.Store.Get(ctx, day); err == nil {
		return p, false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, false, err
	}
	all, err := s.Tasks.List(ctx)
	if err != nil {
		return nil, false, err
	}
	var q *goal.Goal
	if s.Goal != nil {
		if g, err := s.Goal.Get(ctx); err == nil {
			q = g
		}
	}
	p := Compose(day, tz, by, now, all, q)
	return s.Store.Create(ctx, p)
}

// Comment stores the Innkeeper's note on the day's plan (once: a later call
// returns the plan as it is, so a retry is harmless) and renders it into the
// quest board's text.
func (s *Service) Comment(ctx context.Context, by, day, text string) (*Plan, error) {
	if s.Approver != nil && !s.Approver(strings.TrimSpace(by)) {
		return nil, ErrForbidden
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("daily: commentary is empty")
	}
	if len([]rune(text)) > MaxCommentary {
		return nil, ErrTooLong
	}
	at := s.now().UTC()
	return s.Store.Update(ctx, day, func(p *Plan) {
		if p.Commentary != "" {
			return
		}
		p.Commentary, p.CommentaryAt = text, &at
		p.Text = render(p)
	})
}

// Logged records that the plan reached the log (idempotent: the first time stays).
func (s *Service) Logged(ctx context.Context, by, day string) (*Plan, error) {
	if s.Approver != nil && !s.Approver(strings.TrimSpace(by)) {
		return nil, ErrForbidden
	}
	at := s.now().UTC()
	return s.Store.Update(ctx, day, func(p *Plan) {
		if p.LoggedAt == nil {
			p.LoggedAt = &at
		}
	})
}

// Get returns the plan of a day ("" = the latest).
func (s *Service) Get(ctx context.Context, day string) (*Plan, error) {
	if day == "" {
		return s.Store.Latest(ctx)
	}
	return s.Store.Get(ctx, day)
}

// Compose builds the day's plan from the tasks and the quest: each hireling's
// open work (queued or in progress) in the order it takes it (B3: priority,
// then the contribution closest to the goal, more units, oldest), what waits
// on the owner, and the handoffs the Innkeeper has to approve. A task that only
// groups a plan (some other task names it as parent) is left out: its
// children are the work.
func Compose(day, tz, by string, now time.Time, all []*tasks.Task, q *goal.Goal) *Plan {
	p := &Plan{Day: day, TZ: tz, PostedBy: by, PostedAt: now.UTC(), Owners: []Owner{}}
	kinds := goal.DefaultKinds
	if q != nil {
		kinds = q.AllowedKinds()
		p.Quest = q.Bar()
	}
	isParent := map[string]bool{}
	for _, t := range all {
		if t.Parent != "" {
			isParent[t.Parent] = true
		}
	}
	var open []*tasks.Task
	for _, t := range all {
		switch {
		case t.Status == tasks.Blocked && t.NeedsApproval:
			p.Approvals = append(p.Approvals, item(t))
		case t.Status == tasks.WaitingOwner, t.Status == tasks.Blocked && t.BlockedReason == tasks.NeedsHuman:
			p.Waiting = append(p.Waiting, item(t))
		case (t.Status == tasks.Queued || t.Status == tasks.Leased) && !isParent[t.ID] && t.Owner != "":
			open = append(open, t)
		}
	}
	tasks.Rank(open, kinds)
	byOwner := map[string]int{}
	for _, t := range open {
		i, ok := byOwner[t.Owner]
		if !ok {
			i = len(p.Owners)
			byOwner[t.Owner] = i
			p.Owners = append(p.Owners, Owner{Agent: t.Owner, Tasks: []Item{}})
		}
		if len(p.Owners[i].Tasks) < PerOwner {
			p.Owners[i].Tasks = append(p.Owners[i].Tasks, item(t))
		} else {
			p.Owners[i].More++
		}
	}
	byID := func(items []Item) {
		sort.SliceStable(items, func(i, j int) bool {
			if a, b := tasks.PriorityRank(items[i].Priority), tasks.PriorityRank(items[j].Priority); a != b {
				return a < b
			}
			return items[i].Task < items[j].Task
		})
	}
	byID(p.Waiting)
	byID(p.Approvals)
	p.Summary = summary(p, q)
	p.Text = render(p)
	return p
}

func item(t *tasks.Task) Item {
	it := Item{Task: t.ID, Title: t.Title, Owner: t.Owner, Priority: t.Priority, Status: string(t.Status)}
	if c := t.GoalContribution; c.Kind != "" {
		it.Goal = fmt.Sprintf("%d %s", c.Expected, c.Kind)
	}
	return it
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// summary is the log line: tasks per hireling, what waits on the owner, the quest %.
func summary(p *Plan, q *goal.Goal) string {
	var parts []string
	if n := p.Tasks(); n == 0 {
		parts = append(parts, "no open tasks")
	} else {
		var who []string
		for _, o := range p.Owners {
			who = append(who, fmt.Sprintf("%s %d", o.Agent, len(o.Tasks)+o.More))
		}
		parts = append(parts, plural(n, "task", "tasks")+": "+strings.Join(who, ", "))
	}
	if n := len(p.Waiting); n > 0 {
		parts = append(parts, fmt.Sprintf("%d waiting on you", n))
	}
	if n := len(p.Approvals); n > 0 {
		parts = append(parts, plural(n, "handoff", "handoffs")+" to approve")
	}
	if q != nil && q.Title != "" {
		parts = append(parts, fmt.Sprintf("quest %.1f%%", q.Percent()))
	}
	s := strings.Join(parts, " · ")
	if rs := []rune(s); len(rs) > 120 {
		s = string(rs[:119]) + "…"
	}
	return s
}

func line(it Item, owner bool) string {
	s := fmt.Sprintf("- %s %s %s", it.Task, it.Priority, it.Title)
	if owner {
		s += " · " + it.Owner
	}
	if it.Status == string(tasks.Leased) {
		s += " (in progress)"
	}
	if it.Goal != "" {
		s += " · " + it.Goal
	}
	return s
}

// render is the plan as the quest board shows it.
func render(p *Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Daily plan · %s (%s)\n", p.Day, p.TZ)
	if p.Quest != "" {
		b.WriteString(p.Quest + "\n")
	}
	if len(p.Owners) == 0 {
		b.WriteString("\nNo open tasks in the queue.\n")
	}
	for _, o := range p.Owners {
		fmt.Fprintf(&b, "\n%s\n", o.Agent)
		for _, it := range o.Tasks {
			b.WriteString(line(it, false) + "\n")
		}
		if o.More > 0 {
			fmt.Fprintf(&b, "- … and %d more\n", o.More)
		}
	}
	if len(p.Waiting) > 0 {
		b.WriteString("\nWaiting on you\n")
		for _, it := range p.Waiting {
			b.WriteString(line(it, true) + "\n")
		}
	}
	if len(p.Approvals) > 0 {
		b.WriteString("\nHandoffs to approve\n")
		for _, it := range p.Approvals {
			b.WriteString(line(it, true) + "\n")
		}
	}
	if p.Commentary != "" {
		b.WriteString("\nInnkeeper's note\n" + p.Commentary + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- Stores -----------------------------------------------------------------

// Memory is the in-memory store (dev and tests).
type Memory struct {
	mu    sync.Mutex
	plans map[string]Plan
}

func NewMemory() *Memory { return &Memory{plans: map[string]Plan{}} }

func (m *Memory) Create(_ context.Context, p *Plan) (*Plan, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.plans[p.Day]; ok {
		return &old, false, nil
	}
	m.plans[p.Day] = *p
	c := *p
	return &c, true, nil
}

func (m *Memory) Get(_ context.Context, day string) (*Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plans[day]
	if !ok {
		return nil, ErrNotFound
	}
	return &p, nil
}

func (m *Memory) Latest(_ context.Context) (*Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var last string
	for d := range m.plans {
		if d > last {
			last = d
		}
	}
	if last == "" {
		return nil, ErrNotFound
	}
	p := m.plans[last]
	return &p, nil
}

func (m *Memory) Update(_ context.Context, day string, fn func(p *Plan)) (*Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.plans[day]
	if !ok {
		return nil, ErrNotFound
	}
	fn(&p)
	m.plans[day] = p
	c := p
	return &c, nil
}

// Redis keeps one JSON record per day under `<prefix>:plan:daily:<day>`
// (SET NX: the first post of a day wins) and the newest day under
// `<prefix>:plan:daily:latest`.
type Redis struct {
	C      *redis.Client
	Prefix string
}

func (r *Redis) key(s string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":plan:daily:" + s
}

// latestScript moves the pointer forward only (an older day never wins).
var latestScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
if (not cur) or cur < ARGV[1] then redis.call('SET', KEYS[1], ARGV[1]) end
return 1`)

func (r *Redis) Create(ctx context.Context, p *Plan) (*Plan, bool, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, false, err
	}
	ok, err := r.C.SetNX(ctx, r.key(p.Day), raw, Retention).Result()
	if err != nil {
		return nil, false, err
	}
	if !ok {
		old, err := r.Get(ctx, p.Day)
		return old, false, err
	}
	// The day is claimed: a failed pointer move must not hide that, or the
	// caller would never log the plan it just created. GET by day still works.
	_ = latestScript.Run(ctx, r.C, []string{r.key("latest")}, p.Day).Err()
	c := *p
	return &c, true, nil
}

func (r *Redis) Get(ctx context.Context, day string) (*Plan, error) {
	raw, err := r.C.Get(ctx, r.key(day)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p Plan
	return &p, json.Unmarshal(raw, &p)
}

func (r *Redis) Latest(ctx context.Context) (*Plan, error) {
	day, err := r.C.Get(ctx, r.key("latest")).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return r.Get(ctx, day)
}

// Update rewrites the day's record under WATCH, keeping its TTL; a concurrent
// change makes it try again.
func (r *Redis) Update(ctx context.Context, day string, fn func(p *Plan)) (*Plan, error) {
	key := r.key(day)
	for i := 0; i < 5; i++ {
		var out *Plan
		err := r.C.Watch(ctx, func(tx *redis.Tx) error {
			raw, err := tx.Get(ctx, key).Bytes()
			if errors.Is(err, redis.Nil) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			var p Plan
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			fn(&p)
			next, err := json.Marshal(&p)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				pipe.SetArgs(ctx, key, next, redis.SetArgs{KeepTTL: true})
				return nil
			})
			out = &p
			return err
		}, key)
		if errors.Is(err, redis.TxFailedErr) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	return nil, redis.TxFailedErr
}
