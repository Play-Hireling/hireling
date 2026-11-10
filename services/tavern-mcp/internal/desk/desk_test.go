package desk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tok = "owner-secret-token-0123456789abcdef"

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnv(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		url     string
		wantErr string
	}{
		{"defaults", map[string]string{"FRONTDESK_OWNER_TOKEN": tok}, DefaultURL, ""},
		{"fallbacks", map[string]string{"TAVERN_URL": "http://127.0.0.1:9000/", "FRONTDESK_TOKEN": tok}, "http://127.0.0.1:9000", ""},
		{"localhost is a name, not loopback", map[string]string{"FRONTDESK_URL": "http://localhost:8080", "FRONTDESK_OWNER_TOKEN": tok}, "", "plain http"},
		{"frontdesk wins", map[string]string{"FRONTDESK_URL": "https://t.example.com", "TAVERN_URL": "http://127.0.0.1:1", "FRONTDESK_TOKEN": tok}, "https://t.example.com", ""},
		{"ipv6 loopback", map[string]string{"FRONTDESK_URL": "http://[::1]:8080", "FRONTDESK_TOKEN": tok}, "http://[::1]:8080", ""},
		{"no token", map[string]string{}, "", "FRONTDESK_OWNER_TOKEN is not set"},
		{"http remote", map[string]string{"FRONTDESK_URL": "http://10.0.0.5:8080", "FRONTDESK_TOKEN": tok}, "", "plain http"},
		{"http remote allowed", map[string]string{"FRONTDESK_URL": "http://tavern-core:8080", "FRONTDESK_TOKEN": tok, "FRONTDESK_ALLOW_HTTP": "1"}, "http://tavern-core:8080", ""},
		{"http not enabled by true", map[string]string{"FRONTDESK_URL": "http://tavern-core:8080", "FRONTDESK_TOKEN": tok, "FRONTDESK_ALLOW_HTTP": "true"}, "", "plain http"},
		{"http not enabled by yes", map[string]string{"FRONTDESK_URL": "http://10.0.0.5:8080", "FRONTDESK_TOKEN": tok, "FRONTDESK_ALLOW_HTTP": "yes"}, "", "plain http"},
		{"http not enabled by zero", map[string]string{"FRONTDESK_URL": "http://10.0.0.5:8080", "FRONTDESK_TOKEN": tok, "FRONTDESK_ALLOW_HTTP": "0"}, "", "plain http"},
		{"http not enabled by padded 1", map[string]string{"FRONTDESK_URL": "http://10.0.0.5:8080", "FRONTDESK_TOKEN": tok, "FRONTDESK_ALLOW_HTTP": "1 "}, "", "plain http"},
		{"userinfo looks like loopback", map[string]string{"FRONTDESK_URL": "http://127.0.0.1@evil", "FRONTDESK_TOKEN": tok}, "", "credentials"},
		{"127.x is not loopback", map[string]string{"FRONTDESK_URL": "http://127.0.0.2:8080", "FRONTDESK_TOKEN": tok}, "", "plain http"},
		{"127 shorthand", map[string]string{"FRONTDESK_URL": "http://127.1:8080", "FRONTDESK_TOKEN": tok}, "", "plain http"},
		{"ipv4-mapped ipv6", map[string]string{"FRONTDESK_URL": "http://[::ffff:127.0.0.1]:8080", "FRONTDESK_TOKEN": tok}, "", "plain http"},
		{"ipv4-mapped hex", map[string]string{"FRONTDESK_URL": "http://[::ffff:7f00:1]:8080", "FRONTDESK_TOKEN": tok}, "", "plain http"},
		{"credentials in url", map[string]string{"FRONTDESK_URL": "https://u:p@t.example.com", "FRONTDESK_TOKEN": tok}, "", "must not carry credentials"},
		{"relative", map[string]string{"FRONTDESK_URL": "tavern:8080", "FRONTDESK_TOKEN": tok}, "", "FRONTDESK_URL"},
		{"ftp", map[string]string{"FRONTDESK_URL": "ftp://t.example.com", "FRONTDESK_TOKEN": tok}, "", "http:// or https://"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := FromEnv(env(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want %q", err, c.wantErr)
				}
				if strings.Contains(err.Error(), tok) {
					t.Fatal("token in the error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.URL != c.url || cfg.Token != tok {
				t.Fatalf("cfg = %+v", cfg)
			}
		})
	}
}

// FRONTDESK_OWNER_TOKEN is the name; FRONTDESK_TOKEN only fills in when it is unset.
func TestOwnerTokenWins(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{"FRONTDESK_OWNER_TOKEN": tok, "FRONTDESK_TOKEN": "fallback-token-value"}))
	if err != nil || cfg.Token != tok {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	cfg, err = FromEnv(env(map[string]string{"FRONTDESK_OWNER_TOKEN": "  ", "FRONTDESK_TOKEN": tok}))
	if err != nil || cfg.Token != tok {
		t.Fatalf("blank owner token must fall back: cfg = %+v, err = %v", cfg, err)
	}
}

func TestDoHeadersAndErrors(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"echo":"` + tok + `"}`))
		case "/bad":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"rejected ` + tok + `"}`))
		case "/401":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
		case "/html":
			w.Write([]byte(`<html>`))
		case "/redirect":
			http.Redirect(w, r, "https://elsewhere.example.com/", http.StatusFound)
		}
	}))
	defer srv.Close()
	c := New(Config{URL: srv.URL, Token: tok, CFAccessID: "cf-id", CFAccessSecret: "cf-secret-value"}, srv.Client())
	ctx := context.Background()

	raw, err := c.Do(ctx, http.MethodPost, "/ok", map[string]string{"a": "b"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Header.Get("Authorization") != "Bearer "+tok || got.Header.Get("Content-Type") != "application/json" ||
		got.Header.Get("CF-Access-Client-Id") != "cf-id" || got.Header.Get("CF-Access-Client-Secret") != "cf-secret-value" {
		t.Fatalf("headers: %v", got.Header)
	}
	if strings.Contains(string(raw), tok) || !strings.Contains(string(raw), "[redacted]") {
		t.Fatalf("token not scrubbed from the answer: %s", raw)
	}

	_, err = c.Do(ctx, http.MethodGet, "/bad", nil)
	var de *Error
	if !errors.As(err, &de) || de.Status != 400 || !strings.Contains(err.Error(), "rejected [redacted]") || strings.Contains(err.Error(), tok) {
		t.Fatalf("bad: %v", err)
	}
	if _, err = c.Do(ctx, http.MethodGet, "/401", nil); err == nil || !strings.Contains(err.Error(), "owner token") {
		t.Fatalf("401: %v", err)
	}
	if _, err = c.Do(ctx, http.MethodGet, "/html", nil); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Fatalf("html: %v", err)
	}
	if _, err = c.Do(ctx, http.MethodGet, "/redirect", nil); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect followed or not reported: %v", err)
	}
}

func TestDoNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	c := New(Config{URL: url, Token: tok}, nil)
	_, err := c.Do(context.Background(), http.MethodGet, "/api/status", nil)
	if err == nil || !strings.Contains(err.Error(), "cannot reach the tavern at "+url) || strings.Contains(err.Error(), tok) {
		t.Fatalf("err = %v", err)
	}
	if NotFound(err) {
		t.Fatal("a network error is not a 404")
	}
}

// An owner token shorter than tavern-core accepts is refused here too.
func TestShortToken(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"FRONTDESK_OWNER_TOKEN": "abc"}))
	if err == nil || !strings.Contains(err.Error(), "at least 32") || strings.Contains(err.Error(), "abc") {
		t.Fatalf("err = %v", err)
	}
}
