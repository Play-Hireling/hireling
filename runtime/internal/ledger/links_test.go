package ledger

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var mdLink = regexp.MustCompile(`\]\(([^)\s]+)\)`)

// E7 — the Ledger opens as an Obsidian vault (ledger/.obsidian, markdown links)
// and every relative link resolves inside it: no wikilinks (GitHub does not
// render them), no link that leaves the vault, no target that does not exist.
func TestVaultLinks(t *testing.T) {
	if _, err := os.Stat(filepath.Join(root, ".obsidian", "app.json")); err != nil {
		t.Fatalf("ledger is not an Obsidian vault: %v", err)
	}
	vault, _ := filepath.Abs(root)
	files := 0
	_ = filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		files++
		src, _ := os.ReadFile(path)
		text := string(src)
		if strings.Contains(text, "[[") {
			t.Errorf("%s: wikilink — use a relative markdown link", path)
		}
		for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			dec, err := url.PathUnescape(target)
			if err != nil {
				t.Errorf("%s: link %q: %v", path, m[1], err)
				continue
			}
			abs := filepath.Clean(filepath.Join(filepath.Dir(path), dec))
			if !strings.HasPrefix(abs, vault+string(os.PathSeparator)) {
				t.Errorf("%s: link %q leaves the vault", path, m[1])
				continue
			}
			if _, err := os.Stat(abs); err != nil {
				t.Errorf("%s: link %q points to nothing", path, m[1])
			}
		}
		return nil
	})
	if files < 20 {
		t.Fatalf("only %d markdown files in the vault", files)
	}
}
