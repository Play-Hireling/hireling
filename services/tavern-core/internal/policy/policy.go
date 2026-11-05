// Package policy is the autonomy policy gate (docs/architecture.md, "Autonomy"):
// for every action a hireling wants to take it answers Free, Capped or the owner.
// This ships the parts that never change — the hard deny list (D5) and the
// data rule (D6). Caps (D2) are counted per UTC day or ISO week by the HTTP
// layer (Counter) when the hireling is about to act; budgets (D7, D8) follow.
package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

// Level is the gate's answer.
type Level string

const (
	Free   Level = "free"
	Capped Level = "capped"
	Owner  Level = "owner"
)

// SensitiveSystems is D6 (the list lives in runtime/profile, shared with the
// profile loader and the egress proxy).
var SensitiveSystems = profile.SensitiveSystems

// Request is POST /actions/check.
type Request struct {
	Agent  string `json:"agent"`
	Action string `json:"action"`           // e.g. draft, publish_owned_channel, payment
	Target string `json:"target,omitempty"` // system, channel or resource (free text)
	// Use is true when the hireling is about to act (the D1 hook): a grant
	// from the owner is consumed and a block is remembered for the next card.
	Use bool `json:"use,omitempty"`
	// D4: context for the Autonomy Log, sent by the hook (free text, clipped).
	Why  string `json:"why,omitempty"`
	Turn string `json:"turn,omitempty"`
	Task string `json:"task,omitempty"`
}

// Decision is the gate's answer.
type Decision struct {
	Level  Level  `json:"level"`
	Reason string `json:"reason"`
	// Limit is the cap when Level is Capped: uses per Period. 0 means none
	// without the owner (a cap of zero), never "unlimited".
	Limit  int    `json:"limit"`
	Period string `json:"period,omitempty"` // day | week
	// Used and Counted are set when a "use" check counted this action (D2).
	Used    int  `json:"used,omitempty"`
	Counted bool `json:"counted,omitempty"`
}

// Gate evaluates requests against loaded profiles.
type Gate struct {
	mu       sync.RWMutex
	profiles map[string]*profile.Profile
}

// LoadDir loads every agents/<id>/profile.yaml under dir.
func LoadDir(dir string) (*Gate, error) {
	g := &Gate{profiles: map[string]*profile.Profile{}}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := profile.Load(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		g.profiles[p.ID] = p
	}
	return g, nil
}

// New builds a gate from profiles already in memory (tests).
func New(ps ...*profile.Profile) *Gate {
	g := &Gate{profiles: map[string]*profile.Profile{}}
	for _, p := range ps {
		g.profiles[p.ID] = p
	}
	return g
}

// Profile returns a loaded profile or nil.
func (g *Gate) Profile(id string) *profile.Profile {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.profiles[id]
}

// Agents lists loaded ids.
func (g *Gate) Agents() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ids := make([]string, 0, len(g.profiles))
	for id := range g.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Check answers one request. Order matters: deny list, data rule, profile.
func (g *Gate) Check(r Request) Decision {
	action := strings.ToLower(strings.TrimSpace(r.Action))
	// D5 by category, so a renamed action (`send_payment`, `delete_post`) is caught too.
	if c := profile.HardDenyCategory(action); c != "" {
		return Decision{Level: Owner, Reason: "hard deny list (D5): " + c}
	}
	if sys := sensitive(r.Target); sys != "" {
		return Decision{Level: Owner, Reason: "data rule (D6): target touches " + sys}
	}
	p := g.Profile(r.Agent)
	if p == nil {
		return Decision{Level: Owner, Reason: "unknown agent " + r.Agent}
	}
	for _, a := range p.Autonomy.OwnerDecides {
		if action == a {
			return Decision{Level: Owner, Reason: "profile: owner_decides"}
		}
	}
	if caps, ok := p.Autonomy.Capped[action]; ok {
		limit, period := Cap(caps)
		return Decision{Level: Capped, Reason: "profile: capped", Limit: limit, Period: period}
	}
	for _, a := range p.Autonomy.Free {
		if action == a {
			return Decision{Level: Free, Reason: "profile: free"}
		}
	}
	// Anything the profile does not name is not free.
	return Decision{Level: Owner, Reason: "action not in profile"}
}

// sensitive is the D6 check on a free-text target: a sensitive word, or a
// known finance/KYC/legal provider host anywhere in it.
func sensitive(target string) string {
	if w := profile.SensitiveWord(target); w != "" {
		return w
	}
	for _, f := range strings.Fields(strings.ToLower(target)) {
		f = strings.Trim(f, "/:,;()[]<>\"'")
		if i := strings.Index(f, "://"); i >= 0 {
			f = f[i+3:]
		}
		if i := strings.IndexAny(f, "/:"); i >= 0 {
			f = f[:i]
		}
		if why := profile.SensitiveHost(f); why != "" && strings.Contains(f, ".") {
			return why
		}
	}
	return ""
}

// Cap reads a capped entry (`{per_day: 3}` or `{per_week: 1}`). A cap given
// both ways keeps the stricter daily one; an entry with neither is a cap of 0.
func Cap(caps map[string]int) (int, string) {
	if n, ok := caps["per_day"]; ok {
		return max(n, 0), "day"
	}
	if n, ok := caps["per_week"]; ok {
		return max(n, 0), "week"
	}
	return 0, "day"
}

// PeriodKey names the counting window that contains t (UTC): 2026-11-05 or 2026-W45.
func PeriodKey(period string, t time.Time) string {
	t = t.UTC()
	if period == "week" {
		y, w := t.ISOWeek()
		return fmt.Sprintf("%d-W%02d", y, w)
	}
	return t.Format("2006-01-02")
}
