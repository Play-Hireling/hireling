package composio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	created int
	gets    int
	gone    bool
	body    map[string]any
	key     string
}

func (f *fake) handler(w http.ResponseWriter, r *http.Request) {
	f.key = r.Header.Get("x-api-key")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v3.1/tool_router/session":
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &f.body)
		f.created++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"session_id":"trs_new` + strings.Repeat("x", f.created) + `","mcp":{"type":"http","url":"https://backend.composio.dev/tool_router/trs_new/mcp"},"tool_router_tools":["COMPOSIO_SEARCH_TOOLS"]}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v3.1/tool_router/session/"):
		f.gets++
		if f.gone {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"message":"Session not found"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"session_id":"trs_newx"}`))
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Validation error while processing request"}}`))
	}
}

func TestEnsureCreatesThenReuses(t *testing.T) {
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "ak_test"}
	file := filepath.Join(t.TempDir(), "composio-session.json")
	ctx := context.Background()

	s, err := c.Ensure(ctx, file, "hireling-tavern", []string{"github"})
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "trs_newx" || !strings.HasSuffix(s.URL, "/mcp") || f.created != 1 || f.key != "ak_test" {
		t.Fatalf("session %+v created=%d key=%q", s, f.created, f.key)
	}
	// the request limits toolkits, keeps connections in-session, forbids removal, deletes and the workbench
	if got := f.body["toolkits"].(map[string]any)["enable"].([]any); len(got) != 1 || got[0] != "github" {
		t.Fatalf("toolkits %v", got)
	}
	mc := f.body["manage_connections"].(map[string]any)
	if mc["enable"] != true || mc["enable_connection_removal"] != false {
		t.Fatalf("manage_connections %v", mc)
	}
	if tags := f.body["tags"].(map[string]any)["disable"].([]any); len(tags) != 1 || tags[0] != "destructiveHint" {
		t.Fatalf("tags %v", tags)
	}
	if wb := f.body["workbench"].(map[string]any); wb["enable"] != false {
		t.Fatalf("workbench %v", wb)
	}

	// same user + toolkits: reused after one liveness check
	if s2, err := c.Ensure(ctx, file, "hireling-tavern", []string{"github"}); err != nil || s2.ID != s.ID || f.created != 1 || f.gets != 1 {
		t.Fatalf("reuse: %+v %v created=%d gets=%d", s2, err, f.created, f.gets)
	}
	// toolkits changed: new session
	if _, err := c.Ensure(ctx, file, "hireling-tavern", []string{"github", "googledocs"}); err != nil || f.created != 2 {
		t.Fatalf("toolkits changed: %v created=%d", err, f.created)
	}
	// session gone on Composio's side: new session
	f.gone = true
	if _, err := c.Ensure(ctx, file, "hireling-tavern", []string{"github", "googledocs"}); err != nil || f.created != 3 {
		t.Fatalf("gone: %v created=%d", err, f.created)
	}
}

func TestNewSessionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer srv.Close()
	_, err := (&Client{BaseURL: srv.URL, Key: "bad"}).NewSession(context.Background(), "u", []string{"github"})
	if err == nil || !strings.Contains(err.Error(), "Invalid API key") || strings.Contains(err.Error(), "bad") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstructionsNameToolkitsAndForbidPasswords(t *testing.T) {
	s := Instructions([]string{"github", "googledocs"})
	for _, want := range []string{"github, googledocs", "COMPOSIO_MANAGE_CONNECTIONS", "Never ask the owner for a password", "`ask`"} {
		if !strings.Contains(s, want) {
			t.Fatalf("instructions miss %q", want)
		}
	}
}
