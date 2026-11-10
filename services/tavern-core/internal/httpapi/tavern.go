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
// It sets TavernContentSecurityPolicy only when it serves a file. A miss,
// a path that cleans out of /tavern/, and anything under docs/provenance
// or withheld is a 404, and the middleware then applies the panel policy.
// An empty ClientDir is that same 404: the panel at / still works.
func (s *Server) serveTavern(w http.ResponseWriter, r *http.Request) {
	root := s.ClientDir
	if root == "" {
		http.NotFound(w, r)
		return
	}
	// Decoded path. "/tavern/../" cleans to "/" and is not this tree.
	// An encoded slash stays in the segment, so "/tavern/..%2f" arrives
	// decoded as "/tavern/../" and takes the same exit.
	// path.Clean drops the trailing slash, so "/tavern/" becomes "/tavern".
	clean := path.Clean(r.URL.Path)
	if clean != "/tavern" && !strings.HasPrefix(clean, "/tavern/") {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(clean, "/tavern")
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
	// This is the only place the game policy is chosen. The middleware
	// leaves a policy the handler already set, and uses the panel policy
	// for every other non-JSON response.
	w.Header().Set("Content-Security-Policy", TavernContentSecurityPolicy)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
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
