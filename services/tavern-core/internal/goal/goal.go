// Package goal is the tavern's one shared quest (feature C1; docs/concepts/quest-and-tasks.md).
// One record: title, target, current, currency, source.
// The bar only moves when the quest's source (C2) reports the
// aggregate number — hirelings never edit it. Only the number crosses the
// boundary: no statements, no customer data (hard rule in CLAUDE.md).
package goal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultKinds are the contribution kinds a task may declare (C3) when the
// goal does not list its own — ordered from closest to the goal to farthest,
// which is how B3 breaks ties between tasks of the same priority.
var DefaultKinds = []string{"revenue", "leads", "posts", "videos", "support"}

// Goal is the quest record.
type Goal struct {
	Title     string    `json:"title"`
	Target    float64   `json:"target"`
	Current   float64   `json:"current"`
	Currency  string    `json:"currency"`         // ISO code, e.g. USD
	Source    string    `json:"source"`           // who may report progress (default "owner")
	Kinds     []string  `json:"kinds,omitempty"`  // contribution kinds tasks may declare (C3), closest to the goal first (B3 tie-break)
	Period    string    `json:"period,omitempty"` // free text: "2026-Q4", "November"
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by,omitempty"`
}

// Percent reached, 0–100, one decimal; capped at 100 for the bar.
func (g Goal) Percent() float64 {
	if g.Target <= 0 {
		return 0
	}
	p := math.Round(g.Current/g.Target*1000) / 10
	if p > 100 {
		return 100
	}
	if p < 0 {
		return 0
	}
	return p
}

// AllowedKinds is Kinds or DefaultKinds.
func (g Goal) AllowedKinds() []string {
	if len(g.Kinds) > 0 {
		return g.Kinds
	}
	return DefaultKinds
}

// KindAllowed reports whether a task may declare this contribution kind.
func (g Goal) KindAllowed(kind string) bool {
	kind = strings.ToLower(strings.TrimSpace(kind))
	for _, k := range g.AllowedKinds() {
		if k == kind {
			return true
		}
	}
	return false
}

// Bar renders the quest line for logs and the CLI: `Quest: <title> — 12.5% [██░░░░░░░░] 15,000/120,000 BRL`.
func (g Goal) Bar() string {
	if g.Title == "" {
		return "Quest: (not set)"
	}
	n := int(g.Percent() / 10)
	return fmt.Sprintf("Quest: %s — %.1f%% [%s%s] %s/%s %s", g.Title, g.Percent(), strings.Repeat("█", n), strings.Repeat("░", 10-n), num(g.Current), num(g.Target), g.Currency)
}

func num(f float64) string {
	s := fmt.Sprintf("%.0f", math.Abs(f))
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if f < 0 {
		return "-" + string(out)
	}
	return string(out)
}

var (
	ErrNotSet    = errors.New("goal: no quest set")
	ErrNotSource = errors.New("goal: only the goal's source may report progress")
	ErrForbidden = errors.New("goal: only an approver may define the quest")
)

// Store persists the single record; Memory and Redis implement it.
type Store interface {
	Get(ctx context.Context) (*Goal, error) // ErrNotSet when empty
	Put(ctx context.Context, g *Goal) error
}

// Define is the body of PUT /goal (the owner or a planner).
type Define struct {
	Title    string   `json:"title"`
	Target   float64  `json:"target"`
	Currency string   `json:"currency,omitempty"`
	Source   string   `json:"source,omitempty"`
	Kinds    []string `json:"kinds,omitempty"`
	Period   string   `json:"period,omitempty"`
	// Current is accepted on define so the owner can seed the number once; later
	// moves go through Report.
	Current *float64 `json:"current,omitempty"`
}

// Validate normalises a definition.
func (d *Define) Validate() error {
	d.Title = strings.TrimSpace(d.Title)
	d.Currency = strings.ToUpper(strings.TrimSpace(d.Currency))
	d.Source = strings.ToLower(strings.TrimSpace(d.Source))
	switch {
	case d.Title == "":
		return errors.New("goal: title is required")
	case len([]rune(d.Title)) > 140:
		return errors.New("goal: title over 140 characters")
	case d.Target <= 0:
		return errors.New("goal: target must be > 0")
	case d.Currency != "" && len(d.Currency) != 3:
		return errors.New("goal: currency must be a 3-letter code")
	}
	if d.Currency == "" {
		d.Currency = "USD"
	}
	if d.Source == "" {
		d.Source = "owner"
	}
	seen := map[string]bool{}
	var kinds []string
	for _, k := range d.Kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || seen[k] {
			continue
		}
		if len(k) > 30 {
			return fmt.Errorf("goal: kind %q over 30 characters", k)
		}
		seen[k] = true
		kinds = append(kinds, k)
	}
	d.Kinds = kinds // order kept: the first kind is the one closest to the goal (B3 ties)
	return nil
}

// Service applies the rules on top of a Store.
type Service struct {
	Store Store
	Now   func() time.Time
	// Approver says who may define the quest (the owner, planners). nil = everyone (dev, tests).
	Approver func(agent string) bool
	mu       sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}

// Get returns the quest; ErrNotSet when none.
func (s *Service) Get(ctx context.Context) (*Goal, error) { return s.Store.Get(ctx) }

// Set defines (or redefines) the quest. Current is kept unless the body sets it.
func (s *Service) Set(ctx context.Context, by string, d Define) (*Goal, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if s.Approver != nil && !s.Approver(by) {
		return nil, ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.Store.Get(ctx)
	if errors.Is(err, ErrNotSet) {
		g = &Goal{}
	} else if err != nil {
		return nil, err
	}
	g.Title, g.Target, g.Currency, g.Source, g.Kinds, g.Period = d.Title, d.Target, d.Currency, d.Source, d.Kinds, d.Period
	if d.Current != nil {
		g.Current = *d.Current
	}
	g.UpdatedAt, g.UpdatedBy = s.now(), by
	return g, s.Store.Put(ctx, g)
}

// Report moves the bar: only the goal's source may call it, with the one
// aggregate number (absolute, not a delta).
func (s *Service) Report(ctx context.Context, by string, current float64) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, err := s.Store.Get(ctx)
	if err != nil {
		return nil, err
	}
	// X-Hireling is compared exactly. "Owner" is not the source "owner".
	if strings.TrimSpace(by) != g.Source {
		return nil, ErrNotSource
	}
	if math.IsNaN(current) || math.IsInf(current, 0) {
		return nil, errors.New("goal: current must be a number")
	}
	g.Current, g.UpdatedAt, g.UpdatedBy = current, s.now(), g.Source
	return g, s.Store.Put(ctx, g)
}

// Memory is the in-memory store (dev and tests).
type Memory struct {
	mu sync.Mutex
	g  *Goal
}

func NewMemory() *Memory { return &Memory{} }

func (m *Memory) Get(context.Context) (*Goal, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.g == nil {
		return nil, ErrNotSet
	}
	c := *m.g
	return &c, nil
}

func (m *Memory) Put(_ context.Context, g *Goal) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *g
	m.g = &c
	return nil
}

// Redis keeps the record as JSON under `<prefix>:goal`.
type Redis struct {
	C      *redis.Client
	Prefix string
}

func (r *Redis) key() string {
	p := r.Prefix
	if p == "" {
		p = "tavern"
	}
	return p + ":goal"
}

func (r *Redis) Get(ctx context.Context) (*Goal, error) {
	raw, err := r.C.Get(ctx, r.key()).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotSet
	}
	if err != nil {
		return nil, err
	}
	var g Goal
	return &g, json.Unmarshal(raw, &g)
}

func (r *Redis) Put(ctx context.Context, g *Goal) error {
	raw, err := json.Marshal(g)
	if err != nil {
		return err
	}
	return r.C.Set(ctx, r.key(), raw, 0).Err()
}
