package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---- the owner's approvals (D1 + D3) ---------------------------------------------
//
// When the gate blocks an action (the owner decides, or Capped), the hireling asks
// the owner with an `ask` block. The runtime attaches the actions the gate blocked
// during that turn to the decision (Pend); when the owner's answer comes back and
// approves it (Answer), each of those actions gets one use for that hireling
// (a grant, valid for GrantTTL). The next check of the same action consumes it
// and answers Free. Everything lives here, server-side, so a hireling cannot
// write itself a grant through its own volume.

// Act is one action the gate saw.
type Act struct {
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at,omitempty"`
}

// Pending is a decision card that would grant actions if the owner approves it.
type Pending struct {
	Decision string `json:"decision"`
	Agent    string `json:"agent"`
	Actions  []Act  `json:"actions"`
	Approve  string `json:"approve,omitempty"` // the option that means yes (the card's first option)
}

const (
	BlockedTTL  = 2 * time.Hour      // how long a blocked action can still be attached to a card
	PendingTTL  = 7 * 24 * time.Hour // a card unanswered for a week grants nothing
	GrantTTL    = 24 * time.Hour     // an approval is used within a day or lapses
	keepBlocked = 50
)

// ErrNoPending is returned when a decision carries no gate actions.
var ErrNoPending = errors.New("policy: no gate actions pending for this decision")

// Grants keeps blocked actions, pending cards and approvals.
type Grants interface {
	Blocked(ctx context.Context, agent string, a Act) error
	RecentBlocked(ctx context.Context, agent string, since time.Time) ([]Act, error)
	Pend(ctx context.Context, p Pending) error
	TakePending(ctx context.Context, decision string) (Pending, error)
	Grant(ctx context.Context, agent, action string, uses int) error
	Consume(ctx context.Context, agent, action string) (bool, error)
	// Use counts one use of a capped action in a window (D2); within limit it
	// keeps it, over the limit it gives it back and reports false.
	Use(ctx context.Context, agent, action, window string, limit int, ttl time.Duration) (used int, ok bool, err error)
	// Count reads the uses of a capped action in a window without counting one (D11).
	Count(ctx context.Context, agent, action, window string) (int, error)
}

var approveWords = regexp.MustCompile(`(?i)^\s*(approve|approved|yes|ok|okay|go|go ahead|do it|allow|confirm)\b`)

// Approves reports whether the owner's choice says yes to the card.
func Approves(choice, approveOption string) bool {
	c := strings.TrimSpace(choice)
	if c == "" {
		return false
	}
	if approveOption != "" && strings.EqualFold(c, strings.TrimSpace(approveOption)) {
		return true
	}
	return approveWords.MatchString(c)
}

// Answer applies the owner's choice to a pending card: on yes, one use of every
// attached action is granted to the hireling. Returns what was granted.
func Answer(ctx context.Context, g Grants, decision, choice string) ([]Act, error) {
	p, err := g.TakePending(ctx, decision)
	if err != nil {
		return nil, err
	}
	return grantCard(ctx, g, p, choice)
}

// ErrOtherAgent means the card of that decision is pending for another
// hireling: it is left in place, nothing is granted.
var ErrOtherAgent = errors.New("policy: this decision's card belongs to another hireling")

// AnswerFor is Answer for one hireling: the pending card must be agent's.
// A card pending for someone else stays where it is.
func AnswerFor(ctx context.Context, g Grants, decision, agent, choice string) ([]Act, error) {
	p, err := g.TakePending(ctx, decision)
	if err != nil {
		return nil, err
	}
	if p.Agent != agent {
		if perr := g.Pend(ctx, p); perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("%w (%s)", ErrOtherAgent, decision)
	}
	return grantCard(ctx, g, p, choice)
}

func grantCard(ctx context.Context, g Grants, p Pending, choice string) ([]Act, error) {
	if !Approves(choice, p.Approve) {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []Act
	for _, a := range p.Actions {
		if seen[a.Action] {
			continue
		}
		seen[a.Action] = true
		if err := g.Grant(ctx, p.Agent, a.Action, 1); err != nil {
			return out, err
		}
		out = append(out, a)
	}
	return out, nil
}

// ---- in-memory store (tests, REDIS_URL unset) --------------------------------

type grant struct {
	uses    int
	expires time.Time
}

// MemoryGrants is Grants in process memory.
type MemoryGrants struct {
	mu      sync.Mutex
	Now     func() time.Time
	blocked map[string][]Act
	pending map[string]Pending
	grants  map[string]grant
	counts  map[string]int
}

func NewMemoryGrants() *MemoryGrants {
	return &MemoryGrants{blocked: map[string][]Act{}, pending: map[string]Pending{}, grants: map[string]grant{}, counts: map[string]int{}}
}

func (m *MemoryGrants) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *MemoryGrants) Blocked(_ context.Context, agent string, a Act) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.At.IsZero() {
		a.At = m.now()
	}
	l := append([]Act{a}, m.blocked[agent]...)
	if len(l) > keepBlocked {
		l = l[:keepBlocked]
	}
	m.blocked[agent] = l
	return nil
}

func (m *MemoryGrants) RecentBlocked(_ context.Context, agent string, since time.Time) ([]Act, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return recent(m.blocked[agent], since, m.now()), nil
}

func recent(l []Act, since, now time.Time) []Act {
	var out []Act
	for _, a := range l {
		if !a.At.Before(since) && now.Sub(a.At) <= BlockedTTL {
			out = append(out, a)
		}
	}
	return out
}

func (m *MemoryGrants) Pend(_ context.Context, p Pending) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[p.Decision] = p
	return nil
}

func (m *MemoryGrants) TakePending(_ context.Context, decision string) (Pending, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[decision]
	if !ok {
		return p, ErrNoPending
	}
	delete(m.pending, decision)
	return p, nil
}

func (m *MemoryGrants) Grant(_ context.Context, agent, action string, uses int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := agent + "\x00" + action
	g := m.grants[k]
	if m.now().After(g.expires) {
		g.uses = 0
	}
	m.grants[k] = grant{uses: g.uses + uses, expires: m.now().Add(GrantTTL)}
	return nil
}

func (m *MemoryGrants) Consume(_ context.Context, agent, action string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := agent + "\x00" + action
	g, ok := m.grants[k]
	if !ok || g.uses <= 0 || m.now().After(g.expires) {
		return false, nil
	}
	g.uses--
	m.grants[k] = g
	return true, nil
}

func (m *MemoryGrants) Use(_ context.Context, agent, action, window string, limit int, _ time.Duration) (int, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := agent + "\x00" + action + "\x00" + window
	if m.counts[k] >= limit {
		return m.counts[k], false, nil
	}
	m.counts[k]++
	return m.counts[k], true, nil
}

func (m *MemoryGrants) Count(_ context.Context, agent, action, window string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[agent+"\x00"+action+"\x00"+window], nil
}

// ---- Redis store --------------------------------------------------------------

// RedisGrants keeps `tavern:gate:blocked:<agent>` (list, JSON, newest first),
// `tavern:gate:pending:<decision>` (JSON) and `tavern:gate:grant:<agent>:<action>`
// (a counter of uses), each with its TTL.
type RedisGrants struct {
	C      *redis.Client
	Prefix string // default "tavern"
	Now    func() time.Time
}

func (r *RedisGrants) key(parts ...string) string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":gate:" + strings.Join(parts, ":")
}

func (r *RedisGrants) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *RedisGrants) Blocked(ctx context.Context, agent string, a Act) error {
	if a.At.IsZero() {
		a.At = r.now()
	}
	raw, _ := json.Marshal(a)
	k := r.key("blocked", agent)
	_, err := r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LPush(ctx, k, raw)
		p.LTrim(ctx, k, 0, keepBlocked-1)
		p.Expire(ctx, k, BlockedTTL)
		return nil
	})
	return err
}

func (r *RedisGrants) RecentBlocked(ctx context.Context, agent string, since time.Time) ([]Act, error) {
	raws, err := r.C.LRange(ctx, r.key("blocked", agent), 0, keepBlocked-1).Result()
	if err != nil {
		return nil, err
	}
	var l []Act
	for _, s := range raws {
		var a Act
		if json.Unmarshal([]byte(s), &a) == nil {
			l = append(l, a)
		}
	}
	return recent(l, since, r.now()), nil
}

func (r *RedisGrants) Pend(ctx context.Context, p Pending) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return r.C.Set(ctx, r.key("pending", p.Decision), raw, PendingTTL).Err()
}

func (r *RedisGrants) TakePending(ctx context.Context, decision string) (Pending, error) {
	var p Pending
	raw, err := r.C.GetDel(ctx, r.key("pending", decision)).Result()
	if errors.Is(err, redis.Nil) {
		return p, ErrNoPending
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(raw), &p)
}

func (r *RedisGrants) Grant(ctx context.Context, agent, action string, uses int) error {
	k := r.key("grant", agent, action)
	_, err := r.C.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.IncrBy(ctx, k, int64(uses))
		p.Expire(ctx, k, GrantTTL)
		return nil
	})
	return err
}

// consumeScript takes one use only when one is left (atomic).
var consumeScript = redis.NewScript(`
local n = tonumber(redis.call('GET', KEYS[1]) or '0')
if n <= 0 then return 0 end
redis.call('DECR', KEYS[1])
return 1`)

func (r *RedisGrants) Consume(ctx context.Context, agent, action string) (bool, error) {
	n, err := consumeScript.Run(ctx, r.C, []string{r.key("grant", agent, action)}).Int()
	return n == 1, err
}

// useScript counts one use only while under the limit (atomic).
var useScript = redis.NewScript(`
local n = tonumber(redis.call('GET', KEYS[1]) or '0')
if n >= tonumber(ARGV[1]) then return {n, 0} end
n = redis.call('INCR', KEYS[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
return {n, 1}`)

func (r *RedisGrants) Use(ctx context.Context, agent, action, window string, limit int, ttl time.Duration) (int, bool, error) {
	res, err := useScript.Run(ctx, r.C, []string{r.key("count", agent, action, window)}, limit, int(ttl.Seconds())).Int64Slice()
	if err != nil || len(res) != 2 {
		return 0, false, err
	}
	return int(res[0]), res[1] == 1, nil
}

func (r *RedisGrants) Count(ctx context.Context, agent, action, window string) (int, error) {
	n, err := r.C.Get(ctx, r.key("count", agent, action, window)).Int()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}
