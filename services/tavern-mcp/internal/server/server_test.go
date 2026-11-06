package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Play-Hireling/hireling/services/tavern-mcp/internal/desk"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const tok = "owner-secret-token-123"

// fakeDesk is a Front Desk that records what it is asked. Its error answers
// echo the token on purpose, to prove the server never passes it on.
type fakeDesk struct {
	mu       sync.Mutex
	reqs     []seen
	noQuest  bool
	decided  bool
	tasksRaw string
}

type seen struct {
	Method, Path, Auth string
	Body               map[string]any
}

func (f *fakeDesk) last(t *testing.T) seen {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) == 0 {
		t.Fatal("no request reached the Front Desk")
	}
	return f.reqs[len(f.reqs)-1]
}

func (f *fakeDesk) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func (f *fakeDesk) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeDesk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s := seen{Method: r.Method, Path: r.URL.EscapedPath(), Auth: r.Header.Get("Authorization")}
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &s.Body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, s)
	reply := func(code int, body string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		io.WriteString(w, body)
	}
	if s.Auth != "Bearer "+tok {
		reply(401, `{"error":"unauthorized"}`)
		return
	}
	switch r.Method + " " + s.Path {
	case "POST /api/command":
		if s.Body["agent"] != "innkeeper" {
			reply(400, `{"error":"a known agent and a text are required (got token `+tok+`)"}`)
			return
		}
		reply(200, `{"command":{"id":"c-1","agent":"innkeeper","text":`+quote(s.Body["text"])+`,"at":"2026-11-05T10:00:00Z"}}`)
	case "GET /api/status":
		reply(200, `{"agents":[{"id":"innkeeper","pending":1}],"decisions":[`+decisionJSON+`],"interactions":[],"at":"2026-11-05T10:00:00Z"}`)
	case "GET /api/decisions":
		reply(200, `{"decisions":[`+decisionJSON+`]}`)
	case "GET /api/decisions/d-1":
		if f.decided {
			reply(200, `{"decision":{"id":"d-1","from":"social-media","question":"When do we publish?","options":["Publish Friday","Wait a week"],"chosen":"Publish Friday"}}`)
			return
		}
		reply(200, `{"decision":`+decisionJSON+`}`)
	case "GET /api/decisions/d-404":
		reply(404, `{"error":"no such decision"}`)
	case "POST /api/decisions/d-1/choose":
		if f.decided {
			reply(409, `{"error":"already decided"}`)
			return
		}
		if !strings.EqualFold(s.Body["option"].(string), "publish friday") {
			reply(400, `{"error":"option not offered"}`)
			return
		}
		f.decided = true
		reply(200, `{"decision":{"id":"d-1","chosen":"Publish Friday"},"command":{"id":"c-2","agent":"social-media","decisionId":"d-1"}}`)
	case "POST /api/decisions/d-9/choose":
		reply(404, `{"error":"no such decision"}`)
	case "GET /api/tasks":
		if f.tasksRaw != "" {
			reply(200, f.tasksRaw)
			return
		}
		reply(200, `{"tasks":`+tasksJSON+`}`)
	case "GET /api/tasks/T-1":
		reply(200, `{"task":{"id":"T-1","title":"Plan the post","events":[]}}`)
	case "GET /api/tasks/T-404":
		reply(404, `{"error":"task not found"}`)
	case "GET /api/quest":
		if f.noQuest {
			reply(404, `{"error":"quest not set"}`)
			return
		}
		reply(200, `{"quest":{"title":"First 10 customers","target":10,"current":2,"percent":20,"bar":"[##--------] 20%"}}`)
	default:
		// The internal routes (/tasks, /goal) are not the owner's: the server must never call them.
		reply(404, `{"error":"no route"}`)
	}
}

func quote(v any) string { b, _ := json.Marshal(v); return string(b) }

const decisionJSON = `{"id":"d-1","from":"social-media","question":"When do we publish?","options":["Publish Friday","Wait a week"],"urgency":"normal","at":"2026-11-05T09:00:00Z"}`

const tasksJSON = `[
 {"id":"T-1","title":"Plan the post","owner":"innkeeper","status":"done","updated_at":"2026-11-05T08:00:00Z"},
 {"id":"T-2","title":"Write the script","owner":"scriptwriter","status":"leased","updated_at":"2026-11-05T09:30:00Z"},
 {"id":"T-3","title":"Ship CSV export","owner":"dev","status":"queued","updated_at":"2026-11-05T09:00:00Z"}
]`

func connect(t *testing.T, token string) (*mcp.ClientSession, *fakeDesk) {
	t.Helper()
	f := &fakeDesk{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s := New(desk.New(desk.Config{URL: srv.URL, Token: token}, srv.Client()))
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := s.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, f
}

// call runs a tool and returns its text, failing on a tool error unless wantErr.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, wantErr bool) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	out := b.String()
	if raw, _ := json.Marshal(res); strings.Contains(string(raw), tok) {
		t.Fatalf("%s: the token is in the result: %s", name, raw)
	}
	if res.IsError != wantErr {
		t.Fatalf("%s: IsError = %v, want %v: %s", name, res.IsError, wantErr, out)
	}
	return out
}

func TestToolsList(t *testing.T) {
	cs, _ := connect(t, tok)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tl := range res.Tools {
		got[tl.Name] = tl
	}
	for _, name := range []string{"send_order", "tavern_status", "list_decisions", "get_decision", "choose_option", "list_tasks", "get_task", "get_quest"} {
		if got[name] == nil {
			t.Errorf("missing tool %s", name)
		}
	}
	if len(got) != 8 {
		t.Errorf("%d tools, want 8", len(got))
	}
	ch := got["choose_option"]
	if ch == nil || !strings.HasPrefix(ch.Description, "OWNER ACTION") || !strings.Contains(ch.Description, "explicitly told you") ||
		ch.Annotations == nil || ch.Annotations.ReadOnlyHint || ch.Annotations.DestructiveHint == nil || !*ch.Annotations.DestructiveHint {
		t.Errorf("choose_option must say it is an owner action and be marked destructive: %+v", ch)
	}
	for _, name := range []string{"tavern_status", "list_decisions", "get_decision", "list_tasks", "get_task", "get_quest"} {
		if tl := got[name]; tl != nil && (tl.Annotations == nil || !tl.Annotations.ReadOnlyHint) {
			t.Errorf("%s should be read-only", name)
		}
	}
}

func TestSendOrder(t *testing.T) {
	cs, f := connect(t, tok)
	out := call(t, cs, "send_order", map[string]any{"agent": " innkeeper ", "text": "  Plan a launch post for Friday "}, false)
	r := f.last(t)
	if r.Method != "POST" || r.Path != "/api/command" || r.Auth != "Bearer "+tok {
		t.Fatalf("request: %+v", r)
	}
	if r.Body["agent"] != "innkeeper" || r.Body["text"] != "Plan a launch post for Friday" || len(r.Body) != 2 {
		t.Fatalf("body: %v", r.Body)
	}
	if !strings.Contains(out, `"id": "c-1"`) {
		t.Fatalf("out: %s", out)
	}

	// The Front Desk refuses (and echoes the token): the error is shown, the token is not.
	out = call(t, cs, "send_order", map[string]any{"agent": "nobody", "text": "hi"}, true)
	if !strings.Contains(out, "HTTP 400") || !strings.Contains(out, "a known agent and a text are required") {
		t.Fatalf("out: %s", out)
	}

	n := f.count()
	call(t, cs, "send_order", map[string]any{"agent": "innkeeper", "text": "   "}, true)
	call(t, cs, "send_order", map[string]any{"agent": "innkeeper", "text": strings.Repeat("x", MaxOrder+1)}, true)
	if f.count() != n {
		t.Fatal("invalid orders must not reach the Front Desk")
	}
	// Missing required argument: rejected by the input schema.
	call(t, cs, "send_order", map[string]any{"agent": "innkeeper"}, true)
}

func TestStatusAndDecisions(t *testing.T) {
	cs, f := connect(t, tok)
	out := call(t, cs, "tavern_status", nil, false)
	if r := f.last(t); r.Method != "GET" || r.Path != "/api/status" || r.Auth != "Bearer "+tok || r.Body != nil {
		t.Fatalf("request: %+v", r)
	}
	if !strings.Contains(out, `"innkeeper"`) {
		t.Fatalf("out: %s", out)
	}

	out = call(t, cs, "list_decisions", nil, false)
	if r := f.last(t); r.Method != "GET" || r.Path != "/api/decisions" {
		t.Fatalf("request: %+v", r)
	}
	if !strings.Contains(out, "When do we publish?") {
		t.Fatalf("out: %s", out)
	}

	out = call(t, cs, "get_decision", map[string]any{"id": "d-1"}, false)
	if r := f.last(t); r.Method != "GET" || r.Path != "/api/decisions/d-1" || r.Auth != "Bearer "+tok {
		t.Fatalf("request: %+v", r)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(out), &d); err != nil || d["id"] != "d-1" || d["chosen"] != nil {
		t.Fatalf("get_decision: %s", out)
	}
	out = call(t, cs, "get_decision", map[string]any{"id": "d-404"}, true)
	if !strings.Contains(out, "no decision with that id") {
		t.Fatalf("out: %s", out)
	}
	// An answered decision is still readable, with the owner's choice.
	call(t, cs, "choose_option", map[string]any{"id": "d-1", "option": "Publish Friday"}, false)
	out = call(t, cs, "get_decision", map[string]any{"id": "d-1"}, false)
	d = nil
	if err := json.Unmarshal([]byte(out), &d); err != nil || d["chosen"] != "Publish Friday" {
		t.Fatalf("answered decision: %s", out)
	}
	// The id is path-escaped.
	call(t, cs, "get_decision", map[string]any{"id": "../status"}, true)
	if r := f.last(t); r.Path != "/api/decisions/..%2Fstatus" {
		t.Fatalf("id not escaped: %s", r.Path)
	}
}

func TestChooseOption(t *testing.T) {
	cs, f := connect(t, tok)
	out := call(t, cs, "choose_option", map[string]any{"id": "d-1", "option": "publish friday"}, false)
	r := f.last(t)
	if r.Method != "POST" || r.Path != "/api/decisions/d-1/choose" || r.Auth != "Bearer "+tok || r.Body["option"] != "publish friday" || len(r.Body) != 1 {
		t.Fatalf("request: %+v", r)
	}
	if !strings.Contains(out, `"chosen": "Publish Friday"`) {
		t.Fatalf("out: %s", out)
	}
	if out = call(t, cs, "choose_option", map[string]any{"id": "d-1", "option": "Wait a week"}, true); !strings.Contains(out, "HTTP 409: already decided") {
		t.Fatalf("second choice: %s", out)
	}
	if out = call(t, cs, "choose_option", map[string]any{"id": "d-9", "option": "x"}, true); !strings.Contains(out, "HTTP 404") {
		t.Fatalf("unknown: %s", out)
	}
	// An id cannot walk to another endpoint.
	call(t, cs, "choose_option", map[string]any{"id": "../command", "option": "x"}, true)
	if r := f.last(t); r.Path != "/api/decisions/..%2Fcommand/choose" {
		t.Fatalf("id not escaped: %s", r.Path)
	}
}

func TestTasks(t *testing.T) {
	cs, f := connect(t, tok)
	out := call(t, cs, "list_tasks", nil, false)
	if r := f.last(t); r.Method != "GET" || r.Path != "/api/tasks" || r.Auth != "Bearer "+tok {
		t.Fatalf("request: %+v", r)
	}
	var got struct {
		Total int `json:"total"`
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Total != 3 || got.Tasks[0].ID != "T-2" || got.Tasks[2].ID != "T-1" {
		t.Fatalf("newest first: %s", out)
	}
	out = call(t, cs, "list_tasks", map[string]any{"owner": "DEV", "status": "queued"}, false)
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Total != 1 || got.Tasks[0].ID != "T-3" {
		t.Fatalf("filter: %s", out)
	}
	out = call(t, cs, "list_tasks", map[string]any{"limit": 1}, false)
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Total != 3 || len(got.Tasks) != 1 {
		t.Fatalf("limit: %s", out)
	}

	out = call(t, cs, "get_task", map[string]any{"id": "T-1"}, false)
	if r := f.last(t); r.Path != "/api/tasks/T-1" || r.Auth != "Bearer "+tok {
		t.Fatalf("request: %+v", r)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(out), &task); err != nil || task["id"] != "T-1" || task["title"] != "Plan the post" {
		t.Fatalf("get_task shows the task itself: %s", out)
	}
	if out = call(t, cs, "get_task", map[string]any{"id": "T-404"}, true); !strings.Contains(out, "HTTP 404") {
		t.Fatalf("out: %s", out)
	}

	f.set(func() { f.tasksRaw = `{"not":"a list"}` })
	call(t, cs, "list_tasks", nil, true)
	f.set(func() { f.tasksRaw = `[]` }) // the shape of the internal GET /tasks
	call(t, cs, "list_tasks", nil, true)
	f.set(func() { f.tasksRaw = `{"tasks":[]}` })
	if out = call(t, cs, "list_tasks", nil, false); !strings.Contains(out, `"total": 0`) {
		t.Fatalf("empty board: %s", out)
	}
}

func TestQuest(t *testing.T) {
	cs, f := connect(t, tok)
	out := call(t, cs, "get_quest", nil, false)
	if r := f.last(t); r.Method != "GET" || r.Path != "/api/quest" || r.Auth != "Bearer "+tok {
		t.Fatalf("request: %+v", r)
	}
	var q map[string]any
	if err := json.Unmarshal([]byte(out), &q); err != nil || q["title"] != "First 10 customers" {
		t.Fatalf("get_quest shows the quest itself: %s", out)
	}
	f.set(func() { f.noQuest = true })
	if out = call(t, cs, "get_quest", nil, true); !strings.Contains(out, "no quest yet") {
		t.Fatalf("out: %s", out)
	}
}

func TestWrongToken(t *testing.T) {
	cs, _ := connect(t, "not-the-owner")
	for _, name := range []string{"tavern_status", "list_decisions", "list_tasks", "get_quest", "get_task", "get_decision"} {
		args := map[string]any{}
		if name == "get_task" || name == "get_decision" {
			args["id"] = "x-1"
		}
		out := call(t, cs, name, args, true)
		if !strings.Contains(out, "HTTP 401") || !strings.Contains(out, "owner token") || strings.Contains(out, "not-the-owner") {
			t.Fatalf("%s: %s", name, out)
		}
	}
}

func TestResources(t *testing.T) {
	cs, f := connect(t, tok)
	ctx := context.Background()
	list, err := cs.ListResources(ctx, nil)
	if err != nil || len(list.Resources) != 4 {
		t.Fatalf("resources: %v %v", list, err)
	}
	tpl, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil || len(tpl.ResourceTemplates) != 2 {
		t.Fatalf("templates: %v %v", tpl, err)
	}
	for uri, path := range map[string]string{
		"tavern://status":        "/api/status",
		"tavern://decisions":     "/api/decisions",
		"tavern://tasks":         "/api/tasks",
		"tavern://quest":         "/api/quest",
		"tavern://decisions/d-1": "/api/decisions/d-1",
		"tavern://tasks/T-1":     "/api/tasks/T-1",
	} {
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if r := f.last(t); r.Path != path || r.Auth != "Bearer "+tok {
			t.Fatalf("%s: request %+v", uri, r)
		}
		if len(res.Contents) != 1 || res.Contents[0].MIMEType != "application/json" || !json.Valid([]byte(res.Contents[0].Text)) {
			t.Fatalf("%s: %+v", uri, res.Contents)
		}
	}
	for _, uri := range []string{"tavern://decisions/d-404", "tavern://tasks/T-404"} {
		if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatalf("%s: want not found", uri)
		}
	}
}

// TestOnlyAPIRoutes: every call the server makes is under /api/* with the
// owner token, so a proxy that exposes only /api/* serves all of it.
func TestOnlyAPIRoutes(t *testing.T) {
	cs, f := connect(t, tok)
	call(t, cs, "send_order", map[string]any{"agent": "innkeeper", "text": "hi"}, false)
	for _, name := range []string{"tavern_status", "list_decisions", "list_tasks", "get_quest"} {
		call(t, cs, name, nil, false)
	}
	call(t, cs, "get_task", map[string]any{"id": "T-1"}, false)
	call(t, cs, "get_decision", map[string]any{"id": "d-1"}, false)
	call(t, cs, "choose_option", map[string]any{"id": "d-1", "option": "Publish Friday"}, false)
	for _, uri := range []string{"tavern://status", "tavern://decisions", "tavern://tasks", "tavern://quest", "tavern://decisions/d-1", "tavern://tasks/T-1"} {
		if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri}); err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if !strings.HasPrefix(r.Path, "/api/") || r.Auth != "Bearer "+tok {
			t.Errorf("call outside /api/* or without the owner token: %+v", r)
		}
	}
}

func TestRedactMiddleware(t *testing.T) {
	c := desk.New(desk.Config{URL: "http://127.0.0.1:1", Token: tok}, nil)
	mw := redactMiddleware(c)
	h := mw(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: "leak " + tok}},
			StructuredContent: map[string]string{"t": tok},
		}, io.ErrUnexpectedEOF
	})
	res, err := h(context.Background(), "tools/call", nil)
	r := res.(*mcp.CallToolResult)
	if r.Content[0].(*mcp.TextContent).Text != "leak [redacted]" || r.StructuredContent != nil || err != io.ErrUnexpectedEOF {
		t.Fatalf("%+v %v", r, err)
	}
	h = mw(func(context.Context, string, mcp.Request) (mcp.Result, error) { return nil, io.EOF })
	if _, err := h(context.Background(), "x", nil); err != io.EOF {
		t.Fatal(err)
	}
	h = mw(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{Text: tok}}}, errorString("bad " + tok)
	})
	res, err = h(context.Background(), "resources/read", nil)
	if res.(*mcp.ReadResourceResult).Contents[0].Text != "[redacted]" || err.Error() != "bad [redacted]" {
		t.Fatalf("%v %v", res, err)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
