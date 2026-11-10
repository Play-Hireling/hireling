package httpapi

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// tavernSlash sends /tavern to /tavern/ so relative asset URLs in the
// self-host build (Vite base ”) resolve under the game.
func (s *Server) tavernSlash(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/tavern/", http.StatusPermanentRedirect)
}

// serveTavern serves the self-host client from ClientDir at /tavern/.
// HTMLSecurity adds the CSP because index.html is text/html. Files outside
// the directory, and anything under docs/provenance or withheld, are 404.
// An empty or missing directory is 404: the engine still serves the panel.
func (s *Server) serveTavern(w http.ResponseWriter, r *http.Request) {
	root := s.ClientDir
	if root == "" {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/tavern/")
	rel = path.Clean("/" + rel)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "." || rel == "" {
		rel = "index.html"
	}
	if deniedClientPath(rel) {
		http.NotFound(w, r)
		return
	}
	full, ok := inside(root, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

// deniedClientPath is the public-bundle denylist for paths the file server
// must not emit even if a bad image copied them: provenance and withheld.
func deniedClientPath(rel string) bool {
	rel = strings.ToLower(strings.ReplaceAll(rel, "\\", "/"))
	if strings.Contains(rel, "docs/provenance") || strings.HasPrefix(rel, "docs/provenance") {
		return true
	}
	if strings.Contains(rel, "packages/assets/withheld") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "withheld" {
			return true
		}
	}
	return false
}

// inside returns the absolute path of rel under root, after resolving
// symlinks. ok is false when the path escapes root.
func inside(root, rel string) (string, bool) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(rel))
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", false
	}
	sep := string(os.PathSeparator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+sep) {
		return "", false
	}
	return fullAbs, true
}
