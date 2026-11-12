package httpapi

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// clientHTML is the owner panel (OS5). Its script and stylesheet are separate
// files in this binary (client.js, client.css) so the page can run under
// PanelContentSecurityPolicy without 'unsafe-inline'. The page loads nothing except
// those files and tavern-core's own /api/status. It shows the party, the
// hirelings' balloons (their current turn), the open decisions and the
// conversations at the table, plus the quest bar. It is not the pixel-art
// tavern. An unset quest is an empty state on that 200; the page does not
// request GET /api/quest, which is 404 until a quest exists.
//
//go:embed client.html
var clientHTML []byte

//go:embed client.css
var clientCSS []byte

//go:embed client.js
var clientJS []byte

// clientBuildNotice replaces the route-notice comment when no client bundle is
// mounted. It tells the owner how to build the plain-art client without
// promising a route that would only redirect back to the panel.
const clientBuildNotice = `<section class="notice" id="client-build">
<h2>Plain-art client not built</h2>
<p>This is the owner panel. The tavern client is a separate data-only image and is not published. Build it from a checkout of <code>Play-Hireling/playhireling</code>, pack it here and start the <code>game</code> profile:</p>
<pre><code>npm run build:selfhost   # in the playhireling checkout
scripts/pack-client.sh /path/to/playhireling/dist-selfhost
docker compose --profile game up -d --build</code></pre>
<p>Then <code>http://127.0.0.1:8080/</code> opens the tavern and this panel stays at <code>/panel/</code>. Full steps: <a href="https://github.com/Play-Hireling/hireling/blob/main/docs/getting-started/quickstart.md#optional-pixel-art-client">quickstart</a>.</p>
</section>`

// panelTavernLink replaces the same comment when the bundle exists, so the
// owner can leave the panel for the game.
const panelTavernLink = `<p class="route">The tavern is installed. <a href="/tavern/">Open the tavern</a> · this panel stays at <code>/panel/</code>.</p>`

// hasClient reports whether ClientDir holds a client bundle. It checks the
// file the route actually serves, so an empty volume behaves like no volume.
func (s *Server) hasClient() bool {
	if s.ClientDir == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(s.ClientDir, "index.html"))
	return err == nil && st.Mode().IsRegular()
}

// root is the default door. With a client bundle it sends the owner to the
// tavern; without one the root is the panel, which explains how to build the
// client. See panel for the panel route.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if s.hasClient() {
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, "/tavern/", http.StatusTemporaryRedirect)
		return
	}
	s.panel(w, r)
}

// panelSlash keeps /panel canonical with a trailing slash, beside /tavern/.
func (s *Server) panelSlash(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/panel/", http.StatusPermanentRedirect)
}

// panel serves the owner panel at /panel/ in every setup, and at / when no
// client bundle is mounted. It is a GET, so it passes jsonOnly untouched.
// HTMLSecurity adds the CSP. The page only ever reads /api/* with the owner
// token, which a self-hoster enters in the page.
func (s *Server) panel(w http.ResponseWriter, _ *http.Request) {
	note := clientBuildNotice
	if s.hasClient() {
		note = panelTavernLink
	}
	page := strings.Replace(string(clientHTML), "<!-- route-notice -->", note, 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

func (s *Server) clientCSS(w http.ResponseWriter, _ *http.Request) {
	serveStatic(w, "text/css; charset=utf-8", clientCSS)
}

func (s *Server) clientJS(w http.ResponseWriter, _ *http.Request) {
	serveStatic(w, "text/javascript; charset=utf-8", clientJS)
}

func serveStatic(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
