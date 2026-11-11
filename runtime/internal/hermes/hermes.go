// Package hermes renders the Hermes Agent configuration for one hireling and
// runs single-query turns (`hermes chat -q`). Reference: the official image
// nousresearch/hermes-agent (data in /opt/data, s6 entrypoint) and
// https://hermes-agent.nousresearch.com/docs/user-guide/security (approvals).
package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"gopkg.in/yaml.v3"
)

// DeepSeekBaseURL is the OpenAI-compatible endpoint (A2).
const DeepSeekBaseURL = "https://api.deepseek.com/v1"

// DeniedCommands is A8 at the Hermes level: these never run, whatever the
// approval mode says (approvals.deny is checked before everything else).
var DeniedCommands = []string{
	"sudo *", "su *", "docker *", "podman *", "nerdctl *",
	"rm -rf /*", "rm -rf ~*", "rm -rf /tavern/shared*",
	"mkfs*", "dd *", "shutdown*", "reboot*", "systemctl *",
	"curl * | sh*", "curl * | bash*", "wget * | sh*", "wget * | bash*",
	"ssh *", "scp *", "nc *", "ncat *", "socat *",
	"env", "printenv*", "cat /proc/*/environ*", "cat /opt/data/.env*",
}

// Config is the subset of ~/.hermes/config.yaml the runtime owns. Everything
// else keeps Hermes defaults.
type Config struct {
	Model struct {
		Provider string `yaml:"provider"`
		Default  string `yaml:"default"`
		BaseURL  string `yaml:"base_url"`
		KeyEnv   string `yaml:"key_env"`
	} `yaml:"model"`
	Approvals struct {
		Mode            string   `yaml:"mode"`
		SingleQueryMode string   `yaml:"single_query_mode"`
		CronMode        string   `yaml:"cron_mode"`
		UnattendedMode  string   `yaml:"unattended_mode"`
		Timeout         int      `yaml:"timeout"`
		Deny            []string `yaml:"deny"`
	} `yaml:"approvals"`
	// Hermes tries these, in order, when the primary errors: 429/5xx after its
	// retries, 401/403/404 at once. Per turn: the next turn starts on the
	// primary again.
	FallbackProviders []FallbackProvider `yaml:"fallback_providers,omitempty"`
	Display           struct {
		Interface string `yaml:"interface"`
	} `yaml:"display"`
	// MCP servers (A12: Composio). Hermes registers their tools as
	// mcp_<server>_<tool> and expands ${VAR} in url/headers at connect time.
	MCPServers map[string]MCPServer `yaml:"mcp_servers,omitempty"`
	// D1: shell hooks. pre_tool_call runs `hireling gate` before every tool
	// call, fail-closed; consent is the allowlist WriteConfig writes next to
	// this file (hooks_auto_accept stays off).
	Hooks map[string][]ShellHook `yaml:"hooks,omitempty"`
}

// ShellHook is one entry of `hooks.<event>`.
type ShellHook struct {
	Matcher    string `yaml:"matcher,omitempty"`
	Command    string `yaml:"command"`
	Timeout    int    `yaml:"timeout,omitempty"`
	FailClosed bool   `yaml:"fail_closed,omitempty"`
}

// GateTimeout bounds one gate decision (the HTTP call to tavern-core is 4 s).
const GateTimeout = 15

// MCPServer is one streamable-HTTP entry of `mcp_servers`. Header values name
// environment variables (${VAR}); the secret itself is never written.
type MCPServer struct {
	URL            string            `yaml:"url"`
	Headers        map[string]string `yaml:"headers,omitempty"`
	Timeout        int               `yaml:"timeout,omitempty"`
	ConnectTimeout int               `yaml:"connect_timeout,omitempty"`
	Prompts        *bool             `yaml:"prompts,omitempty"`
	Resources      *bool             `yaml:"resources,omitempty"`
}

// FallbackProvider is one entry of `fallback_providers`.
type FallbackProvider struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// Options are environment facts the config depends on (never secret values).
type Options struct {
	// OpenRouter is true when OPENROUTER_API_KEY is set: only then the
	// OpenRouter fallback is written (without a key it would only add 401s).
	OpenRouter bool
	// MCP servers to add (A12). Values carry ${VAR} references, not secrets.
	MCP map[string]MCPServer
	// Gate is the pre_tool_call command (D1), e.g. `/usr/local/bin/hireling
	// gate --agent dev`. Empty = no gate (tests and local smoke runs only).
	Gate string
	// BaseURL overrides DeepSeekBaseURL when set (HIRELING_MODEL_BASE_URL).
	// Empty keeps https://api.deepseek.com/v1. The value is an OpenAI-compatible
	// endpoint, so a test can point a turn at a stub.
	BaseURL string
}

// BaseURL returns the OpenAI-compatible model endpoint. override replaces
// DeepSeekBaseURL when it is non-empty. A trailing slash is dropped.
func BaseURL(override string) string {
	v := strings.TrimSpace(override)
	if v == "" {
		return DeepSeekBaseURL
	}
	return strings.TrimRight(v, "/")
}

// Render builds the config for a profile. Model routing is the profile's
// `model` field (A2); nothing else chooses the model.
func Render(p *profile.Profile, o Options) *Config {
	c := &Config{}
	c.Model.Provider = "custom"
	c.Model.Default = profile.Models[p.Model]
	c.Model.BaseURL = BaseURL(o.BaseURL)
	c.Model.KeyEnv = "DEEPSEEK_API_KEY"
	// A8: command approval stays on. Unattended turns can never wait for a
	// human, so a dangerous command is denied instead of approved.
	c.Approvals.Mode = "manual"
	c.Approvals.SingleQueryMode = "deny"
	c.Approvals.CronMode = "deny"
	c.Approvals.UnattendedMode = "deny"
	c.Approvals.Timeout = 30
	c.Approvals.Deny = append([]string(nil), DeniedCommands...)
	c.Display.Interface = "cli"
	if m := p.FallbackModel(); o.OpenRouter && m != "" {
		c.FallbackProviders = []FallbackProvider{{Provider: "openrouter", Model: m}}
	}
	if len(o.MCP) > 0 {
		c.MCPServers = o.MCP
	}
	if o.Gate != "" {
		// No matcher: every tool call, so a tool added by a newer Hermes is
		// gated too (the gate lets local tools through without a network call).
		c.Hooks = map[string][]ShellHook{"pre_tool_call": {{Command: o.Gate, Timeout: GateTimeout, FailClosed: true}}}
	}
	return c
}

// AllowlistFile is Hermes' shell-hook consent file under HERMES_HOME.
const AllowlistFile = "shell-hooks-allowlist.json"

// WriteConfig writes config.yaml under home (HERMES_HOME, /opt/data in the
// image) and the system prompt next to it. It never writes secrets.
func WriteConfig(home string, p *profile.Profile, o Options) error {
	if err := os.MkdirAll(home, 0o750); err != nil {
		return err
	}
	out, err := yaml.Marshal(Render(p, o))
	if err != nil {
		return err
	}
	header := "# Generated by hireling from agents/" + p.ID + "/profile.yaml — do not edit; secrets live in the environment.\n"
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), append([]byte(header), out...), 0o640); err != nil {
		return err
	}
	// Consent for exactly the hooks we configured: the gateway never prompts,
	// so a hook missing from this file would be skipped (D1 would be off).
	type approval struct {
		Event   string `json:"event"`
		Command string `json:"command"`
	}
	allow := struct {
		Approvals []approval `json:"approvals"`
	}{Approvals: []approval{}}
	if o.Gate != "" {
		allow.Approvals = append(allow.Approvals, approval{Event: "pre_tool_call", Command: o.Gate})
	}
	js, err := json.MarshalIndent(allow, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(home, AllowlistFile), append(js, '\n'), 0o640); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, "hireling-prompt.md"), []byte(p.Prompt), 0o640)
}

// Runner executes one Hermes turn.
type Runner struct {
	Bin     string        // defaults to "hermes"; tests point it at a script
	Home    string        // HERMES_HOME
	Prompt  string        // system prompt, prepended to every query
	Timeout time.Duration // hard stop for one turn
	Extra   []string      // extra args before -q
}

// Run performs `hermes chat -q <prompt + text>` and returns its stdout.
func (r *Runner) Run(ctx context.Context, text string) (string, error) {
	bin := r.Bin
	if bin == "" {
		bin = "hermes"
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	query := text
	if strings.TrimSpace(r.Prompt) != "" {
		query = strings.TrimSpace(r.Prompt) + "\n\n---\n\nCommand from the front desk:\n" + text
	}
	args := append([]string{"chat"}, r.Extra...)
	args = append(args, "-q", query)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), "HERMES_HOME="+r.Home, "TERM=dumb", "NO_COLOR=1")
	// After the deadline, do not wait for grandchildren holding the pipes open.
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return stdout.String(), fmt.Errorf("hermes: turn exceeded %s", timeout)
	}
	if err != nil {
		tail := strings.TrimSpace(stderr.String())
		if len(tail) > 300 {
			tail = tail[len(tail)-300:]
		}
		return stdout.String(), fmt.Errorf("hermes: %w: %s", err, tail)
	}
	return stdout.String(), nil
}
