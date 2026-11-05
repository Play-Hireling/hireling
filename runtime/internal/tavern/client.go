// Package tavern is the hireling's client for tavern-core (services/tavern-core),
// reached on the Compose network (`TAVERN_URL`, e.g. http://tavern-core:8080).
// Like the front desk client, a failure never blocks the work: callers log and go on.
package tavern

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/usage"
)

// Client talks to one tavern-core as one hireling.
type Client struct {
	BaseURL string
	Agent   string
	HTTP    *http.Client
}

// ErrNotConfigured is returned when TAVERN_URL is unset.
var ErrNotConfigured = errors.New("tavern: TAVERN_URL not set")

// ReportUsage posts one turn's usage: POST /agents/{id}/usage.
func (c *Client) ReportUsage(ctx context.Context, t usage.Turn) error {
	return c.post(ctx, "/agents/"+c.Agent+"/usage", t, nil)
}

// Goal mirrors tavern-core's goal_contribution.
type Goal struct {
	Kind     string `json:"kind,omitempty"`
	Expected int    `json:"expected,omitempty"`
	Note     string `json:"note,omitempty"` // C3: how it moves the quest
}

// CreateTask is the body of POST /tasks (services/tavern-core tasks.CreateInput).
type CreateTask struct {
	Title            string   `json:"title"`
	RequestedBy      string   `json:"requested_by"`
	Owner            string   `json:"owner,omitempty"`
	Priority         string   `json:"priority,omitempty"`
	GoalContribution Goal     `json:"goal_contribution"`
	Attachments      []string `json:"attachments,omitempty"`
	Parent           string   `json:"parent,omitempty"`
	Brief            string   `json:"brief,omitempty"`
}

// Task is what tavern-core answers (the fields the runtime uses).
type Task struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	Owner            string   `json:"owner"`
	RequestedBy      string   `json:"requested_by"`
	Priority         string   `json:"priority"`
	Status           string   `json:"status"`
	LeaseExpiresAt   string   `json:"lease_expires_at,omitempty"`
	Progress         string   `json:"progress,omitempty"`
	Parent           string   `json:"parent,omitempty"`
	Brief            string   `json:"brief,omitempty"`
	Attachments      []string `json:"attachments,omitempty"`
	GoalContribution Goal     `json:"goal_contribution"`
	NeedsApproval    bool     `json:"needs_approval,omitempty"`
	DecidedBy        string   `json:"decided_by,omitempty"`
	DecisionNote     string   `json:"decision_note,omitempty"`
	Failures         int      `json:"failures,omitempty"`
	LastError        string   `json:"last_error,omitempty"`
	BlockedReason    string   `json:"blocked_reason,omitempty"`
}

// Decide approves or denies a requested handoff (B6): POST /tasks/{id}/approve|deny.
func (c *Client) Decide(ctx context.Context, id string, approve bool, note string) (*Task, error) {
	path := "/tasks/" + id + "/deny"
	if approve {
		path = "/tasks/" + id + "/approve"
	}
	var t Task
	if err := c.post(ctx, path, map[string]string{"note": note}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// NewTask creates a task (B2): POST /tasks.
func (c *Client) NewTask(ctx context.Context, in CreateTask) (*Task, error) {
	var t Task
	if err := c.post(ctx, "/tasks", in, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Client) post(ctx context.Context, path string, in any, out any) error {
	if c.BaseURL == "" {
		return ErrNotConfigured
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("tavern: %s %s: %s", path, res.Status, bytes.TrimSpace(b))
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

// Quest is GET /goal (C1): the shared goal with the percentage for the bar.
type Quest struct {
	Title    string   `json:"title"`
	Target   float64  `json:"target"`
	Current  float64  `json:"current"`
	Currency string   `json:"currency"`
	Source   string   `json:"source"`
	Period   string   `json:"period,omitempty"`
	Percent  float64  `json:"percent"`
	Bar      string   `json:"bar"`
	Kinds    []string `json:"kinds"`
}

// ErrNoQuest is returned when tavern-core has no quest yet (404).
var ErrNoQuest = errors.New("tavern: no quest set")

// Quest reads the shared goal.
func (c *Client) Quest(ctx context.Context) (*Quest, error) {
	if c.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/goal", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNoQuest
	}
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("tavern: GET /goal %s: %s", res.Status, bytes.TrimSpace(b))
	}
	var q Quest
	return &q, json.NewDecoder(res.Body).Decode(&q)
}

// Queue is GET /agents/{id}/queue (B3): this hireling's queued tasks in the
// order to take them (P0 > P1 > P2 > P3, ties by goal contribution, oldest).
func (c *Client) Queue(ctx context.Context) ([]Task, error) {
	if c.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/agents/"+c.Agent+"/queue", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("tavern: GET queue %s: %s", res.Status, bytes.TrimSpace(b))
	}
	var out struct {
		Tasks []Task `json:"tasks"`
	}
	return out.Tasks, json.NewDecoder(res.Body).Decode(&out)
}

// ---- Leases (B4) ------------------------------------------------------------

// GetTask is GET /tasks/{id}.
func (c *Client) GetTask(ctx context.Context, id string) (*Task, error) {
	if c.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/tasks/"+id, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("tavern: GET /tasks/%s %s: %s", id, res.Status, bytes.TrimSpace(b))
	}
	var t Task
	return &t, json.NewDecoder(res.Body).Decode(&t)
}

// Lease takes the task for 15 minutes: POST /tasks/{id}/lease.
func (c *Client) Lease(ctx context.Context, id string) (*Task, error) {
	var t Task
	return &t, c.post(ctx, "/tasks/"+id+"/lease", map[string]string{}, &t)
}

// Renew extends the lease (every step): POST /tasks/{id}/renew.
func (c *Client) Renew(ctx context.Context, id string) error {
	return c.post(ctx, "/tasks/"+id+"/renew", map[string]string{}, nil)
}

// Finish closes the task as done or failed: POST /tasks/{id}/finish.
func (c *Client) Finish(ctx context.Context, id string, done bool) error {
	status := "failed"
	if done {
		status = "done"
	}
	return c.post(ctx, "/tasks/"+id+"/finish", map[string]string{"status": status}, nil)
}

// Release lets the lease go without finishing: back to the queue, or parked
// until the owner answers (status queued | waiting_owner): POST /tasks/{id}/release.
func (c *Client) Release(ctx context.Context, id, status, progress string) error {
	return c.post(ctx, "/tasks/"+id+"/release", map[string]string{"status": status, "progress": progress}, nil)
}

// Interrupt returns a leased task to the queue after a P0 cut its turn short,
// with where the progress is (B5): POST /tasks/{id}/release, reason interrupt.
func (c *Client) Interrupt(ctx context.Context, id, progress string) error {
	return c.post(ctx, "/tasks/"+id+"/release", map[string]string{"status": "queued", "progress": progress, "reason": "interrupt"}, nil)
}

// Fail records a failed turn on a leased task (B11): back to the queue, or
// blocked needs_human once max failures in a row: POST /tasks/{id}/fail.
func (c *Client) Fail(ctx context.Context, id, lastError string, max int) (*Task, error) {
	var t Task
	if err := c.post(ctx, "/tasks/"+id+"/fail", map[string]any{"error": lastError, "max": max}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Unblock applies the owner's answer to an escalated task (B11): drop cancels it,
// anything else re-queues it with the count reset: POST /tasks/{id}/unblock.
func (c *Client) Unblock(ctx context.Context, id, action string) (*Task, error) {
	var t Task
	if err := c.post(ctx, "/tasks/"+id+"/unblock", map[string]string{"action": action}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// GateAct is one action the autonomy gate blocked or granted (D1).
type GateAct struct {
	Action string    `json:"action"`
	Target string    `json:"target,omitempty"`
	At     time.Time `json:"at,omitempty"`
}

// RecentBlocked lists what the gate blocked for this hireling since a time
// (GET /agents/{id}/blocked).
func (c *Client) RecentBlocked(ctx context.Context, since time.Time) ([]GateAct, error) {
	if c.BaseURL == "" {
		return nil, ErrNotConfigured
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/agents/"+url.PathEscape(c.Agent)+"/blocked?since="+url.QueryEscape(since.UTC().Format(time.RFC3339Nano)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Hireling", c.Agent)
	h := c.HTTP
	if h == nil {
		h = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return nil, fmt.Errorf("tavern: blocked %s: %s", res.Status, bytes.TrimSpace(b))
	}
	var acts []GateAct
	return acts, json.NewDecoder(res.Body).Decode(&acts)
}

// Pend attaches gate blocks to the owner's decision card (POST /actions/pending).
func (c *Client) Pend(ctx context.Context, decision string, acts []GateAct, approve string) error {
	return c.post(ctx, "/actions/pending", map[string]any{"decision": decision, "agent": c.Agent, "actions": acts, "approve": approve}, nil)
}

// Answer passes the owner's choice on a card; a yes returns the granted actions
// (POST /actions/answer).
func (c *Client) Answer(ctx context.Context, decision, choice string) ([]GateAct, error) {
	var out struct {
		Granted []GateAct `json:"granted"`
	}
	if err := c.post(ctx, "/actions/answer", map[string]string{"decision": decision, "choice": choice}, &out); err != nil {
		return nil, err
	}
	return out.Granted, nil
}

// ---- D11: autonomy first ----------------------------------------------------

// AskCheck is the question the runtime puts to tavern-core before opening a card.
type AskCheck struct {
	Task     string `json:"task,omitempty"`
	Turn     string `json:"turn,omitempty"`
	Action   string `json:"action,omitempty"`
	Question string `json:"question,omitempty"`
	Blocked  bool   `json:"blocked,omitempty"` // the gate blocked an action in this turn
}

// AskVerdict is tavern-core's answer: yours | joined | ask.
type AskVerdict struct {
	Verdict   string   `json:"verdict"`
	Level     string   `json:"level,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Decision  string   `json:"decision,omitempty"`
	FollowUps []string `json:"follow_ups,omitempty"`
}

// OpenAsk is a card the runtime opened (POST /asks/opened).
type OpenAsk struct {
	Decision    string `json:"decision"`
	Task        string `json:"task,omitempty"`
	Action      string `json:"action,omitempty"`
	Question    string `json:"question"`
	Recommended string `json:"recommended,omitempty"`
}

// AnsweredAsk is a closed card: its follow-ups and whether the owner picked the
// recommendation.
type AnsweredAsk struct {
	Task              string   `json:"task,omitempty"`
	Question          string   `json:"question"`
	FollowUps         []string `json:"follow_ups,omitempty"`
	Choice            string   `json:"choice"`
	PickedRecommended bool     `json:"picked_recommended"`
}

// PreAsk asks whether an ask block opens a card (POST /asks).
func (c *Client) PreAsk(ctx context.Context, in AskCheck) (AskVerdict, error) {
	var v AskVerdict
	body := map[string]any{"agent": c.Agent, "task": in.Task, "turn": in.Turn, "action": in.Action, "question": in.Question, "blocked": in.Blocked}
	err := c.post(ctx, "/asks", body, &v)
	return v, err
}

// AskOpened records the card the runtime opened (POST /asks/opened).
func (c *Client) AskOpened(ctx context.Context, o OpenAsk) error {
	return c.post(ctx, "/asks/opened", map[string]any{"agent": c.Agent, "decision": o.Decision, "task": o.Task,
		"action": o.Action, "question": o.Question, "recommended": o.Recommended}, nil)
}

// AskAnswer closes a card with the owner's choice (POST /asks/answer); nil when
// tavern-core has no card with that id.
func (c *Client) AskAnswer(ctx context.Context, decision, choice string) (*AnsweredAsk, error) {
	var out struct {
		Found bool        `json:"found"`
		Card  AnsweredAsk `json:"card"`
	}
	if err := c.post(ctx, "/asks/answer", map[string]string{"decision": decision, "choice": choice}, &out); err != nil {
		return nil, err
	}
	if !out.Found {
		return nil, nil
	}
	return &out.Card, nil
}
