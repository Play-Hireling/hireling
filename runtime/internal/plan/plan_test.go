package plan

import (
	"strings"
	"testing"
)

const answer = "Two posts for Monday, split between the writer and social.\n\n```plan\n{\"summary\": \"Monday launch post\", \"tasks\": [\n {\"title\": \"3 hooks for the launch post\", \"owner\": \"Scriptwriter\", \"priority\": \"p2\", \"goal\": {\"kind\": \"leads\", \"expected\": 10}, \"brief\": \"Audience: SMB owners\"},\n {\"title\": \"Carousel from the best hook\", \"owner\": \"social-media\", \"goal\": {\"kind\": \"posts\", \"expected\": 1, \"note\": \"the Monday post itself\"}, \"depends_on\": [0]}\n]}\n```\n"

func TestParseAndValidate(t *testing.T) {
	p, rest, err := Parse(answer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rest, "```") || !strings.HasPrefix(rest, "Two posts") {
		t.Fatalf("rest = %q", rest)
	}
	if err := p.Validate([]string{"innkeeper", "scriptwriter", "social-media", "dev"}, nil); err != nil {
		t.Fatal(err)
	}
	if p.Tasks[0].Owner != "scriptwriter" || p.Tasks[0].Priority != "P2" || p.Tasks[1].Priority != "P2" || p.Tasks[0].Goal.Expected != 10 {
		t.Fatalf("normalised = %+v", p.Tasks)
	}
	got := Render(p, []string{"T-2", "T-3"})
	if !strings.Contains(got, "- T-2 3 hooks for the launch post · scriptwriter · P2 · 10 leads") || !strings.Contains(got, "- T-3 Carousel") {
		t.Fatalf("render = %q", got)
	}
}

func TestParseNoBlock(t *testing.T) {
	if _, rest, err := Parse("Just an answer."); err != ErrNone || rest != "Just an answer." {
		t.Fatal(err, rest)
	}
}

func TestValidateRejects(t *testing.T) {
	party := []string{"scriptwriter"}
	cases := map[string]Plan{
		"no tasks":      {},
		"unknown owner": {Tasks: []Task{{Title: "x", Owner: "ghost", Goal: Goal{Kind: "leads"}}}},
		"bad priority":  {Tasks: []Task{{Title: "x", Owner: "scriptwriter", Priority: "P9", Goal: Goal{Kind: "leads"}}}},
		"no title":      {Tasks: []Task{{Owner: "scriptwriter", Goal: Goal{Kind: "leads"}}}},
		"self dep":      {Tasks: []Task{{Title: "x", Owner: "scriptwriter", DependsOn: []int{0}, Goal: Goal{Kind: "leads"}}}},
		"dep out":       {Tasks: []Task{{Title: "x", Owner: "scriptwriter", DependsOn: []int{3}, Goal: Goal{Kind: "leads"}}}},
		"no goal":       {Tasks: []Task{{Title: "x", Owner: "scriptwriter"}}},                             // C3
		"foreign kind":  {Tasks: []Task{{Title: "x", Owner: "scriptwriter", Goal: Goal{Kind: "coffee"}}}}, // C3
		"long note":     {Tasks: []Task{{Title: "x", Owner: "scriptwriter", Goal: Goal{Kind: "leads", Note: strings.Repeat("n", 141)}}}},
	}
	for name, p := range cases {
		if err := p.Validate(party, []string{"leads", "posts"}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, _, err := Parse("```plan\n{not json}\n```"); err == nil || err == ErrNone {
		t.Fatalf("invalid JSON must be an error: %v", err)
	}
	if _, _, err := Parse("```plan\n{\"tasks\": [], \"extra\": 1}\n```"); err == nil || err == ErrNone {
		t.Fatalf("unknown field must be an error: %v", err)
	}
}

func TestRequestAndDecision(t *testing.T) {
	ans := "I can draft the copy, but the visuals are not mine.\n\n```request\n{\"to\": \"Social-Media\", \"title\": \"Carousel for the launch post\", \"brief\": \"Draft at /tavern/shared/T-2/copy.md\", \"goal\": {\"kind\": \"Posts\", \"expected\": 1}}\n```"
	r, rest, err := ParseRequest(ans)
	if err != nil || strings.Contains(rest, "```") {
		t.Fatal(err, rest)
	}
	if err := r.Validate([]string{"scriptwriter", "social-media"}, "scriptwriter", []string{"leads", "posts"}); err != nil {
		t.Fatal(err)
	}
	if r.To != "social-media" || r.Priority != "P2" || r.Goal.Kind != "posts" {
		t.Fatalf("%+v", r)
	}
	if err := (&Request{To: "scriptwriter", Title: "x", Goal: Goal{Kind: "posts"}}).Validate([]string{"scriptwriter"}, "scriptwriter", nil); err == nil {
		t.Fatal("self request must fail")
	}
	if err := (&Request{To: "ghost", Title: "x", Goal: Goal{Kind: "posts"}}).Validate([]string{"scriptwriter"}, "dev", nil); err == nil {
		t.Fatal("unknown target must fail")
	}
	if err := (&Request{To: "scriptwriter", Title: "x"}).Validate([]string{"scriptwriter"}, "dev", nil); err == nil || !strings.Contains(err.Error(), "goal.kind is required") {
		t.Fatalf("request without a contribution must fail (C3): %v", err)
	}
	d, rest, err := ParseDecision("Fits the plan.\n\n```decision\n{\"task\": \"T-9\", \"approve\": true, \"note\": \"go\"}\n```")
	if err != nil || d.Task != "T-9" || !d.Approve || rest != "Fits the plan." {
		t.Fatal(err, d, rest)
	}
	if _, _, err := ParseDecision("```decision\n{\"approve\": true}\n```"); err == nil || err == ErrNone {
		t.Fatal("decision without task must fail")
	}
	if _, _, err := ParseRequest("plain"); err != ErrNone {
		t.Fatal(err)
	}
}

func TestAsk(t *testing.T) {
	a, rest, err := ParseAsk("Campaign is ready in /tavern/shared/T-9/plan.md.\n\n```ask\n{\"question\": \"Release US$ 50/day for the Monday campaign?\", \"options\": [\"Yes, 50/day\", \"Only 20/day\", \"Not now\"], \"context\": \"Audience and creatives on the table.\", \"urgency\": \"high\"}\n```")
	if err != nil || rest != "Campaign is ready in /tavern/shared/T-9/plan.md." || a.Urgency != "high" || len(a.Options) != 3 {
		t.Fatal(err, rest, a)
	}
	bad := []string{
		"```ask\n{\"question\": \"x\", \"options\": [\"a\"]}\n```",        // one option
		"```ask\n{\"question\": \"\", \"options\": [\"a\", \"b\"]}\n```",  // no question
		"```ask\n{\"question\": \"x\", \"options\": [\"a\", \"a\"]}\n```", // repeated
		"```ask\n{\"question\": \"x\", \"options\": [\"a\", \"b\"], \"urgency\": \"maybe\"}\n```",
	}
	for _, in := range bad {
		if _, _, err := ParseAsk(in); err == nil || err == ErrNone {
			t.Fatalf("%q must fail", in)
		}
	}
	if _, _, err := ParseAsk("plain"); err != ErrNone {
		t.Fatal(err)
	}
}

func TestParseLedger(t *testing.T) {
	ans := "Done, the carousel is on the table.\n\n````ledger learnings/dev/retry.md\n---\ntitle: Retry\ntype: learning\n---\n\n```go\nx := 1\n```\n\n---\n\n## Timeline\n- **2026-11-04** | dev — x\n````\n\nThanks."
	pages, rest, err := ParseLedger(ans)
	if err != nil || len(pages) != 1 || pages[0].Path != "learnings/dev/retry.md" {
		t.Fatalf("%v %v", pages, err)
	}
	if !strings.Contains(pages[0].Content, "```go") || !strings.HasSuffix(pages[0].Content, "| dev — x\n") {
		t.Fatalf("content = %q", pages[0].Content)
	}
	if rest != "Done, the carousel is on the table.\n\n\n\nThanks." && !strings.Contains(rest, "Thanks.") || strings.Contains(rest, "ledger") {
		t.Fatalf("rest = %q", rest)
	}
	if _, _, err := ParseLedger("no blocks here"); err != ErrNone {
		t.Fatal(err)
	}
	three := "```ledger learnings/dev/a.md\nA\n```\n```ledger learnings/dev/b.md\nB\n```"
	if p, _, err := ParseLedger(three); err != nil || len(p) != 2 || p[1].Path != "learnings/dev/b.md" {
		t.Fatalf("%v %v", p, err)
	}
	many := strings.Repeat("```ledger learnings/dev/a.md\nA\n```\n", 4)
	if _, _, err := ParseLedger(many); err == nil {
		t.Fatal("more than MaxLedgerPages accepted")
	}
}
