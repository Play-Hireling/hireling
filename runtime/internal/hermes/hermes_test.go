package hermes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/profile"
	"gopkg.in/yaml.v3"
)

func TestRenderRoutesModelFromProfile(t *testing.T) {
	c := Render(&profile.Profile{ID: "x", Model: "deepseek-v4-pro"}, Options{})
	if c.Model.Default != "deepseek-v4-pro" || c.Model.Provider != "custom" || c.Model.BaseURL != DeepSeekBaseURL {
		t.Fatalf("%+v", c.Model)
	}
	if c.Approvals.SingleQueryMode != "deny" || len(c.Approvals.Deny) == 0 {
		t.Fatalf("approvals must stay on: %+v", c.Approvals)
	}
}

func TestWriteConfigHasNoSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DEEPSEEK_API_KEY", "sk-this-must-not-appear-0123456789")
	p := &profile.Profile{ID: "x", Model: "deepseek-v4-flash", Prompt: "be nice"}
	if err := WriteConfig(home, p, Options{OpenRouter: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if strings.Contains(string(raw), "sk-this") {
		t.Fatal("secret leaked into config.yaml")
	}
	if !strings.Contains(string(raw), "key_env: DEEPSEEK_API_KEY") {
		t.Fatal(string(raw))
	}
}

func TestFallbackOnlyWithKey(t *testing.T) {
	p := &profile.Profile{ID: "x", Model: "deepseek-v4-pro"}
	if c := Render(p, Options{}); len(c.FallbackProviders) != 0 {
		t.Fatal("no key, no fallback")
	}
	c := Render(p, Options{OpenRouter: true})
	if len(c.FallbackProviders) != 1 || c.FallbackProviders[0] != (FallbackProvider{"openrouter", "deepseek/deepseek-v4-pro"}) {
		t.Fatalf("%+v", c.FallbackProviders)
	}
	p.Fallback.Model = "deepseek/deepseek-v4-flash"
	if c := Render(p, Options{OpenRouter: true}); c.FallbackProviders[0].Model != "deepseek/deepseek-v4-flash" {
		t.Fatal("override ignored")
	}
	p.Fallback.Disabled = true
	if c := Render(p, Options{OpenRouter: true}); len(c.FallbackProviders) != 0 {
		t.Fatal("disabled fallback still rendered")
	}
}

func TestFallbackYAMLKey(t *testing.T) {
	home := t.TempDir()
	if err := WriteConfig(home, &profile.Profile{ID: "x", Model: "deepseek-v4-flash"}, Options{OpenRouter: true}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if !strings.Contains(string(raw), "fallback_providers:\n    - provider: openrouter\n      model: deepseek/deepseek-v4-flash") {
		t.Fatal(string(raw))
	}
}

func fakeHermes(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "hermes")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestRunPassesPromptAndQuery(t *testing.T) {
	bin := fakeHermes(t, `[ "$1" = chat ] && [ "$2" = -q ] || exit 9
printf '%s' "$3"`)
	r := &Runner{Bin: bin, Home: t.TempDir(), Prompt: "SYSTEM", Timeout: 5 * time.Second}
	out, err := r.Run(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "SYSTEM") || !strings.HasSuffix(out, "hello") {
		t.Fatalf("query = %q", out)
	}
}

func TestRunTimeout(t *testing.T) {
	bin := fakeHermes(t, "sleep 5")
	r := &Runner{Bin: bin, Home: t.TempDir(), Timeout: 200 * time.Millisecond}
	if _, err := r.Run(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("err = %v", err)
	}
}

// TestMCPServerRendersEnvReference: the Composio server (A12) names the key by
// ${VAR}; Hermes expands it at connect time, so the value never hits the disk.
func TestMCPServerRendersEnvReference(t *testing.T) {
	home := t.TempDir()
	off := false
	o := Options{MCP: map[string]MCPServer{"composio": {URL: "https://backend.composio.dev/tool_router/trs_x/mcp", Headers: map[string]string{"x-api-key": "${COMPOSIO_API_KEY}"}, Timeout: 120, Prompts: &off}}}
	if err := WriteConfig(home, &profile.Profile{ID: "x", Model: "deepseek-v4-flash"}, o); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	want := "mcp_servers:\n    composio:\n        url: https://backend.composio.dev/tool_router/trs_x/mcp\n        headers:\n            x-api-key: ${COMPOSIO_API_KEY}\n        timeout: 120\n        prompts: false\n"
	if !strings.Contains(string(raw), want) {
		t.Fatal(string(raw))
	}
	if c := Render(&profile.Profile{ID: "x", Model: "deepseek-v4-flash"}, Options{}); c.MCPServers != nil {
		t.Fatal("no hands, no mcp_servers")
	}
}

// D1: the gate hook is configured fail-closed on every tool call, and the
// consent allowlist approves exactly that command (the gateway never prompts).
func TestGateHookAndConsent(t *testing.T) {
	home := t.TempDir()
	gate := "/usr/local/bin/hireling gate --agent dev --home /opt/data --tavern http://tavern-core:8080"
	if err := WriteConfig(home, &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}, Options{Gate: gate}); err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Hooks map[string][]map[string]any `yaml:"hooks"`
		Auto  *bool                       `yaml:"hooks_auto_accept"`
	}
	raw, _ := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	h := cfg.Hooks["pre_tool_call"]
	if len(h) != 1 || h[0]["command"] != gate || h[0]["fail_closed"] != true || h[0]["matcher"] != nil {
		t.Fatalf("pre_tool_call hook = %v\n%s", h, raw)
	}
	if cfg.Auto != nil {
		t.Fatal("hooks_auto_accept must not be set: consent is the allowlist")
	}
	var allow struct {
		Approvals []struct{ Event, Command string } `json:"approvals"`
	}
	js, err := os.ReadFile(filepath.Join(home, AllowlistFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(js, &allow); err != nil || len(allow.Approvals) != 1 ||
		allow.Approvals[0].Event != "pre_tool_call" || allow.Approvals[0].Command != gate {
		t.Fatalf("allowlist = %s (%v)", js, err)
	}
	// Without a gate command nothing is approved (and no hook is written).
	if err := WriteConfig(home, &profile.Profile{ID: "dev", Model: "deepseek-v4-flash"}, Options{}); err != nil {
		t.Fatal(err)
	}
	js, _ = os.ReadFile(filepath.Join(home, AllowlistFile))
	if !strings.Contains(string(js), `"approvals": []`) {
		t.Fatalf("stale approval left behind: %s", js)
	}
}
