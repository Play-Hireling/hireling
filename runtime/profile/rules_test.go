package profile

import (
	"path/filepath"
	"strings"
	"testing"
)

// D5 — every category, its renamed variants, and the deliberate Capped actions
// that must stay out.
func TestHardDenyCategory(t *testing.T) {
	cases := map[string]string{
		"payment": "payment", "send_payment": "payment", "pix-transfer": "payment", "issue refund": "payment", "pagar_boleto": "payment",
		"contract": "contract", "sign_contract": "contract", "e-sign NDA": "contract", "assinar contrato": "contract",
		"customer_data": "customer_data", "export_customers": "customer_data", "read KYC": "customer_data", "lookup cpf": "customer_data",
		"delete": "delete", "delete_post": "delete", "rm -rf": "delete", "drop table": "delete", "purge_cache": "delete",
		"production_critical": "production_critical", "deploy_production": "production_critical", "run migration": "production_critical", "edit DNS": "production_critical", "sudo": "production_critical",
		// not on the list: deliberate Capped or Free actions of the repo profiles
		"deploy_non_critical": "", "ad_spend_usd": "", "publish_owned_channel": "", "publish_draft_to_cms": "",
		"draft": "", "research": "", "write_shared_file": "", "message_agent": "", "open_pr": "", "run_tests": "",
		"assign_task": "", "approve_handoff": "", "interrupt_low_priority": "", "propose_hire": "", "propose_firing": "",
	}
	for action, want := range cases {
		if got := HardDenyCategory(action); got != want {
			t.Errorf("HardDenyCategory(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestValidateRejectsRenamedHardDeny(t *testing.T) {
	for _, mut := range []func(*Profile){
		func(p *Profile) { p.Autonomy.Free = []string{"send_payment"} },
		func(p *Profile) { p.Autonomy.Free = []string{"delete_post"} },
		func(p *Profile) { p.Autonomy.Capped = map[string]map[string]int{"sign_contract": {"per_day": 1}} },
		func(p *Profile) { p.Autonomy.Capped = map[string]map[string]int{"deploy_production": {"per_day": 1}} },
	} {
		p := &Profile{ID: "x", DeskID: "x", Model: "deepseek-v4-flash"}
		mut(p)
		if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "D5") {
			t.Errorf("expected a D5 error, got %v (%+v)", err, p.Autonomy)
		}
	}
}

// D6 — role allowlists never include finance, KYC or legal systems.
func TestSensitiveHost(t *testing.T) {
	bad := []string{"api.stripe.com", "stripe.com", "kyc.example.io", "bank.example.com", "app.clicksign.com", "my-finance.dev", "contabilidade.example.com.br"}
	for _, h := range bad {
		if SensitiveHost(h) == "" {
			t.Errorf("%s should be sensitive", h)
		}
	}
	ok := append([]string{"github.com", "pypi.org", "api.github.com", "youtube.com", "graph.instagram.com"}, BaseAllow...)
	for _, h := range ok {
		if why := SensitiveHost(h); why != "" {
			t.Errorf("%s flagged: %s", h, why)
		}
	}
	p := &Profile{ID: "x", DeskID: "x", Model: "deepseek-v4-flash", Network: Network{Allow: []string{"github.com", "api.stripe.com"}}}
	if err := p.Validate(); err == nil || !strings.Contains(err.Error(), "D6") {
		t.Fatalf("expected a D6 error, got %v", err)
	}
}

// Every profile in the repo passes both rules.
func TestRepoProfilesPassHardRules(t *testing.T) {
	ps, err := List(filepath.Join("..", "..", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) < 6 {
		t.Fatalf("only %d profiles loaded", len(ps))
	}
	for _, p := range ps {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		for _, h := range p.AllowedHosts() {
			if why := SensitiveHost(h); why != "" {
				t.Errorf("%s allows %s (%s)", p.ID, h, why)
			}
		}
	}
}
