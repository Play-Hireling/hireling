package frontdesk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A13: GET /api/turn/stream pushes the turns as the hireling reports them, so
// the owner reads the first words of an answer before the turn ends.

type sseEvent struct {
	name string
	data []byte
}

type sseStream struct {
	rd *bufio.Reader
}

// readEvent reads one event, skipping keepalive comments.
func readEvent(rd *bufio.Reader) (sseEvent, error) {
	ev := sseEvent{}
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return ev, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if ev.name == "" {
				continue
			}
			return ev, nil
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = []byte(strings.TrimPrefix(line, "data: "))
		}
	}
}

func openStream(t *testing.T, srv *httptest.Server, query string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/turn/stream"+query, nil)
	req.Header.Set("Authorization", "Bearer owner-tok")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	return &sseStream{rd: bufio.NewReader(res.Body)}
}

func (s *sseStream) next(t *testing.T) sseEvent {
	t.Helper()
	done := make(chan sseEvent, 1)
	errs := make(chan error, 1)
	go func() {
		ev, err := readEvent(s.rd)
		if err != nil {
			errs <- err
			return
		}
		done <- ev
	}()
	select {
	case ev := <-done:
		return ev
	case err := <-errs:
		t.Fatalf("read stream: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no stream event within 3s")
	}
	return sseEvent{}
}

func postTurn(t *testing.T, srv *httptest.Server, body string) (int, string) {
	return postTurnAs(t, srv, "dev-tok", body)
}

func postTurnAs(t *testing.T, srv *httptest.Server, token, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/turn", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func turnOf(t *testing.T, ev sseEvent) Turn {
	t.Helper()
	var turn Turn
	if err := json.Unmarshal(ev.data, &turn); err != nil {
		t.Fatalf("event data %q: %v", ev.data, err)
	}
	return turn
}

func TestTurnStreamFirstWordsBeforeEnd(t *testing.T) {
	_, srv := newDesk(t, nil)
	s := openStream(t, srv, "")

	// The first event is the status snapshot: a client never polls again.
	ev := s.next(t)
	if ev.name != "status" {
		t.Fatalf("first event = %q, want status", ev.name)
	}
	var st Status
	if err := json.Unmarshal(ev.data, &st); err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) != 2 {
		t.Fatalf("status agents = %d, want 2", len(st.Agents))
	}

	// A fake model, as runtime/internal/loop reports it: start, the text so far
	// on each step, then the end. Each event must arrive before the next phase
	// is posted, which is what "before the turn ends" means here.
	started := time.Now()
	if code, body := postTurn(t, srv, `{"agent":"dev","phase":"start","topic":"README"}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("start: %d %s", code, body)
	}
	ev = s.next(t)
	if ev.name != "turn" || turnOf(t, ev).Phase != "start" {
		t.Fatalf("event = %q %s, want a turn start", ev.name, ev.data)
	}
	if d := time.Since(started); d > time.Second {
		t.Fatalf("first turn event took %s, want <= 1s", d)
	}

	if code, _ := postTurn(t, srv, `{"agent":"dev","phase":"step","step":"Primeira parte"}`); code != 200 {
		t.Fatalf("step: %d", code)
	}
	ev = s.next(t)
	if turn := turnOf(t, ev); turn.Phase != "step" || turn.Step != "Primeira parte" {
		t.Fatalf("first words = %+v", turn)
	}

	if code, _ := postTurn(t, srv, `{"agent":"dev","phase":"step","step":"Primeira parte, e o resto"}`); code != 200 {
		t.Fatalf("step 2: %d", code)
	}
	ev = s.next(t)
	if turn := turnOf(t, ev); turn.Step != "Primeira parte, e o resto" {
		t.Fatalf("accumulated text = %+v", turn)
	}

	if code, _ := postTurn(t, srv, `{"agent":"dev","phase":"end","result":"Primeira parte, e o resto"}`); code != 200 {
		t.Fatalf("end: %d", code)
	}
	ev = s.next(t)
	if turn := turnOf(t, ev); turn.Phase != "end" || turn.Result != "Primeira parte, e o resto" {
		t.Fatalf("last event = %+v", turn)
	}

	// The old JSON contract is untouched: one JSON answer per report.
	if code, body := postTurn(t, srv, `{"agent":"dev","phase":"end","result":"x"}`); code != 200 || strings.TrimSpace(body) != `{"ok":true}` {
		t.Fatalf("POST /api/turn = %d %s, want 200 {\"ok\":true}", code, body)
	}
}

func TestTurnStreamFiltersAgent(t *testing.T) {
	_, srv := newDesk(t, nil)
	s := openStream(t, srv, "?agent=dev")
	s.next(t) // status

	// The innkeeper reports; a dev-only stream must not receive it.
	if code, _ := postTurnAs(t, srv, "shared", `{"agent":"innkeeper","phase":"start","topic":"plan"}`); code != 200 {
		t.Fatalf("innkeeper start: %d", code)
	}
	if code, _ := postTurn(t, srv, `{"agent":"dev","phase":"start","topic":"README"}`); code != 200 {
		t.Fatalf("dev start: %d", code)
	}
	ev := s.next(t)
	if turn := turnOf(t, ev); turn.Agent != "dev" {
		t.Fatalf("filtered stream got %+v, want only dev", turn)
	}
}

func TestTurnStreamClosesWithExplicitError(t *testing.T) {
	d, srv := newDesk(t, nil)
	s := openStream(t, srv, "?agent=dev")
	s.next(t) // status
	if code, _ := postTurn(t, srv, `{"agent":"dev","phase":"start","topic":"README"}`); code != 200 {
		t.Fatalf("start: %d", code)
	}
	s.next(t)

	d.Close()
	ev := s.next(t)
	if ev.name != "error" || !strings.Contains(string(ev.data), "desk closed") {
		t.Fatalf("mid-stream close = %q %s, want an error event", ev.name, ev.data)
	}
	// The stream ends right after the error, not hanging.
	done := make(chan error, 1)
	go func() {
		_, err := readEvent(s.rd)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("stream after error: %v, want EOF", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream kept hanging after the error event")
	}
	// Closing the streams does not break the desk.
	if code, body := postTurn(t, srv, `{"agent":"dev","phase":"end","result":"done"}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("turn after close: %d %s", code, body)
	}
}

func TestTurnStreamRejectsBeforeFirstByte(t *testing.T) {
	_, srv := newDesk(t, nil)

	res, err := http.Get(srv.URL + "/api/turn/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("no token: %d %q, want 401 JSON", res.StatusCode, res.Header.Get("Content-Type"))
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/turn/stream?agent=nobody", nil)
	req.Header.Set("Authorization", "Bearer owner-tok")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusBadRequest || !strings.HasPrefix(res2.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown agent: %d %q, want 400 JSON", res2.StatusCode, res2.Header.Get("Content-Type"))
	}
}

// A writer that cannot flush gets an explicit status before any byte, not a
// stream that would only arrive when the turn ends.
type noFlushWriter struct{ http.ResponseWriter }

func TestTurnStreamWithoutAFlusherSaysSo(t *testing.T) {
	d, _ := newDesk(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/turn/stream", nil)
	req.Header.Set("Authorization", "Bearer owner-tok")
	d.turnStream(noFlushWriter{rec}, req)
	if rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("no flusher: %d %q, want 500 JSON", rec.Code, rec.Header().Get("Content-Type"))
	}
}
