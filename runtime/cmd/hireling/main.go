// hireling is the process that runs inside every hireling container (A1):
//
//	hireling run             poll the front desk queue forever (the container's command)
//	hireling check           validate the profile, render the Hermes config, ping the front desk
//	hireling once "<text>"   run one command locally without the queue (smoke test)
//	hireling usage [1h]      what the turns of the last period cost, from the Hermes store (A2)
//	hireling send <to> "<text>" [type] [task]   put a message in another hireling's inbox (A5 smoke test)
//	hireling gate --agent <id> [--home dir] [--tavern url]   D1: Hermes pre_tool_call hook (stdin → stdout)
//
// Configuration comes from the environment (per-service variables, G4):
//
//	HIRELING_AGENT            agent id (default: test)
//	HIRELING_AGENTS_DIR       where agents/<id>/ live (default: /agents)
//	HERMES_HOME               Hermes data dir (default: /opt/data)
//	HERMES_BIN                hermes executable (default: hermes)
//	HIRELING_SERVER / REGION  where this container runs (default: the profile's home_server / region)
//	FRONTDESK_URL            http://tavern-core:8080
//	FRONTDESK_TOKEN_<ID>     this hireling's front desk token (falls back to FRONTDESK_AGENT_TOKEN)
//	CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET   optional, when the desk sits behind Cloudflare Access
//	DEEPSEEK_API_KEY          read by Hermes, never by this program
//	OPENROUTER_API_KEY        optional; when set, Hermes falls back to the same model on OpenRouter
//	TAVERN_URL                tavern-core on the Compose network (http://tavern-core:8080); usage per turn goes there
//	REDIS_URL                 the bar (redis://redis:6379/0); unset = no inbox loop
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Play-Hireling/hireling/runtime/internal/bar"
	"github.com/Play-Hireling/hireling/runtime/internal/composio"
	"github.com/Play-Hireling/hireling/runtime/internal/frontdesk"
	"github.com/Play-Hireling/hireling/runtime/internal/gate"
	"github.com/Play-Hireling/hireling/runtime/internal/hermes"
	"github.com/Play-Hireling/hireling/runtime/internal/loop"
	"github.com/Play-Hireling/hireling/runtime/internal/plan"
	"github.com/Play-Hireling/hireling/runtime/internal/redact"
	"github.com/Play-Hireling/hireling/runtime/internal/tavern"
	"github.com/Play-Hireling/hireling/runtime/internal/usage"
	"github.com/Play-Hireling/hireling/runtime/profile"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	if len(os.Args) < 2 {
		printUsage()
	}
	if os.Args[1] == "gate" {
		// The hook runs once per tool call, outside the loop: no config
		// writing, no Front Desk, no Redis — stdin → decision → stdout.
		os.Exit(runGate(os.Args[2:], logger))
	}
	l, err := build(logger)
	if err != nil {
		logger.Fatalf("hireling: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "run":
		if err := l.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Fatalf("hireling: %v", err)
		}
	case "check":
		fmt.Printf("profile  %s (front desk agent %s, model %s, server %s/%s)\n", l.Profile.ID, l.Profile.DeskID, l.Profile.Model, l.Server, l.Region)
		fmt.Printf("config   %s\n", filepath.Join(env("HERMES_HOME", "/opt/data"), "config.yaml"))
		if m := l.Profile.FallbackModel(); m != "" && os.Getenv("OPENROUTER_API_KEY") != "" {
			fmt.Printf("fallback openrouter %s\n", m)
		} else {
			fmt.Println("fallback off (no OPENROUTER_API_KEY or disabled in the profile)")
		}
		if l.Tavern != nil {
			fmt.Printf("usage    %s → tavern-core %s\n", l.UsageDir, os.Getenv("TAVERN_URL"))
		} else {
			fmt.Printf("usage    %s (TAVERN_URL unset: local only)\n", l.UsageDir)
		}
		if l.Profile.Planner {
			fmt.Printf("planner  on, party %v\n", l.Party)
		} else {
			fmt.Printf("party    %v (handoffs approved by %s)\n", l.Party, l.Approver)
		}
		if l.Leases != nil {
			fmt.Printf("escalate after %d failed turns in a row on a task (B11)\n", l.Profile.Runtime.EscalateAfter)
		}
		fmt.Printf("shared   %s\n", sharedState(env("TAVERN_SHARED", "/tavern/shared"), l.Profile.ID))
		fmt.Printf("quest    %s\n", questState(ctx, l.Quest))
		if l.Bus != nil {
			fmt.Printf("bar      %s (group %s)\n", bar.Inbox(l.Profile.ID), bar.Group)
		} else {
			fmt.Println("bar      off (REDIS_URL unset or unreachable)")
		}
		fmt.Printf("gate     pre_tool_call → %s (D1, fail-closed; approved in %s)\n", gateCommand(l.Profile.ID, env("HERMES_HOME", "/opt/data")), hermes.AllowlistFile)
		fmt.Printf("hands    %s\n", handsState)
		fmt.Printf("ledger   %s\n", ledgerState)
		if a, ok := l.Turner.(*hermes.Auto); ok {
			fmt.Printf("turns    %s; chat reasoning %s\n", a.Describe(ctx), l.Profile.Runtime.ChatReasoning)
		}
		fmt.Printf("latency  %s\n", usage.Latencies(l.UsageDir, time.Now()))
		if _, err := l.Client.Commands(ctx, l.Profile.DeskID); err != nil {
			fmt.Printf("frontdesk  %v\n", err)
			os.Exit(2)
		}
		fmt.Println("frontdesk  ok (queue readable)")
	case "once":
		if len(os.Args) < 3 {
			printUsage()
		}
		l.Handle(ctx, frontdesk.Command{ID: "local-" + time.Now().UTC().Format("150405"), Text: strings.Join(os.Args[2:], " ")})
	case "send":
		if len(os.Args) < 4 {
			printUsage()
		}
		if l.Bus == nil {
			logger.Fatal("hireling send: REDIS_URL not set")
		}
		m := bar.Message{To: os.Args[2], Text: os.Args[3], Type: "request"}
		if len(os.Args) > 4 {
			m.Type = os.Args[4]
		}
		if len(os.Args) > 5 {
			m.Task = os.Args[5]
		}
		id, err := l.Bus.Send(ctx, m)
		if err != nil {
			logger.Fatalf("hireling send: %v", err)
		}
		fmt.Printf("sent %s → %s (%s)\n", id, bar.Inbox(m.To), m.Type)
	case "quest":
		if l.Quest == nil {
			logger.Fatal("hireling quest: TAVERN_URL not set")
		}
		q, err := l.Quest.Quest(ctx)
		if err != nil {
			logger.Fatalf("hireling quest: %v", err)
		}
		fmt.Println(q.Bar)
		fmt.Printf("source %s · kinds %s", q.Source, strings.Join(q.Kinds, ", "))
		if q.Period != "" {
			fmt.Printf(" · period %s", q.Period)
		}
		fmt.Println()
	case "usage":
		since := 24 * time.Hour
		if len(os.Args) > 2 {
			d, err := time.ParseDuration(os.Args[2])
			if err != nil {
				logger.Fatalf("hireling usage: %v", err)
			}
			since = d
		}
		t, err := l.Usage.Collect(ctx, l.Profile.ID, "", time.Now().Add(-since))
		if err != nil {
			logger.Fatalf("hireling usage: %v", err)
		}
		fmt.Printf("last %s: %d sessions, %s\n", since, t.Sessions, t)
		for _, r := range t.Records {
			task := r.Task
			if task == "" {
				task = "turn"
			}
			fmt.Printf("  %-22s %-18s calls=%-3d in=%-7d cached=%-7d out=%-6d reasoning=%-5d cost=%.4f priced=%v\n", r.Model, task, r.Calls, r.InputTokens, r.CacheReadTokens, r.OutputTokens, r.ReasoningTokens, r.CostUSD, r.Priced)
		}
	default:
		printUsage()
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: hireling run | check | once \"<text>\" | usage [1h] | quest | send <to> \"<text>\" [type] [task] | gate --agent <id>")
	os.Exit(2)
}

// sharedState probes the common table (A7): writable by this hireling, and
// listed in Hermes' write-safe roots so its file tools can use it too.
func sharedState(dir, id string) string {
	probe := filepath.Join(dir, ".probe-"+id)
	if err := os.WriteFile(probe, []byte(time.Now().UTC().Format(time.RFC3339)), 0o664); err != nil {
		return dir + " NOT writable: " + err.Error()
	}
	_ = os.Remove(probe)
	for _, root := range strings.Split(os.Getenv("HERMES_WRITE_SAFE_ROOT"), string(os.PathListSeparator)) {
		if root == dir {
			return dir + " (writable; in HERMES_WRITE_SAFE_ROOT)"
		}
	}
	return dir + " (writable, but NOT in HERMES_WRITE_SAFE_ROOT: Hermes file tools will refuse it)"
}

// questState is the `check` line for the shared goal (C1).
func questState(ctx context.Context, q loop.QuestReader) string {
	if q == nil {
		return "off (TAVERN_URL unset)"
	}
	g, err := q.Quest(ctx)
	if err != nil {
		if errors.Is(err, tavern.ErrNoQuest) {
			return "not set (PUT /goal on tavern-core, or GOAL_* env)"
		}
		return "error: " + err.Error()
	}
	return g.Bar
}

// handsState is the `check` line for A12 (Composio accounts).
var handsState = "off (no hands.toolkits in the profile)"

// hands opens (or reuses) the hireling's Composio session when the profile
// lists toolkits and COMPOSIO_API_KEY is set, adds the MCP server to the Hermes
// options and returns the prompt section. Any failure leaves the hireling
// without hands (logged), never without its turn loop.
func hands(logger *log.Logger, p *profile.Profile, home string, opts *hermes.Options) string {
	if len(p.Hands.Toolkits) == 0 {
		return ""
	}
	key := os.Getenv(composio.KeyEnv)
	if key == "" {
		handsState = "off (" + composio.KeyEnv + " unset)"
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &composio.Client{BaseURL: os.Getenv("COMPOSIO_BASE_URL"), Key: key}
	s, err := c.Ensure(ctx, filepath.Join(home, "composio-session.json"), env("COMPOSIO_USER_ID", "hireling-tavern"), p.Hands.Toolkits)
	if err != nil {
		logger.Printf("hands off: %v", err)
		handsState = "error: " + err.Error()
		return ""
	}
	off := false
	if opts.MCP == nil {
		opts.MCP = map[string]hermes.MCPServer{}
	}
	opts.MCP[composio.Server] = hermes.MCPServer{
		URL:            s.URL,
		Headers:        map[string]string{"x-api-key": "${" + composio.KeyEnv + "}"},
		Timeout:        120,
		ConnectTimeout: 30,
		Prompts:        &off,
		Resources:      &off,
	}
	handsState = fmt.Sprintf("composio %s, toolkits %s, accounts of %s", s.ID, strings.Join(s.Toolkits, ", "), s.UserID)
	return composio.Instructions(p.Hands.Toolkits)
}

// ledgerState is the `check` line for E2 (the Tavern Ledger through GBrain).
var ledgerState = "off (no ledger.read in the profile)"

// ledger gives the hireling read access to the Tavern Ledger (E2): when the
// profile reads any area and the gbrain service minted this hireling's token
// (gbrain-tokens volume, /run/gbrain/<id>.token), Hermes gets the GBrain MCP
// server with that read-only token. Writes never go through GBrain: they are
// the runtime's (E3), checked against ledger.write. Returns the prompt section
// and the token (for the redactor).
func ledger(logger *log.Logger, p *profile.Profile, opts *hermes.Options) (string, string) {
	if len(p.Ledger.Read) == 0 {
		return "", ""
	}
	path := env("GBRAIN_TOKEN_FILE", filepath.Join("/run/gbrain", p.ID+".token"))
	raw, err := os.ReadFile(path)
	tok := strings.TrimSpace(string(raw))
	if err != nil || !strings.HasPrefix(tok, "gbrain_") {
		ledgerState = "off (no GBrain token at " + path + " — the gbrain service mints it when the brain profile is on)"
		return "", ""
	}
	url := env("GBRAIN_MCP_URL", "http://gbrain:3131/mcp")
	off := false
	if opts.MCP == nil {
		opts.MCP = map[string]hermes.MCPServer{}
	}
	// Hermes expands only ${VAR} from its environment, and this token is minted
	// after the container starts, so the header carries the value itself. It
	// is read-only, limited to the ledger source, and redacted from every log.
	opts.MCP["gbrain"] = hermes.MCPServer{
		URL:            url,
		Headers:        map[string]string{"Authorization": "Bearer " + tok},
		Timeout:        60,
		ConnectTimeout: 15,
		Prompts:        &off,
		Resources:      &off,
	}
	ledgerState = fmt.Sprintf("gbrain %s, read-only (areas %s), writes %s through the runtime", url, strings.Join(p.Ledger.Read, ", "), strings.Join(p.Ledger.Write, ", "))
	logger.Printf("ledger: GBrain MCP on %s (read-only)", url)
	text := LedgerInstructions
	if len(p.Ledger.Write) > 0 && os.Getenv("LEDGER_WRITER_URL") != "" {
		text += "\n\n" + strings.ReplaceAll(plan.LedgerInstructions, "<your id>", p.ID)
		ledgerState += " (writer " + os.Getenv("LEDGER_WRITER_URL") + ")"
	}
	return text, tok
}

// LedgerInstructions tell a hireling how to use the shared brain.
const LedgerInstructions = `## The Tavern Ledger (shared brain)
The tavern keeps one shared brain, the Tavern Ledger: typed pages for the goal, the agents, projects, audiences, decisions and what each hireling learned. Search it with the gbrain tools (search, then get_page) before asking someone else or redoing work, and cite the page you used. Reading goes through the gbrain tools; writing does not — it goes through ledger blocks (below), which the runtime commits as you. Never put customer, KYC, financial or legal data in a question to the brain.`

// tavernClient is nil when TAVERN_URL is unset (usage then stays on the volume).
func tavernClient(agent string) loop.UsageReporter {
	url := strings.TrimRight(os.Getenv("TAVERN_URL"), "/")
	if url == "" {
		return nil
	}
	return &tavern.Client{BaseURL: url, Agent: agent}
}

func build(logger *log.Logger) (*loop.Loop, error) {
	id := env("HIRELING_AGENT", "test")
	dir := filepath.Join(env("HIRELING_AGENTS_DIR", "/agents"), id)
	p, err := profile.Load(dir)
	if err != nil {
		return nil, err
	}
	if p.ID != id {
		return nil, fmt.Errorf("HIRELING_AGENT=%s but %s says id %s", id, dir, p.ID)
	}
	home := env("HERMES_HOME", "/opt/data")
	opts := hermes.Options{OpenRouter: os.Getenv("OPENROUTER_API_KEY") != "", Gate: gateCommand(p.ID, home)}
	handsPrompt := hands(logger, p, home, &opts)
	ledgerPrompt, ledgerToken := ledger(logger, p, &opts)
	if err := hermes.WriteConfig(home, p, opts); err != nil {
		return nil, fmt.Errorf("write hermes config: %w", err)
	}

	token := os.Getenv(p.TokenEnv())
	if token == "" {
		token = os.Getenv("FRONTDESK_AGENT_TOKEN")
	}
	client := &frontdesk.Client{
		BaseURL:        os.Getenv("FRONTDESK_URL"),
		Token:          token,
		CFAccessID:     os.Getenv("CF_ACCESS_CLIENT_ID"),
		CFAccessSecret: os.Getenv("CF_ACCESS_CLIENT_SECRET"),
	}
	// A9: every value named in profile.secrets (plus the Access token) is masked
	// wherever text leaves the container.
	known := []string{token, os.Getenv("CF_ACCESS_CLIENT_SECRET"), os.Getenv("OPENROUTER_API_KEY"), os.Getenv("DEEPSEEK_API_KEY"), os.Getenv(composio.KeyEnv), ledgerToken}
	for _, name := range p.Secrets {
		known = append(known, os.Getenv(name))
	}
	var bus *bar.Bus
	if url := os.Getenv("REDIS_URL"); url != "" {
		b, err := bar.Open(context.Background(), url, p.ID)
		if err != nil {
			logger.Printf("bar unavailable, running without the inbox loop: %v", err)
		} else {
			bus = b
		}
	}
	// Everyone knows the party (owners of requests must exist) and who approves
	// handoffs; the planner gets the plan + decision formats, the others the
	// request format (B2, B6).
	prompt := strings.TrimSpace(p.Prompt)
	var party []string
	approver := "innkeeper"
	escIDs := map[string]string{}
	if all, err := profile.List(env("HIRELING_AGENTS_DIR", "/agents")); err == nil {
		for _, q := range all {
			party = append(party, q.ID)
			escIDs[q.ID] = q.DeskID
			if q.Planner {
				approver = q.ID
			}
		}
	}
	if p.Planner {
		prompt += "\n\n" + plan.Instructions + "\n\n" + plan.DecisionInstructions
	} else {
		prompt += "\n\n" + plan.RequestInstructions
	}
	prompt += "\n\n" + plan.AskInstructions
	if ledgerPrompt != "" {
		prompt += "\n\n" + ledgerPrompt
	}
	if handsPrompt != "" {
		prompt += "\n\n" + handsPrompt
	}
	var tasks loop.TaskCreator
	var quest loop.QuestReader
	var ranker loop.Ranker
	var leases loop.Leaser
	if tc := tavernClient(p.ID); tc != nil {
		leases = tc.(*tavern.Client)
		tasks = tc.(*tavern.Client)
		quest = tc.(*tavern.Client)
		ranker = tc.(*tavern.Client)
	}
	turnTimeout := time.Duration(p.Runtime.TurnTTLMinutes-1) * time.Minute
	// A13: turns go through the warm Hermes API server the image runs in this
	// container (no per-turn start: ~2 s instead of ~20 s for a short reply);
	// `hermes chat -q` stays as the fallback and with HIRELING_TURNS=cli.
	turner := &hermes.Auto{
		API: &hermes.APIRunner{
			BaseURL: env("HERMES_API_URL", "http://127.0.0.1:8642"),
			Key:     os.Getenv("API_SERVER_KEY"),
			Prompt:  prompt,
			Timeout: turnTimeout,
		},
		CLI:     &hermes.Runner{Bin: env("HERMES_BIN", "hermes"), Home: home, Prompt: prompt, Timeout: turnTimeout},
		Mode:    os.Getenv("HIRELING_TURNS"),
		KeyFile: filepath.Join(home, ".env"),
		Log:     logger.Printf,
	}
	return &loop.Loop{
		Profile:  p,
		Client:   client,
		Turner:   turner,
		Redactor: redact.New(known...),
		Server:   env("HIRELING_SERVER", p.HomeServer),
		Region:   env("HIRELING_REGION", p.Region),
		Log:      logger,
		Extract:  hermes.Answer,
		TurnsDir: filepath.Join(home, "turns"),
		// A13: wait on the front desk queue instead of polling it; HIRELING_LONG_POLL=off restores polling.
		NoLongPoll: os.Getenv("HIRELING_LONG_POLL") == "off",
		// E3: ledger blocks go to the ledger writer, which commits them as this hireling.
		LedgerWriter: os.Getenv("LEDGER_WRITER_URL"),
		// B11: decisions opened after repeated failures survive a restart.
		EscalationsFile: filepath.Join(home, "escalations.json"),
		Usage:           &usage.Store{DB: filepath.Join(home, "state.db")},
		UsageDir:        filepath.Join(home, "usage"),
		Tavern:          tavernClient(p.ID),
		Gate:            gateGrants(p.ID),
		Asks:            askBook(p.ID),
		GateTurnFile:    filepath.Join(home, gate.TurnFile),
		Bus:             bus,
		Tasks:           tasks,
		Quest:           quest,
		Ranker:          ranker,
		Leases:          leases,
		Party:           party,
		Approver:        approver,

		DeskIDs: escIDs,
	}, nil
}

// askBook is tavern-core's book of asks (D11); nil without TAVERN_URL.
func askBook(agent string) loop.AskBook {
	url := strings.TrimRight(os.Getenv("TAVERN_URL"), "/")
	if url == "" {
		return nil
	}
	return &tavern.Client{BaseURL: url, Agent: agent}
}

// gateCommand is the pre_tool_call hook Hermes runs before every tool call
// (D1). The agent id, home and tavern URL are in the command itself because
// Hermes may scrub the hook's environment; the exact string is also what the
// consent allowlist approves.
func gateCommand(id, home string) string {
	bin, err := os.Executable()
	if err != nil || bin == "" || strings.ContainsAny(bin, " \t\"'") {
		bin = "/usr/local/bin/hireling"
	}
	cmd := bin + " gate --agent " + id + " --home " + home
	if u := strings.TrimRight(os.Getenv("TAVERN_URL"), "/"); u != "" && !strings.ContainsAny(u, " \t\"'") {
		cmd += " --tavern " + u
	}
	return cmd
}

// runGate is `hireling gate`: exit 0 with Hermes' directive on stdout.
func runGate(args []string, logger *log.Logger) int {
	h := &gate.Hook{Home: env("HERMES_HOME", "/opt/data")}
	tavernURL := os.Getenv("TAVERN_URL")
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--agent":
			h.Agent = next()
		case "--home":
			h.Home = next()
		case "--tavern":
			tavernURL = next()
		}
	}
	if h.Agent == "" {
		h.Agent = env("HIRELING_AGENT", "")
	}
	h.Checker = &gate.HTTPChecker{BaseURL: tavernURL, Agent: h.Agent}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(hermes.GateTimeout-2)*time.Second)
	defer cancel()
	if err := h.Run(ctx, os.Stdin, os.Stdout, logger.Printf); err != nil {
		logger.Printf("gate: %v", err)
		return 2 // exit 2 blocks the tool call even without JSON
	}
	return 0
}

// gateGrants is tavern-core's side of the owner's approvals (D1); nil without TAVERN_URL.
func gateGrants(agent string) loop.GateGrants {
	url := strings.TrimRight(os.Getenv("TAVERN_URL"), "/")
	if url == "" {
		return nil
	}
	return &tavern.Client{BaseURL: url, Agent: agent}
}
