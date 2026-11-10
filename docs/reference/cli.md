# Command-line reference

Two CLIs: **`bin/tavern`** for you (the owner, on the host) and **`hireling`** inside each hireling container (for checks and debugging). The same owner console is also an MCP server, **`tavern-mcp`** ([below](#tavern-mcp)).

## `bin/tavern`

POSIX shell + `curl`. Reads `TAVERN_URL` (default `http://127.0.0.1:${TAVERN_PORT:-8080}`) and `FRONTDESK_OWNER_TOKEN` (environment, else `./.env`). Prints JSON; pipe to `jq` for colour.

| Command | Does | API |
| --- | --- | --- |
| `tavern status` | Every hireling's current turn, pending orders and last answer; open decisions; conversations | `GET /api/status` |
| `tavern order <hireling> "<text>"` | Queue an order (usually for the `innkeeper`) | `POST /api/command` |
| `tavern decisions` | Decisions waiting on you | `GET /api/decisions` |
| `tavern choose <decision-id> "<option>"` | Answer one (case-insensitive match on an offered option) | `POST /api/decisions/{id}/choose` |
| `tavern quest` | The shared goal | `GET /goal` |
| `tavern tasks` | The task board | `GET /tasks` |
| `tavern health` | tavern-core and its store | `GET /healthz` |

Examples:

```bash
./bin/tavern order innkeeper "Plan a launch post for Friday"
./bin/tavern status | jq '.agents[] | {id, topic: .turn.topic, phase: .turn.phase}'
./bin/tavern decisions | jq -r '.decisions[] | "\(.id)  \(.from): \(.question)  \(.options)"'
TAVERN_URL=https://tavern.example.com ./bin/tavern status     # a remote tavern behind TLS
# Behind a proxy that exposes only /api/*, `quest`, `tasks` and `health` (which call /goal, /tasks, /healthz) are not reachable — use an SSH tunnel for those.
```

## `tavern-mcp`

The Tavern MCP server: `bin/tavern`'s commands as MCP tools and resources on stdio, for Claude Code and other MCP clients. Build with `make mcp` (→ `dist/tavern-mcp`) or `go install github.com/Play-Hireling/hireling/services/tavern-mcp/cmd/tavern-mcp@latest`. Reads `FRONTDESK_URL` (falls back to `TAVERN_URL`, default `http://127.0.0.1:8080`) and `FRONTDESK_OWNER_TOKEN` (falls back to `FRONTDESK_TOKEN`); it does not read `.env`. It calls only the owner's `/api/*` routes.

| Command | Does |
| --- | --- |
| `tavern-mcp` | Serve MCP on stdin/stdout (what `claude mcp add tavern -- tavern-mcp` runs) |
| `tavern-mcp -check` | Call `GET /api/status` once with the owner token and print the party and open decisions |
| `tavern-mcp -version` | Print the version |

Tools, resources and setup: [Run your tavern from Claude Code over MCP](../guides/claude-code-mcp.md).

## `hireling` (inside a container)

Run as the `hermes` user: `docker compose exec -u hermes agent-<id> hireling <command>`.

| Command | Does |
| --- | --- |
| `hireling run` | The container's command: Front Desk queue + bar loop, forever |
| `hireling check` | Validate the profile, write Hermes' config, and print one line per subsystem (profile, fallback, usage, planner/party, escalation, shared, quest, bar, hands, ledger, turns, latency, frontdesk). The provider key probe runs unless you pass `--offline`, which skips it. Exits 2 when the Front Desk is unreachable |
| `hireling once "<text>"` | Run one command locally without the queue (smoke test) |
| `hireling usage [1h]` | What the turns of the last period cost, from Hermes' store |
| `hireling send <to> "<text>" [type] [task]` | Put a message in another hireling's inbox (`type` = request, reply, handoff, note, alert) |
| `hireling quest` | Print the quest bar |

`ledger-writer` and `tavern-core`/`egress` binaries accept `-healthcheck` (used by Docker health checks).
