package redact

import (
	"strings"
	"testing"
)

func TestKnownValueIsMasked(t *testing.T) {
	r := New("super-secret-value-123")
	out := r.Text("the key is super-secret-value-123 ok")
	if strings.Contains(out, "super-secret") {
		t.Fatal(out)
	}
}

func TestPatterns(t *testing.T) {
	r := New()
	cases := []string{
		"Authorization: Bearer abcdefghijklmnop1234",
		"api_key=sk-abcdefghijklmnopqrstuvwxyz0123",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789",
		"token: 0123456789abcdef0123456789abcdef",
		"https://user:pw@host/x",
		"https://x.com/cb?token=abc123",
		"github_pat_11ABCDEFG0abcdefghijklmn_opq",
		"see gbrain_AbCdEf0123456789xyz in the log",
		// P1 gaps found by the security review (2026-11-05).
		"postgres://gbrain:s3cr3t@p4ss@db:5432/gbrain",
		"redis://:senha@redis:6379/0",
		"amqp://user:pass@broker:5672",
		"xox" + "b-1234567890-abcdefghijkl", // split so secret scanners skip the fixture
		"xox" + "s-1234567890-abcdefghijkl",
		"sk_" + "live_abcdefghijklmnopqrstuvwx",
		"rk_" + "live_abcdefghijklmnopqrstuvwx",
		"AI" + "zaSyA1234567890abcdefghijklmnopqrstu",
		"composio ak_AbCdEf0123456789xyzAB",
		// More shapes from the review (split so secret scanners skip them).
		"hf_" + "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789",
		"AS" + "IAABCDEFGHIJKLMNOP",
		"glpat-" + "AbCdEfGhIjKlMnOpQrSt",
		"glpat-" + "aB1-aB1-aB1-aB1-aB1-",
		"DEEPSEEK_API_KEY=" + "abc123def456",
		"S" + "K0123456789abcdef0123456789abcdef",
		"sk_" + "test_abcdefghijklmnopqrstuvwx",
		"https://hooks.slack.com/" + "services/T000/B000/XXXXXXXXXXXX",
	}
	for _, c := range cases {
		if out := r.Text(c); !strings.Contains(out, Hidden) {
			t.Errorf("not redacted: %q -> %q", c, out)
		}
	}
}

// A PEM private key and a full JWT are masked whole, not just a line or a
// payload.
func TestKeysAndTokensMaskedWhole(t *testing.T) {
	r := New()
	pem := "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END PRIVATE KEY-----"
	if out := r.Text(pem); strings.Contains(out, "MIIEvQ") || !strings.Contains(out, Hidden) {
		t.Fatalf("pem: %q", out)
	}
	cut := "key:\n-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC"
	if out := r.Text(cut); strings.Contains(out, "MIIEvQ") || !strings.Contains(out, Hidden) {
		t.Fatalf("cut pem: %q", out)
	}
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if out := r.Text(jwt); strings.Contains(out, "eyJhbGci") || strings.Contains(out, "dBjftJe") || !strings.Contains(out, Hidden) {
		t.Fatalf("jwt: %q", out)
	}
}

func TestSummaryLength(t *testing.T) {
	r := New()
	s := r.Summary(strings.Repeat("palavra ", 40))
	if n := len([]rune(s)); n > MaxLen {
		t.Fatal(n)
	}
	if r.Summary("  a   b\nc ") != "a b c" {
		t.Fatal("whitespace not collapsed")
	}
}

func TestPlainTextUntouched(t *testing.T) {
	r := New()
	in := "Drafting hook 2 of 3 for Monday's post"
	if out := r.Text(in); out != in {
		t.Fatal(out)
	}
}

// Ledger page paths are long but are not secrets; mixed-case blobs still are.
func TestPathsAreNotSecrets(t *testing.T) {
	r := New()
	for _, keep := range []string{
		"Source: brain/decisions/2026-11-04-gbrain-on-the-box (decision)",
		"see /tavern/shared/T-12/replies/innkeeper-1793798223512-0.md",
	} {
		if out := r.Text(keep); out != keep {
			t.Errorf("path redacted: %q -> %q", keep, out)
		}
	}
	for _, mask := range []string{
		"aB3dE5gH7jK9mN1pQ3sT5vX7zA9cE1gI3kM5oQ7sU9w/Yz",
		"abcdefghijklmnopqrstuvwxyz0123456789abcdefgh",
	} {
		if out := r.Text(mask); !strings.Contains(out, Hidden) {
			t.Errorf("secret kept: %q", mask)
		}
	}
}
