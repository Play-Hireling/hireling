package hermes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fake Hermes API server: tool progress events, two content deltas, [DONE].
func fakeAPI(t *testing.T, got *map[string]any, sid *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"status":"ok"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer k-123" {
			http.Error(w, "unauthorized", 401)
			return
		}
		*sid = r.Header.Get("X-Hermes-Session-Id")
		_ = json.NewDecoder(r.Body).Decode(got)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Hermes-Session-Id", *sid)
		f := w.(http.Flusher)
		fmt.Fprint(w, "event: hermes.tool.progress\ndata: {\"tool\":\"terminal\",\"emoji\":\"💻\",\"label\":\"ls /tavern/shared\",\"status\":\"running\"}\n\n")
		fmt.Fprint(w, "event: hermes.tool.progress\ndata: {\"tool\":\"terminal\",\"status\":\"completed\"}\n\n")
		f.Flush()
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Plan:\\n```plan\\n\"}}]}\n\n")
		f.Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{}\\n```\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func TestAPIRunnerStreamsAndKeepsFences(t *testing.T) {
	var got map[string]any
	var sid string
	s := fakeAPI(t, &got, &sid)
	defer s.Close()
	r := &APIRunner{BaseURL: s.URL, Key: "k-123", Prompt: "You are the Dev."}
	r.SetReasoning("low")
	out, err := r.Run(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "💻 terminal: ls /tavern/shared") {
		t.Fatalf("tool line missing: %q", out)
	}
	if a := Answer(out); a != "Plan:\n```plan\n{}\n```" {
		t.Fatalf("answer = %q (fences must survive)", a)
	}
	if got["reasoning_effort"] != "low" || got["stream"] != true {
		t.Fatalf("body = %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "hi" {
		t.Fatalf("messages = %v", msgs)
	}
	if !strings.HasPrefix(sid, "hl-") || r.Stats().Session != sid {
		t.Fatalf("session = %q / %q", sid, r.Stats().Session)
	}
	first, total := r.Latency()
	if first <= 0 || total < first {
		t.Fatalf("latency = %v %v", first, total)
	}
	r.SetReasoning("")
	got = nil
	_, _ = r.Run(context.Background(), "again")
	if _, ok := got["reasoning_effort"]; ok {
		t.Fatal("empty reasoning must keep the profile default")
	}
}

func TestAutoFallsBackToCLIWhenServerIsDown(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "hermes")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho '☤ Hermes'\necho 'from the cli'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	envFile := filepath.Join(dir, ".env")
	_ = os.WriteFile(envFile, []byte("# comment\nAPI_SERVER_KEY=k-123\n"), 0o600)
	var logged []string
	a := &Auto{
		API:     &APIRunner{BaseURL: "http://127.0.0.1:1"},
		CLI:     &Runner{Bin: bin, Home: dir},
		KeyFile: envFile,
		Log:     func(f string, v ...any) { logged = append(logged, fmt.Sprintf(f, v...)) },
	}
	out, err := a.Run(context.Background(), "hi")
	if err != nil || Answer(out) != "from the cli" {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if a.API.Key != "k-123" || len(logged) != 1 {
		t.Fatalf("key %q, logged %v", a.API.Key, logged)
	}
	if f, tot := a.Latency(); f != 0 || tot != 0 {
		t.Fatal("a CLI turn has no measured latency")
	}

	var got map[string]any
	var sid string
	s := fakeAPI(t, &got, &sid)
	defer s.Close()
	a.API.BaseURL = s.URL
	if out, err := a.Run(context.Background(), "hi"); err != nil || !strings.Contains(out, "```plan") {
		t.Fatalf("api turn: %q %v", out, err)
	}
	if _, tot := a.Latency(); tot == 0 {
		t.Fatal("api turn latency missing")
	}
	a.Mode = "cli"
	if out, _ := a.Run(context.Background(), "hi"); Answer(out) != "from the cli" {
		t.Fatalf("HIRELING_TURNS=cli ignored: %q", out)
	}
}

func TestAPIRunnerHTTPError(t *testing.T) {
	var got map[string]any
	var sid string
	s := fakeAPI(t, &got, &sid)
	defer s.Close()
	r := &APIRunner{BaseURL: s.URL, Key: "wrong"}
	if _, err := r.Run(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}

func TestKeyFromEnvFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".env")
	_ = os.WriteFile(p, []byte("# API_SERVER_KEY=no\nAPI_SERVER_KEY=\"abc\"\n"), 0o600)
	if k := KeyFromEnvFile(p); k != "abc" {
		t.Fatalf("key = %q", k)
	}
	if KeyFromEnvFile(filepath.Join(t.TempDir(), "none")) != "" {
		t.Fatal("missing file must give no key")
	}
}

// A13: the answer text so far is streamed to the sink, never more often than
// PartialEvery, and the first delta goes out immediately.
func TestAPIRunnerReportsPartialAnswerThrottled(t *testing.T) {
	stream := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, delta := range []string{"Hello", " world", "!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", delta)
			f.Flush()
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"status":"ok"}`)
			return
		}
		stream(w)
	}))
	defer s.Close()

	var slow []string
	r := &APIRunner{BaseURL: s.URL, Key: "k", PartialEvery: time.Hour}
	r.SetOnPartial(func(_ context.Context, text string) { slow = append(slow, text) })
	if out, err := r.Run(context.Background(), "hi"); err != nil || !strings.Contains(out, "Hello world!") {
		t.Fatalf("out = %q err = %v", out, err)
	}
	if len(slow) != 1 || slow[0] != "Hello" {
		t.Fatalf("partial calls = %v (first delta immediately, then throttled)", slow)
	}

	var fast []string
	r.PartialEvery = 10 * time.Millisecond
	r.SetOnPartial(func(_ context.Context, text string) { fast = append(fast, text) })
	if _, err := r.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(fast) < 2 || fast[0] != "Hello" || !strings.HasSuffix(fast[len(fast)-1], "!") {
		t.Fatalf("partial calls = %v", fast)
	}
}

// A13: Auto forwards the partial sink to the API path.
func TestAutoForwardsPartialAnswer(t *testing.T) {
	var partial string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer s.Close()
	a := &Auto{API: &APIRunner{BaseURL: s.URL, Key: "k"}}
	a.SetOnPartial(func(_ context.Context, text string) { partial = text })
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if partial != "ok" {
		t.Fatalf("partial = %q", partial)
	}
}
