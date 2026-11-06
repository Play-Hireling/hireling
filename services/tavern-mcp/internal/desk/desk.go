// Package desk is the owner's client of a tavern: the Front Desk API
// (docs/api/front-desk.md), including the owner's read-only task board and
// quest (GET /api/tasks, /api/tasks/{id}, /api/quest). Every call is under
// /api/* and carries the owner token.
//
// The owner token is sent as a bearer header on every request and nowhere
// else: errors never carry it, response bodies are scrubbed of it, and
// redirects are not followed (so it cannot be replayed to another host).
package desk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultURL is where a box started with compose.yaml publishes tavern-core.
const DefaultURL = "http://127.0.0.1:8080"

// maxBody caps what is read from one response.
const maxBody = 4 << 20

// Config is read from the environment (see FromEnv).
type Config struct {
	URL   string // base URL of tavern-core, no trailing slash
	Token string // the owner token (FRONTDESK_OWNER_TOKEN on the box)
	// Optional headers for a desk behind Cloudflare Access.
	CFAccessID, CFAccessSecret string
	// AllowHTTP permits plain http:// to a host that is not loopback
	// (a private network you trust). Off by default.
	AllowHTTP bool
}

// FromEnv reads the configuration:
//
//	FRONTDESK_URL      base URL (falls back to TAVERN_URL, then DefaultURL)
//	FRONTDESK_OWNER_TOKEN  the owner token (FRONTDESK_TOKEN is accepted as a fallback)
//	FRONTDESK_ALLOW_HTTP=1  allow http:// to a non-loopback host
//	CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET  optional
func FromEnv(getenv func(string) string) (Config, error) {
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	c := Config{
		URL:            first("FRONTDESK_URL", "TAVERN_URL"),
		Token:          first("FRONTDESK_OWNER_TOKEN", "FRONTDESK_TOKEN"),
		CFAccessID:     first("CF_ACCESS_CLIENT_ID"),
		CFAccessSecret: first("CF_ACCESS_CLIENT_SECRET"),
		AllowHTTP:      first("FRONTDESK_ALLOW_HTTP") == "1",
	}
	if c.URL == "" {
		c.URL = DefaultURL
	}
	return c, c.Validate()
}

// Validate checks the URL and that a token is present. Its errors never
// contain the token.
func (c *Config) Validate() error {
	if c.Token == "" {
		return errors.New("FRONTDESK_OWNER_TOKEN is not set: pass the tavern's owner token (FRONTDESK_OWNER_TOKEN in the box's .env)")
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" {
		return errors.New("FRONTDESK_URL must be an absolute URL such as http://127.0.0.1:8080")
	}
	if u.User != nil {
		return errors.New("FRONTDESK_URL must not carry credentials; the owner token goes in FRONTDESK_OWNER_TOKEN")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback(u.Hostname()) && !c.AllowHTTP {
			return fmt.Errorf("FRONTDESK_URL: plain http is only allowed to this machine by IP (127.0.0.1 or ::1; got host %q); use https://, or set FRONTDESK_ALLOW_HTTP=1 on a private network you trust", u.Hostname())
		}
	default:
		return errors.New("FRONTDESK_URL must start with http:// or https://")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("FRONTDESK_URL must not have a query or fragment")
	}
	c.URL = strings.TrimRight(u.String(), "/")
	return nil
}

// loopback accepts loopback IP literals only: a name such as localhost is
// whatever the hosts file says it is.
func loopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Client calls one tavern with the owner token.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client. hc may be nil (a 30 s timeout client is used).
// Redirects are never followed.
func New(cfg Config, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	cp := *hc
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{cfg: cfg, http: &cp}
}

// BaseURL is the tavern's address (safe to show: it never carries credentials).
func (c *Client) BaseURL() string { return c.cfg.URL }

// Redact replaces every secret this client knows with "[redacted]".
func (c *Client) Redact(s string) string {
	for _, secret := range []string{c.cfg.Token, c.cfg.CFAccessSecret} {
		if len(secret) >= 4 {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return s
}

// Error is a failed call. Its text is safe to show to the model and the user.
type Error struct {
	Method, Path string
	Status       int    // 0 = the request did not get an answer
	Msg          string // the server's {"error": …} or a short description
}

func (e *Error) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("front desk: %s %s: %s", e.Method, e.Path, e.Msg)
	}
	return fmt.Sprintf("front desk: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Msg)
}

// NotFound reports a 404.
func NotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Do sends one request and returns the JSON body. body may be nil.
func (c *Client) Do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	fail := func(status int, msg string) error {
		return &Error{Method: method, Path: path, Status: status, Msg: c.Redact(cut(msg, 300))}
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fail(0, "encode request: "+err.Error())
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL+path, rdr)
	if err != nil {
		return nil, fail(0, "bad request")
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "tavern-mcp")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cfg.CFAccessID != "" {
		req.Header.Set("CF-Access-Client-Id", c.cfg.CFAccessID)
		req.Header.Set("CF-Access-Client-Secret", c.cfg.CFAccessSecret)
	}
	res, err := c.http.Do(req)
	if err != nil {
		// Report the cause (connection refused, timeout…) without the URL.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fail(0, "cannot reach the tavern at "+c.cfg.URL+": "+err.Error())
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil {
		return nil, fail(res.StatusCode, "reading the answer: "+err.Error())
	}
	if len(data) > maxBody {
		return nil, fail(res.StatusCode, "answer larger than 4 MiB")
	}
	if res.StatusCode/100 != 2 {
		return nil, fail(res.StatusCode, explain(res.StatusCode, data))
	}
	if !json.Valid(data) {
		return nil, fail(res.StatusCode, "the answer is not JSON (is FRONTDESK_URL a tavern?)")
	}
	return json.RawMessage(c.Redact(string(data))), nil
}

// explain turns an error answer into one line: the server's {"error": …}
// when there is one, plus a hint for the usual mistakes.
func explain(status int, data []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	msg := http.StatusText(status)
	if json.Unmarshal(data, &e) == nil && strings.TrimSpace(e.Error) != "" {
		msg = strings.TrimSpace(e.Error)
	}
	switch status {
	case http.StatusUnauthorized:
		msg += " (FRONTDESK_OWNER_TOKEN must be the tavern's owner token, not a hireling's)"
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		msg += " (redirects are not followed; point FRONTDESK_URL at the final address)"
	}
	return msg
}
