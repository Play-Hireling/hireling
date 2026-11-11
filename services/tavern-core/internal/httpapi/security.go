package httpapi

import (
	"mime"
	"net/http"
)

// TavernContentSecurityPolicy is the playhireling #110 policy (head
// cd6033e072db800a970cd08de810f224f6a2a6b5, packages/assets/public/_headers).
// serveTavern sets it when it serves a game file. The middleware does not
// choose it from the path. style-src allows 'unsafe-inline' because the game
// sets style attributes. script-src does not. There is no 'unsafe-eval'.
const TavernContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; frame-src 'none'; worker-src 'self'; manifest-src 'self'; form-action 'self'"

// PanelContentSecurityPolicy is the SH1a policy for the owner panel and every
// other route. It is the default, and it is the stricter of the two: no
// 'unsafe-inline' and no 'unsafe-eval'.
const PanelContentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// HTMLSecurity is the default policy. Every non-JSON response gets
// PanelContentSecurityPolicy, nosniff and Referrer-Policy, unless the
// handler already set a Content-Security-Policy (serveTavern does that
// for a file it actually serves). JSON keeps the headers the handler set
// and gains nosniff. It does not gain a CSP, and Cache-Control and
// Content-Type are left as the handler wrote them.
func HTMLSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&htmlHeaderWriter{ResponseWriter: w}, r)
	})
}

// SetHTMLSecurity writes the panel policy and the two companion headers.
// HTML handlers that build a response outside Handler can call it.
func SetHTMLSecurity(h http.Header) {
	setPolicy(h, PanelContentSecurityPolicy)
}

func setPolicy(h http.Header, policy string) {
	h.Set("Content-Security-Policy", policy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

// htmlHeaderWriter fills the security headers at WriteHeader, after the
// handler has set Content-Type. It does not read the request path.
type htmlHeaderWriter struct {
	http.ResponseWriter
	wrote bool
}

func isJSON(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "application/json"
}

func (w *htmlHeaderWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		ct := w.Header().Get("Content-Type")
		switch {
		case isJSON(ct):
			// nosniff only. Do not set a CSP and do not replace Cache-Control
			// or Content-Type.
			w.Header().Set("X-Content-Type-Options", "nosniff")
		case w.Header().Get("Content-Security-Policy") != "":
			// serveTavern already chose the game policy for a file it serves.
			if w.Header().Get("X-Content-Type-Options") == "" {
				w.Header().Set("X-Content-Type-Options", "nosniff")
			}
			if w.Header().Get("Referrer-Policy") == "" {
				w.Header().Set("Referrer-Policy", "no-referrer")
			}
		default:
			setPolicy(w.Header(), PanelContentSecurityPolicy)
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *htmlHeaderWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *htmlHeaderWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
