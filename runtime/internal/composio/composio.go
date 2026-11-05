// Package composio is the first layer of a hireling's hands
// (A12): the owner's accounts (GitHub, Google, social networks…) reached
// through Composio's Tool Router. The runtime opens one Tool Router session
// per hireling, limited to the toolkits of its profile, and gives Hermes the
// session's MCP URL; the API key stays in the environment (Hermes expands
// ${COMPOSIO_API_KEY} in the header at connect time), so it is never written
// to disk.
//
// Accounts are connected by the owner on Composio's own page: when a toolkit has
// no active connection, the COMPOSIO_MANAGE_CONNECTIONS meta tool returns a
// Connect Link (expires in 10 minutes), which the hireling hands to the owner as a
// "Waiting on you" card. No password, token or code ever passes through the
// tavern. Connections belong to the Composio user id, one per tavern, so every
// hireling of the tavern shares them, each limited
// to its own toolkits.
//
// API checked against backend.composio.dev on 2026-11-04:
// POST /api/v3.1/tool_router/session → 201 {session_id "trs_…", mcp {type "http", url}}.
package composio

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
	"slices"
	"strings"
	"time"
)

// DefaultBaseURL is Composio's API (also the MCP host).
const DefaultBaseURL = "https://backend.composio.dev"

// Host is what the egress proxy must allow for a hireling with hands.
const Host = "backend.composio.dev"

// KeyEnv names the environment variable with the project API key.
const KeyEnv = "COMPOSIO_API_KEY"

// Server is the Hermes `mcp_servers` key; tools register as mcp_composio_*.
const Server = "composio"

// Session is one Tool Router session.
type Session struct {
	ID       string   `json:"session_id"`
	URL      string   `json:"url"`
	UserID   string   `json:"user_id"`
	Toolkits []string `json:"toolkits"`
}

// Client talks to the Composio API. HTTP nil = a client honouring HTTPS_PROXY
// (the egress proxy).
type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
}

// sessionBody is the request: only the profile's toolkits, connections managed
// in-session (that is the Connect Link), never removed by the agent, no tool
// tagged destructive (D5: deleting is always the owner's), and no remote workbench
// (code running on Composio's side is outside the egress and the audit).
type sessionBody struct {
	UserID   string `json:"user_id"`
	Toolkits struct {
		Enable []string `json:"enable"`
	} `json:"toolkits"`
	ManageConnections struct {
		Enable           bool `json:"enable"`
		ConnectionRemove bool `json:"enable_connection_removal"`
	} `json:"manage_connections"`
	Tags struct {
		Disable []string `json:"disable"`
	} `json:"tags"`
	Workbench struct {
		Enable bool `json:"enable"`
	} `json:"workbench"`
}

type sessionResp struct {
	SessionID string `json:"session_id"`
	MCP       struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"mcp"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ErrNotFound means the session no longer exists on Composio's side.
var ErrNotFound = errors.New("composio: session not found")

func (c *Client) base() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultBaseURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*sessionResp, int, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("x-api-key", c.Key)
	req.Header.Set("content-type", "application/json")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out sessionResp
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode >= 300 {
		msg := res.Status
		if out.Error != nil && out.Error.Message != "" {
			msg += ": " + out.Error.Message
		}
		return &out, res.StatusCode, fmt.Errorf("composio %s %s: %s", method, path, msg)
	}
	return &out, res.StatusCode, nil
}

// NewSession opens a Tool Router session for userID limited to toolkits.
func (c *Client) NewSession(ctx context.Context, userID string, toolkits []string) (*Session, error) {
	var b sessionBody
	b.UserID = userID
	b.Toolkits.Enable = toolkits
	b.ManageConnections.Enable = true
	b.Tags.Disable = []string{"destructiveHint"}
	out, _, err := c.do(ctx, http.MethodPost, "/api/v3.1/tool_router/session", b)
	if err != nil {
		return nil, err
	}
	if out.SessionID == "" || out.MCP.URL == "" {
		return nil, errors.New("composio: session created without an MCP url")
	}
	return &Session{ID: out.SessionID, URL: out.MCP.URL, UserID: userID, Toolkits: toolkits}, nil
}

// Alive reports whether a session still exists.
func (c *Client) Alive(ctx context.Context, id string) error {
	_, code, err := c.do(ctx, http.MethodGet, "/api/v3.1/tool_router/session/"+id, nil)
	if code == http.StatusNotFound {
		return ErrNotFound
	}
	return err
}

// Ensure returns the session kept in file when it was opened for the same
// user and toolkits and still exists; otherwise it opens a new one and keeps
// it. One API call per container start in the common case (the liveness check).
func (c *Client) Ensure(ctx context.Context, file, userID string, toolkits []string) (*Session, error) {
	if raw, err := os.ReadFile(file); err == nil {
		var s Session
		if json.Unmarshal(raw, &s) == nil && s.ID != "" && s.URL != "" && s.UserID == userID && slices.Equal(s.Toolkits, toolkits) {
			switch err := c.Alive(ctx, s.ID); {
			case err == nil:
				return &s, nil
			case !errors.Is(err, ErrNotFound):
				return nil, err
			}
		}
	}
	s, err := c.NewSession(ctx, userID, toolkits)
	if err != nil {
		return nil, err
	}
	raw, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(filepath.Dir(file), 0o750); err == nil {
		_ = os.WriteFile(file, raw, 0o640)
	}
	return s, nil
}

// Instructions is the prompt section of a hireling with hands. toolkits are
// the profile's, so the model knows which accounts it may ask for.
func Instructions(toolkits []string) string {
	return `## The owner's accounts (Composio)

You reach the owner's accounts only through the ` + "`mcp_composio_*`" + ` tools, and only these toolkits: ` + strings.Join(toolkits, ", ") + `. Find the tool with COMPOSIO_SEARCH_TOOLS, then run it with COMPOSIO_MULTI_EXECUTE_TOOL. Deleting is disabled.

When a toolkit is not connected, call COMPOSIO_MANAGE_CONNECTIONS with it: it returns a Connect Link. Never ask the owner for a password, token, cookie or 2FA code, and never paste one anywhere. Instead end your answer with an ` + "`ask`" + ` block: question "Connect <toolkit> so I can <what for>?", options ["Connected", "Send a new link", "Not now"], the link in the context with "the link expires in 10 minutes", urgency "high" when a task waits on it. When the owner answers "Connected", check with COMPOSIO_MANAGE_CONNECTIONS and carry on with the task; "Send a new link" means call it again for a fresh link.

Your autonomy still applies to everything you do in these accounts: reading and drafting are free; posting, sending, publishing, inviting or changing settings follow your capped and the owner-decides lists; payments, contracts and customer data are always the owner's.`
}
