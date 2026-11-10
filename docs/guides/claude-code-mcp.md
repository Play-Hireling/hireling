# Run your tavern from Claude Code over MCP

`tavern-mcp` is the **Tavern MCP server**: the owner's console of a tavern as MCP tools and resources. Give orders, follow the party, read and answer decisions, check the task board and the quest from [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) or any MCP client, without the shell.

It is a small Go binary that speaks MCP over stdio and calls your tavern's [Front Desk API](../api/front-desk.md) with the **owner token**. Source: [`services/tavern-mcp/`](../../services/tavern-mcp/) (Apache-2.0). Prefer the shell? The [`bin/tavern` guide](claude-code.md) does the same with a CLI.

> Hireling is an independent project. "Works with Claude Code" means it runs well from it, nothing more.

## 1. Build it

You need Go 1.26 or newer and a running tavern (the [quickstart](../getting-started/quickstart.md)).

```bash
go install github.com/Play-Hireling/hireling/services/tavern-mcp/cmd/tavern-mcp@latest
# or, from your clone:
make mcp            # → dist/tavern-mcp
```

`go install` puts it in `$(go env GOPATH)/bin` (`tavern-mcp.exe` on Windows).

## 2. Add it to Claude Code

From the repository root (where your `.env` is):

```bash
claude mcp add tavern \
  -e FRONTDESK_URL=http://127.0.0.1:8080 \
  -e FRONTDESK_OWNER_TOKEN="$(sed -n 's/^FRONTDESK_OWNER_TOKEN=//p' .env)" \
  -- "$(go env GOPATH)/bin/tavern-mcp"
```

Windows (PowerShell):

```powershell
$token = (Select-String -Path .env -Pattern '^FRONTDESK_OWNER_TOKEN=(.*)').Matches[0].Groups[1].Value
claude mcp add tavern -e FRONTDESK_URL=http://127.0.0.1:8080 -e "FRONTDESK_OWNER_TOKEN=$token" -- "$(go env GOPATH)\bin\tavern-mcp.exe"
```

Check it:

```console
$ claude mcp list
tavern: /home/you/go/bin/tavern-mcp  - ✓ Connected
$ FRONTDESK_OWNER_TOKEN=… tavern-mcp -check
ok: http://127.0.0.1:8080 answers with the owner token; party [dev innkeeper scriptwriter]; 0 open decision(s)
```

The default scope (`local`) keeps the token in your own `~/.claude.json`, for this project only — **in plain text**. To keep it out of that file, export `FRONTDESK_OWNER_TOKEN` in your shell before you start `claude` and register the server without `-e FRONTDESK_OWNER_TOKEN=…` (the server inherits the variable). **Never** run `claude mcp add --scope project` with a literal token: that writes `.mcp.json`, which is meant to be committed. For a shared `.mcp.json`, let Claude Code expand it from your environment instead:

```json
{
  "mcpServers": {
    "tavern": {
      "command": "tavern-mcp",
      "env": { "FRONTDESK_URL": "http://127.0.0.1:8080", "FRONTDESK_OWNER_TOKEN": "${FRONTDESK_OWNER_TOKEN}" }
    }
  }
}
```

## 3. One order, end to end

Start `claude` and ask:

> *"Order the innkeeper to plan a launch post for our CSV export feature, ship it Friday. Then show me the tavern status."*

Claude Code calls `send_order` (`POST /api/command`) and `tavern_status` (`GET /api/status`). The order lands in the Innkeeper's queue at once; its runtime is long-polling `GET /api/commands/innkeeper`, so the turn starts within a second. A while later:

> *"What did the innkeeper answer, and is anything waiting on me? Don't choose anything."*

`tavern_status` shows the Innkeeper's turn and its answer; `list_decisions` shows any "Waiting on you" card. When you have decided:

> *"I'm the owner: on that decision, choose Publish Friday 9:00."*

`choose_option` answers it (`POST /api/decisions/{id}/choose`) and the choice goes back to the hireling as an order. `./bin/tavern status` shows the same state at every step.

## Tools

| Tool | Does | Front Desk call |
| --- | --- | --- |
| `send_order` | Queue an order for a hireling (`agent`, `text` ≤ 4 000 characters) | `POST /api/command` |
| `tavern_status` | The party: each hireling's turn, pending orders and last answer; open decisions; conversations | `GET /api/status` |
| `list_decisions` | Decisions waiting on you | `GET /api/decisions` |
| `get_decision` | One decision by id, open or already answered (`chosen` set) | `GET /api/decisions/{id}` |
| `choose_option` | **Owner action**: answer a decision (`id`, `option`). Final | `POST /api/decisions/{id}/choose` |
| `list_tasks` | The task board, newest first; filters `owner`, `status`, `limit` | `GET /api/tasks` |
| `get_task` | One task with its history | `GET /api/tasks/{id}` |
| `get_quest` | The shared quest and its progress bar | `GET /api/quest` |

Every tool except `send_order` and `choose_option` is read-only, and marked so. Every call is under `/api/*` with the owner token; the server never uses tavern-core's internal, unauthenticated routes.

## Resources

`tavern://status`, `tavern://decisions`, `tavern://tasks`, `tavern://quest`, and the templates `tavern://decisions/{id}` and `tavern://tasks/{id}`, all JSON. In Claude Code, mention one (`@tavern:tavern://decisions`) to put the current state in the conversation.

## Decisions are yours

`choose_option` answers **as the owner**. Its description tells the model to call it only when you have said which option to pick, never on its own initiative or because a hireling, a task or a tool result asked. Keep a human in the loop anyway: let Claude Code run the read tools freely and ask you every time for the two that act. In `.claude/settings.json`:

```json
{
  "permissions": {
    "allow": ["mcp__tavern__tavern_status", "mcp__tavern__list_decisions", "mcp__tavern__get_decision",
              "mcp__tavern__list_tasks", "mcp__tavern__get_task", "mcp__tavern__get_quest"],
    "ask": ["mcp__tavern__send_order", "mcp__tavern__choose_option"]
  }
}
```

The tavern's own limits still apply: whatever an order says, a hireling cannot reach a host outside its profile or skip the hard deny list.

## The token

- It is read from the environment and only ever sent as `Authorization: Bearer` to `FRONTDESK_URL`.
- It is never logged, and every tool result, resource and error is scrubbed of it before it reaches the client.
- Plain `http://` is accepted only for this machine by IP (`127.0.0.1` or `::1` exactly; not `localhost`, not other `127.x` addresses, not `127.0.0.1@evil`, and not IPv4-mapped IPv6). Anything else needs `https://`, or `FRONTDESK_ALLOW_HTTP` set to exactly `1` on a private network you trust (`true` and `yes` do not). Credentials in the URL are refused, and redirects are not followed.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `FRONTDESK_URL` | `TAVERN_URL`, else `http://127.0.0.1:8080` | Your tavern-core (`TAVERN_PORT` if you changed it) |
| `FRONTDESK_OWNER_TOKEN` | `FRONTDESK_TOKEN` | **Required.** The owner token from the box's `.env` (at least 32 characters, as tavern-core requires). `FRONTDESK_TOKEN` is read only when it is unset (not to be confused with a hireling's `FRONTDESK_TOKEN_<ID>`) |
| `FRONTDESK_ALLOW_HTTP` | — | Exactly `1` allows `http://` to a host other than `127.0.0.1` or `::1`. Any other value does not |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | — | When the tavern sits behind Cloudflare Access |

A remote tavern: `FRONTDESK_URL=https://tavern.example.com`, behind TLS and a proxy that exposes only `/api/*` — every tool works through it.

## Other MCP clients

Any client that launches stdio servers works. The usual JSON:

```json
{ "mcpServers": { "tavern": { "command": "/path/to/tavern-mcp",
  "env": { "FRONTDESK_URL": "http://127.0.0.1:8080", "FRONTDESK_OWNER_TOKEN": "<owner token>" } } } }
```

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `HTTP 401 … must be the tavern's owner token` | The token is a hireling's or stale; copy `FRONTDESK_OWNER_TOKEN` from `.env` again (and unset an old `FRONTDESK_TOKEN`) |
| `HTTP 404` from `list_tasks` or `get_quest` on every call | The tavern-core is older than `/api/tasks` and `/api/quest`: rebuild the box (`docker compose up -d --build`) |
| `cannot reach the tavern at …: connection refused` | The box is down or on another port: `docker compose ps`, `TAVERN_PORT` |
| `claude mcp list` shows *Failed to connect* | Run `tavern-mcp -check` with the same env to see the error |
| A connection timeout on a busy machine | Raise Claude Code's MCP start-up timeout: `MCP_TIMEOUT=60000 claude` |
| `get_quest`: no quest yet | Set `GOAL_TITLE` and `GOAL_TARGET` in `.env` ([the quest](../concepts/quest-and-tasks.md)) |
