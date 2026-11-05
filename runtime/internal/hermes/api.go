package hermes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// APIRunner runs a turn through the Hermes API server that the official image
// already keeps running inside the container (the s6 `gateway-default`
// service, OpenAI-compatible, on 127.0.0.1:8642, key generated into
// $HERMES_HOME/.env as API_SERVER_KEY). The agent stays warm between turns:
// no Python start, no MCP reconnect, no session bootstrap per turn — measured
// (2026-11-04) a trivial turn takes 1.4–2.6 s here against 17–27 s
// with `hermes chat -q` (A13).
//
// Same safety as the CLI path: the request runs with the hireling's
// config.yaml (approvals, deny list, MCP servers); a denied command is denied
// here too. One fresh session per turn (X-Hermes-Session-Id), so A2 usage and
// the raw answer are read from state.db exactly as before.
type APIRunner struct {
	BaseURL string        // default http://127.0.0.1:8642
	Key     string        // API_SERVER_KEY
	Prompt  string        // hireling system prompt (role system)
	Timeout time.Duration // hard stop for one turn
	Client  *http.Client

	// PartialEvery is the minimum interval between OnPartial calls (default 2.5 s).
	PartialEvery time.Duration

	mu        sync.Mutex
	reasoning string
	stats     TurnStats
	onPartial func(context.Context, string)
}

// DefaultPartialEvery keeps partial reports from turning the answer stream
// into one front desk write per token (A13).
const DefaultPartialEvery = 2500 * time.Millisecond

// SetOnPartial registers the sink for the answer text so far while a turn
// streams. It is called at most once every PartialEvery and never with text
// the model did not write.
func (r *APIRunner) SetOnPartial(fn func(context.Context, string)) {
	r.mu.Lock()
	r.onPartial = fn
	r.mu.Unlock()
}

func (r *APIRunner) partialSink() (func(context.Context, string), time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	every := r.PartialEvery
	if every <= 0 {
		every = DefaultPartialEvery
	}
	return r.onPartial, every
}

// TurnStats is the latency of the last turn (A13 p50/p95).
type TurnStats struct {
	Session    string
	FirstWords time.Duration // request → first text delta (0 = none)
	Total      time.Duration
}

// ErrAPIUnavailable means the API server did not answer; the caller may fall
// back to the CLI runner.
var ErrAPIUnavailable = errors.New("hermes api server unavailable")

// KeyFromEnvFile reads API_SERVER_KEY from a Hermes .env file (the image
// writes it there on first start). The value never leaves the process.
func KeyFromEnvFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	key := ""
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "API_SERVER_KEY="); ok {
			key = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return key
}

func (r *APIRunner) base() string {
	if r.BaseURL == "" {
		return "http://127.0.0.1:8642"
	}
	return strings.TrimRight(r.BaseURL, "/")
}

func (r *APIRunner) client() *http.Client {
	if r.Client != nil {
		return r.Client
	}
	// Loopback only: never through the egress proxy.
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

// Healthy reports whether the API server answers /health.
func (r *APIRunner) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, r.base()+"/health", nil)
	res, err := r.client().Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK && r.Key != ""
}

// SetReasoning sets the reasoning effort of the next turns ("" = the
// profile's default). The loop sets it per turn (A13 light chat mode).
func (r *APIRunner) SetReasoning(level string) {
	r.mu.Lock()
	r.reasoning = level
	r.mu.Unlock()
}

// Stats returns the latency of the last turn.
func (r *APIRunner) Stats() TurnStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

func newSessionID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "hl-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Run streams one turn. It returns a transcript in the CLI's shape (tool
// lines, then "☤ Hermes" and the answer) so the audit log and hermes.Answer
// keep working unchanged.
func (r *APIRunner) Run(ctx context.Context, text string) (string, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.mu.Lock()
	reasoning := r.reasoning
	r.mu.Unlock()
	msgs := []map[string]string{}
	if p := strings.TrimSpace(r.Prompt); p != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": p})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": text})
	body := map[string]any{"model": "hermes", "messages": msgs, "stream": true}
	if reasoning != "" {
		body["reasoning_effort"] = reasoning
	}
	raw, _ := json.Marshal(body)
	sid := newSessionID()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base()+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+r.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Hermes-Session-Id", sid)

	started := time.Now()
	stats := TurnStats{Session: sid}
	defer func() {
		stats.Total = time.Since(started)
		r.mu.Lock()
		r.stats = stats
		r.mu.Unlock()
	}()
	res, err := r.client().Do(req)
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("hermes: turn exceeded %s", timeout)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", ErrAPIUnavailable, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		// Nothing ran yet: let Auto fall back to the CLI for this turn.
		return "", fmt.Errorf("%w: HTTP %d: %s", ErrAPIUnavailable, res.StatusCode, strings.TrimSpace(string(b)))
	}
	if s := res.Header.Get("X-Hermes-Session-Id"); s != "" {
		stats.Session = s
	}

	var tools, answer strings.Builder
	onPartial, partialEvery := r.partialSink()
	var lastPartial time.Time
	event := ""
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	done := false
	for !done && sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				done = true
				break
			}
			if event != "" {
				if event == "hermes.tool.progress" {
					toolLine(&tools, data)
				}
				event = ""
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(data), &chunk) != nil {
				break
			}
			if chunk.Error != nil {
				return transcript(tools.String(), answer.String()), fmt.Errorf("hermes api: %s", chunk.Error.Message)
			}
			for _, c := range chunk.Choices {
				if c.Delta.Content == "" {
					continue
				}
				if stats.FirstWords == 0 {
					stats.FirstWords = time.Since(started)
				}
				answer.WriteString(c.Delta.Content)
				if onPartial != nil && (lastPartial.IsZero() || time.Since(lastPartial) >= partialEvery) {
					onPartial(ctx, answer.String())
					lastPartial = time.Now()
				}
			}
		}
	}
	if err := sc.Err(); err != nil && !done {
		if ctx.Err() == context.DeadlineExceeded {
			return transcript(tools.String(), answer.String()), fmt.Errorf("hermes: turn exceeded %s", timeout)
		}
		if ctx.Err() != nil {
			return transcript(tools.String(), answer.String()), ctx.Err()
		}
		return transcript(tools.String(), answer.String()), fmt.Errorf("hermes api stream: %v", err)
	}
	return transcript(tools.String(), answer.String()), nil
}

// toolLine turns a hermes.tool.progress event into a CLI-like audit line
// ("💻 terminal: ls" when it starts, "✗ terminal" when it fails).
func toolLine(b *strings.Builder, data string) {
	var ev struct {
		Tool   string `json:"tool"`
		Emoji  string `json:"emoji"`
		Label  string `json:"label"`
		Status string `json:"status"`
	}
	if json.Unmarshal([]byte(data), &ev) != nil || ev.Tool == "" {
		return
	}
	switch ev.Status {
	case "running":
		fmt.Fprintf(b, "%s %s: %s\n", strings.TrimSpace(ev.Emoji+" "), ev.Tool, ev.Label)
	case "failed", "error":
		fmt.Fprintf(b, "✗ %s failed\n", ev.Tool)
	}
}

func transcript(tools, answer string) string {
	var b strings.Builder
	if t := strings.TrimSpace(tools); t != "" {
		b.WriteString(t + "\n")
	}
	b.WriteString("────────────────────────────────────────\n☤ Hermes\n")
	b.WriteString(strings.TrimSpace(answer))
	return b.String()
}

// Latency is the last turn's request → first words and request → end (A13).
func (r *APIRunner) Latency() (first, total time.Duration) {
	s := r.Stats()
	return s.FirstWords, s.Total
}

// Auto runs each turn through the warm API server and falls back to the CLI
// (`hermes chat -q`) only when the server cannot be reached — e.g. right
// after a container start, before the s6 gateway is up. HIRELING_TURNS=cli
// forces the CLI (Mode "cli").
type Auto struct {
	API  *APIRunner
	CLI  *Runner
	Mode string // "" | "api" | "cli"
	// KeyFile is re-read when the key is still empty (the image writes it on first start).
	KeyFile string
	Log     func(format string, a ...any)

	mu      sync.Mutex
	usedCLI bool
}

// Run picks the path for this turn.
func (a *Auto) Run(ctx context.Context, text string) (string, error) {
	if a.Mode != "cli" && a.API != nil {
		if a.API.Key == "" && a.KeyFile != "" {
			a.API.Key = KeyFromEnvFile(a.KeyFile)
		}
		if a.API.Key != "" {
			out, err := a.API.Run(ctx, text)
			if !errors.Is(err, ErrAPIUnavailable) {
				a.setCLI(false)
				return out, err
			}
			if a.Log != nil {
				a.Log("turns: %v — this turn falls back to hermes chat -q", err)
			}
		}
	}
	a.setCLI(true)
	return a.CLI.Run(ctx, text)
}

func (a *Auto) setCLI(v bool) {
	a.mu.Lock()
	a.usedCLI = v
	a.mu.Unlock()
}

// SetReasoning applies to the API path (the CLI keeps the profile default).
func (a *Auto) SetReasoning(level string) {
	if a.API != nil {
		a.API.SetReasoning(level)
	}
}

// SetOnPartial applies to the API path; the CLI path emits no deltas.
func (a *Auto) SetOnPartial(fn func(context.Context, string)) {
	if a.API != nil {
		a.API.SetOnPartial(fn)
	}
}

// Latency of the last turn; zero when it ran on the CLI (not measured there).
func (a *Auto) Latency() (first, total time.Duration) {
	a.mu.Lock()
	cli := a.usedCLI
	a.mu.Unlock()
	if cli || a.API == nil {
		return 0, 0
	}
	return a.API.Latency()
}

// Describe is the `hireling check` line.
func (a *Auto) Describe(ctx context.Context) string {
	if a.Mode == "cli" || a.API == nil {
		return "hermes chat -q per turn (HIRELING_TURNS=cli)"
	}
	if a.API.Key == "" && a.KeyFile != "" {
		a.API.Key = KeyFromEnvFile(a.KeyFile)
	}
	if a.API.Healthy(ctx) {
		return "warm Hermes API " + a.API.base() + " (streaming; CLI only as fallback)"
	}
	return "Hermes API " + a.API.base() + " not answering — turns use hermes chat -q until it is up"
}
