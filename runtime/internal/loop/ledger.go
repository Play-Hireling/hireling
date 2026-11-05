package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/ledger"
	"github.com/Play-Hireling/hireling/runtime/internal/plan"
)

// applyLedger (E3) sends each `ledger` block of an answer to the ledger
// writer, which commits the page as this hireling. The block is replaced by
// one line saying what happened. A page outside the profile's ledger.write
// areas is refused here already (the writer checks again: it trusts nothing
// the runtime says about who it is).
func (l *Loop) applyLedger(ctx context.Context, answer string) string {
	pages, rest, err := plan.ParseLedger(answer)
	if errors.Is(err, plan.ErrNone) {
		return answer
	}
	if err != nil {
		l.logf("ledger: %v", err)
		return rest + "\n\n(ledger: " + err.Error() + ")"
	}
	var notes []string
	for _, p := range pages {
		notes = append(notes, l.writePage(ctx, p))
	}
	return strings.TrimSpace(rest + "\n\n" + strings.Join(notes, "\n"))
}

func (l *Loop) writePage(ctx context.Context, p plan.LedgerPage) string {
	if l.LedgerWriter == "" {
		return "(ledger page " + p.Path + " not written: the ledger writer is not configured here)"
	}
	if !ledger.InArea(l.Profile.Ledger.Write, p.Path) {
		return fmt.Sprintf("(ledger page %s not written: you may write only %s)", p.Path, strings.Join(l.Profile.Ledger.Write, ", "))
	}
	body, _ := json.Marshal(map[string]string{"path": p.Path, "content": p.Content})
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.LedgerWriter, "/")+"/pages", bytes.NewReader(body))
	if err != nil {
		return "(ledger page " + p.Path + " not written: " + err.Error() + ")"
	}
	req.Header.Set("Content-Type", "application/json")
	c := &http.Client{Transport: &http.Transport{Proxy: nil}}
	res, err := c.Do(req)
	if err != nil {
		l.logf("ledger %s: %v", p.Path, err)
		return "(ledger page " + p.Path + " not written: the ledger writer did not answer)"
	}
	defer res.Body.Close()
	var out struct {
		Path, Commit, Author, Error string
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != http.StatusOK {
		l.logf("ledger %s refused (%d): %s", p.Path, res.StatusCode, out.Error)
		return "(ledger page " + p.Path + " not written: " + l.Redactor.Summary(out.Error) + ")"
	}
	l.logf("ledger: wrote %s (%s)", out.Path, out.Commit)
	if l.Bus != nil {
		_ = l.Bus.Event(ctx, "ledger.written", map[string]any{"path": out.Path, "commit": out.Commit, "author": out.Author})
	}
	return fmt.Sprintf("Ledger: wrote %s (commit %s, signed by %s).", out.Path, out.Commit, out.Author)
}
