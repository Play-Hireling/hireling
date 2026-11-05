package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRepoTestProfile(t *testing.T) {
	p, err := Load(filepath.Join("..", "..", "agents", "test"))
	if err != nil {
		t.Fatalf("agents/test must load: %v", err)
	}
	if p.ID != "test" || p.Model != "deepseek-v4-flash" {
		t.Fatalf("unexpected profile %+v", p)
	}
	if p.Prompt == "" {
		t.Fatal("prompt.md not loaded")
	}
	if p.DeskID != "test" {
		t.Fatalf("desk_id = %s", p.DeskID)
	}
	if p.TokenEnv() != "FRONTDESK_TOKEN_TEST" {
		t.Fatalf("token env = %s", p.TokenEnv())
	}
}

func TestDeskIDDefaultsToID(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "profile.yaml", "id: dev\nmodel: deepseek-v4-flash\n")
	p, err := Load(dir)
	if err != nil || p.DeskID != "dev" {
		t.Fatalf("%v %+v", err, p)
	}
}

func TestHardDenyIsAlwaysPresent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "profile.yaml", "id: x\nmodel: deepseek-v4-pro\nautonomy:\n  owner_decides: []\n")
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Autonomy.OwnerDecides) != len(HardDeny) {
		t.Fatalf("owner_decides = %v", p.Autonomy.OwnerDecides)
	}
}

func TestFreeCannotContainHardDeny(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "profile.yaml", "id: x\nmodel: deepseek-v4-flash\nautonomy:\n  free: [payment]\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected D5 violation")
	}
}

func TestUnknownModelRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "profile.yaml", "id: x\nmodel: gpt-4\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("expected unknown model error")
	}
}

func TestFallbackModel(t *testing.T) {
	p := Profile{Model: "deepseek-v4-flash"}
	if p.FallbackModel() != "deepseek/deepseek-v4-flash" {
		t.Fatal(p.FallbackModel())
	}
	p.Fallback.Disabled = true
	if p.FallbackModel() != "" {
		t.Fatal("disabled")
	}
}

func TestTokenEnvSlug(t *testing.T) {
	p := Profile{ID: "paid-traffic"}
	if p.TokenEnv() != "FRONTDESK_TOKEN_PAID_TRAFFIC" {
		t.Fatal(p.TokenEnv())
	}
}

func TestNetworkAllowlist(t *testing.T) {
	p := &Profile{ID: "x", DeskID: "x", Model: "deepseek-v4-flash"}
	p.Network.Allow = []string{"GitHub.com", "api.deepseek.com", "docs.youtube.com"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	got := p.AllowedHosts()
	want := []string{"api.deepseek.com", "docs.youtube.com", "github.com", "openrouter.ai"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allowed = %v", got)
	}
	for host, ok := range map[string]bool{"api.github.com": true, "github.com": true, "evilgithub.com": false, "youtube.com": false, "api.deepseek.com.": true, "pypi.org": false} {
		if HostAllowed(host, got) != ok {
			t.Fatalf("HostAllowed(%s) != %v", host, ok)
		}
	}
	for _, bad := range []string{"https://github.com", "github.com/path", "*.github.com", "localhost", "127.0.0.1", "github.com:443"} {
		p.Network.Allow = []string{bad}
		if err := p.Validate(); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
}

// TestHands (A12): toolkits are slugs, never finance/KYC/legal (D6), and a role
// with hands may reach Composio through the egress proxy.
func TestHands(t *testing.T) {
	p := &Profile{ID: "x", DeskID: "x", Model: "deepseek-v4-flash"}
	for _, h := range p.AllowedHosts() {
		if h == ComposioHost {
			t.Fatal("no toolkits, no Composio host")
		}
	}
	p.Hands.Toolkits = []string{"github", "googledocs", "instagram", "googleads"}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !HostAllowed("backend.composio.dev", p.AllowedHosts()) {
		t.Fatal("hands need backend.composio.dev")
	}
	for _, bad := range []string{"stripe", "paypal", "quickbooks", "docusign", "mercadopago", "xero", "brex", "zoho_books", "gusto", "GitHub", "git hub", "github"} {
		p.Hands.Toolkits = []string{"github", bad}
		if err := p.Validate(); err == nil {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	if SensitiveToolkit("googlesheets") != "" || SensitiveToolkit("linkedin") != "" {
		t.Fatal("ordinary toolkits are allowed")
	}
}
