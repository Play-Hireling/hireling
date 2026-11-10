package httpapi

import (
	_ "embed"
	"net/http"
)

// clientHTML is the owner panel (OS5): one self-contained page, no external
// assets and no network loads except tavern-core's own /api/status. It shows
// the party, the hirelings' balloons (their current turn), the open decisions
// and the conversations at the table, plus the quest bar. It is not the
// pixel-art tavern. An unset quest is an empty state on that 200; the page
// does not request GET /api/quest, which is 404 until a quest exists.
//
//go:embed client.html
var clientHTML []byte

// client serves the owner panel at the root. It is a GET, so it passes
// jsonOnly untouched; the page only ever reads /api/* with the owner token,
// which a self-hoster enters in the page.
func (s *Server) client(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(clientHTML)
}
