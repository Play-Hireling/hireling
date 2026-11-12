# Configuration

Everything is configured with environment variables (per service) and the hireling profiles in `agents/`. `compose.yaml` wires them from `.env`; see [`.env.example`](../../.env.example).

## Hireling runtime (`agent-*` services)

| Variable | Default | Meaning |
| --- | --- | --- |
| `HIRELING_AGENT` | `test` | Which `agents/<id>/` this container is |
| `HIRELING_AGENTS_DIR` | `/agents` | Where profiles live (baked into the image) |
| `HERMES_HOME` | `/opt/data` | Hermes data dir — the hireling's private volume |
| `HERMES_BIN` | `hermes` | Hermes executable |
| `HERMES_API_URL` | `http://127.0.0.1:8642` | Hermes' warm API server inside the container |
| `HERMES_GATEWAY_BOOTSTRAP_STATE` | — | Set `running` so a fresh volume starts the API server |
| `HIRELING_TURNS` | auto | `cli` forces `hermes chat -q` turns (slow path) |
| `HIRELING_LONG_POLL` | on | `off` disables the Front Desk long poll |
| `HIRELING_SERVER`, `HIRELING_REGION` | profile's `home_server` / `region` | Where it runs; reported with every turn (server flag) |
| `FRONTDESK_URL` | — | Front Desk base URL (`http://tavern-core:8080` in Compose). Unset = no command queue |
| `FRONTDESK_ALLOW_HTTP` | — | Exactly `1` allows `http://` to a host other than `127.0.0.1` or `::1`. `true`, `yes`, and `1 ` do not. Compose sets `1`, because the desk is on the internal network. Leave it unset when the desk is on the public internet. `localhost`, other `127.x` addresses, `127.0.0.1@evil`, and IPv4-mapped IPv6 are not treated as that loopback |
| `FRONTDESK_TOKEN_<ID>` | — | This hireling's token (`<ID>` = upper-case id, `-` → `_`) |
| `FRONTDESK_AGENT_TOKEN` | — | Shared token, used when the per-hireling one is unset |
| `FRONTDESK_OWNER_TOKEN` | — | The owner bearer tavern-core already uses. The runtime sends it on `/tasks`, `PUT /goal`, `/actions/*`, `POST /goal/progress`, `POST /asks`, `POST /asks/opened` and `POST /agents/{id}/usage`. Compose passes the same `.env` value into every hireling. Not logged. Residual: that token is in every hireling's environment |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | — | Only when the Front Desk sits behind Cloudflare Access |
| `TAVERN_URL` | — | tavern-core (usage, quest, tasks, leases). Unset = local-only usage |
| `REDIS_URL` | — | The bar. Unset = no inbox loop |
| `TAVERN_SHARED` | `/tavern/shared` | The common table |
| `DEEPSEEK_API_KEY` | — | **Required.** Read by Hermes, never by the runtime |
| `HIRELING_MODEL_BASE_URL` | `https://api.deepseek.com/v1` | OpenAI-compatible model base URL. Empty keeps the default, so a stub can stand in for DeepSeek |
| `OPENROUTER_API_KEY` | — | Enables the per-turn fallback |
| `COMPOSIO_API_KEY`, `COMPOSIO_USER_ID` | —, `hireling-tavern` | Hands (accounts) |
| `COMPOSIO_BASE_URL` | Composio's | Override for testing |
| `GBRAIN_MCP_URL` | `http://gbrain:3131/mcp` | The Ledger's MCP server |
| `GBRAIN_TOKEN_FILE` | `/run/gbrain/<id>.token` | This hireling's read-only Ledger token |
| `LEDGER_WRITER_URL` | — | ledger-writer; unset = no ledger blocks |
| `HTTP(S)_PROXY`, `http(s)_proxy`, `NO_PROXY` | — | Point at `egress`; both cases are needed (curl reads only lower-case for HTTP) |

## tavern-core

| Variable | Default | Meaning |
| --- | --- | --- |
| `TAVERN_ADDR` | `:8080` | Listen address |
| `TAVERN_AGENTS_DIR` | `/agents` | Profiles (the policy gate and the Front Desk read them) |
| `REDIS_URL` | — | State store. Unset = in-memory (lost on restart — dev only) |
| `FRONTDESK_OWNER_TOKEN` | — | The owner's bearer token. Unset = owner endpoints closed |
| `FRONTDESK_AGENT_TOKEN` | — | Any hireling of this tavern may use it |
| `FRONTDESK_TOKEN_<ID>` | — | A hireling's own token |
| `GOAL_TITLE`, `GOAL_TARGET`, `GOAL_CURRENCY`, `GOAL_SOURCE`, `GOAL_KINDS`, `GOAL_PERIOD` | —, 0, `USD`, `owner`, default kinds, — | Seed the quest on first boot only; later changes via `PUT /goal` |
| `TAVERN_CLIENT_DIR` | `/tavern/client` | Self-host game files served at `/tavern/`. When it holds an `index.html`, `/` redirects there and the owner panel is at `/panel/`; without it, `/` is the panel, which says how to build the client, and `/tavern/` is 404 |

## tavern-mcp (on your machine)

| Variable | Default | Meaning |
| --- | --- | --- |
| `FRONTDESK_URL` | `TAVERN_URL`, else `http://127.0.0.1:8080` | The tavern to manage |
| `FRONTDESK_OWNER_TOKEN` | `FRONTDESK_TOKEN` | **Required.** The owner token (`FRONTDESK_TOKEN` is read only when it is unset) |
| `FRONTDESK_ALLOW_HTTP` | — | Exactly `1` allows `http://` to a host other than `127.0.0.1` or `::1` (otherwise `https://` only). Any other value does not |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | — | Only when the tavern sits behind Cloudflare Access |

## egress

| Variable | Default | Meaning |
| --- | --- | --- |
| `EGRESS_ADDR` | `:3128` | Listen address |
| `EGRESS_AGENTS_DIR` | `/agents` | Profiles (allowlists) |

## gbrain (profile `brain`)

| Variable | Meaning |
| --- | --- |
| `GBRAIN_DATABASE_URL` | Postgres with pgvector (`ledger-db` in Compose) |
| `LEDGER_REPO_URL` | https URL of the brain repo — no credentials in it |
| `LEDGER_GIT_TOKEN` | Token used by a credential helper; never written to disk |
| `LEDGER_SYNC_SECONDS` | Pull + re-index interval (default 120) |
| `GBRAIN_ADMIN_BOOTSTRAP_TOKEN` | Used to mint per-hireling read tokens |
| `HIRELING_LEDGER_READERS` | Space-separated hireling ids that get a read token |
| `LEDGER_DIR`, `LEDGER_SOURCE`, `GBRAIN_HOME`, `GBRAIN_TOKENS_DIR` | Paths and source name; defaults fit the Compose file |

## ledger-writer (profile `brain`)

| Variable | Default | Meaning |
| --- | --- | --- |
| `LEDGER_WRITER_ADDR` | `:8090` | Listen address |
| `LEDGER_WRITER_DIR` | `/repo/hireling-ledger` | Working clone |
| `LEDGER_REPO_URL`, `LEDGER_GIT_TOKEN` | — (required) | Same repo and token as gbrain; it refuses to start without the URL |
| `HIRELING_AGENTS_DIR` | `/agents` | Profiles (`ledger.write` areas) |

## Compose-level

| Variable | Meaning |
| --- | --- |
| `COMPOSE_PROFILES` | `full`, `test`, `brain` — comma separated |
| `HIRELING_TAG` | Image tag for every service (`local` by default); pin a release to roll back |
| `TAVERN_PORT` | Host port for tavern-core on `127.0.0.1` |
| `LEDGER_DB_PASSWORD` | Password of the `ledger-db` Postgres |
