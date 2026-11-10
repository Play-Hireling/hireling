package httpapi

import (
	_ "embed"
	"net/http"
)

// clientHTML is the owner panel (OS5). Its script and stylesheet are separate
// files in this binary (client.js, client.css) so the page can run under
// ContentSecurityPolicy without 'unsafe-inline'. The page loads nothing except
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

// client serves the owner panel at the root. It is a GET, so it passes
// jsonOnly untouched. HTMLSecurity adds the CSP. The page only ever reads
// /api/* with the owner token, which a self-hoster enters in the page.
func (s *Server) client(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(clientHTML)
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
