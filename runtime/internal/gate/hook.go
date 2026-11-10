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

	"github.com/Play-Hireling/hireling/runtime/internal/usage"
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
	// TurnTokens is the running turn's spend so far, sent with the D7
	// `tokens` check only.
	TurnTokens int64 `json:"-"`
}

// Checker asks the policy gate about one action.
type Checker interface {
	Check(ctx context.Context, action, target string, c Context) (Decision, error)
}

// HTTPChecker calls POST /actions/check on tavern-core as one hireling.
type HTTPChecker struct {
	BaseURL string
	Agent   string
	// Token is the owner bearer (FRONTDESK_OWNER_TOKEN). Header only.
	Token string
	HTTP  *http.Client
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
	fields := map[string]any{"agent": c.Agent, "action": action, "target": target, "use": true,
		"why": tc.Why, "turn": tc.Turn, "task": tc.Task}
	if action == Tokens {
		// Always sent: a tavern-core without D7's in-turn check refuses the
		// unknown field (400) instead of judging `tokens` as an action.
		fields["turn_tokens"] = tc.TurnTokens
	}
	body, _ := json.Marshal(fields)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/actions/check", bytes.NewReader(body))
	if err != nil {
		return d, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hireling", c.Agent)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
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
	// Spend reads the running turn's tokens (D7); nil = Hermes' state.db
	// under Home.
	Spend Spender
}

// Spender reads what the sessions started since a time have spent so far.
type Spender interface {
	Tokens(ctx context.Context, since time.Time) (int64, error)
}

// Tokens is the gate action checked before every tool call of a P2/P3 turn
// when the profile has a daily token budget (D7): tavern-core adds the
// running turn's spend to today's tokens and answers free or budget.
const Tokens = "tokens"

// TurnFile is where the runtime writes the running turn for the hook (D4),
// under HERMES_HOME. It is context for the log and the D7 budget check (the
// work's priority, the profile's tokens_per_day), never a permission; the
// hook refuses tool calls that touch it.
const TurnFile = "gate-turn.json"

// turnInfo is the content of TurnFile.
type turnInfo struct {
	Turn         string `json:"turn"`
	Task         string `json:"task"`
	Topic        string `json:"topic"`
	At           string `json:"at"`             // when the turn started (RFC 3339)
	Priority     string `json:"priority"`       // P0–P3 of the work (D7); "" = P2
	TokensPerDay int    `json:"tokens_per_day"` // the profile's budget; 0 = none (D7)
}

func (h *Hook) readTurn() turnInfo {
	var t turnInfo
	if raw, err := os.ReadFile(filepath.Join(h.Home, TurnFile)); err == nil {
		_ = json.Unmarshal(raw, &t)
	}
	return t
}

// turnContext is the running turn and the hireling's own stated reason:
// Composio's MULTI_EXECUTE carries a `thought`; otherwise the turn's topic.
func (h *Hook) turnContext(p Payload, t turnInfo) Context {
	c := Context{Turn: t.Turn, Task: t.Task, Why: t.Topic}
	for _, k := range []string{"thought", "reason", "description"} {
		if v, ok := p.Input[k].(string); ok && strings.TrimSpace(v) != "" {
			c.Why = v
			break
		}
	}
	c.Why = cut(strings.Join(strings.Fields(c.Why), " "), 300)
	return c
}

// Decide classifies the call, stops it when a P2/P3 turn is over the day's
// token budget (D7, any tool) and asks the gate about every external action.
// The strictest answer wins; an unreachable gate blocks external actions
// (fail closed).
func (h *Hook) Decide(ctx context.Context, p Payload) Outcome {
	v := Classify(p.Tool, p.Input, Env{Home: h.Home, Cwd: p.Cwd})
	if v.Kind == Refuse {
		return Outcome{Message: "Blocked by the autonomy gate (D1): " + v.Reason + ". Do not try another way; carry on with the task without it."}
	}
	t := h.readTurn()
	tc := h.turnContext(p, t)
	checked, stop := h.tokenBudget(ctx, p, t, tc)
	if stop != "" {
		return Outcome{Checked: checked, Message: stop}
	}
	if v.Kind == Local {
		return Outcome{Allow: true, Checked: checked}
	}
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

// tokenBudget is D7 inside a turn. Before each tool call of a P2/P3 turn whose
// profile has tokens_per_day, the turn's spend so far (Hermes' store) goes to
// tavern-core, which adds today's recorded tokens: at or over the budget the
// call is refused (and logged there), so the model wraps up instead of
// spending on. P0/P1 work, turns without a budget and calls outside a runtime
// turn are not checked. If tavern-core cannot answer, the turn's own spend is
// held to one day's budget: an outage neither stops the turn's local work
// (as for D1) nor lets it spend without a limit. An unreadable store is
// logged and not cut (the bar loop still holds the next P2/P3 message).
// Returns the log notes and, when the call must stop, the model's message.
func (h *Hook) tokenBudget(ctx context.Context, p Payload, t turnInfo, tc Context) ([]string, string) {
	if t.TokensPerDay <= 0 || exempt(t.Priority) {
		return nil, ""
	}
	at, err := time.Parse(time.RFC3339, t.At)
	if err != nil {
		return nil, "" // no turn start: nothing to measure
	}
	spend := h.Spend
	if spend == nil {
		spend = &usage.Store{DB: filepath.Join(h.Home, "state.db")}
	}
	spent, err := spend.Tokens(ctx, at.Add(-time.Second))
	if err != nil {
		return []string{Tokens + "=unread(" + cut(err.Error(), 80) + ")"}, ""
	}
	tc.TurnTokens = spent
	d, err := h.Checker.Check(ctx, Tokens, p.Tool, tc)
	if err == nil {
		switch d.Level {
		case "free":
			return nil, ""
		case "budget":
			return []string{Tokens + "=budget"}, budgetStop(d.Reason)
		}
		err = fmt.Errorf("unexpected level %q", d.Level)
	}
	if spent >= int64(t.TokensPerDay) {
		return []string{Tokens + "=budget(local)"}, budgetStop(fmt.Sprintf(
			"this turn alone has used %d tokens, the whole daily budget of %d (tavern-core could not confirm today's total: %s)",
			spent, t.TokensPerDay, cut(err.Error(), 120)))
	}
	return []string{Tokens + "=unknown(" + cut(err.Error(), 80) + ")"}, ""
}

// exempt: P0/P1 work still runs over the token budget (D7).
func exempt(prio string) bool {
	p := strings.ToUpper(strings.TrimSpace(prio))
	return p == "P0" || p == "P1"
}

func budgetStop(reason string) string {
	return "Stopped by the token budget (D7): " + reason + ". This tool call was not run. Today's tokens are spent, so this P2/P3 work stops here until the budget resets at 00:00 UTC. " +
		"Do not call any more tools (they will be refused too), do not try a workaround and do not ask the owner for more budget. " +
		"Finish now with a short answer: what is done, what is left, and that the rest waits for the budget reset."
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
