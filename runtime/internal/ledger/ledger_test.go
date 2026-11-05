package ledger

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

const root = "../../../ledger"

// E4 — one template per page type, each with the two layers.
func TestTemplates(t *testing.T) {
	seen := map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(root, "templates", "*.md"))
	for _, f := range files {
		if filepath.Base(f) == "README.md" {
			continue
		}
		src, _ := os.ReadFile(f)
		p, err := Parse(src)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := p.ValidateTemplate(); err != nil {
			t.Errorf("%s: %v", f, err)
		}
		if strings.TrimSuffix(filepath.Base(f), "-template.md") != p.Type {
			t.Errorf("%s declares type %q", f, p.Type)
		}
		seen[p.Type] = true
	}
	for _, ty := range Types {
		if !seen[ty] {
			t.Errorf("no template for %s", ty)
		}
	}
}

// E4 — every page in the brain seed is a valid page; indexes parse.
func TestBrainPages(t *testing.T) {
	n := 0
	err := filepath.WalkDir(filepath.Join(root, "brain"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		src, _ := os.ReadFile(path)
		p, err := Parse(src)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		if p.IsPage() {
			n++
			if err := p.Validate(); err != nil {
				t.Errorf("%s: %v", path, err)
			}
		}
		return nil
	})
	if err != nil || n < 8 {
		t.Fatalf("walked %d pages: %v", n, err)
	}
}

// E4 — one agent page per profile, and nothing else in agents/.
func TestAgentPagesMatchProfiles(t *testing.T) {
	ids, err := profile.ListIDs(filepath.Join("..", "..", "..", "agents"))
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "brain", "agents", "*.md"))
	var pages []string
	for _, f := range files {
		if b := filepath.Base(f); b != "README.md" {
			pages = append(pages, strings.TrimSuffix(b, ".md"))
		}
	}
	sort.Strings(ids)
	sort.Strings(pages)
	if strings.Join(ids, ",") != strings.Join(pages, ",") {
		t.Fatalf("profiles %v, agent pages %v", ids, pages)
	}
}

func TestValidateRejects(t *testing.T) {
	ok := "---\ntitle: X\ntype: learning\nowner: scriptwriter\ntags: [learning]\n---\n\n# X\n\n> y\n\n---\n\n## Timeline\n- **2026-11-05** | scriptwriter — second\n- **2026-11-04** | scriptwriter — first\n"
	p, _ := Parse([]byte(ok))
	if err := p.Validate(); err != nil {
		t.Fatalf("valid page refused: %v", err)
	}
	if len(p.Timeline) != 2 || p.Timeline[0].Source != "scriptwriter" || p.Timeline[1].Summary != "first" {
		t.Fatalf("%+v", p.Timeline)
	}
	cases := map[string]string{
		"sensitive tag":   strings.Replace(ok, "tags: [learning]", "tags: [learning, confidential]", 1),
		"finance tag":     strings.Replace(ok, "tags: [learning]", "tags: [finance]", 1),
		"kyc tag":         strings.Replace(ok, "tags: [learning]", "tags: [KYC]", 1),
		"unknown type":    strings.Replace(ok, "type: learning", "type: diary", 1),
		"no separator":    strings.Replace(ok, "\n---\n\n## Timeline", "\n\n## Timeline", 1),
		"bad entry":       strings.Replace(ok, "- **2026-11-04** | scriptwriter — first", "- 2026-11-04 scriptwriter first", 1),
		"oldest first":    strings.Replace(strings.Replace(strings.Replace(ok, "2026-11-05", "TMP", 1), "2026-11-04", "2026-11-05", 1), "TMP", "2026-11-04", 1),
		"source not id":   strings.Replace(ok, "| scriptwriter — first", "| Some Person — first", 1),
		"no timeline":     strings.Split(ok, "- **")[0],
		"tags not a list": strings.Replace(ok, "tags: [learning]\n", "", 1),
	}
	for name, src := range cases {
		p, err := Parse([]byte(src))
		if err == nil {
			err = p.Validate()
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if name == "oldest first" && (err == nil || !strings.Contains(err.Error(), "newest first")) {
			t.Errorf("oldest first: %v", err)
		}
		if strings.HasSuffix(name, "tag") && !errors.Is(err, ErrSensitive) {
			t.Errorf("%s: %v is not ErrSensitive", name, err)
		}
	}
}
