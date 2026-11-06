package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tok = "owner-secret-token-123"

func TestRunCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/status" || r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		w.Write([]byte(`{"agents":[{"id":"dev"},{"id":"innkeeper"}],"decisions":[{"id":"d-1"}]}`))
	}))
	defer srv.Close()
	env := map[string]string{"FRONTDESK_URL": srv.URL, "FRONTDESK_TOKEN": tok}
	var out, errb bytes.Buffer
	if code := run([]string{"-check"}, func(k string) string { return env[k] }, &out, &errb); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), "party [dev innkeeper]; 1 open decision(s)") {
		t.Fatalf("out: %s", out.String())
	}

	env["FRONTDESK_TOKEN"] = "wrong-token-value"
	out.Reset()
	errb.Reset()
	if code := run([]string{"-check"}, func(k string) string { return env[k] }, &out, &errb); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "HTTP 401") || strings.Contains(errb.String()+out.String(), "wrong-token-value") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

func TestRunConfigErrors(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, func(string) string { return "" }, &out, &errb); code != 1 || !strings.Contains(errb.String(), "FRONTDESK_TOKEN is not set") {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	errb.Reset()
	env := map[string]string{"FRONTDESK_URL": "http://192.0.2.10:8080", "FRONTDESK_TOKEN": tok}
	if code := run(nil, func(k string) string { return env[k] }, &out, &errb); code != 1 || strings.Contains(errb.String(), tok) {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	out.Reset()
	if code := run([]string{"-version"}, func(string) string { return "" }, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "tavern-mcp ") {
		t.Fatalf("version: %d %s", code, out.String())
	}
}
