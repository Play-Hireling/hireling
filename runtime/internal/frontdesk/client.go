// Package frontdesk is the client of the Front Desk API (docs/api/front-desk.md):
// turn reports (A3), the command queue and acknowledgements (A4).
// The reference server is services/tavern-core/internal/frontdesk.
package frontdesk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Phase of a turn.
type Phase string

const (
	Start Phase = "start"
	Step  Phase = "step"
	End   Phase = "end"
)

// Turn is the body of POST /api/turn.
type Turn struct {
	Agent          string `json:"agent"`
	Phase          Phase  `json:"phase"`
	With           string `json:"with,omitempty"`
	Topic          string `json:"topic,omitempty"`
	Step           string `json:"step,omitempty"`
	Result         string `json:"result,omitempty"`
	Screen         string `json:"screen,omitempty"`
	WaitingOnOwner string `json:"waitingOnOwner,omitempty"`
	// Where the container runs (the server flag).
	Server string `json:"server,omitempty"`
	Region string `json:"region,omitempty"`
}

// Command is one entry of GET /api/commands/<agent>. DecisionID is set when
// the command is the owner's answer to a decision this agent asked for (D3): the
// text reads `Decision: <question> → chosen: <option> (decision <id>)`.
type Command struct {
	ID            string `json:"id"`
	Text          string `json:"text"`
	Instructions  string `json:"instructions,omitempty"`
	WaitingAnswer string `json:"waitingAnswer,omitempty"`
	DecisionID    string `json:"decisionId,omitempty"`
}

// Client talks to one front desk instance with one agent's token.
type Client struct {
	BaseURL        string
	Token          string
	CFAccessID     string
	CFAccessSecret string
	HTTP           *http.Client
}

// ErrNotConfigured is returned when the base URL or token is missing; the
// runtime keeps working and only logs (the desk is never allowed to block the work).
var ErrNotConfigured = errors.New("frontdesk: FRONTDESK_URL or token not set")

func (c *Client) ready() error {
	if c.BaseURL == "" || c.Token == "" {
		return ErrNotConfigured
	}
	if !strings.HasPrefix(c.BaseURL, "https://") && !strings.HasPrefix(c.BaseURL, "http://localhost") && !strings.HasPrefix(c.BaseURL, "http://127.0.0.1") {
		return fmt.Errorf("frontdesk: FRONTDESK_URL must be https://")
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if err := c.ready(); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	if c.CFAccessID != "" {
		req.Header.Set("CF-Access-Client-Id", c.CFAccessID)
		req.Header.Set("CF-Access-Client-Secret", c.CFAccessSecret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return fmt.Errorf("frontdesk: %s %s: network: %w", method, path, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		// Never echo the response body: it could carry what the server rejected.
		return fmt.Errorf("frontdesk: %s %s: HTTP %d", method, path, res.StatusCode)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// ReportTurn reports a turn phase (POST /api/turn). Texts must already be redacted and summarized.
func (c *Client) ReportTurn(ctx context.Context, t Turn) error {
	return c.do(ctx, http.MethodPost, "/api/turn", t, nil)
}

// Commands reads the pending queue, oldest first.
func (c *Client) Commands(ctx context.Context, agent string) ([]Command, error) {
	var out struct {
		Commands []Command `json:"commands"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/commands/"+agent, nil, &out); err != nil {
		return nil, err
	}
	return out.Commands, nil
}

// LongPoll is how long WaitCommands asks the front desk to hold an empty
// queue (A13; the server caps it at 25 s).
const LongPoll = 25 * time.Second

// WaitCommands is Commands with ?wait= (A13 long poll): with an empty queue the
// front desk answers as soon as a command arrives, or after LongPoll. waited
// is false when the server ignored the parameter (an front desk without long
// poll answers at once) — the caller then keeps its own polling interval.
func (c *Client) WaitCommands(ctx context.Context, agent string) (cmds []Command, waited bool, err error) {
	if err := c.ready(); err != nil {
		return nil, false, err
	}
	var out struct {
		Commands []Command `json:"commands"`
		Waited   bool      `json:"waited"`
	}
	long := *c
	if c.HTTP == nil || c.HTTP.Timeout > 0 && c.HTTP.Timeout < LongPoll+15*time.Second {
		h := &http.Client{Timeout: LongPoll + 15*time.Second}
		if c.HTTP != nil {
			h.Transport = c.HTTP.Transport
		}
		long.HTTP = h
	}
	if err := long.do(ctx, http.MethodGet, "/api/commands/"+agent+"?wait="+fmt.Sprint(int(LongPoll.Seconds())), nil, &out); err != nil {
		return nil, false, err
	}
	return out.Commands, out.Waited, nil
}

// Ack acknowledges a command. result is "done", "rejected" or "received" (received
// only removes the command from the queue; finish later with done/rejected).
// note ≤ 200 chars shows in the panel; answer ≤ 4000 chars is the full reply.
func (c *Client) Ack(ctx context.Context, agent, id, result, note, answer string) error {
	body := map[string]string{"id": id}
	if result != "received" {
		body["result"] = result
	}
	if note != "" {
		body["note"] = cut(note, 200)
	}
	if answer != "" {
		body["answer"] = cut(answer, 4000)
	}
	return c.do(ctx, http.MethodPost, "/api/commands/"+agent+"/ack", body, nil)
}

func cut(s string, n int) string {
	if rs := []rune(s); len(rs) > n {
		return string(rs[:n-1]) + "…"
	}
	return s
}

// ---- Conversations (B8) --------------------

// Interaction is one entry of GET /api/interactions: an assistant talking to
// another; the tavern shows both at the table until `reply` arrives.
type Interaction struct {
	ID    string `json:"id"`
	From  string `json:"from"`
	To    string `json:"to"`
	Text  string `json:"text"`
	Reply string `json:"reply,omitempty"`
	At    string `json:"at"`
}

// Interactions lists the recent interactions (GET /api/interactions).
func (c *Client) Interactions(ctx context.Context) ([]Interaction, error) {
	var out struct {
		Interactions []Interaction `json:"interactions"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/interactions", nil, &out); err != nil {
		return nil, err
	}
	return out.Interactions, nil
}

// NewInteraction starts a conversation from `from` (whose token this client holds) to `to`.
func (c *Client) NewInteraction(ctx context.Context, from, to, text string) (string, error) {
	var out struct {
		Interaction Interaction `json:"interaction"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/interaction", map[string]string{"from": from, "to": to, "text": text}, &out); err != nil {
		return "", err
	}
	return out.Interaction.ID, nil
}

// ReplyInteraction closes an open conversation addressed to `agent`.
func (c *Client) ReplyInteraction(ctx context.Context, id, agent, reply string) error {
	return c.do(ctx, http.MethodPost, "/api/interaction/"+id+"/reply", map[string]string{"agent": agent, "reply": reply}, nil)
}

// ChatWindow is how far back an open interaction counts as "the same conversation".
const ChatWindow = 2 * time.Hour

// ReportChat mirrors one bar message on the Front Desk with this rule: an
// open interaction from `other` to `me` in the last two
// hours gets `subject` as its reply (the pair leaves the table); otherwise a
// new interaction from `me` to `other` is created (the pair sits down).
// Returns the interaction id and "reply" or "new".
func (c *Client) ReportChat(ctx context.Context, me, other, subject string) (string, string, error) {
	if me == other {
		return "", "", fmt.Errorf("frontdesk: chat with oneself")
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = "-"
	}
	if items, err := c.Interactions(ctx); err == nil {
		var open *Interaction
		cutoff := time.Now().Add(-ChatWindow)
		for i := range items {
			it := &items[i]
			if it.From != other || it.To != me || it.Reply != "" {
				continue
			}
			if at, err := time.Parse(time.RFC3339Nano, it.At); err == nil && at.Before(cutoff) {
				continue
			}
			if open == nil || it.At > open.At {
				open = it
			}
		}
		if open != nil {
			if err := c.ReplyInteraction(ctx, open.ID, me, subject); err == nil {
				return open.ID, "reply", nil
			}
			// answered or removed meanwhile: fall through to a new conversation
		}
	}
	id, err := c.NewInteraction(ctx, me, other, subject)
	if err != nil {
		return "", "", err
	}
	return id, "new", nil
}

// ---- Decisions (D3) ------------------

// DecisionRequest is the body of POST /api/decisions.
type DecisionRequest struct {
	From     string   `json:"from"`
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Context  string   `json:"context,omitempty"`
	Urgency  string   `json:"urgency,omitempty"`
}

// RequestDecision opens a card on the owner's "Waiting on you" board and returns
// its id. The choice comes back through GET /api/commands/<agent> with
// DecisionID set.
func (c *Client) RequestDecision(ctx context.Context, d DecisionRequest) (string, error) {
	var out struct {
		Decision struct {
			ID string `json:"id"`
		} `json:"decision"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/decisions", d, &out); err != nil {
		return "", err
	}
	return out.Decision.ID, nil
}
