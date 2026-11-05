package loop

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// E3 — a ledger block goes to the writer and comes back as one line; a page
// outside the profile's areas never leaves the container.
func TestApplyLedger(t *testing.T) {
	var got []map[string]string
	w := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		got = append(got, in)
		if strings.Contains(in["path"], "bad") {
			rw.WriteHeader(422)
			_, _ = rw.Write([]byte(`{"error":"ledger writer: invalid page: timeline missing"}`))
			return
		}
		_, _ = rw.Write([]byte(`{"path":"brain/` + in["path"] + `","commit":"abc1234","author":"test"}`))
	}))
	defer w.Close()
	_, h := newServer(t)
	l := newLoop(h, &fakeTurner{})
	l.Profile.Ledger.Write = []string{"learnings/test/*"}
	l.LedgerWriter = w.URL
	ans := "Carousel ready.\n\n````ledger learnings/test/ok.md\n---\ntitle: X\n---\n````\n````ledger learnings/test/bad.md\nx\n````\n````ledger goal.md\nx\n````"
	out := l.applyLedger(context.Background(), ans)
	if len(got) != 2 {
		t.Fatalf("writer got %d pages (goal.md must not leave): %v", len(got), got)
	}
	for _, want := range []string{
		"Carousel ready.",
		"Ledger: wrote brain/learnings/test/ok.md (commit abc1234, signed by test).",
		"(ledger page learnings/test/bad.md not written: ledger writer: invalid page: timeline missing)",
		"(ledger page goal.md not written: you may write only learnings/test/*)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "````") {
		t.Fatalf("blocks left in the answer: %q", out)
	}
	l.LedgerWriter = ""
	if out := l.applyLedger(context.Background(), "x\n````ledger learnings/test/a.md\ny\n````"); !strings.Contains(out, "not configured") {
		t.Fatal(out)
	}
	if out := l.applyLedger(context.Background(), "plain answer"); out != "plain answer" {
		t.Fatal(out)
	}
}
