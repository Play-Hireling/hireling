// Package plan is the Innkeeper's planning protocol (features B2 and B1).
// A planning hireling ends its answer with a fenced block:
//
//	```plan
//	{"summary": "Two posts for Monday",
//	 "tasks": [
//	   {"title": "3 hooks for the launch post", "owner": "scriptwriter", "priority": "P2",
//	    "goal": {"kind": "leads", "expected": 10}, "brief": "Audience: … Tone: …"},
//	   {"title": "Carousel from the best hook", "owner": "social-media", "priority": "P2",
//	    "depends_on": [0], "brief": "…"}
//	 ]}
//	```
//
// The runtime parses it, validates owners and priorities against the party,
// creates the parent task and one child per entry in tavern-core, writes every
// brief on the common table and tells each owner on the bar. The model never
// talks to tavern-core or Redis itself: the plan is data, the runtime acts.
package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Goal is how a task moves the quest (C3; docs/architecture.md "Task"): the kind
// (one of the quest's contribution kinds, e.g. leads, posts, revenue), the
// expected number of units, and a short note on the mechanism.
type Goal struct {
	Kind     string `json:"kind,omitempty"`
	Expected int    `json:"expected,omitempty"`
	Note     string `json:"note,omitempty"`
}

// MaxGoalNote bounds the mechanism note.
const MaxGoalNote = 140

// Check validates a contribution: a kind is always required (every task
// declares how it moves the goal); when the quest lists kinds, it must be one
// of them. `what` names the block for the error ("plan: task 2", "request").
func (g *Goal) Check(what string, kinds []string) error {
	g.Kind = strings.ToLower(strings.TrimSpace(g.Kind))
	g.Note = strings.TrimSpace(g.Note)
	switch {
	case g.Kind == "":
		if len(kinds) > 0 {
			return fmt.Errorf("%s: goal.kind is required — how does it move the quest? one of %v", what, kinds)
		}
		return fmt.Errorf("%s: goal.kind is required — how does it move the quest?", what)
	case g.Expected < 0:
		return fmt.Errorf("%s: goal.expected must be ≥ 0", what)
	case len([]rune(g.Note)) > MaxGoalNote:
		return fmt.Errorf("%s: goal.note over %d characters", what, MaxGoalNote)
	}
	if len(kinds) > 0 {
		ok := false
		for _, k := range kinds {
			if k == g.Kind {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("%s: goal.kind %q is not one of the quest's kinds %v", what, g.Kind, kinds)
		}
	}
	return nil
}

// String renders `10 leads` or `10 leads (warm-up for the launch)`.
func (g Goal) String() string {
	if g.Kind == "" {
		return ""
	}
	s := fmt.Sprintf("%d %s", g.Expected, g.Kind)
	if g.Note != "" {
		s += " (" + g.Note + ")"
	}
	return s
}

// Task is one planned piece of work.
type Task struct {
	Title     string `json:"title"`
	Owner     string `json:"owner"`
	Priority  string `json:"priority,omitempty"` // P0..P3, default P2
	Goal      Goal   `json:"goal"`
	Brief     string `json:"brief,omitempty"`
	DependsOn []int  `json:"depends_on,omitempty"` // indexes into tasks
}

// Plan is the parsed block.
type Plan struct {
	Summary string `json:"summary"`
	Tasks   []Task `json:"tasks"`
}

// MaxTasks bounds one plan: more than this is a sign the request should be split.
const MaxTasks = 12

var fence = regexp.MustCompile("(?s)```plan[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")

// ErrNone means the answer carries no plan block (a plain answer).
var ErrNone = errors.New("plan: no plan block")

// Parse extracts the plan from an answer and returns the answer without the
// block. ErrNone when there is no block; any other error means the block is
// present but unusable (the turn still succeeds; the error is reported).
func Parse(answer string) (*Plan, string, error) {
	m := fence.FindStringSubmatchIndex(answer)
	if m == nil {
		return nil, answer, ErrNone
	}
	raw := answer[m[2]:m[3]]
	rest := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	var p Plan
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, rest, fmt.Errorf("plan: invalid JSON: %w", err)
	}
	return &p, rest, nil
}

var priorities = map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true}

// Validate checks the plan against the party (known hireling ids). It fills
// defaults (priority P2) and normalises owners to lower case.
// Validate checks owners against the party and every contribution against
// the quest's kinds (nil = any kind, but one is still required).
func (p *Plan) Validate(party []string, kinds []string) error {
	known := map[string]bool{}
	for _, id := range party {
		known[strings.ToLower(id)] = true
	}
	switch {
	case len(p.Tasks) == 0:
		return errors.New("plan: no tasks")
	case len(p.Tasks) > MaxTasks:
		return fmt.Errorf("plan: %d tasks, max %d — split the request", len(p.Tasks), MaxTasks)
	}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		t.Title = strings.TrimSpace(t.Title)
		t.Owner = strings.ToLower(strings.TrimSpace(t.Owner))
		if t.Title == "" {
			return fmt.Errorf("plan: task %d has no title", i)
		}
		if len(t.Title) > 140 {
			return fmt.Errorf("plan: task %d title over 140 characters", i)
		}
		if !known[t.Owner] {
			return fmt.Errorf("plan: task %d owner %q is not in the party %v", i, t.Owner, sorted(known))
		}
		if t.Priority == "" {
			t.Priority = "P2"
		}
		t.Priority = strings.ToUpper(t.Priority)
		if !priorities[t.Priority] {
			return fmt.Errorf("plan: task %d priority %q must be P0..P3", i, t.Priority)
		}
		for _, d := range t.DependsOn {
			if d < 0 || d >= len(p.Tasks) || d == i {
				return fmt.Errorf("plan: task %d depends on %d, which does not exist", i, d)
			}
		}
		if err := t.Goal.Check(fmt.Sprintf("plan: task %d", i), kinds); err != nil {
			return err
		}
	}
	return nil
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Render is the human line list that goes to the owner (and the front desk) once
// the tasks exist: ids come from tavern-core, in plan order.
func Render(p *Plan, ids []string) string {
	var sb strings.Builder
	if p.Summary != "" {
		sb.WriteString(strings.TrimSpace(p.Summary))
		sb.WriteString("\n")
	}
	for i, t := range p.Tasks {
		id := ""
		if i < len(ids) {
			id = ids[i] + " "
		}
		fmt.Fprintf(&sb, "- %s%s · %s · %s", id, t.Title, t.Owner, t.Priority)
		if g := t.Goal.String(); g != "" {
			fmt.Fprintf(&sb, " · %s", g)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// Instructions is the paragraph appended to a planning hireling's prompt.
const Instructions = `## Plan format

When the request needs work from the party, end your answer with ONE fenced block tagged ` + "`plan`" + ` containing JSON:
{"summary": "<one line>", "tasks": [{"title": "<imperative, ≤ 140 chars>", "owner": "<hireling id>", "priority": "P0|P1|P2|P3", "goal": {"kind": "<one of the quest's contribution kinds>", "expected": <units>, "note": "<how it moves the quest, ≤ 140 chars>"}, "brief": "<what done looks like, inputs, deadline>", "depends_on": [<indexes>]}]}
Owners must be hireling ids of this tavern. Every task declares how it moves the quest: ` + "`goal.kind`" + ` is required (the kinds are listed with the quest bar at the top of the turn; use ` + "`support`" + ` with expected 0 for work that only enables others). Keep it to what the request needs (max 12 tasks). Write the human explanation before the block; the tavern creates the tasks, files the briefs and tells each owner — you do not message them yourself. Answer without a plan block when nothing has to be delegated.

Plan straight from the request and the quest bar: the owner waits for the plan (target: under one minute). Do not browse the common table, read old files or write briefs, READMEs or plan files yourself — the tavern files one brief per task. Use a tool only when the request names a file you must read to split the work.`

// ---- Handoffs (B6) ------------------------------------------------------

// Request is what a non-planning hireling may append to an answer to ask work
// of another hireling: one fenced block tagged `request`. The runtime creates
// the task (tavern-core blocks it until the Innkeeper decides) and sends the
// handoff to the Innkeeper; the requester never messages the owner directly.
type Request struct {
	To       string `json:"to"`
	Title    string `json:"title"`
	Priority string `json:"priority,omitempty"` // default P2 (a request from another agent)
	Brief    string `json:"brief,omitempty"`
	Goal     Goal   `json:"goal"`
}

// Decision is what the planner appends when answering a handoff: approve or
// deny one task, with a note the requester and the owner will read.
type Decision struct {
	Task    string `json:"task"`
	Approve bool   `json:"approve"`
	Note    string `json:"note,omitempty"`
}

var requestFence = regexp.MustCompile("(?s)```request[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")
var decisionFence = regexp.MustCompile("(?s)```decision[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")

// ParseRequest extracts a request block; ErrNone when absent.
func ParseRequest(answer string) (*Request, string, error) {
	m := requestFence.FindStringSubmatchIndex(answer)
	if m == nil {
		return nil, answer, ErrNone
	}
	rest := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	var r Request
	dec := json.NewDecoder(strings.NewReader(answer[m[2]:m[3]]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, rest, fmt.Errorf("request: invalid JSON: %w", err)
	}
	return &r, rest, nil
}

// Validate checks a request against the party; self is the requester.
// Validate checks the target against the party and the contribution against
// the quest's kinds (a kind is required; nil kinds = any).
func (r *Request) Validate(party []string, self string, kinds []string) error {
	r.To = strings.ToLower(strings.TrimSpace(r.To))
	r.Title = strings.TrimSpace(r.Title)
	known := map[string]bool{}
	for _, id := range party {
		known[strings.ToLower(id)] = true
	}
	switch {
	case r.Title == "":
		return errors.New("request: title is required")
	case len(r.Title) > 140:
		return errors.New("request: title over 140 characters")
	case r.To == self:
		return errors.New("request: cannot request work from yourself")
	case !known[r.To]:
		return fmt.Errorf("request: %q is not in the party %v", r.To, sorted(known))
	}
	if r.Priority == "" {
		r.Priority = "P2"
	}
	r.Priority = strings.ToUpper(r.Priority)
	if !priorities[r.Priority] {
		return fmt.Errorf("request: priority %q must be P0..P3", r.Priority)
	}
	return r.Goal.Check("request", kinds)
}

// ParseDecision extracts a decision block; ErrNone when absent.
func ParseDecision(answer string) (*Decision, string, error) {
	m := decisionFence.FindStringSubmatchIndex(answer)
	if m == nil {
		return nil, answer, ErrNone
	}
	rest := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	var d Decision
	dec := json.NewDecoder(strings.NewReader(answer[m[2]:m[3]]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, rest, fmt.Errorf("decision: invalid JSON: %w", err)
	}
	d.Task = strings.TrimSpace(d.Task)
	if d.Task == "" {
		return nil, rest, errors.New("decision: task id is required")
	}
	return &d, rest, nil
}

// RequestInstructions is appended to every non-planning hireling's prompt.
const RequestInstructions = `## Asking another hireling for work

When your task needs something only another hireling can do, do not message them: end your answer with ONE fenced block tagged ` + "`request`" + `:
{"to": "<hireling id>", "title": "<imperative, ≤ 140 chars>", "priority": "P2", "brief": "<what you need, by when, where the inputs are on the common table>", "goal": {"kind": "<one of the quest's kinds; support when it only enables your task>", "expected": <units>, "note": "<how it moves the quest>"}}
` + "`goal.kind`" + ` is required — every task declares how it moves the quest. The tavern files the task and asks the Innkeeper; you will get a note when it is approved or denied. Keep working on what does not depend on it.`

// DecisionInstructions is appended to the planner's prompt.
const DecisionInstructions = `## Deciding a handoff

When a message on the bar asks you to approve a task one hireling requested from another (it names the task id), end your answer with ONE fenced block tagged ` + "`decision`" + `:
{"task": "<task id>", "approve": true|false, "note": "<one line for the requester and the owner>"}
Approve when the request fits the owner's role and the week's priorities; deny with the reason otherwise. The tavern records the decision and tells both sides.`

// ---- "Waiting on you" (D3) ---------------------------------------------

// Ask is what any hireling appends when an action is the owner's to decide
// (payment, contract, customer data, deletion, production — the hard deny list
// — or anything beyond its autonomy): one fenced block tagged `ask`. The
// runtime opens the decision card in the front desk; the turn ends and nothing
// moves until the owner's choice comes back through the command queue.
//
// D11: the block names the gate action it is about (`action`, the profile's
// action name) so the runtime can send back an ask about the hireling's own
// action, and the first option is the hireling's recommendation, with `why`.
type Ask struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Context  string   `json:"context,omitempty"`
	Urgency  string   `json:"urgency,omitempty"` // normal | high
	Action   string   `json:"action,omitempty"`  // the gate action, e.g. merge_pr, payment, connect_account
	Why      string   `json:"why,omitempty"`     // why the first option is the recommendation
}

// Recommendation is the line that leads the card's context: the first
// option and why (D11). Empty without a reason.
func (a *Ask) Recommendation() string {
	if a.Why == "" || len(a.Options) == 0 {
		return ""
	}
	return "Recommended: " + a.Options[0] + " — " + a.Why
}

var askFence = regexp.MustCompile("(?s)```ask[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")

// Limits mirror the front desk's validation (src/shared/validation.ts).
const (
	MaxQuestion = 300
	MaxOption   = 80
	MaxContext  = 1000
	MinOptions  = 2
	MaxOptions  = 6
	MaxWhy      = 300
	MaxAction   = 60
)

var actionName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ParseAsk extracts an ask block; ErrNone when absent.
func ParseAsk(answer string) (*Ask, string, error) {
	m := askFence.FindStringSubmatchIndex(answer)
	if m == nil {
		return nil, answer, ErrNone
	}
	rest := strings.TrimSpace(answer[:m[0]] + answer[m[1]:])
	var a Ask
	dec := json.NewDecoder(strings.NewReader(answer[m[2]:m[3]]))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, rest, fmt.Errorf("ask: invalid JSON: %w", err)
	}
	if err := a.Validate(); err != nil {
		return nil, rest, err
	}
	return &a, rest, nil
}

// Validate applies the front desk's limits and normalises urgency.
func (a *Ask) Validate() error {
	a.Question = strings.TrimSpace(a.Question)
	switch {
	case a.Question == "":
		return errors.New("ask: question is required")
	case len([]rune(a.Question)) > MaxQuestion:
		return fmt.Errorf("ask: question over %d characters", MaxQuestion)
	case len(a.Options) < MinOptions || len(a.Options) > MaxOptions:
		return fmt.Errorf("ask: %d to %d options", MinOptions, MaxOptions)
	case len([]rune(a.Context)) > MaxContext:
		return fmt.Errorf("ask: context over %d characters", MaxContext)
	}
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	a.Why = strings.Join(strings.Fields(a.Why), " ")
	switch {
	case a.Action != "" && (len(a.Action) > MaxAction || !actionName.MatchString(a.Action)):
		return fmt.Errorf("ask: action %q must be the profile's action name (snake_case, ≤ %d)", a.Action, MaxAction)
	case len([]rune(a.Why)) > MaxWhy:
		return fmt.Errorf("ask: why over %d characters", MaxWhy)
	}
	seen := map[string]bool{}
	for i, o := range a.Options {
		o = strings.TrimSpace(o)
		if o == "" || len([]rune(o)) > MaxOption {
			return fmt.Errorf("ask: option %d must be 1–%d characters", i+1, MaxOption)
		}
		if seen[strings.ToLower(o)] {
			return fmt.Errorf("ask: option %q repeated", o)
		}
		seen[strings.ToLower(o)] = true
		a.Options[i] = o
	}
	switch strings.ToLower(strings.TrimSpace(a.Urgency)) {
	case "", "normal":
		a.Urgency = "normal"
	case "high", "urgent":
		a.Urgency = "high"
	default:
		return fmt.Errorf("ask: urgency %q must be normal or high", a.Urgency)
	}
	return nil
}

// AskInstructions is appended to every hireling's prompt (D3 + D11).
const AskInstructions = `## Autonomy first: ask the owner only what is theirs

Before you ask, check your autonomy in your profile:
- **Free** action → do it; the Autonomy Log records it. Do not ask.
- **Capped** action with uses left this day/week → do it. Do not ask.
- Ask only for: an action listed under "owner decides" (payments, contracts, customer data, deleting anything, production changes…), a capped action over its cap, connecting an account (a Connect card), or a choice that cannot be undone and that the brief does not settle. When the autonomy gate blocks a tool call, that action is the owner's too.

When you ask, do not ask in prose and do not pick for them: stop, say what you prepared, and end your answer with ONE fenced block tagged ` + "`ask`" + `:
{"question": "<one question, ≤ 300 chars>", "options": ["<your recommendation>", "<other option>", "…up to 6, ≤ 80 chars each"], "why": "<why you recommend the first option, ≤ 300 chars>", "action": "<the action name from your profile, e.g. merge_pr, payment, connect_account>", "context": "<what the owner needs to know, ≤ 1000 chars, paths on the common table>", "urgency": "normal|high"}
The first option is always your recommendation. An ask about an action that is yours comes back to you: decide it and carry on. One card per task: a second question on a task that is already waiting joins that card and comes back to you with the owner's answer. Their choice comes back to you as a new command; until then, wait — work on something else if you have it.`

// ---- Ledger pages (E3) ------------------------------------------------

// LedgerPage is a page a hireling asks the runtime to write to the Tavern
// Ledger: a fenced block tagged `ledger` with the page path after the tag and
// the whole markdown page as the body. Four backticks are accepted (and
// preferred) so the page itself may contain code fences.
type LedgerPage struct {
	Path    string
	Content string
}

var (
	ledgerFence4 = regexp.MustCompile("(?s)````ledger[ \\t]+(\\S+)[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*````")
	ledgerFence3 = regexp.MustCompile("(?s)```ledger[ \\t]+(\\S+)[ \\t]*\\r?\\n(.*?)\\r?\\n[ \\t]*```")
)

// ParseLedger extracts every ledger block (at most MaxLedgerPages) and returns
// the answer without them; ErrNone when there is none.
func ParseLedger(answer string) ([]LedgerPage, string, error) {
	var pages []LedgerPage
	rest := answer
	for _, re := range []*regexp.Regexp{ledgerFence4, ledgerFence3} {
		for {
			m := re.FindStringSubmatchIndex(rest)
			if m == nil {
				break
			}
			pages = append(pages, LedgerPage{Path: rest[m[2]:m[3]], Content: strings.TrimSpace(rest[m[4]:m[5]]) + "\n"})
			rest = rest[:m[0]] + rest[m[1]:]
		}
	}
	rest = strings.TrimSpace(rest)
	if len(pages) == 0 {
		return nil, answer, ErrNone
	}
	if len(pages) > MaxLedgerPages {
		return nil, rest, fmt.Errorf("ledger: at most %d pages per turn, got %d", MaxLedgerPages, len(pages))
	}
	return pages, rest, nil
}

// MaxLedgerPages bounds the pages one turn may write.
const MaxLedgerPages = 3

// LedgerInstructions tell a hireling how to record what it learned (E3).
const LedgerInstructions = "To record something you learned that will help the tavern again (it worked, or it failed in an instructive way), add a ledger block at the end of your answer: four backticks, `ledger`, the page path, the whole page, four backticks. You may only write under `learnings/<your id>/`; the file name is a short lowercase slug. The page is a learning page: frontmatter `title`, `type: learning`, `owner: <your id>`, `tags`; the compiled truth (a one-line quote, `## State`), one `---` line, then `## Timeline` with your newest line first, signed by you: `- **YYYY-MM-DD** | <your id> — <what you learned>`. The runtime commits it as you; never put customer, KYC, financial or legal data in a page. Example:\n\n" +
	"````ledger learnings/<your id>/retry-canva-502.md\n---\ntitle: Retry Canva on 502\ntype: learning\nowner: <your id>\ntags: [learning, canva]\n---\n\n# Retry Canva on 502\n\n> Canva's export returns 502 under load; one retry after 10 s works.\n\n## State\n- **Seen:** 3 times on carousel exports.\n\n---\n\n## Timeline\n- **2026-11-04** | <your id> — Learned that one retry after 10 s fixes Canva 502s.\n````"
