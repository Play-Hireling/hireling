package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNoteAllowHTTP(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	for _, v := range []string{"", "0", "2", "true", "TRUE", "yes", "on", "1 ", " 1", "1\n"} {
		buf.Reset()
		if noteAllowHTTP(logger, v) || buf.Len() != 0 {
			t.Fatalf("%q enabled plain http: %q", v, buf.String())
		}
	}
	buf.Reset()
	if !noteAllowHTTP(logger, "1") {
		t.Fatal(`"1" did not enable plain http`)
	}
	if got := buf.String(); !strings.Contains(got, "FRONTDESK_ALLOW_HTTP=1") || strings.Count(got, "\n") != 1 {
		t.Fatalf("log = %q, want one line naming FRONTDESK_ALLOW_HTTP=1", got)
	}
}

func TestProviderKeyLine(t *testing.T) {
	const phrase = "provider key missing/invalid"
	const real = "sk-real-looking-key-0123456789abcdef"
	ctx := context.Background()
	for _, key := range []string{"", "  ", "sk-paste-your-key-here", "sk-placeholder-not-a-real-key", "changeme"} {
		got := providerKeyLineAt(ctx, "http://127.0.0.1:1/models", key, providerClient)
		if got != phrase || (strings.TrimSpace(key) != "" && strings.Contains(got, strings.TrimSpace(key))) {
			t.Fatalf("%q -> %q", key, got)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), real) && r.URL.RawQuery == "" {
			switch r.URL.Path {
			case "/deny":
				w.WriteHeader(http.StatusForbidden)
			case "/ok":
				w.WriteHeader(http.StatusOK)
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	client := srv.Client()
	if got := providerKeyLineAt(ctx, srv.URL+"/models", real, client); got != phrase || strings.Contains(got, real) {
		t.Fatalf("401: %q", got)
	}
	if got := providerKeyLineAt(ctx, srv.URL+"/deny", real, client); got != phrase {
		t.Fatalf("403: %q", got)
	}
	if got := providerKeyLineAt(ctx, srv.URL+"/ok", real, client); got != "" {
		t.Fatalf("200: %q", got)
	}
	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close()
	if got := providerKeyLineAt(ctx, down.URL+"/models", real, client); got != "" {
		t.Fatalf("network: %q", got)
	}
	// The probe must not write the key. A body that echoes it is discarded.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, r.Header.Get("Authorization"))
	}))
	defer echo.Close()
	if got := providerKeyLineAt(ctx, echo.URL, real, echo.Client()); got != phrase || strings.Contains(got, real) {
		t.Fatalf("echoed key leaked: %q", got)
	}
}
