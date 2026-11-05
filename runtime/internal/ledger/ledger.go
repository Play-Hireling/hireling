// Package ledger reads and checks Tavern Ledger pages (E4; docs/architecture.md
// "Ledger page"). A page is GBrain's two-layer markdown: frontmatter (`title`,
// `type`, `owner`, `tags`), the compiled truth, one `---` line, and an
// append-only `## Timeline` whose lines are
//
//   - **YYYY-MM-DD** | <hireling id> — <summary>
//
// newest first, the source always being the hireling that wrote the entry.
// Folder indexes (README.md, RESOLVER.md) carry no `type` and are not pages.
// The data rule (D6) is checked here too: a page tagged sensitive never enters
// the Ledger. E3 runs Validate on every write before committing it.
package ledger

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

// Types are the six page types (E4); each has a template in ledger/templates.
var Types = []string{"goal", "agent", "project", "audience", "learning", "decision"}

// BlockedTags mark a page that must never enter the Ledger (D6), on top of
// the shared sensitive words (finance, kyc, legal, customer …).
var BlockedTags = []string{"sensitive", "confidential", "pii", "secret", "private", "restricted"}

// Page is one parsed file.
type Page struct {
	Title    string   `yaml:"title"`
	Type     string   `yaml:"type"`
	Owner    string   `yaml:"owner"`
	Tags     []string `yaml:"tags"`
	Truth    string   `yaml:"-"` // above the line
	Timeline []Entry  `yaml:"-"`
	// raw timeline lines that did not parse (reported by Validate)
	bad []string
	sep bool
}

// Entry is one timeline line.
type Entry struct {
	Date, Source, Summary string
}

// ErrSensitive is returned for a page tagged sensitive (D6).
var ErrSensitive = errors.New("ledger: page tagged sensitive — it never enters the Ledger (D6)")

var (
	slugRe  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	entryRe = regexp.MustCompile(`^- \*\*(\d{4}-\d{2}-\d{2})\*\* \| ([^ ]+) (?:—|–|-) (.+)$`)
)

// Parse reads a page or an index. It only fails on a broken frontmatter.
func Parse(src []byte) (*Page, error) {
	src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
	var p Page
	body := string(src)
	if strings.HasPrefix(body, "---\n") {
		end := strings.Index(body[4:], "\n---\n")
		if end < 0 {
			return nil, errors.New("ledger: frontmatter not closed")
		}
		if err := yaml.Unmarshal([]byte(body[4:4+end]), &p); err != nil {
			return nil, fmt.Errorf("ledger: frontmatter: %w", err)
		}
		body = body[4+end+5:]
	}
	lines := strings.Split(body, "\n")
	inTimeline := false
	var truth []string
	for _, ln := range lines {
		t := strings.TrimRight(ln, " \t")
		switch {
		case !p.sep && t == "---":
			p.sep = true
			continue
		case p.sep && strings.HasPrefix(t, "## Timeline"):
			inTimeline = true
			continue
		case inTimeline && strings.HasPrefix(t, "## "):
			inTimeline = false
		}
		if !p.sep {
			truth = append(truth, ln)
			continue
		}
		if inTimeline && strings.HasPrefix(t, "- ") {
			if m := entryRe.FindStringSubmatch(t); m != nil {
				p.Timeline = append(p.Timeline, Entry{Date: m[1], Source: m[2], Summary: strings.TrimSpace(m[3])})
			} else {
				p.bad = append(p.bad, t)
			}
		}
	}
	p.Truth = strings.TrimSpace(strings.Join(truth, "\n"))
	return &p, nil
}

// IsPage reports whether the file is a typed page (indexes are not).
func (p *Page) IsPage() bool { return p.Type != "" }

// Validate checks a filled page: the four frontmatter keys, the two layers,
// every timeline line in the grammar, newest first, and the data rule.
func (p *Page) Validate() error {
	if err := p.structure(); err != nil {
		return err
	}
	if !slugRe.MatchString(p.Owner) {
		return fmt.Errorf("ledger: owner %q must be a hireling id, owner or seed", p.Owner)
	}
	if len(p.bad) > 0 {
		return fmt.Errorf("ledger: timeline line %q is not `- **YYYY-MM-DD** | <hireling id> — <summary>`", p.bad[0])
	}
	if len(p.Timeline) == 0 {
		return errors.New("ledger: a page needs at least one timeline entry (who created it, when)")
	}
	for i, e := range p.Timeline {
		if !slugRe.MatchString(e.Source) {
			return fmt.Errorf("ledger: timeline source %q must be the hireling id that wrote the entry", e.Source)
		}
		if i > 0 && e.Date > p.Timeline[i-1].Date {
			return fmt.Errorf("ledger: timeline is newest first (%s after %s)", e.Date, p.Timeline[i-1].Date)
		}
	}
	return nil
}

// ValidateTemplate checks a template: a known type, the two layers and the
// sensitive-tag rule; `<…>` placeholders are allowed everywhere else.
func (p *Page) ValidateTemplate() error { return p.structure() }

func (p *Page) structure() error {
	if strings.TrimSpace(p.Title) == "" {
		return errors.New("ledger: title is required")
	}
	known := false
	for _, t := range Types {
		if p.Type == t {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("ledger: type %q is not one of %v", p.Type, Types)
	}
	if strings.TrimSpace(p.Owner) == "" {
		return errors.New("ledger: owner is required")
	}
	if p.Tags == nil {
		return errors.New("ledger: tags must be a YAML list")
	}
	if err := Sensitive(p.Tags); err != nil {
		return err
	}
	if !p.sep {
		return errors.New("ledger: no `---` line between the compiled truth and the timeline")
	}
	if p.Truth == "" {
		return errors.New("ledger: the compiled truth above the line is empty")
	}
	return nil
}

// Sensitive rejects tags that mark a page as sensitive (D6).
func Sensitive(tags []string) error {
	for _, t := range tags {
		tl := strings.ToLower(strings.TrimSpace(t))
		for _, b := range BlockedTags {
			if tl == b {
				return fmt.Errorf("%w: tag %q", ErrSensitive, t)
			}
		}
		if w := profile.SensitiveWord(tl); w != "" {
			return fmt.Errorf("%w: tag %q", ErrSensitive, t)
		}
	}
	return nil
}
