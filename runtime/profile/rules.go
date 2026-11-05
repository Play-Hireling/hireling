package profile

import "strings"

// ---- D5: the hard deny list -------------------------------------------------
//
// Payments, contracts, customer data, deletions and critical production are
// always the owner's, whatever a profile says. HardDeny holds the category names
// (what profiles list under owner_decides); denyTokens holds the words that put
// any other action name in a category, so `send_payment`, `pix-transfer`,
// `delete_post` or `sign_contract` cannot be made free or capped by renaming.
// Matching is per token (split on anything that is not a letter or digit), so
// `deploy_non_critical` and `ad_spend_usd` — deliberate Capped actions — stay
// out: spending under a cap is not a payment, and only *production* is critical.

var denyTokens = map[string][]string{
	"payment":             {"pay", "pays", "paying", "payment", "payments", "payout", "payouts", "pix", "transfer", "transfers", "wire", "refund", "refunds", "invoice", "invoices", "charge", "charges", "withdraw", "withdrawal", "remittance", "remittances", "remessa", "pagamento", "pagar", "transferir", "boleto"},
	"contract":            {"contract", "contracts", "sign", "signs", "signature", "esign", "agreement", "agreements", "nda", "contrato", "contratos", "assinar", "assinatura"},
	"customer_data":       {"customer", "customers", "client", "clients", "cliente", "clientes", "kyc", "pii", "cpf", "cnpj", "cedula", "passport"},
	"delete":              {"delete", "deletes", "deletion", "remove", "rm", "drop", "purge", "destroy", "wipe", "erase", "truncate", "excluir", "apagar", "deletar"},
	"production_critical": {"production", "prod", "migrate", "migration", "migrations", "dns", "firewall", "sudo", "root", "producao", "produção"},
}

// HardDenyCategory returns the D5 category an action belongs to ("" when none).
func HardDenyCategory(action string) string {
	a := strings.ToLower(strings.TrimSpace(action))
	if a == "" {
		return ""
	}
	for _, c := range HardDeny {
		if a == c {
			return c
		}
	}
	for _, tok := range tokens(a) {
		for _, c := range HardDeny {
			for _, w := range denyTokens[c] {
				if tok == w {
					return c
				}
			}
		}
	}
	return ""
}

// ---- D6: the data rule (network half) ---------------------------------------
//
// No hireling's allowlist includes finance, KYC or legal systems. A host is
// sensitive when one of its labels is a sensitive word, or when it is (a
// subdomain of) a known payment, banking, KYC, e-signature or accounting
// provider. Checked when a profile loads and again by the egress proxy on every
// connection, so a profile edited around the loader still cannot reach them.

// SensitiveSystems are the words that mark a finance, KYC or legal system.
var SensitiveSystems = []string{
	"finance", "financeiro", "financial", "accounting", "contabilidade", "contador",
	"legal", "juridico", "jurídico", "kyc", "banking", "bank", "pix", "payment", "payments",
	"card", "cards", "customer", "customers", "cliente", "clientes",
}

// SensitiveProviders are hosts of payment, banking, KYC, e-signature and
// accounting services. A profile may never allow them (or a subdomain).
var SensitiveProviders = []string{
	// payments and banking
	"stripe.com", "paypal.com", "adyen.com", "wise.com", "plaid.com", "mercadopago.com", "mercadopago.com.br",
	"pagar.me", "pagseguro.uol.com.br", "asaas.com", "iugu.com", "ebanx.com", "efipay.com.br", "gerencianet.com.br",
	"bcb.gov.br", "bridge.xyz", "circle.com", "transak.com", "moonpay.com",
	// KYC / identity
	"sumsub.com", "onfido.com", "withpersona.com", "veriff.com", "idwall.co", "unico.io", "caf.io", "serpro.gov.br",
	// e-signature and legal
	"docusign.com", "docusign.net", "clicksign.com", "d4sign.com.br", "zapsign.com.br", "jusbrasil.com.br",
	// accounting and tax
	"contaazul.com", "omie.com.br", "quickbooks.intuit.com", "xero.com", "receita.fazenda.gov.br", "dgi.gub.uy",
}

// sensitiveToolkits are Composio toolkit slugs of payment, banking,
// accounting, payroll, KYC and e-signature services that SensitiveProviders
// does not already name (its first labels are checked too).
var sensitiveToolkits = []string{
	"square", "braintree", "razorpay", "paddle", "chargebee", "lemon_squeezy", "lemonsqueezy", "coinbase", "brex",
	"ramp", "mercury", "revolut", "freshbooks", "zoho_books", "zohobooks", "wave", "wave_accounting", "sage",
	"netsuite", "gusto", "deel", "rippling", "adp", "bill", "billcom", "expensify", "pandadoc", "hellosign",
	"dropbox_sign", "signnow", "adobe_sign", "yousign", "persona", "jumio",
}

// SensitiveToolkit explains why a Composio toolkit is off limits under D6 ("" when it is not).
func SensitiveToolkit(slug string) string {
	t := strings.ToLower(strings.TrimSpace(slug))
	for _, p := range SensitiveProviders {
		name := strings.SplitN(p, ".", 2)[0]
		if p == "quickbooks.intuit.com" {
			name = "quickbooks"
		}
		if t == name || t == strings.ReplaceAll(name, "-", "") || strings.HasPrefix(t, name+"_") {
			return "finance/KYC/legal toolkit (" + p + ")"
		}
	}
	for _, s := range sensitiveToolkits {
		if t == s || strings.HasPrefix(t, s+"_") {
			return "finance/KYC/legal toolkit (" + s + ")"
		}
	}
	if w := SensitiveWord(t); w != "" {
		return w + " toolkit"
	}
	return ""
}

// SensitiveHost explains why a host is off limits under D6 ("" when it is not).
func SensitiveHost(host string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if h == "" {
		return ""
	}
	for _, p := range SensitiveProviders {
		if h == p || strings.HasSuffix(h, "."+p) {
			return "known finance/KYC/legal provider (" + p + ")"
		}
	}
	if w := SensitiveWord(h); w != "" {
		return w
	}
	return ""
}

// SensitiveWord returns the first sensitive word found as a whole token in s
// (a host, a system name, a free-text target), or "".
func SensitiveWord(s string) string {
	for _, tok := range tokens(strings.ToLower(s)) {
		for _, w := range SensitiveSystems {
			if tok == w {
				return w
			}
		}
	}
	return ""
}

func tokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
}
