// tavern-mcp: the Tavern MCP server. It gives an MCP client (Claude Code and
// others) the owner's console of a Hireling tavern over stdio: orders, status,
// decisions, tasks and the quest (docs/guides/claude-code-mcp.md).
//
//	FRONTDESK_URL      tavern-core (default http://127.0.0.1:8080; falls back to TAVERN_URL)
//	FRONTDESK_OWNER_TOKEN  the owner token (FRONTDESK_TOKEN is accepted as a fallback)
//	FRONTDESK_ALLOW_HTTP=1  allow plain http:// to a non-loopback host
//	CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET  optional, behind Cloudflare Access
//
//	tavern-mcp           serve MCP on stdin/stdout
//	tavern-mcp -check    call GET /api/status once and print a one-line summary
//	tavern-mcp -version
//
// stdout belongs to the protocol; the few log lines go to stderr and never
// contain the token.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Play-Hireling/hireling/services/tavern-mcp/internal/desk"
	"github.com/Play-Hireling/hireling/services/tavern-mcp/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("tavern-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	check := fs.Bool("check", false, "call the Front Desk once and print a summary")
	version := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *version {
		fmt.Fprintln(stdout, "tavern-mcp", server.Version)
		return 0
	}
	cfg, err := desk.FromEnv(getenv)
	if err != nil {
		fmt.Fprintln(stderr, "tavern-mcp:", err)
		return 1
	}
	client := desk.New(cfg, nil)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *check {
		return doCheck(ctx, client, stdout, stderr)
	}
	fmt.Fprintf(stderr, "tavern-mcp %s: Front Desk at %s (owner token set)\n", server.Version, client.BaseURL())
	if err := server.New(client).Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		fmt.Fprintln(stderr, "tavern-mcp:", client.Redact(err.Error()))
		return 1
	}
	return 0
}

func doCheck(ctx context.Context, c *desk.Client, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := c.Do(ctx, http.MethodGet, "/api/status", nil)
	if err != nil {
		fmt.Fprintln(stderr, "tavern-mcp:", err)
		return 1
	}
	var st struct {
		Agents    []struct{ ID string } `json:"agents"`
		Decisions []json.RawMessage     `json:"decisions"`
	}
	_ = json.Unmarshal(raw, &st)
	ids := make([]string, 0, len(st.Agents))
	for _, a := range st.Agents {
		ids = append(ids, a.ID)
	}
	fmt.Fprintf(stdout, "ok: %s answers with the owner token; party %v; %d open decision(s)\n", c.BaseURL(), ids, len(st.Decisions))
	return 0
}
