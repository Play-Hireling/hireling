// Package profile loads agents/<id>/profile.yaml — the one file that defines a
// hireling (docs/reference/profile.md).
package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// HardDeny is D5: actions that always require the owner, whatever the profile says.
var HardDeny = []string{"payment", "contract", "customer_data", "delete", "production_critical"}

// Models accepted by A2. The key is what the profile says; the value is the id
// sent to the DeepSeek API.
var Models = map[string]string{
	"deepseek-v4-flash": "deepseek-v4-flash",
	"deepseek-v4-pro":   "deepseek-v4-pro",
}

// OpenRouterModels is the same model on OpenRouter, used as the fallback
// provider when DeepSeek's API errors (Hermes `fallback_providers`).
var OpenRouterModels = map[string]string{
	"deepseek-v4-flash": "deepseek/deepseek-v4-flash",
	"deepseek-v4-pro":   "deepseek/deepseek-v4-pro",
}

var idRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Budget struct {
	TokensPerDay   int `yaml:"tokens_per_day"`
	WebCallsPerDay int `yaml:"web_calls_per_day"`
}

type Autonomy struct {
	Free         []string                  `yaml:"free"`
	Capped       map[string]map[string]int `yaml:"capped"`
	OwnerDecides []string                  `yaml:"owner_decides"`
}

type Ledger struct {
	Read  []string `yaml:"read"`
	Write []string `yaml:"write"`
}

// Fallback overrides the OpenRouter fallback. Empty = same model on OpenRouter.
type Fallback struct {
	Disabled bool   `yaml:"disabled"`
	Model    string `yaml:"model"` // OpenRouter slug, e.g. deepseek/deepseek-v4-pro
}

// Network is A8's egress allowlist for this role. Every hireling can always
// reach the model providers and the front desk (BaseAllow); Allow adds the
// hosts the role needs (a subdomain matches its parent: `github.com` also
// allows `api.github.com`). Everything else is refused by the egress proxy.
type Network struct {
	Allow []string `yaml:"allow"`
}

// BaseAllow are the hosts every hireling needs, whatever the profile says.
var BaseAllow = []string{"api.deepseek.com", "openrouter.ai"}

// BlockedHosts can never be allowed by a profile (D6, the data rule: no
// hireling reaches finance, KYC or legal systems, and none talks to the Docker
// or container control planes).
var BlockedHosts = []string{"localhost", "127.0.0.1", "0.0.0.0", "host.docker.internal", "metadata.google.internal", "169.254.169.254"}

// Hands is A12, the first layer of the hands: the Composio
// toolkits (slugs such as github, googledocs, instagram) whose accounts this
// role may use. Empty = no account access. The accounts are the owner's, connected
// once per tavern on Composio's page; the profile only limits which ones this
// role sees. Toolkits of finance, KYC, signature or tax services are refused (D6).
type Hands struct {
	Toolkits []string `yaml:"toolkits"`
}

// ComposioHost is added to the egress allowlist of a role with toolkits.
const ComposioHost = "backend.composio.dev"

var toolkitRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

type Runtime struct {
	PollSeconds    int    `yaml:"poll_seconds"`
	TurnTTLMinutes int    `yaml:"turn_ttl_minutes"`
	Screen         string `yaml:"screen"`
	// EscalateAfter is how many failed turns in a row on one task open a
	// "Waiting on you" decision by themselves (B11). Default 3.
	EscalateAfter int `yaml:"escalate_after"`
	// ChatReasoning is the reasoning effort of a turn that is conversation —
	// no task, not the planner (A13 light chat mode). Default "low"; "default"
	// keeps the model's own setting for every turn.
	ChatReasoning string `yaml:"chat_reasoning"`
}

type Profile struct {
	ID             string `yaml:"id"`
	DisplayNameKey string `yaml:"display_name_key"`
	// DeskID is the agent id the Front Desk knows this hireling as, for desks
	// that already name their agents differently. Defaults to ID (tavern-core's
	// own desk uses the Hireling ids directly).
	DeskID     string   `yaml:"desk_id"`
	Model      string   `yaml:"model"`
	HomeServer string   `yaml:"home_server"`
	Region     string   `yaml:"region"`
	Sprite     string   `yaml:"sprite"`
	Budget     Budget   `yaml:"budget"`
	Autonomy   Autonomy `yaml:"autonomy"`
	Tools      []string `yaml:"tools"`
	Secrets    []string `yaml:"secrets"`
	Ledger     Ledger   `yaml:"ledger"`
	Runtime    Runtime  `yaml:"runtime"`
	Fallback   Fallback `yaml:"fallback"`
	Network    Network  `yaml:"network"`
	Hands      Hands    `yaml:"hands"`
	// Planner marks the hireling that turns requests into tasks for the party
	// (B2: the Innkeeper). Its prompt gets the plan format appended and the
	// runtime acts on the plan block of its answers.
	Planner bool `yaml:"planner"`

	// Prompt is agents/<id>/prompt.md, loaded next to the profile.
	Prompt string `yaml:"-"`
}

// Load reads <dir>/profile.yaml and <dir>/prompt.md and validates them.
func Load(dir string) (*Profile, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "profile.yaml"))
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("profile.yaml: %w", err)
	}
	if prompt, err := os.ReadFile(filepath.Join(dir, "prompt.md")); err == nil {
		p.Prompt = string(prompt)
	}
	p.applyDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Profile) applyDefaults() {
	if p.DeskID == "" {
		p.DeskID = p.ID
	}
	if p.Runtime.PollSeconds <= 0 {
		p.Runtime.PollSeconds = 20
	}
	if p.Runtime.TurnTTLMinutes <= 0 {
		p.Runtime.TurnTTLMinutes = 15
	}
	if p.Runtime.ChatReasoning == "" {
		p.Runtime.ChatReasoning = "low"
	}
	if p.Runtime.EscalateAfter <= 0 {
		p.Runtime.EscalateAfter = 3
	}
	if p.Runtime.Screen == "" {
		p.Runtime.Screen = "code"
	}
	// D5: the hard deny list is always present.
	have := map[string]bool{}
	for _, a := range p.Autonomy.OwnerDecides {
		have[a] = true
	}
	for _, a := range HardDeny {
		if !have[a] {
			p.Autonomy.OwnerDecides = append(p.Autonomy.OwnerDecides, a)
		}
	}
}

// Validate enforces the invariants every other component relies on.
func (p *Profile) Validate() error {
	if !idRe.MatchString(p.ID) {
		return fmt.Errorf("profile id %q: must be a lowercase slug", p.ID)
	}
	if !idRe.MatchString(p.DeskID) {
		return fmt.Errorf("profile %s: desk_id %q must be a lowercase slug", p.ID, p.DeskID)
	}
	if _, ok := Models[p.Model]; !ok {
		return fmt.Errorf("profile %s: unknown model %q (A2 accepts deepseek-v4-flash or deepseek-v4-pro)", p.ID, p.Model)
	}
	free := map[string]bool{}
	for _, a := range p.Autonomy.Free {
		free[a] = true
		if c := HardDenyCategory(a); c != "" {
			return fmt.Errorf("profile %s: free action %q is %s — on the hard deny list, always the owner's (D5)", p.ID, a, c)
		}
	}
	for a := range p.Autonomy.Capped {
		if c := HardDenyCategory(a); c != "" {
			return fmt.Errorf("profile %s: capped action %q is %s — on the hard deny list, always the owner's (D5)", p.ID, a, c)
		}
	}
	for _, h := range p.Network.Allow {
		h = strings.ToLower(strings.TrimSpace(h))
		if !hostRe.MatchString(h) {
			return fmt.Errorf("profile %s: network.allow %q is not a host name (no scheme, path, port or wildcard)", p.ID, h)
		}
		for _, b := range BlockedHosts {
			if h == b {
				return fmt.Errorf("profile %s: network.allow %q can never be allowed (A8)", p.ID, h)
			}
		}
		if why := SensitiveHost(h); why != "" {
			return fmt.Errorf("profile %s: network.allow %q is a %s system — no role reaches finance, KYC or legal systems (D6)", p.ID, h, why)
		}
	}
	seen := map[string]bool{}
	for _, t := range p.Hands.Toolkits {
		if !toolkitRe.MatchString(t) {
			return fmt.Errorf("profile %s: hands.toolkits %q is not a Composio toolkit slug (lowercase, e.g. github)", p.ID, t)
		}
		if seen[t] {
			return fmt.Errorf("profile %s: hands.toolkits lists %q twice", p.ID, t)
		}
		seen[t] = true
		if why := SensitiveToolkit(t); why != "" {
			return fmt.Errorf("profile %s: hands.toolkits %q is a %s — no role reaches finance, KYC or legal systems (D6)", p.ID, t, why)
		}
	}
	return nil
}

var hostRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// AllowedHosts is the full egress allowlist of this hireling: BaseAllow plus
// the profile's network.allow, lower-cased, de-duplicated, sorted.
func (p *Profile) AllowedHosts() []string {
	seen := map[string]bool{}
	var out []string
	hosts := append(append([]string(nil), BaseAllow...), p.Network.Allow...)
	if len(p.Hands.Toolkits) > 0 {
		hosts = append(hosts, ComposioHost)
	}
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// HostAllowed reports whether host (no port) is on the list: equal to an entry
// or a subdomain of one.
func HostAllowed(host string, allow []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, a := range allow {
		if host == a || strings.HasSuffix(host, "."+a) {
			return true
		}
	}
	return false
}

// FallbackModel is the OpenRouter model to use when the primary errors, or ""
// when the profile disables the fallback.
func (p *Profile) FallbackModel() string {
	if p.Fallback.Disabled {
		return ""
	}
	if p.Fallback.Model != "" {
		return p.Fallback.Model
	}
	return OpenRouterModels[p.Model]
}

// TokenEnv is the name of the front desk token variable for this hireling.
func (p *Profile) TokenEnv() string {
	return "FRONTDESK_TOKEN_" + upperSlug(p.ID)
}

func upperSlug(s string) string {
	b := []byte(s)
	for i, c := range b {
		switch {
		case c >= 'a' && c <= 'z':
			b[i] = c - 'a' + 'A'
		case c == '-':
			b[i] = '_'
		}
	}
	return string(b)
}

// List loads every agents/<id>/profile.yaml under dir (invalid ones skipped), sorted by id.
func List(dir string) ([]*Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ps []*Profile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p, err := Load(filepath.Join(dir, e.Name())); err == nil {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].ID < ps[j].ID })
	return ps, nil
}

// ListIDs returns the ids of every agents/<id>/profile.yaml under dir, sorted:
// the party, as the planner (B2) knows it. Profiles that fail to load are
// skipped (the service that owns them reports the error).
func ListIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p, err := Load(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
