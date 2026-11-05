// Package redact is A9 on the runtime side: nothing that leaves the container
// (turn reports, messages, logs) may carry a secret. The front desk applies its
// own redaction too (scripts/redact.jq); this is the first line, not the only one.
package redact

import (
	"regexp"
	"strings"
)

const Hidden = "[hidden]"

// Summary length the front desk keeps.
const MaxLen = 120

var patterns = []*regexp.Regexp{
	// Bearer / Basic tokens and explicit key=value secrets.
	regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`),
	regexp.MustCompile(`(?i)\b(api[_-]?key|token|secret|password|passwd|pwd|senha|chave)\s*[:=]\s*\S+`),
	// Well-known key shapes.
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{16,}\b`),
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bgbrain_[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bxox[abp]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// Long opaque strings (hex / base64) are almost always credentials.
	regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`),
	longBlob,
	// URLs with embedded credentials or tokens.
	regexp.MustCompile(`(?i)https?://[^\s]*(?:token|key|secret|password)=[^\s&]+`),
	regexp.MustCompile(`(?i)https?://[^\s/@]+:[^\s/@]+@`),
}

// Redactor masks secret values known at runtime (the environment) and
// anything that looks like a credential.
type Redactor struct {
	known []string
}

// New builds a redactor from explicit secret values; empty values are ignored.
func New(values ...string) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		if len(strings.TrimSpace(v)) >= 6 {
			r.known = append(r.known, v)
		}
	}
	return r
}

// longBlob catches unlabelled tokens (40+ base64-ish chars). It is the only
// pattern that lets a path through (looksLikePath).
var longBlob = regexp.MustCompile(`\b[A-Za-z0-9+/_-]{40,}={0,2}\b`)

// Text masks secrets in s. It never truncates.
func (r *Redactor) Text(s string) string {
	for _, v := range r.known {
		s = strings.ReplaceAll(s, v, Hidden)
	}
	for _, p := range patterns {
		s = p.ReplaceAllStringFunc(s, func(m string) string {
			if p == longBlob && looksLikePath(m) {
				return m
			}
			return Hidden
		})
	}
	return s
}

// looksLikePath spots a file path or Ledger slug such as
// brain/decisions/2026-11-04-gbrain-on-ny1 or /tavern/shared/T-12/reply.md:
// long, but not a secret. A segment that looks random (16+ chars mixing
// upper case, lower case and digits) keeps the whole match masked.
func looksLikePath(m string) bool {
	if !strings.Contains(m, "/") {
		return false
	}
	for _, seg := range strings.Split(strings.Trim(m, "/"), "/") {
		if seg == "" || len(seg) > 48 || strings.ContainsAny(seg, "+=") {
			return false
		}
		var up, low, dig bool
		for _, c := range seg {
			switch {
			case c >= 'A' && c <= 'Z':
				up = true
			case c >= 'a' && c <= 'z':
				low = true
			case c >= '0' && c <= '9':
				dig = true
			}
		}
		if len(seg) >= 16 && up && low && dig {
			return false
		}
	}
	return true
}

// Summary masks and cuts to MaxLen runes, one line, like the front desk does.
func (r *Redactor) Summary(s string) string {
	s = strings.Join(strings.Fields(r.Text(s)), " ")
	if rs := []rune(s); len(rs) > MaxLen {
		return string(rs[:MaxLen-1]) + "…"
	}
	return s
}
