package tavern

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/usage"
)

// seen is one request the fake tavern-core received.
type seen struct {
	Method, Path, RawPath, Query string
	ContentType, Hireling        string
	Body                         []byte
}

// json decodes the request body into a generic map (numbers stay float64).
func (s seen) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(s.Body, &m); err != nil {
		t.Fatalf("%s %s body %q: %v", s.Method, s.Path, s.Body, err)
	}
	return m
}

// fake is a tavern-core that records every request and answers with a fixed
// status and body.
type fake struct {
	mu   sync.Mutex
	reqs []seen
}

func (f *fake) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 1 {
		t.Fatalf("want 1 request, got %d: %+v", len(f.reqs), f.reqs)
	}
	return f.reqs[0]
}

func serve(t *testing.T, status int, reply string) (*Client, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, seen{Method: r.Method, Path: r.URL.Path, RawPath: r.URL.EscapedPath(), Query: r.URL.RawQuery,
			ContentType: r.Header.Get("Content-Type"), Hireling: r.Header.Get("X-Hireling"), Body: b})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Agent: "dev", HTTP: srv.Client()}, f
}

// wantPost checks a POST's path, headers and JSON body.
func wantPost(t *testing.T, s seen, path string, body map[string]any) {
	t.Helper()
	if s.Method != http.MethodPost || s.Path != path {
		t.Fatalf("request = %s %s, want POST %s", s.Method, s.Path, path)
	}
	if s.ContentType != "application/json" || s.Hireling != "dev" {
		t.Fatalf("headers: Content-Type %q, X-Hireling %q", s.ContentType, s.Hireling)
	}
	if got := s.json(t); !reflect.DeepEqual(got, body) {
		t.Fatalf("body = %v\nwant   %v", got, body)
	}
}

// wantGet checks a GET's path and headers.
func wantGet(t *testing.T, s seen, path string) {
	t.Helper()
	if s.Method != http.MethodGet || s.Path != path {
		t.Fatalf("request = %s %s, want GET %s", s.Method, s.Path, path)
	}
	if s.Hireling != "dev" || len(s.Body) != 0 {
		t.Fatalf("X-Hireling %q, body %q", s.Hireling, s.Body)
	}
}

// wantHTTPError checks that a non-2xx answer becomes an error carrying the
// status and the server's message.
func wantHTTPError(t *testing.T, err error, status, msg string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), status) || !strings.Contains(err.Error(), msg) {
		t.Fatalf("err = %v, want %q and %q", err, status, msg)
	}
}

var at = time.Date(2026, 11, 5, 14, 30, 0, 123, time.UTC)

// ---- gate grants (D1) -------------------------------------------------------

func TestPend(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"decision":"d-1"}`)
	acts := []GateAct{{Action: "open_pr", Target: "my-app", At: at}, {Action: "web_call", At: at}}
	if err := c.Pend(context.Background(), "d-1", acts, "Yes, go"); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/actions/pending", map[string]any{
		"decision": "d-1", "agent": "dev", "approve": "Yes, go",
		"actions": []any{
			map[string]any{"action": "open_pr", "target": "my-app", "at": "2026-11-05T14:30:00.000000123Z"},
			map[string]any{"action": "web_call", "at": "2026-11-05T14:30:00.000000123Z"},
		},
	})
}

func TestPendErrors(t *testing.T) {
	c, _ := serve(t, http.StatusBadRequest, `{"error":"decision, agent and actions are required"}`)
	wantHTTPError(t, c.Pend(context.Background(), "", nil, ""), "400 Bad Request", "decision, agent and actions are required")
	c, _ = serve(t, http.StatusServiceUnavailable, `{"error":"grants are off"}`)
	wantHTTPError(t, c.Pend(context.Background(), "d-1", []GateAct{{Action: "x"}}, "Yes"), "503", "grants are off")
}

// The answer names the hireling, so tavern-core can check the Front Desk's
// record of the owner's choice; a yes returns the actions granted.
func TestAnswer(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"granted":[{"action":"open_pr","target":"my-app","at":"2026-11-05T14:30:00Z"}]}`)
	got, err := c.Answer(context.Background(), "d-1", "Yes, go")
	if err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/actions/answer", map[string]any{"decision": "d-1", "choice": "Yes, go", "agent": "dev"})
	want := []GateAct{{Action: "open_pr", Target: "my-app", At: time.Date(2026, 11, 5, 14, 30, 0, 0, time.UTC)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("granted = %+v", got)
	}
}

func TestAnswerNothingGranted(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `{"granted":[]}`)
	if got, err := c.Answer(context.Background(), "d-1", "No"); err != nil || len(got) != 0 {
		t.Fatalf("granted = %v %v", got, err)
	}
}

func TestAnswerRefused(t *testing.T) {
	for _, tc := range []struct {
		status int
		text   string
		reply  string
	}{
		{http.StatusForbidden, "403 Forbidden", `{"error":"frontdesk: decision not approved"}`},
		{http.StatusBadRequest, "400 Bad Request", `{"error":"body must be {decision, choice, agent}"}`},
		{http.StatusServiceUnavailable, "503 Service Unavailable", `{"error":"frontdesk: not configured"}`},
	} {
		c, _ := serve(t, tc.status, tc.reply)
		got, err := c.Answer(context.Background(), "d-1", "Yes")
		if got != nil {
			t.Fatalf("%d: granted %v on error", tc.status, got)
		}
		wantHTTPError(t, err, tc.text, strings.TrimSuffix(strings.TrimPrefix(tc.reply, `{"error":"`), `"}`))
	}
}

func TestRecentBlocked(t *testing.T) {
	c, f := serve(t, http.StatusOK, `[{"action":"open_pr","target":"repo","at":"2026-11-05T14:00:00Z"},{"action":"web_call"}]`)
	since := time.Date(2026, 11, 5, 11, 0, 0, 5, time.FixedZone("BRT", -3*3600))
	got, err := c.RecentBlocked(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	wantGet(t, s, "/agents/dev/blocked")
	if s.Query != "since=2026-11-05T14%3A00%3A00.000000005Z" {
		t.Fatalf("query = %q (since must be RFC 3339 nano in UTC)", s.Query)
	}
	want := []GateAct{{Action: "open_pr", Target: "repo", At: time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)}, {Action: "web_call"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("acts = %+v", got)
	}
}

func TestRecentBlockedEscapesAgent(t *testing.T) {
	c, f := serve(t, http.StatusOK, `[]`)
	c.Agent = "a b/c"
	if got, err := c.RecentBlocked(context.Background(), at); err != nil || len(got) != 0 {
		t.Fatalf("acts = %v %v", got, err)
	}
	if s := f.last(t); s.RawPath != "/agents/a%20b%2Fc/blocked" || s.Hireling != "a b/c" {
		t.Fatalf("path %q, X-Hireling %q", s.RawPath, s.Hireling)
	}
}

func TestRecentBlockedErrors(t *testing.T) {
	c, _ := serve(t, http.StatusBadRequest, `{"error":"since must be RFC 3339"}`)
	_, err := c.RecentBlocked(context.Background(), at)
	wantHTTPError(t, err, "400 Bad Request", "since must be RFC 3339")
	c, _ = serve(t, http.StatusOK, `{"not":"a list"}`)
	if _, err := c.RecentBlocked(context.Background(), at); err == nil {
		t.Fatal("object decoded as a list")
	}
}

// ---- leases (B4, B5, B11) ---------------------------------------------------

const leasedTask = `{"id":"T-1","title":"hooks","owner":"dev","requested_by":"innkeeper","priority":"P2","status":"leased","lease_expires_at":"2026-11-05T14:45:00Z","progress":"half"}`

func TestLease(t *testing.T) {
	c, f := serve(t, http.StatusOK, leasedTask)
	got, err := c.Lease(context.Background(), "T-1")
	if err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/lease", map[string]any{})
	if got.ID != "T-1" || got.Status != "leased" || got.Owner != "dev" || got.LeaseExpiresAt != "2026-11-05T14:45:00Z" || got.Progress != "half" {
		t.Fatalf("task = %+v", got)
	}
}

func TestLeaseConflict(t *testing.T) {
	c, _ := serve(t, http.StatusConflict, "  {\"error\":\"not the lease owner\"}\n")
	_, err := c.Lease(context.Background(), "T-1")
	wantHTTPError(t, err, "tavern: /tasks/T-1/lease 409 Conflict: ", `{"error":"not the lease owner"}`)
	if strings.HasSuffix(err.Error(), "\n") {
		t.Fatalf("body not trimmed: %q", err)
	}
}

func TestLeaseBadJSON(t *testing.T) {
	c, _ := serve(t, http.StatusOK, `<html>proxy</html>`)
	if _, err := c.Lease(context.Background(), "T-1"); err == nil {
		t.Fatal("html decoded as a task")
	}
}

func TestRenew(t *testing.T) {
	c, f := serve(t, http.StatusOK, leasedTask)
	if err := c.Renew(context.Background(), "T-1"); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/renew", map[string]any{})

	c, _ = serve(t, http.StatusConflict, `{"error":"task is not leased"}`)
	wantHTTPError(t, c.Renew(context.Background(), "T-1"), "409", "task is not leased")
}

func TestFinish(t *testing.T) {
	for _, tc := range []struct {
		done bool
		want string
	}{{true, "done"}, {false, "failed"}} {
		c, f := serve(t, http.StatusOK, `{"id":"T-1","status":"`+tc.want+`"}`)
		if err := c.Finish(context.Background(), "T-1", tc.done); err != nil {
			t.Fatal(err)
		}
		wantPost(t, f.last(t), "/tasks/T-1/finish", map[string]any{"status": tc.want})
	}
	c, _ := serve(t, http.StatusNotFound, `{"error":"task not found"}`)
	wantHTTPError(t, c.Finish(context.Background(), "T-9", true), "404 Not Found", "task not found")
}

func TestRelease(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"id":"T-1","status":"waiting_owner"}`)
	if err := c.Release(context.Background(), "T-1", "waiting_owner", "decision d-3"); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/release", map[string]any{"status": "waiting_owner", "progress": "decision d-3"})

	c, _ = serve(t, http.StatusBadRequest, `{"error":"release to queued or waiting_owner"}`)
	wantHTTPError(t, c.Release(context.Background(), "T-1", "done", ""), "400", "release to queued or waiting_owner")
}

func TestInterrupt(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"id":"T-1","status":"queued"}`)
	if err := c.Interrupt(context.Background(), "T-1", "/tavern/shared/T-1/progress-1.md"); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/release", map[string]any{"status": "queued", "progress": "/tavern/shared/T-1/progress-1.md", "reason": "interrupt"})

	c, _ = serve(t, http.StatusConflict, `{"error":"not the lease owner"}`)
	wantHTTPError(t, c.Interrupt(context.Background(), "T-1", "x"), "409", "not the lease owner")
}

func TestFail(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"id":"T-1","status":"blocked","failures":3,"last_error":"canva: 502","blocked_reason":"needs_human"}`)
	got, err := c.Fail(context.Background(), "T-1", "canva: 502", 3)
	if err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/fail", map[string]any{"error": "canva: 502", "max": float64(3)})
	if got.Status != "blocked" || got.Failures != 3 || got.LastError != "canva: 502" || got.BlockedReason != "needs_human" {
		t.Fatalf("task = %+v", got)
	}
	c, _ = serve(t, http.StatusConflict, `{"error":"not the lease owner"}`)
	if got, err := c.Fail(context.Background(), "T-1", "x", 3); got != nil || err == nil {
		t.Fatalf("fail on 409 = %+v %v", got, err)
	}
}

func TestUnblock(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"id":"T-1","status":"cancelled"}`)
	got, err := c.Unblock(context.Background(), "T-1", "drop")
	if err != nil || got.Status != "cancelled" {
		t.Fatalf("unblock = %+v %v", got, err)
	}
	wantPost(t, f.last(t), "/tasks/T-1/unblock", map[string]any{"action": "drop"})
	c, _ = serve(t, http.StatusConflict, `{"error":"task is not escalated"}`)
	if got, err := c.Unblock(context.Background(), "T-1", "Retry"); got != nil || err == nil {
		t.Fatalf("unblock on 409 = %+v %v", got, err)
	}
}

func TestGetTask(t *testing.T) {
	c, f := serve(t, http.StatusOK, leasedTask)
	got, err := c.GetTask(context.Background(), "T-1")
	if err != nil || got.ID != "T-1" || got.Title != "hooks" {
		t.Fatalf("task = %+v %v", got, err)
	}
	wantGet(t, f.last(t), "/tasks/T-1")
	c, _ = serve(t, http.StatusNotFound, `{"error":"task not found"}`)
	_, err = c.GetTask(context.Background(), "T-9")
	wantHTTPError(t, err, "GET /tasks/T-9 404 Not Found", "task not found")
}

// ---- queue, quest, budget ---------------------------------------------------

func TestQueue(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"tasks":[{"id":"T-3","priority":"P0"},{"id":"T-1","priority":"P2"}]}`)
	got, err := c.Queue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantGet(t, f.last(t), "/agents/dev/queue")
	if len(got) != 2 || got[0].ID != "T-3" || got[1].ID != "T-1" {
		t.Fatalf("queue = %+v", got)
	}
	c, _ = serve(t, http.StatusInternalServerError, "redis: connection refused")
	_, err = c.Queue(context.Background())
	wantHTTPError(t, err, "500 Internal Server Error", "redis: connection refused")
}

func TestQuest(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"title":"R$10k MRR","target":10000,"current":2500,"currency":"BRL","source":"finance","percent":25,"bar":"██░░░░░░","kinds":["revenue","leads"]}`)
	got, err := c.Quest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantGet(t, f.last(t), "/goal")
	if got.Title != "R$10k MRR" || got.Percent != 25 || !reflect.DeepEqual(got.Kinds, []string{"revenue", "leads"}) {
		t.Fatalf("quest = %+v", got)
	}
	c, _ = serve(t, http.StatusNotFound, `{"error":"no goal"}`)
	if _, err := c.Quest(context.Background()); !errors.Is(err, ErrNoQuest) {
		t.Fatalf("404 = %v", err)
	}
	c, _ = serve(t, http.StatusBadGateway, `upstream down`)
	_, err = c.Quest(context.Background())
	wantHTTPError(t, err, "GET /goal 502 Bad Gateway", "upstream down")
}

func TestBudget(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"agent":"dev","day":"2026-11-05","tokens_per_day":200000,"tokens_used":250000,"cost_usd":1.5,"over_budget":true}`)
	got, err := c.Budget(context.Background())
	if err != nil || !got.OverBudget || got.TokensUsed != 250000 || got.TokensPerDay != 200000 {
		t.Fatalf("budget = %+v %v", got, err)
	}
	wantGet(t, f.last(t), "/agents/dev/budget")
	c, _ = serve(t, http.StatusInternalServerError, `boom`)
	_, err = c.Budget(context.Background())
	wantHTTPError(t, err, "budget 500", "boom")
}

// ---- tasks, handoffs, usage, asks ------------------------------------------

func TestNewTaskAndDecide(t *testing.T) {
	c, f := serve(t, http.StatusCreated, `{"id":"T-4","title":"carousel","status":"blocked","needs_approval":true}`)
	got, err := c.NewTask(context.Background(), CreateTask{Title: "carousel", RequestedBy: "dev", Owner: "social-media",
		GoalContribution: Goal{Kind: "posts", Expected: 1}, Brief: "need visuals"})
	if err != nil || got.ID != "T-4" || !got.NeedsApproval {
		t.Fatalf("task = %+v %v", got, err)
	}
	wantPost(t, f.last(t), "/tasks", map[string]any{"title": "carousel", "requested_by": "dev", "owner": "social-media",
		"goal_contribution": map[string]any{"kind": "posts", "expected": float64(1)}, "brief": "need visuals"})

	for _, tc := range []struct {
		approve bool
		path    string
	}{{true, "/tasks/T-4/approve"}, {false, "/tasks/T-4/deny"}} {
		c, f := serve(t, http.StatusOK, `{"id":"T-4","status":"queued"}`)
		if _, err := c.Decide(context.Background(), "T-4", tc.approve, "ok"); err != nil {
			t.Fatal(err)
		}
		wantPost(t, f.last(t), tc.path, map[string]any{"note": "ok"})
	}
	c, _ = serve(t, http.StatusForbidden, `{"error":"not an approver"}`)
	if got, err := c.Decide(context.Background(), "T-4", true, ""); got != nil || err == nil {
		t.Fatalf("decide on 403 = %+v %v", got, err)
	}
	c, _ = serve(t, http.StatusBadRequest, `{"error":"title is required"}`)
	if got, err := c.NewTask(context.Background(), CreateTask{}); got != nil || err == nil {
		t.Fatalf("new task on 400 = %+v %v", got, err)
	}
}

func TestReportUsage(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{}`)
	if err := c.ReportUsage(context.Background(), usage.Turn{Agent: "dev", CommandID: "c1", At: at, Sessions: 1}); err != nil {
		t.Fatal(err)
	}
	s := f.last(t)
	if s.Method != http.MethodPost || s.Path != "/agents/dev/usage" || s.json(t)["command_id"] != "c1" {
		t.Fatalf("usage = %s %s %s", s.Method, s.Path, s.Body)
	}
}

func TestAsks(t *testing.T) {
	c, f := serve(t, http.StatusOK, `{"verdict":"yours","level":"p1","reason":"reversible"}`)
	v, err := c.PreAsk(context.Background(), AskCheck{Task: "T-1", Turn: "c1", Action: "open_pr", Question: "PR?", Blocked: true})
	if err != nil || v.Verdict != "yours" || v.Level != "p1" {
		t.Fatalf("verdict = %+v %v", v, err)
	}
	wantPost(t, f.last(t), "/asks", map[string]any{"agent": "dev", "task": "T-1", "turn": "c1", "action": "open_pr", "question": "PR?", "blocked": true})

	c, f = serve(t, http.StatusOK, `{}`)
	if err := c.AskOpened(context.Background(), OpenAsk{Decision: "d-1", Task: "T-1", Question: "PR?", Recommended: "Yes"}); err != nil {
		t.Fatal(err)
	}
	wantPost(t, f.last(t), "/asks/opened", map[string]any{"agent": "dev", "decision": "d-1", "task": "T-1", "action": "", "question": "PR?", "recommended": "Yes"})

	c, f = serve(t, http.StatusOK, `{"found":true,"card":{"task":"T-1","question":"PR?","choice":"Yes","picked_recommended":true,"follow_ups":["merge"]}}`)
	a, err := c.AskAnswer(context.Background(), "d-1", "Yes")
	if err != nil || a == nil || !a.PickedRecommended || a.FollowUps[0] != "merge" {
		t.Fatalf("answered = %+v %v", a, err)
	}
	wantPost(t, f.last(t), "/asks/answer", map[string]any{"decision": "d-1", "choice": "Yes"})

	c, _ = serve(t, http.StatusOK, `{"found":false}`)
	if a, err := c.AskAnswer(context.Background(), "d-9", "Yes"); a != nil || err != nil {
		t.Fatalf("unknown card = %+v %v", a, err)
	}
	c, _ = serve(t, http.StatusInternalServerError, `boom`)
	if a, err := c.AskAnswer(context.Background(), "d-1", "Yes"); a != nil || err == nil {
		t.Fatalf("answer on 500 = %+v %v", a, err)
	}
}

// ---- transport ---------------------------------------------------------------

// Every call refuses to run without TAVERN_URL instead of hitting a relative URL.
func TestNotConfigured(t *testing.T) {
	ctx := context.Background()
	c := &Client{Agent: "dev"}
	errs := map[string]error{"ReportUsage": c.ReportUsage(ctx, usage.Turn{}), "Pend": c.Pend(ctx, "d", nil, ""),
		"Renew": c.Renew(ctx, "T-1"), "Finish": c.Finish(ctx, "T-1", true), "Release": c.Release(ctx, "T-1", "queued", ""),
		"Interrupt": c.Interrupt(ctx, "T-1", ""), "AskOpened": c.AskOpened(ctx, OpenAsk{})}
	_, errs["Decide"] = c.Decide(ctx, "T-1", true, "")
	_, errs["NewTask"] = c.NewTask(ctx, CreateTask{})
	_, errs["Quest"] = c.Quest(ctx)
	_, errs["Queue"] = c.Queue(ctx)
	_, errs["GetTask"] = c.GetTask(ctx, "T-1")
	_, errs["Lease"] = c.Lease(ctx, "T-1")
	_, errs["Fail"] = c.Fail(ctx, "T-1", "", 3)
	_, errs["Unblock"] = c.Unblock(ctx, "T-1", "drop")
	_, errs["RecentBlocked"] = c.RecentBlocked(ctx, at)
	_, errs["Answer"] = c.Answer(ctx, "d", "Yes")
	_, errs["PreAsk"] = c.PreAsk(ctx, AskCheck{})
	_, errs["AskAnswer"] = c.AskAnswer(ctx, "d", "Yes")
	_, errs["Budget"] = c.Budget(ctx)
	for name, err := range errs {
		if !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A long error page is cut to 300 bytes in the message.
func TestErrorBodyIsCapped(t *testing.T) {
	c, _ := serve(t, http.StatusBadGateway, strings.Repeat("x", 5000))
	err := c.Renew(context.Background(), "T-1")
	if err == nil || strings.Count(err.Error(), "x") != 300 {
		t.Fatalf("err has %d x: %.80v", strings.Count(err.Error(), "x"), err)
	}
}

// hang is a tavern-core that never answers until the client gives up.
func hang(t *testing.T) string {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	return srv.URL
}

func TestTimeout(t *testing.T) {
	c := &Client{BaseURL: hang(t), Agent: "dev", HTTP: &http.Client{Timeout: 50 * time.Millisecond}}
	ctx := context.Background()
	start := time.Now()
	if err := c.Renew(ctx, "T-1"); err == nil {
		t.Fatal("renew answered by a hung server")
	}
	if _, err := c.Lease(ctx, "T-1"); err == nil {
		t.Fatal("lease answered by a hung server")
	}
	if _, err := c.Queue(ctx); err == nil {
		t.Fatal("queue answered by a hung server")
	}
	if _, err := c.RecentBlocked(ctx, at); err == nil {
		t.Fatal("blocked answered by a hung server")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("timeouts took %v", took)
	}
}

func TestContextCancel(t *testing.T) {
	c := &Client{BaseURL: hang(t), Agent: "dev"} // default HTTP client (10s timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Answer(ctx, "d-1", "Yes"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("answer = %v", err)
	}
	if _, err := c.Quest(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("quest = %v", err)
	}
}

func TestServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url, Agent: "dev"}
	if err := c.Pend(context.Background(), "d-1", []GateAct{{Action: "x"}}, "Yes"); err == nil {
		t.Fatal("pend to a closed port succeeded")
	}
	if _, err := c.Budget(context.Background()); err == nil {
		t.Fatal("budget from a closed port succeeded")
	}
}
