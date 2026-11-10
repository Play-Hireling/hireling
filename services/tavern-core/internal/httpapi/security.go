package httpapi

import (
	"mime"
	"net/http"
)

// ContentSecurityPolicy is the policy on every HTML response from tavern-core
// (the owner panel today, /tavern/ when that handler is added). No
// 'unsafe-inline' and no 'unsafe-eval': scripts and styles are same-origin
// files. img-src allows the panel's data: favicon.
const ContentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

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

func (w *htmlHeaderWriter) WriteHeader(code int) {
	if !w.wrote {
		w.wrote = true
		if isHTML(w.Header().Get("Content-Type")) {
			SetHTMLSecurity(w.Header())
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
