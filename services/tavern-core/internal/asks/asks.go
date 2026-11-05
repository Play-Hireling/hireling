// Package asks is D11, "autonomy first": the book of every question a
// hireling puts to the owner. It keeps one open card per task (a second question
// on the same task joins the open one instead of opening another), remembers
// the asks the runtime sent back because the action was the hireling's own
// to take, and counts, per hireling, how often the owner picks the option the
// hireling recommended — the "could have decided alone" number that feeds the
// scorecard (docs/economy.md) and calibration.
package asks

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Open is a card waiting on the owner.
type Open struct {
	Decision    string    `json:"decision"`
	Agent       string    `json:"agent"`
	Task        string    `json:"task,omitempty"`
	Action      string    `json:"action,omitempty"`
	Question    string    `json:"question"`
	Recommended string    `json:"recommended,omitempty"` // the card's first option
	At          time.Time `json:"at"`
	// FollowUps are the questions asked on the same task while the card was
	// open; they go back to the hireling with the owner's answer.
	FollowUps []string `json:"follow_ups,omitempty"`
}

// Answered is what closing a card returns to the runtime.
type Answered struct {
	Open
	Choice            string `json:"choice"`
	PickedRecommended bool   `json:"picked_recommended"`
}

// Stats are one hireling's counters.
type Stats struct {
	Agent    string `json:"agent"`
	Opened   int    `json:"opened"`   // cards opened
	Joined   int    `json:"joined"`   // questions added to an open card instead
	Bounced  int    `json:"bounced"`  // sent back: the action was the hireling's own
	Answered int    `json:"answered"` // cards the owner answered
	Picked   int    `json:"picked_recommended"`
	Tasks    int    `json:"tasks"` // tasks with at least one ask
	// AsksPerTask is (opened + joined) / tasks; 0 without tasks.
	AsksPerTask float64 `json:"asks_per_task"`
	// CouldHaveDecidedAlone is picked / answered: a high rate means the
	// hireling asks what it could have decided (0 without answers).
	CouldHaveDecidedAlone float64 `json:"could_have_decided_alone"`
}

func (s *Stats) derive() {
	if s.Tasks > 0 {
		s.AsksPerTask = float64(s.Opened+s.Joined) / float64(s.Tasks)
	}
	if s.Answered > 0 {
		s.CouldHaveDecidedAlone = float64(s.Picked) / float64(s.Answered)
	}
}

const (
	TTL          = 7 * 24 * time.Hour // an unanswered card is forgotten after a week (the gate's PendingTTL)
	MaxFollowUps = 5
	statsTTL     = 90 * 24 * time.Hour
)

// ErrNotFound: no open card with that decision id.
var ErrNotFound = errors.New("asks: no open card for this decision")

// Store is the book.
type Store interface {
	// OpenFor returns the open card of a task, or nil.
	OpenFor(ctx context.Context, agent, task string) (*Open, error)
	Opened(ctx context.Context, o Open) error
	// Join adds a question to the task's open card; nil when none is open.
	Join(ctx context.Context, agent, task, question string) (*Open, error)
	// Bounce records that an ask about an action was sent back to the
	// hireling. It is true only the first time for that key (task or turn)
	// and action: a hireling that asks again goes to the owner.
	Bounce(ctx context.Context, agent, key, action string) (bool, error)
	Answer(ctx context.Context, decision, choice string) (*Answered, error)
	Stats(ctx context.Context, agent string) (Stats, error)
	List(ctx context.Context, agent string) ([]Open, []Stats, error)
}

// Picked reports whether the owner's choice is the recommended option.
func Picked(choice, recommended string) bool {
	c, r := norm(choice), norm(recommended)
	return r != "" && (c == r || strings.HasPrefix(c, r+" ") || strings.HasPrefix(c, r+":") || strings.HasPrefix(c, r+"."))
}

func norm(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func addFollowUp(o *Open, q string) {
	q = clip(q, 300)
	for _, f := range o.FollowUps {
		if norm(f) == norm(q) {
			return
		}
	}
	if norm(q) == norm(o.Question) {
		return
	}
	if len(o.FollowUps) < MaxFollowUps {
		o.FollowUps = append(o.FollowUps, q)
	}
}

// ---- in-memory (tests, REDIS_URL unset) ------------------------------------

type Memory struct {
	mu     sync.Mutex
	Now    func() time.Time
	open   map[string]Open   // agent\x00task → card
	byDec  map[string]string // decision → agent\x00task
	bounce map[string]time.Time
	stats  map[string]*Stats
	tasks  map[string]map[string]bool
}

func NewMemory() *Memory {
	return &Memory{open: map[string]Open{}, byDec: map[string]string{}, bounce: map[string]time.Time{},
		stats: map[string]*Stats{}, tasks: map[string]map[string]bool{}}
}

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Memory) st(agent string) *Stats {
	s := m.stats[agent]
	if s == nil {
		s = &Stats{Agent: agent}
		m.stats[agent] = s
	}
	return s
}

func (m *Memory) countTask(agent, task string) {
	if task == "" {
		return
	}
	if m.tasks[agent] == nil {
		m.tasks[agent] = map[string]bool{}
	}
	m.tasks[agent][task] = true
}

func (m *Memory) OpenFor(_ context.Context, agent, task string) (*Open, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task == "" {
		return nil, nil
	}
	o, ok := m.open[agent+"\x00"+task]
	if !ok || m.now().Sub(o.At) > TTL {
		return nil, nil
	}
	return &o, nil
}

func (m *Memory) Opened(_ context.Context, o Open) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o.At.IsZero() {
		o.At = m.now()
	}
	k := o.Agent + "\x00" + o.Task
	if o.Task == "" {
		k = o.Agent + "\x00#" + o.Decision // no task: one card per ask
	}
	m.open[k] = o
	m.byDec[o.Decision] = k
	m.st(o.Agent).Opened++
	m.countTask(o.Agent, o.Task)
	return nil
}

func (m *Memory) Join(_ context.Context, agent, task, question string) (*Open, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if task == "" {
		return nil, nil
	}
	k := agent + "\x00" + task
	o, ok := m.open[k]
	if !ok || m.now().Sub(o.At) > TTL {
		return nil, nil
	}
	addFollowUp(&o, question)
	m.open[k] = o
	m.st(agent).Joined++
	return &o, nil
}

func (m *Memory) Bounce(_ context.Context, agent, key, action string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := agent + "\x00" + key + "\x00" + action
	if at, ok := m.bounce[k]; ok && m.now().Sub(at) <= TTL {
		return false, nil
	}
	m.bounce[k] = m.now()
	m.st(agent).Bounced++
	return true, nil
}

func (m *Memory) Answer(_ context.Context, decision, choice string) (*Answered, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.byDec[decision]
	if !ok {
		return nil, ErrNotFound
	}
	o := m.open[k]
	delete(m.open, k)
	delete(m.byDec, decision)
	a := &Answered{Open: o, Choice: clip(choice, 200), PickedRecommended: Picked(choice, o.Recommended)}
	s := m.st(o.Agent)
	s.Answered++
	if a.PickedRecommended {
		s.Picked++
	}
	return a, nil
}

func (m *Memory) Stats(_ context.Context, agent string) (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := *m.st(agent)
	s.Tasks = len(m.tasks[agent])
	s.derive()
	return s, nil
}

func (m *Memory) List(ctx context.Context, agent string) ([]Open, []Stats, error) {
	m.mu.Lock()
	var open []Open
	agents := map[string]bool{}
	for _, o := range m.open {
		if (agent == "" || o.Agent == agent) && m.now().Sub(o.At) <= TTL {
			open = append(open, o)
		}
	}
	for a := range m.stats {
		if agent == "" || a == agent {
			agents[a] = true
		}
	}
	m.mu.Unlock()
	return sortOpen(open), m.statsFor(ctx, agents), nil
}

func (m *Memory) statsFor(ctx context.Context, agents map[string]bool) []Stats {
	out := []Stats{}
	for a := range agents {
		s, _ := m.Stats(ctx, a)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

func sortOpen(l []Open) []Open {
	if l == nil {
		return []Open{}
	}
	sort.Slice(l, func(i, j int) bool { return l[i].At.After(l[j].At) })
	return l
}

// ---- Redis ---------------------------------------------------------------------

// Redis keeps:
//
//	tavern:asks:open:<agent>:<task|#decision>  the open card (JSON, TTL a week)
//	tavern:asks:dec:<decision>                 → the open card's key
//	tavern:asks:bounce:<agent>:<key>:<action>  an ask sent back once (TTL a week)
//	tavern:asks:stats:<agent>                  hash of counters
//	tavern:asks:tasks:<agent>                  set of tasks with an ask
//	tavern:asks:agents                         set of hirelings with counters
type Redis struct {
	C      *redis.Client
	Prefix string
	Now    func() time.Time
}

func (r *Redis) key(parts ...string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":asks:" + strings.Join(parts, ":")
}

func (r *Redis) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Redis) get(ctx context.Context, k string) (*Open, error) {
	raw, err := r.C.Get(ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Open
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *Redis) incr(p redis.Pipeliner, ctx context.Context, agent, field string) {
	k := r.key("stats", agent)
	p.HIncrBy(ctx, k, field, 1)
	p.Expire(ctx, k, statsTTL)
	p.SAdd(ctx, r.key("agents"), agent)
}

func (r *Redis) OpenFor(ctx context.Context, agent, task string) (*Open, error) {
	if task == "" {
		return nil, nil
	}
	return r.get(ctx, r.key("open", agent, task))
}

func (r *Redis) Opened(ctx context.Context, o Open) error {
	if o.At.IsZero() {
		o.At = r.now()
	}
	k := r.key("open", o.Agent, o.Task)
	if o.Task == "" {
		k = r.key("open", o.Agent, "#"+o.Decision)
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return err
	}
	_, err = r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.Set(ctx, k, raw, TTL)
		p.Set(ctx, r.key("dec", o.Decision), k, TTL)
		r.incr(p, ctx, o.Agent, "opened")
		if o.Task != "" {
			p.SAdd(ctx, r.key("tasks", o.Agent), o.Task)
			p.Expire(ctx, r.key("tasks", o.Agent), statsTTL)
		}
		return nil
	})
	return err
}

func (r *Redis) Join(ctx context.Context, agent, task, question string) (*Open, error) {
	if task == "" {
		return nil, nil
	}
	k := r.key("open", agent, task)
	var out *Open
	err := r.C.Watch(ctx, func(tx *redis.Tx) error {
		raw, err := tx.Get(ctx, k).Result()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		var o Open
		if err := json.Unmarshal([]byte(raw), &o); err != nil {
			return err
		}
		addFollowUp(&o, question)
		b, _ := json.Marshal(o)
		ttl := tx.TTL(ctx, k).Val()
		if ttl <= 0 {
			ttl = TTL
		}
		_, err = tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.Set(ctx, k, b, ttl)
			r.incr(p, ctx, agent, "joined")
			return nil
		})
		if err == nil {
			out = &o
		}
		return err
	}, k)
	return out, err
}

func (r *Redis) Bounce(ctx context.Context, agent, key, action string) (bool, error) {
	ok, err := r.C.SetNX(ctx, r.key("bounce", agent, key, action), r.now().UTC().Format(time.RFC3339), TTL).Result()
	if err != nil || !ok {
		return false, err
	}
	_, err = r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		r.incr(p, ctx, agent, "bounced")
		return nil
	})
	return true, err
}

func (r *Redis) Answer(ctx context.Context, decision, choice string) (*Answered, error) {
	k, err := r.C.GetDel(ctx, r.key("dec", decision)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	raw, err := r.C.GetDel(ctx, k).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var o Open
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		return nil, err
	}
	a := &Answered{Open: o, Choice: clip(choice, 200), PickedRecommended: Picked(choice, o.Recommended)}
	_, err = r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		r.incr(p, ctx, o.Agent, "answered")
		if a.PickedRecommended {
			r.incr(p, ctx, o.Agent, "picked")
		}
		return nil
	})
	return a, err
}

func (r *Redis) Stats(ctx context.Context, agent string) (Stats, error) {
	s := Stats{Agent: agent}
	h, err := r.C.HGetAll(ctx, r.key("stats", agent)).Result()
	if err != nil {
		return s, err
	}
	n := func(f string) int {
		var v int
		_ = json.Unmarshal([]byte(h[f]), &v)
		return v
	}
	s.Opened, s.Joined, s.Bounced, s.Answered, s.Picked = n("opened"), n("joined"), n("bounced"), n("answered"), n("picked")
	t, err := r.C.SCard(ctx, r.key("tasks", agent)).Result()
	if err != nil {
		return s, err
	}
	s.Tasks = int(t)
	s.derive()
	return s, nil
}

func (r *Redis) List(ctx context.Context, agent string) ([]Open, []Stats, error) {
	pattern := r.key("open", "*")
	if agent != "" {
		pattern = r.key("open", agent, "*")
	}
	var open []Open
	iter := r.C.Scan(ctx, 0, pattern, 200).Iterator()
	for iter.Next(ctx) {
		o, err := r.get(ctx, iter.Val())
		if err != nil {
			return nil, nil, err
		}
		if o != nil {
			open = append(open, *o)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, nil, err
	}
	agents := []string{agent}
	if agent == "" {
		var err error
		if agents, err = r.C.SMembers(ctx, r.key("agents")).Result(); err != nil {
			return nil, nil, err
		}
		sort.Strings(agents)
	}
	stats := []Stats{}
	for _, a := range agents {
		s, err := r.Stats(ctx, a)
		if err != nil {
			return nil, nil, err
		}
		stats = append(stats, s)
	}
	return sortOpen(open), stats, nil
}
