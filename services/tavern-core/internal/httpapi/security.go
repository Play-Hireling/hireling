package httpapi

import (
	"mime"
	"net/http"
)

// ContentSecurityPolicy is the policy on every HTML response from tavern-core,
// including the owner panel and /tavern/. It is the playhireling tavern policy
// (playhireling #110, head cd6033e072db800a970cd08de810f224f6a2a6b5,
// packages/assets/public/_headers), not the panel's old style-src 'self'
// policy. style-src allows 'unsafe-inline' because the game sets style
// attributes. script-src does not. There is no 'unsafe-eval': Phaser 4.2.1
// in the self-host build runs without it.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self'; connect-src 'self'; media-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; frame-src 'none'; worker-src 'self'; manifest-src 'self'; form-action 'self'"

// HTMLSecurity sets ContentSecurityPolicy, X-Content-Type-Options: nosniff and
// Referrer-Policy: no-referrer on every response whose Content-Type is
// text/html. Handler wraps the mux with it, so a later HTML route on the same
// Server (including /tavern/) is covered without a second copy of the policy.
func HTMLSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&htmlHeaderWriter{ResponseWriter: w}, r)
	})
}

// SetHTMLSecurity writes the three headers. HTML handlers that build a
// response outside Handler can call it; Handler's wrapper already does.
func SetHTMLSecurity(h http.Header) {
	h.Set("Content-Security-Policy", ContentSecurityPolicy)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
}

func isHTML(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	return err == nil && mt == "text/html"
}

// htmlHeaderWriter fills the security headers at WriteHeader, after the
// handler has set Content-Type.
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
		if isHTML(ct) {
			SetHTMLSecurity(w.Header())
		}
		// Every JSON response, including 401, gets nosniff from this one wrapper.
		if isJSON(ct) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
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
