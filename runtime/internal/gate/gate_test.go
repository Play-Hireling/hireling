package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Play-Hireling/hireling/runtime/profile"
)

var home = Env{Home: "/opt/data", Cwd: "/opt/data/work"}

func TestClassifyLocal(t *testing.T) {
	for _, c := range []struct {
		tool  string
		input map[string]any
	}{
		{"read_file", map[string]any{"path": "/opt/data/config.yaml"}}, // reading is local
		{"web_search", map[string]any{"query": "canva api 502"}},
		{"kanban_list", nil},
		{"write_file", map[string]any{"path": "notes/plan.md"}},
		{"write_file", map[string]any{"path": "/tavern/shared/repo/config.yaml"}}, // a project's own config
		{"terminal", map[string]any{"command": "go test ./... && git status"}},
		{"terminal", nil}, // no input
		{"terminal", map[string]any{"command": "curl -s https://api.github.com/repos/x/y"}},
		{"terminal", map[string]any{"command": "gh pr list && gh pr view 12"}},
		{"mcp_gbrain_search", map[string]any{"query": "canva"}},
		{"mcp_composio_COMPOSIO_SEARCH_TOOLS", map[string]any{"use_case": "post on instagram"}},
		{"mcp_composio_COMPOSIO_MANAGE_CONNECTIONS", map[string]any{"toolkits": []any{"github"}}},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", map[string]any{"tools": []any{
			map[string]any{"tool_slug": "GITHUB_LIST_REPOSITORIES_FOR_THE_AUTHENTICATED_USER", "arguments": map[string]any{}},
			map[string]any{"tool_slug": "GITHUB_GET_A_REPOSITORY"},
		}}},
	} {
		if v := Classify(c.tool, c.input, home); v.Kind != Local {
			t.Errorf("%s %v: kind %d %v, want local", c.tool, c.input, v.Kind, v.Actions)
		}
	}
}

func TestClassifyRefusesControlFiles(t *testing.T) {
	for _, c := range []struct {
		tool  string
		input map[string]any
		cwd   string
	}{
		{"write_file", map[string]any{"path": "/opt/data/config.yaml"}, "/opt/data/work"},
		{"patch", map[string]any{"path": "config.yaml"}, "/opt/data"},
		{"patch", map[string]any{"path": "../shell-hooks-allowlist.json"}, "/opt/data/work"},
		{"terminal", map[string]any{"command": "sed -i '/hooks:/d' /opt/data/config.yaml"}, "/tmp"},
		{"terminal", map[string]any{"command": "cd /opt/data && sed -i 's/fail_closed: true//' config.yaml"}, "/opt/data"},
		{"terminal", map[string]any{"command": "echo '{}' > $HERMES_HOME/shell-hooks-allowlist.json"}, "/tmp"},
		{"terminal", map[string]any{"command": "HERMES_ACCEPT_HOOKS=1 hermes chat"}, "/tmp"},
		{"execute_code", map[string]any{"code": "open('/opt/data/.env').read()"}, "/tmp"},
	} {
		if v := Classify(c.tool, c.input, Env{Home: "/opt/data", Cwd: c.cwd}); v.Kind != Refuse {
			t.Errorf("%s %v: kind %d, want refuse", c.tool, c.input, v.Kind)
		}
	}
}

func actions(v Verdict) []string {
	var out []string
	for _, a := range v.Actions {
		out = append(out, a.Action)
	}
	return out
}

func TestClassifyExternal(t *testing.T) {
	multi := func(slugs ...string) map[string]any {
		var tools []any
		for _, s := range slugs {
			tools = append(tools, map[string]any{"tool_slug": s, "arguments": map[string]any{"text": "hello"}})
		}
		return map[string]any{"tools": tools, "thought": "post it"}
	}
	for _, c := range []struct {
		tool  string
		input map[string]any
		want  string
	}{
		{"terminal", map[string]any{"command": "git push origin feat/d1-gate"}, "open_branch"},
		{"terminal", map[string]any{"command": "git push origin HEAD:main"}, "push_protected_branch"},
		{"terminal", map[string]any{"command": "git push --force origin feat/x"}, "push_protected_branch"},
		{"terminal", map[string]any{"command": "gh pr create --fill"}, "open_pr"},
		{"terminal", map[string]any{"command": "gh pr merge 12 --squash"}, "merge_pr"},
		{"terminal", map[string]any{"command": "gh issue comment 3 -b hi"}, "github_write"},
		{"terminal", map[string]any{"command": "gh api -X DELETE repos/a/b"}, "delete"},
		{"terminal", map[string]any{"command": "curl -X POST https://hooks.slack.com/x -d '{}'"}, "http_write"},
		{"terminal", map[string]any{"command": "npm publish"}, "publish_package"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("INSTAGRAM_CREATE_MEDIA_CONTAINER"), "publish_owned_channel"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("TWITTER_CREATION_OF_A_POST"), "publish_owned_channel"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GITHUB_CREATE_A_PULL_REQUEST"), "open_pr"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GITHUB_MERGE_A_PULL_REQUEST"), "merge_pr"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GOOGLEDOCS_CREATE_DOCUMENT"), "draft"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GOOGLEDRIVE_ADD_FILE_SHARING_PREFERENCE"), "share_external"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GMAIL_CREATE_EMAIL_DRAFT"), "draft"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GMAIL_SEND_EMAIL"), "send_external"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GOOGLEADS_CREATE_CAMPAIGN"), "ad_spend_usd"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GOOGLECALENDAR_CREATE_EVENT"), "googlecalendar_write"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", multi("GITHUB_DELETE_A_REPOSITORY"), "delete_github_delete_a_repository"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", map[string]any{"tools": `[{"tool_slug":"LINKEDIN_CREATE_LINKED_IN_POST"}]`}, "publish_owned_channel"},
		{"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", map[string]any{}, "composio_execute"},
		{"mcp_composio_COMPOSIO_REMOTE_BASH_TOOL", map[string]any{"command": "ls"}, "composio_composio_remote_bash_tool"},
		{"browser_exec", map[string]any{"script": "click()"}, "browser_exec"},
		{"cronjob_manage", map[string]any{"action": "create"}, "cronjob_manage"},
		{"mcp_slack_send_message", map[string]any{"text": "hi"}, "mcp_slack_send_message"},
	} {
		v := Classify(c.tool, c.input, home)
		if v.Kind != External || len(v.Actions) == 0 || v.Actions[0].Action != c.want {
			t.Errorf("%s %v: kind %d %v, want external %s", c.tool, c.input, v.Kind, actions(v), c.want)
		}
	}
}

// The action names line up with the gate in tavern-core: profiles decide
// free/capped, and the hard deny list catches what must be the owner's.
func TestActionsAgainstProfiles(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	agents := filepath.Join(filepath.Dir(file), "..", "..", "..", "agents")
	load := func(id string) *profile.Profile {
		p, err := profile.Load(filepath.Join(agents, id))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	in := func(list []string, a string) bool {
		for _, x := range list {
			if x == a {
				return true
			}
		}
		return false
	}
	dev, social, script := load("dev"), load("social-media"), load("scriptwriter")
	for _, c := range []struct {
		p      *profile.Profile
		action string
		free   bool
	}{
		{dev, Command("git push origin feat/x")[0].Action, true},
		{dev, Command("gh pr create --fill")[0].Action, true},
		{script, Slug("GOOGLEDOCS_CREATE_DOCUMENT").Action, true},
	} {
		if got := in(c.p.Autonomy.Free, c.action); got != c.free {
			t.Errorf("%s: %s free = %v, want %v", c.p.ID, c.action, got, c.free)
		}
	}
	if _, ok := social.Autonomy.Capped[Slug("INSTAGRAM_CREATE_MEDIA_CONTAINER").Action]; !ok {
		t.Errorf("social-media: instagram publishing should be the profile's capped action")
	}
	for _, slug := range []string{"GITHUB_DELETE_A_REPOSITORY", "STRIPE_CREATE_PAYMENT_INTENT", "DOCUSIGN_CREATE_ENVELOPE", "GMAIL_MOVE_TO_TRASH"} {
		if c := profile.HardDenyCategory(Slug(slug).Action); c == "" {
			t.Errorf("%s → %s: not on the hard deny list", slug, Slug(slug).Action)
		}
	}
}

type fake struct {
	answers map[string]Decision
	err     error
	asked   []string
	ctx     []Context
}

func (f *fake) Check(_ context.Context, action, target string, c Context) (Decision, error) {
	f.asked = append(f.asked, action)
	f.ctx = append(f.ctx, c)
	if f.err != nil {
		return Decision{}, f.err
	}
	if d, ok := f.answers[action]; ok {
		return d, nil
	}
	return Decision{Level: "owner", Reason: "action not in profile"}, nil
}

func TestDecide(t *testing.T) {
	ctx := context.Background()
	f := &fake{answers: map[string]Decision{
		"open_pr":               {Level: "free"},
		"publish_owned_channel": {Level: "capped", Limit: 3},
		"deploy_non_critical":   {Level: "capped", Limit: 2, Counted: true, Used: 1},
	}}
	h := &Hook{Agent: "dev", Home: "/opt/data", Checker: f}

	if o := h.Decide(ctx, Payload{Tool: "read_file", Input: map[string]any{"path": "x"}}); !o.Allow || len(f.asked) != 0 {
		t.Fatalf("local tool: %+v, asked %v (must not call the gate)", o, f.asked)
	}
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create --fill"}}); !o.Allow {
		t.Fatalf("free action blocked: %+v", o)
	}
	o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr merge 3"}})
	if o.Allow || !strings.Contains(o.Message, "`merge_pr`") || !strings.Contains(o.Message, "`ask` block") {
		t.Fatalf("the owner action: %+v", o)
	}
	o = h.Decide(ctx, Payload{Tool: "mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", Input: map[string]any{"tools": []any{map[string]any{"tool_slug": "INSTAGRAM_CREATE_MEDIA_CONTAINER"}}}})
	if o.Allow || !strings.Contains(o.Message, "did not count") {
		t.Fatalf("an uncounted capped answer must be the owner's: %+v", o)
	}
	f.answers["open_branch"] = Decision{Level: "capped", Limit: 2, Counted: true, Used: 1}
	if o := h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "git push origin feat/x"}}); !o.Allow {
		t.Fatalf("a capped use counted within the cap was blocked: %+v", o)
	}
	// Two actions in one call: the strictest wins.
	o = h.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create --fill && gh pr merge --auto"}})
	if o.Allow {
		t.Fatalf("free + the owner in one command was allowed")
	}
	// Unreachable gate: fail closed for external actions only.
	down := &Hook{Agent: "dev", Home: "/opt/data", Checker: &fake{err: errors.New("dial tcp: refused")}}
	if o := down.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "git push origin feat/x"}}); o.Allow {
		t.Fatalf("gate down: external action allowed")
	}
	if o := down.Decide(ctx, Payload{Tool: "terminal", Input: map[string]any{"command": "go test ./..."}}); !o.Allow {
		t.Fatalf("gate down: local work blocked")
	}
	if o := h.Decide(ctx, Payload{Tool: "write_file", Input: map[string]any{"path": "/opt/data/config.yaml"}}); o.Allow {
		t.Fatalf("config edit allowed")
	}
}

// The real wire: Hermes' JSON on stdin, tavern-core's /actions/check over HTTP,
// Hermes' directive on stdout.
func TestRunOverHTTP(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/actions/check" || r.Header.Get("X-Hireling") != "social-media" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		level := "owner"
		if got["action"] == "draft" {
			level = "free"
		}
		_ = json.NewEncoder(w).Encode(Decision{Level: level, Reason: "profile"})
	}))
	defer srv.Close()
	h := &Hook{Agent: "social-media", Home: "/opt/data", Checker: &HTTPChecker{BaseURL: srv.URL, Agent: "social-media"}}

	run := func(payload string) map[string]string {
		var out bytes.Buffer
		if err := h.Run(context.Background(), strings.NewReader(payload), &out, nil); err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		_ = json.Unmarshal(out.Bytes(), &m)
		return m
	}
	draft := `{"hook_event_name":"pre_tool_call","tool_name":"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL","tool_input":{"tools":[{"tool_slug":"GOOGLEDOCS_CREATE_DOCUMENT"}]},"cwd":"/opt/data"}`
	if m := run(draft); m["action"] != "" {
		t.Fatalf("free draft blocked: %v", m)
	}
	if got["agent"] != "social-media" || got["action"] != "draft" || !strings.Contains(got["target"], "GOOGLEDOCS_CREATE_DOCUMENT") {
		t.Fatalf("gate request = %v", got)
	}
	post := `{"hook_event_name":"pre_tool_call","tool_name":"mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL","tool_input":{"tools":[{"tool_slug":"GMAIL_SEND_EMAIL"}]}}`
	if m := run(post); m["action"] != "block" || !strings.Contains(m["message"], "send_external") {
		t.Fatalf("send not blocked: %v", m)
	}
	if m := run(`not json`); m["action"] != "block" {
		t.Fatalf("garbage payload not blocked: %v", m)
	}
	none := &Hook{Agent: "dev", Home: "/opt/data", Checker: &HTTPChecker{Agent: "dev"}}
	var out bytes.Buffer
	_ = none.Run(context.Background(), strings.NewReader(`{"tool_name":"terminal","tool_input":{"command":"git push origin feat/x"}}`), &out, nil)
	if !strings.Contains(out.String(), `"block"`) {
		t.Fatalf("no TAVERN_URL: external action not blocked: %s", out.String())
	}
}

// D4: the hook tells the log why and in which turn: the model's own `thought`
// when the tool has one, else the running turn's topic written by the runtime.
func TestHookSendsLogContext(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, TurnFile), []byte(`{"turn":"c-42","task":"T-7","topic":"Prepare Monday's post"}`), 0o640); err != nil {
		t.Fatal(err)
	}
	f := &fake{answers: map[string]Decision{"open_pr": {Level: "free"}, "publish_owned_channel": {Level: "free"}}}
	h := &Hook{Agent: "dev", Home: home, Checker: f}
	h.Decide(context.Background(), Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create --fill"}})
	h.Decide(context.Background(), Payload{Tool: "mcp_composio_COMPOSIO_MULTI_EXECUTE_TOOL", Input: map[string]any{
		"thought": "the owner approved the carousel for Monday", "tools": []any{map[string]any{"tool_slug": "INSTAGRAM_CREATE_MEDIA_CONTAINER"}}}})
	if len(f.ctx) != 2 {
		t.Fatalf("checks = %v", f.asked)
	}
	if c := f.ctx[0]; c.Turn != "c-42" || c.Task != "T-7" || c.Why != "Prepare Monday's post" {
		t.Fatalf("terminal context = %+v", c)
	}
	if c := f.ctx[1]; c.Turn != "c-42" || c.Why != "the owner approved the carousel for Monday" {
		t.Fatalf("composio context = %+v", c)
	}
	// No turn file: the action still runs, just without the link.
	f2 := &fake{answers: map[string]Decision{"open_pr": {Level: "free"}}}
	if o := (&Hook{Agent: "dev", Home: t.TempDir(), Checker: f2}).Decide(context.Background(), Payload{Tool: "terminal", Input: map[string]any{"command": "gh pr create"}}); !o.Allow || f2.ctx[0].Turn != "" {
		t.Fatalf("no turn file: %+v %+v", o, f2.ctx)
	}
}
