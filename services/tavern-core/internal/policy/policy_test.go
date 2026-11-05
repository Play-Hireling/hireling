package policy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

func testProfile() *profile.Profile {
	p := &profile.Profile{ID: "scriptwriter", Model: "deepseek-v4-flash"}
	p.Autonomy.Free = []string{"draft", "research"}
	p.Autonomy.Capped = map[string]map[string]int{"publish_owned_channel": {"per_day": 3}}
	p.Autonomy.OwnerDecides = append([]string{"publish_as_owner"}, profile.HardDeny...)
	return p
}

func TestHardDenyWinsOverProfile(t *testing.T) {
	p := testProfile()
	p.Autonomy.Free = append(p.Autonomy.Free, "payment") // never valid, but the gate must not trust it
	g := New(p)
	for _, a := range profile.HardDeny {
		if d := g.Check(Request{Agent: "scriptwriter", Action: a}); d.Level != Owner {
			t.Fatalf("%s -> %+v", a, d)
		}
	}
}

func TestDataRule(t *testing.T) {
	g := New(testProfile())
	cases := map[string]Level{
		"blog cms":           Free,
		"Finance dashboard":  Owner,
		"kyc/acme-kyc":       Owner,
		"contabilidade 2026": Owner,
		"youtube channel":    Free,
	}
	for target, want := range cases {
		if d := g.Check(Request{Agent: "scriptwriter", Action: "draft", Target: target}); d.Level != want {
			t.Errorf("target %q -> %+v, want %s", target, d, want)
		}
	}
}

func TestProfileLevels(t *testing.T) {
	g := New(testProfile())
	if d := g.Check(Request{Agent: "scriptwriter", Action: "draft"}); d.Level != Free {
		t.Fatal(d)
	}
	if d := g.Check(Request{Agent: "scriptwriter", Action: "publish_owned_channel"}); d.Level != Capped || d.Limit != 3 {
		t.Fatal(d)
	}
	if d := g.Check(Request{Agent: "scriptwriter", Action: "publish_as_owner"}); d.Level != Owner {
		t.Fatal(d)
	}
	if d := g.Check(Request{Agent: "scriptwriter", Action: "something_new"}); d.Level != Owner {
		t.Fatal("unlisted actions must not be free")
	}
	if d := g.Check(Request{Agent: "ghost", Action: "draft"}); d.Level != Owner {
		t.Fatal("unknown agents must not be free")
	}
}

func TestLoadRepoAgents(t *testing.T) {
	g, err := LoadDir(filepath.Join("..", "..", "..", "..", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	if g.Profile("test") == nil {
		t.Fatalf("agents/test not loaded: %v", g.Agents())
	}
}

// D5 — renamed actions in every category are the owner's even when a profile frees them.
func TestHardDenyByCategory(t *testing.T) {
	p := testProfile()
	p.Autonomy.Free = append(p.Autonomy.Free, "send_payment", "delete_post", "sign_contract", "export_customers", "deploy_production")
	g := New(p)
	for _, a := range []string{"send_payment", "pix-transfer", "delete_post", "drop table", "sign_contract", "export_customers", "deploy_production", "run migration"} {
		if d := g.Check(Request{Agent: "scriptwriter", Action: a}); d.Level != Owner || !strings.HasPrefix(d.Reason, "hard deny list (D5)") {
			t.Errorf("%s -> %+v", a, d)
		}
	}
	if d := g.Check(Request{Agent: "scriptwriter", Action: "publish_owned_channel"}); d.Level != Capped {
		t.Fatalf("capped action caught by D5: %+v", d)
	}
}

// D6 — a provider host in the target is the owner's too.
func TestDataRuleProviders(t *testing.T) {
	g := New(testProfile())
	for _, target := range []string{"https://dashboard.stripe.com/payments", "app.clicksign.com"} {
		if d := g.Check(Request{Agent: "scriptwriter", Action: "draft", Target: target}); d.Level != Owner {
			t.Errorf("target %q -> %+v", target, d)
		}
	}
	if d := g.Check(Request{Agent: "scriptwriter", Action: "draft", Target: "https://youtube.com/@example"}); d.Level != Free {
		t.Fatalf("youtube -> %+v", d)
	}
}
