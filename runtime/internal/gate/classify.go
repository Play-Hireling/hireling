// Package gate is D1 on the hireling's side: every tool call Hermes is about
// to run goes through `hireling gate` (a Hermes shell hook on pre_tool_call,
// fail-closed). The hook classifies the call; local work (reading, thinking,
// files in its own volume, tests) runs at once, and anything that acts on the
// outside world is asked of tavern-core's policy gate (POST /actions/check),
// which answers from the profile: Free runs, Capped runs while tavern-core
// counts it within its daily or weekly cap (D2), and the owner-decides (or a cap
// reached) is blocked with a message that tells the hireling to ask the owner
// with an `ask` block (D3 "Waiting on you").
//
// The network half stays the backstop: the egress allowlist (A8) and the data
// rule on hosts (D6) still apply to whatever a shell command reaches.
package gate

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// Kind is what the hook does with one tool call.
type Kind int

const (
	// Local work: no external effect, runs without asking the gate.
	Local Kind = iota
	// External: ask the policy gate for every action in Verdict.Actions.
	External
	// Refuse: never allowed from inside a hireling (the gate's own config).
	Refuse
)

// Act is one external action the call would take.
type Act struct {
	Action string `json:"action"` // the profile's vocabulary: draft, open_pr, publish_owned_channel …
	Target string `json:"target"` // toolkit/slug, host or command — checked against the data rule (D6)
}

// Verdict is the classification of one tool call.
type Verdict struct {
	Kind    Kind
	Actions []Act
	Reason  string // for Refuse
}

// localTools are Hermes built-ins with no effect outside the container (or
// read-only effects: search, page reads). Browser, desktop, cron, messaging
// and connection tools are not here on purpose.
var localTools = map[string]bool{
	"read_file": true, "search_files": true, "session_search": true, "memory": true,
	"todo": true, "todo_list": true, "skill_view": true, "skills_list": true, "skill_manage": true,
	"clarify": true, "delegate_task": true, "vision_analyze": true, "web_search": true,
	"web_extract": true, "process_manage": true, "text_to_speech": true, "image_generate": true,
}

// Env is where the call runs: Hermes' home (HERMES_HOME) and the tool's cwd.
type Env struct {
	Home string
	Cwd  string
}

// controlFiles configure Hermes and this gate. A hireling runs as the same
// user as Hermes, so the hook itself refuses any tool call that touches them
// (removing the hook from config.yaml would unregister it within ~30 s).
var controlFiles = []string{"config.yaml", "config.yml", ".env", "shell-hooks-allowlist.json", "agent-hooks", "hireling-prompt.md"}

// controlWords name the gate's switches anywhere in a command.
var controlWords = regexp.MustCompile(`(?i)shell-hooks-allowlist|agent-hooks|hooks_auto_accept|hermes_accept_hooks|accept-hooks|\$\{?hermes_home\}?|~/\.hermes\b`)

// touchesControl reports whether text names one of the control files inside
// home, as an absolute path, or as a bare name when the cwd is home itself.
func touchesControl(text string, e Env) bool {
	if text == "" {
		return false
	}
	if controlWords.MatchString(text) {
		return true
	}
	home := strings.TrimRight(e.Home, "/")
	if home == "" {
		home = "/opt/data"
	}
	inHome := strings.TrimRight(e.Cwd, "/") == home
	for _, f := range controlFiles {
		if strings.Contains(text, home+"/"+f) {
			return true
		}
		if inHome && regexp.MustCompile(`(^|[\s'"=<>|;&(])(\./)?`+regexp.QuoteMeta(f)+`($|[\s'"<>|;&)])`).MatchString(text) {
			return true
		}
	}
	return false
}

// controlPath is touchesControl for a file tool's path (relative to cwd).
func controlPath(p string, e Env) bool {
	if p == "" {
		return false
	}
	if !strings.HasPrefix(p, "/") && e.Cwd != "" {
		p = strings.TrimRight(e.Cwd, "/") + "/" + strings.TrimPrefix(p, "./")
	}
	return touchesControl(p, Env{Home: e.Home})
}

// Classify decides what one tool call is. input is Hermes' tool_input.
func Classify(tool string, input map[string]any, e Env) Verdict {
	t := strings.TrimSpace(tool)
	lt := strings.ToLower(t)
	switch {
	case localTools[lt] || strings.HasPrefix(lt, "kanban_"):
		return Verdict{Kind: Local}
	case lt == "write_file" || lt == "patch":
		if controlPath(str(input, "path", "file_path", "filename"), e) {
			return Verdict{Kind: Refuse, Reason: "the Hermes config and the gate's hook are not editable by a hireling"}
		}
		return Verdict{Kind: Local}
	case lt == "terminal" || lt == "execute_code":
		cmd := str(input, "command", "code", "cmd")
		if touchesControl(cmd, e) {
			return Verdict{Kind: Refuse, Reason: "the Hermes config and the gate's hook are not editable by a hireling"}
		}
		if acts := Command(cmd); len(acts) > 0 {
			return Verdict{Kind: External, Actions: acts}
		}
		return Verdict{Kind: Local}
	case strings.HasPrefix(lt, "mcp_gbrain_"):
		return Verdict{Kind: Local} // read-only brain tokens (E2); writes go through ledger-writer
	case strings.HasPrefix(lt, "mcp_composio_"):
		return composio(strings.TrimPrefix(lt, "mcp_composio_"), input)
	}
	// Anything else (browser, desktop, cron, messaging, another MCP server, a
	// tool added by a newer Hermes) is an external action named after the tool:
	// the profile has to list it, or the owner decides.
	return Verdict{Kind: External, Actions: []Act{{Action: lt, Target: lt}}}
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// ---- Composio (hands layer 1, A12) -----------------------------------------

// composio classifies a Tool Router meta tool. Only MULTI_EXECUTE acts; the
// others search, describe or hand the owner a Connect Link.
func composio(meta string, input map[string]any) Verdict {
	switch {
	case strings.Contains(meta, "search_tools"), strings.Contains(meta, "get_tool_schemas"),
		strings.Contains(meta, "manage_connections"), strings.Contains(meta, "list_toolkits"):
		return Verdict{Kind: Local}
	case strings.Contains(meta, "multi_execute"):
		var acts []Act
		for _, slug := range slugs(input) {
			if a := Slug(slug); a.Action != "research" {
				acts = append(acts, a)
			}
		}
		if len(acts) == 0 {
			if len(slugs(input)) == 0 {
				return Verdict{Kind: External, Actions: []Act{{Action: "composio_execute", Target: "composio (no tool slug)"}}}
			}
			return Verdict{Kind: Local}
		}
		return Verdict{Kind: External, Actions: acts}
	}
	// Workbench, remote bash or a meta tool we do not know.
	return Verdict{Kind: External, Actions: []Act{{Action: "composio_" + meta, Target: "composio " + meta}}}
}

// slugs finds the tool slugs in a MULTI_EXECUTE call, whatever the nesting
// ({"tools":[{"tool_slug":…}]} today; a string-encoded JSON is tolerated).
func slugs(input map[string]any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				lk := strings.ToLower(k)
				if s, ok := val.(string); ok && (lk == "tool_slug" || lk == "slug" || lk == "tool" || lk == "action") {
					out = append(out, s)
					continue
				}
				walk(val)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case string:
			if t := strings.TrimSpace(x); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
				var j any
				if json.Unmarshal([]byte(t), &j) == nil {
					walk(j)
				}
			}
		}
	}
	walk(input)
	sort.Strings(out)
	return out
}

var (
	readVerbs = set("get", "list", "search", "fetch", "find", "read", "retrieve", "query", "download",
		"check", "count", "view", "describe", "lookup", "export", "show", "inspect", "preview")
	draftWords = set("draft", "drafts")
	shareWords = set("share", "sharing", "permission", "permissions", "invite", "invitation", "collaborator", "collaborators", "member", "members", "transfer", "ownership")
	sendWords  = set("send", "reply", "forward", "email", "message", "dm")
	// Publishing on an owned social channel (Capped for the Social Media hireling).
	socialToolkits = set("instagram", "twitter", "x", "linkedin", "facebook", "tiktok", "youtube", "threads", "pinterest", "reddit", "bluesky", "mastodon")
	adsToolkits    = set("googleads", "metaads", "facebookads", "linkedinads", "tiktokads", "microsoftads", "redditads")
	docToolkits    = set("googledocs", "googledrive", "googlesheets", "googleslides", "notion", "dropbox", "onedrive")
)

func set(ws ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range ws {
		m[w] = true
	}
	return m
}

// Slug maps one Composio tool slug (TOOLKIT_VERB_…) to an action in the
// profile vocabulary. Deletion, payment and contract words are left in the
// action name, so the gate's hard deny list (D5) catches them by token.
func Slug(slug string) Act {
	s := strings.ToLower(strings.TrimSpace(slug))
	parts := strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') })
	target := "composio " + strings.ToUpper(s)
	if len(parts) == 0 {
		return Act{Action: "composio_execute", Target: target}
	}
	toolkit, words := parts[0], parts[1:]
	has := func(m map[string]bool) bool {
		for _, w := range words {
			if m[w] {
				return true
			}
		}
		return false
	}
	// Words of the hard deny list (delete, pay, sign …) win: the action is
	// prefixed with its D5 category so the gate's token match catches it.
	for _, w := range words {
		if c := denyCategory(w); c != "" {
			return Act{Action: c + "_" + toolkit + "_" + strings.Join(words, "_"), Target: target}
		}
	}
	switch {
	case len(words) > 0 && readVerbs[words[0]] && !has(sendWords):
		return Act{Action: "research", Target: target}
	case has(draftWords):
		return Act{Action: "draft", Target: target}
	case has(shareWords):
		return Act{Action: "share_external", Target: target}
	case adsToolkits[toolkit]:
		return Act{Action: "ad_spend_usd", Target: target}
	case socialToolkits[toolkit]:
		return Act{Action: "publish_owned_channel", Target: target}
	case toolkit == "github":
		return Act{Action: github(words), Target: target}
	case docToolkits[toolkit]:
		return Act{Action: "draft", Target: target} // a doc in the owner's drive is a draft until shared
	case has(sendWords):
		return Act{Action: "send_external", Target: target}
	}
	return Act{Action: toolkit + "_write", Target: target}
}

func github(words []string) string {
	j := "_" + strings.Join(words, "_") + "_"
	switch {
	case strings.Contains(j, "_merge_"):
		return "merge_pr"
	case strings.Contains(j, "_pull_request_") && strings.Contains(j, "_create_"):
		return "open_pr"
	case strings.Contains(j, "_create_") && (strings.Contains(j, "_reference_") || strings.Contains(j, "_branch_")):
		return "open_branch"
	case strings.Contains(j, "_release_"):
		return "publish_release"
	}
	return "github_write"
}

// denyCategory maps the words a Composio slug uses for deleting, paying and
// signing to the D5 category the gate's hard deny list knows by token.
func denyCategory(w string) string {
	switch w {
	case "delete", "remove", "trash", "destroy", "purge", "erase", "unpublish", "archive":
		return "delete"
	case "pay", "payment", "payments", "payout", "refund", "charge", "invoice", "transfer", "withdraw", "purchase", "buy", "checkout":
		return "payment"
	case "sign", "signature", "contract", "envelope":
		return "contract"
	}
	return ""
}

// ---- shell commands ---------------------------------------------------------

var (
	gitPush    = regexp.MustCompile(`(?i)\bgit\b[^;&|\n]*\bpush\b([^;&|\n]*)`)
	mainRef    = regexp.MustCompile(`(?i)(\s|:|^)(main|master|production|prod|release)(\s|$)|--force\b|\s-f\b|--mirror\b|--delete\b|\s-d\b`)
	ghCmd      = regexp.MustCompile(`(?i)\bgh\s+(pr|issue|release|repo|api|workflow|secret|gist)\s+([a-z-]+)?([^;&|\n]*)`)
	httpWrite  = regexp.MustCompile(`(?i)\b(curl|wget|http|https|xh)\b[^;&|\n]*(\s-X\s*(POST|PUT|PATCH|DELETE)\b|--request\s+(POST|PUT|PATCH|DELETE)\b|\s(-d|--data|--data-raw|--data-binary|--json|-F|--form|-T|--upload-file|--post-data|--post-file)\b)`)
	httpDelete = regexp.MustCompile(`(?i)(-X\s*DELETE|--request\s+DELETE|--method=DELETE)\b`)
	pkgPublish = regexp.MustCompile(`(?i)\b(npm|pnpm|yarn)\s+publish\b|\btwine\s+upload\b|\bcargo\s+publish\b|\bgem\s+push\b|\bpoetry\s+publish\b`)
	ghWrite    = regexp.MustCompile(`(?i)(-X\s*(POST|PUT|PATCH)|--method[ =](POST|PUT|PATCH)|\s-[fF]\s|--field|--raw-field|--input)`)
	urlHost    = regexp.MustCompile(`(?i)https?://([^/\s'"]+)`)
)

// Command finds the external actions in a shell command (empty = local work).
// It is a classifier, not a sandbox: obfuscated network calls still meet the
// egress allowlist (A8) and the data rule on hosts (D6).
func Command(cmd string) []Act {
	var acts []Act
	c := strings.TrimSpace(cmd)
	if c == "" {
		return nil
	}
	short := c
	if len(short) > 200 {
		short = short[:200]
	}
	for _, m := range gitPush.FindAllStringSubmatch(c, -1) {
		if mainRef.MatchString(" " + m[1] + " ") {
			acts = append(acts, Act{Action: "push_protected_branch", Target: "git push" + m[1]})
		} else {
			acts = append(acts, Act{Action: "open_branch", Target: "git push" + m[1]})
		}
	}
	for _, m := range ghCmd.FindAllStringSubmatch(c, -1) {
		group, verb, rest := strings.ToLower(m[1]), strings.ToLower(m[2]), m[3]
		full := cut(strings.TrimSpace(m[0]), 120) // what the owner sees on the card
		if strings.HasPrefix(verb, "-") {         // `gh api -X DELETE …`: a flag, not a subcommand
			verb, rest = "", " "+m[2]+m[3]
		}
		switch {
		case group == "pr" && verb == "create":
			acts = append(acts, Act{Action: "open_pr", Target: full})
		case group == "pr" && verb == "merge":
			acts = append(acts, Act{Action: "merge_pr", Target: full})
		case group == "api":
			if httpDelete.MatchString(rest) || strings.Contains(strings.ToLower(rest), "-x delete") {
				acts = append(acts, Act{Action: "delete", Target: full})
			} else if ghWrite.MatchString(rest) {
				acts = append(acts, Act{Action: "github_write", Target: full})
			}
		case verb == "delete" || verb == "archive" || verb == "transfer":
			acts = append(acts, Act{Action: "delete", Target: full})
		case verb == "" || verb == "list" || verb == "view" || verb == "status" || verb == "diff" || verb == "checks" || verb == "checkout" || verb == "clone" || verb == "search":
			// read
		case group == "release" && verb == "create":
			acts = append(acts, Act{Action: "publish_release", Target: full})
		default:
			acts = append(acts, Act{Action: "github_write", Target: full})
		}
	}
	if httpWrite.MatchString(c) {
		host := "http"
		if h := urlHost.FindStringSubmatch(c); h != nil {
			host = h[1]
		}
		action := "http_write"
		if httpDelete.MatchString(c) {
			action = "delete"
		}
		acts = append(acts, Act{Action: action, Target: host})
	}
	if pkgPublish.MatchString(c) {
		acts = append(acts, Act{Action: "publish_package", Target: short})
	}
	return acts
}
