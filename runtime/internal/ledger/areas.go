package ledger

import (
	"path"
	"strings"
)

// Areas (E2): a profile's ledger.read / ledger.write entries name parts of the
// brain relative to brain/:
//
//	goal               → brain/goal.md
//	projects/*         → anything under brain/projects/
//	learnings/dev/*    → anything under brain/learnings/dev/
//
// InArea reports whether page (a path relative to brain/, with or without
// ".md") falls in one of the areas. Paths that try to leave brain/ (absolute,
// "..", backslashes) are never in any area.
func InArea(areas []string, page string) bool {
	p, ok := clean(page)
	if !ok {
		return false
	}
	for _, a := range areas {
		a = strings.Trim(strings.TrimSpace(a), "/")
		if a == "" {
			continue
		}
		if dir, ok := strings.CutSuffix(a, "/*"); ok {
			if strings.HasPrefix(p, dir+"/") {
				return true
			}
			continue
		}
		if p == a+".md" || p == a {
			return true
		}
	}
	return false
}

func clean(page string) (string, bool) {
	page = strings.TrimSpace(page)
	if page == "" || strings.HasPrefix(page, "/") || strings.Contains(page, "\\") {
		return "", false
	}
	for _, part := range strings.Split(page, "/") {
		if part == ".." {
			return "", false
		}
	}
	p := path.Clean(strings.TrimPrefix(page, "brain/"))
	if p == "." || strings.HasPrefix(p, "..") {
		return "", false
	}
	if !strings.HasSuffix(p, ".md") {
		p += ".md"
	}
	return p, true
}
