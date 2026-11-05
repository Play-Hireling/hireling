package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Payload is what Hermes pipes to a pre_tool_call shell hook.
type Payload struct {
	Event     string         `json:"hook_event_name"`
	Tool      string         `json:"tool_name"`
	Input     map[string]any `json:"tool_input"`
	SessionID string         `json:"session_id"`
	Cwd       string         `json:"cwd"`
}

// Decision is tavern-core's answer (policy.Decision on the wire).
type Decision struct {
	Level   string `json:"level"` // free | capped | owner | budget (D8)
	Reason  string `json:"reason"`
	Limit   int    `json:"limit"`
	Period  string `json:"period,omitempty"`
	Used    int    `json:"used,omitempty"`
	Counted bool   `json:"counted,omitempty"` // D2: this use was counted within the cap
}

// Context is what the Autonomy Log (D4) records with each action: why the
// hireling did it and the turn (command or message) it happened in.
type Context struct {
	Why  string `json:"why,omitempty"`
	Turn string `json:"turn,omitempty"`
	Task string `json:"task,omitempty"`
}

// Checker asks the policy gate about one action.
type Checker interface {
	Check(ctx context.Context, action, target string, c Context) (Decision, error)
}

// HTTPChecker calls POST /actions/check on tavern-core as one hireling.
type HTTPChecker struct {
	BaseURL string
	Agent   string
	HTTP    *http.Client
}

// ErrNoTavern means TAVERN_URL was not configured: external actions fail closed.
var ErrNoTavern = errors.New("tavern-core is not configured (TAVERN_URL)")

func (c *HTTPChecker) Check(ctx context.Context, action, target string, tc Context) (Decision, error) {
	var d Decision
	if c.BaseURL == "" {
		return d, ErrNoTavern
	}
	// use: the hireling is about to act — tavern-core consumes the owner's grant
	// for it, or remembers the block for the next card.
	body, _ := json.Marshal(map[string]any{"agent": c.Agent, "action": action, "target": target, "use": true,
		"why": tc.Why, "turn": tc.Turn, "task": tc.Task})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/actions/check", bytes.NewReader(body))
	if err != nil {
		return d, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 4 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return d, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 200))
		return d, fmt.Errorf("tavern-core %s: %s", res.Status, bytes.TrimSpace(b))
	}
	if err := json.NewDecoder(res.Body).Decode(&d); err != nil {
		return d, err
	}
	if d.Level == "" {
		return d, errors.New("tavern-core answered without a level")
	}
	return d, nil
}

// Outcome is the hook's answer for one tool call.
type Outcome struct {
	Allow   bool
	Message string // the text the model gets back when blocked
	// For the log line: what was checked and what the gate said.
	Checked []string
}

// Hook decides one tool call.
type Hook struct {
	Agent   string
	Home    string
	Checker Checker
}

// TurnFile is where the runtime writes the running turn for the hook (D4),
// under HERMES_HOME. It is context for the log, never a permission.
const TurnFile = "gate-turn.json"

// turnContext reads the running turn and the hireling's own stated reason:
// Composio's MULTI_EXECUTE carries a `thought`; otherwise the turn's topic.
func (h *Hook) turnContext(p Payload) Context {
	var c Context
	var t struct {
		Turn  string `json:"turn"`
		Task  string `json:"task"`
		Topic string `json:"topic"`
	}
	if raw, err := os.ReadFile(filepath.Join(h.Home, TurnFile)); err == nil && json.Unmarshal(raw, &t) == nil {
		c.Turn, c.Task, c.Why = t.Turn, t.Task, t.Topic
	}
	for _, k := range []string{"thought", "reason", "description"} {
		if v, ok := p.Input[k].(string); ok && strings.TrimSpace(v) != "" {
			c.Why = v
			break
		}
	}
	c.Why = cut(strings.Join(strings.Fields(c.Why), " "), 300)
	return c
}

// Decide classifies the call and asks the gate about every external action.
// The strictest answer wins; an unreachable gate blocks (fail closed).
func (h *Hook) Decide(ctx context.Context, p Payload) Outcome {
	v := Classify(p.Tool, p.Input, Env{Home: h.Home, Cwd: p.Cwd})
	switch v.Kind {
	case Local:
		return Outcome{Allow: true}
	case Refuse:
		return Outcome{Message: "Blocked by the autonomy gate (D1): " + v.Reason + ". Do not try another way; carry on with the task without it."}
	}
	var checked []string
	tc := h.turnContext(p)
	for _, a := range v.Actions {
		d, err := h.Checker.Check(ctx, a.Action, a.Target, tc)
		if err != nil {
			return Outcome{Checked: checked, Message: fmt.Sprintf(
				"Blocked by the autonomy gate (D1): could not reach the policy gate to approve `%s` (%v). Nothing was done. Retry later in this task, or say in your answer that the step is waiting on the gate.",
				a.Action, err)}
		}
		checked = append(checked, a.Action+"="+d.Level)
		switch d.Level {
		case "free":
			continue
		case "budget":
			// D8: the day's web calls are spent; not the owner's to approve.
			return Outcome{Checked: checked, Message: "Blocked by the autonomy gate: " + d.Reason +
				". Nothing was fetched. Do not try another way to reach the web and do not ask the owner for this; carry on with what you have and say in your answer what you could not check."}
		case "capped":
			// D2: tavern-core counted this use within the cap. An answer
			// without the count (an older tavern-core) cannot prove the cap
			// holds, so that use is the owner's.
			if d.Counted {
				continue
			}
			return Outcome{Checked: checked, Message: askOwner(a, "capped action that tavern-core did not count, so each use needs the owner's OK")}
		default:
			return Outcome{Checked: checked, Message: askOwner(a, d.Reason)}
		}
	}
	return Outcome{Allow: true, Checked: checked}
}

func askOwner(a Act, reason string) string {
	return fmt.Sprintf("Blocked by the autonomy gate (D1): `%s` on %s is the owner's decision (%s). Nothing was done. Do not try another tool or a workaround. "+
		"If the task needs it, finish the parts you can, then end your answer with an `ask` block asking the owner to approve exactly this action (what, where, why), with options such as [\"Approve\", \"Not now\"]: his yes lets you do it once.",
		a.Action, cut(a.Target, 120), reason)
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Run is `hireling gate`: one Hermes payload on in, Hermes' directive on out.
// A malformed payload blocks (the hook is fail_closed anyway).
func (h *Hook) Run(ctx context.Context, in io.Reader, out io.Writer, logf func(string, ...any)) error {
	var p Payload
	if err := json.NewDecoder(io.LimitReader(in, 4<<20)).Decode(&p); err != nil {
		return writeBlock(out, "Blocked by the autonomy gate (D1): unreadable tool call.")
	}
	if p.Event != "" && p.Event != "pre_tool_call" {
		_, err := io.WriteString(out, "{}\n")
		return err
	}
	o := h.Decide(ctx, p)
	if logf != nil && (len(o.Checked) > 0 || !o.Allow) {
		state := "allow"
		if !o.Allow {
			state = "block"
		}
		logf("gate %s %s tool=%s checked=%s", h.Agent, state, p.Tool, strings.Join(o.Checked, ","))
	}
	if o.Allow {
		_, err := io.WriteString(out, "{}\n")
		return err
	}
	return writeBlock(out, o.Message)
}

func writeBlock(out io.Writer, msg string) error {
	return json.NewEncoder(out).Encode(map[string]string{"action": "block", "message": msg})
}
