package hermes

import (
	"strings"
)

// Markers of the `hermes chat -q` transcript, as observed (2026-11-03):
//
//	Query: <the whole query, echoed>
//	────────────────────────────────────────   (U+2500 rule)
//	<thinking>  ∴ Thought for 1s
//	💻 $ cmd … / ✗ $ cmd … [exit 2]           (tool calls)
//	────────────────────────────────────────
//	☤ Hermes                                   (the answer starts on the next line)
//	<final answer>
//	────────────────────────────────────────
//	Resume this session with:                  (session footer)
//	  hermes --resume local-…
//	Session: local-… · Title: … · Duration: 19s · Messages: 6
const (
	answerMarker = "Hermes"
	queryPrefix  = "Query:"
	footerMarker = "Resume this session with:"
)

// Answer extracts the agent's final reply from a transcript. Order of fallbacks:
// text after the last "☤ Hermes" line; text after the last rule; the transcript
// without the echoed query. It never returns the echoed prompt.
func Answer(transcript string) string {
	lines := strings.Split(strings.ReplaceAll(transcript, "\r\n", "\n"), "\n")
	lastMarker, lastRule := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case isRule(t):
			lastRule = i
		case isAnswerMarker(t):
			lastMarker = i
		}
	}
	lines = stripFooter(lines, lastMarker)
	switch {
	case lastMarker >= 0:
		return strings.TrimSpace(strings.Join(lines[lastMarker+1:], "\n"))
	case lastRule >= 0 && lastRule < len(lines):
		return strings.TrimSpace(strings.Join(lines[lastRule+1:], "\n"))
	}
	return strings.TrimSpace(stripQueryEcho(lines))
}

// stripFooter drops the "Resume this session with:" block Hermes prints after
// the answer (and the rule just above it), so the footer never reaches the owner.
// The footer only counts after the answer marker (or anywhere when there is none).
func stripFooter(lines []string, after int) []string {
	for i := len(lines) - 1; i > after; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), footerMarker) {
			end := i
			// also drop the blank lines and the rule that precede the footer
			for end > after+1 {
				t := strings.TrimSpace(lines[end-1])
				if t != "" && !isRule(t) {
					break
				}
				end--
			}
			return lines[:end]
		}
	}
	return lines
}

func isRule(t string) bool {
	if len([]rune(t)) < 4 {
		return false
	}
	for _, r := range t {
		if r != '─' && r != '-' && r != '═' {
			return false
		}
	}
	return true
}

// isAnswerMarker matches "☤ Hermes" and any "<symbol> Hermes" variant, but not
// prose that merely mentions Hermes.
func isAnswerMarker(t string) bool {
	if !strings.HasSuffix(t, answerMarker) {
		return false
	}
	head := strings.TrimSpace(strings.TrimSuffix(t, answerMarker))
	return len([]rune(head)) <= 2
}

func stripQueryEcho(lines []string) string {
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), queryPrefix) {
		return strings.Join(lines, "\n")
	}
	// The echo ends at the first blank line after the query block.
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return ""
}
