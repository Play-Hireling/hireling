# Architecture

## Components

```text
                         owner (bin/tavern · game client · Claude Code)
                                    │ HTTPS/HTTP + bearer
┌───────────────────────────────────┴───────────────────────────────────┐
│ tavern-core (Go, distroless)                                          │
│  Front Desk /api/*  ·  tasks + leases  ·  quest  ·  usage/budgets     │
│  policy gate  ·  lease sweeper  ·  events                             │
└──────────┬───────────────────────────────────────────────┬────────────┘
           │ HTTP (internal)                               │
           │                         ┌─────────────────────┴───┐
           │                         │ Redis 7 (AOF)           │
           │                         │ inbox:<id> · bar ·      │
           │                         │ events · state          │
           │                         └─────────────┬───────────┘
┌──────────┴─────────────── one per hireling ──────┴────────────────────┐
│ agent-<id>:  hermes-agent image  +  `hireling` (Go)                   │
│   /opt/data  (private volume)    /tavern/shared  (common table)       │
│   Hermes API server on 127.0.0.1:8642 (warm turns)                    │
└──────────┬──────────────────────────────┬─────────────────────────────┘
           │ HTTP(S)_PROXY                 │ MCP (optional)
   ┌───────┴────────┐               ┌──────┴──────────┐   ┌────────────────┐
   │ egress (Go)    │──► internet   │ gbrain (Bun)    │◄──│ ledger-writer  │──► git repo
   │ allowlist/agent│   (allowed)   │ + Postgres      │   │ (Go)           │
   └────────────────┘               └─────────────────┘   └────────────────┘
```

| Component | Language | State | Talks to |
| --- | --- | --- | --- |
| `hireling` runtime | Go 1.27, static | Hermes store on its volume | Hermes (local), tavern-core, Redis, ledger-writer |
| Hermes Agent | Python (upstream image) | `/opt/data` | model APIs through egress, MCP servers |
| tavern-core | Go 1.27, distroless | Redis | — (called by everyone) |
| egress | Go 1.27, distroless | none (no cache by design) | the internet |
| Redis | — | AOF volume | — |
| gbrain | Bun + GBrain | Postgres + checkout | git host |
| ledger-writer | Go 1.27 + git | working clone | git host |

## Networks

| Network | Members | Route out |
| --- | --- | --- |
| `tavern` (internal) | everything | **none** |
| `outside` | egress, gbrain, ledger-writer | yes |
| `desk` | tavern-core | yes (only to publish `127.0.0.1:8080`) |

## Routes on `127.0.0.1:8080`

| Route | What it serves |
| --- | --- |
| `GET /` | With a client bundle (`TAVERN_CLIENT_DIR/index.html`), a 307 to `/tavern/`; without one, the owner panel |
| `GET /panel/` | The owner panel, in every setup (`/panel` redirects there) |
| `GET /tavern/` | The self-host client bundle, if mounted; 404 otherwise |
| `GET /api/*` | The Front Desk API; everything the owner CLI and the runtimes use |

A hireling has no default route. Its `HTTP(S)_PROXY` points at `egress`, which identifies the caller by reverse DNS of its container name (`…-agent-<id>-N`), loads that profile's allowlist and refuses everything else (ports 80/443, no IP literals). There is deliberately **no identity cache**: Docker reuses IPs immediately when a container goes away.

## Repository layout

```text
runtime/                 Go module · the hireling runtime and ledger-writer
  cmd/hireling           the container's command
  cmd/ledger-writer      the Ledger's only writer
  internal/loop          turns, locks, bar loop, plans, handoffs, leases, interrupts, escalation
  internal/hermes        config rendering, warm API turns, CLI fallback, answer extraction
  internal/frontdesk     Front Desk client
  internal/bar           Redis Streams
  internal/plan          answer blocks: plan, request, decision, ask, ledger
  internal/usage         Hermes store → tokens → USD
  internal/redact        masking of secrets and credential-shaped strings
  internal/composio      hands (Tool Router sessions)
  internal/ledger, ledgerwriter   page grammar, commits as the hireling
  internal/tavern        tavern-core client
  profile/               the profile schema and validation (shared with tavern-core and egress)
services/tavern-core/    Go module (FSL) · httpapi, frontdesk, tasks, goal, policy, usage
services/egress/         Go module · the proxy
services/gbrain/         GBrain image and entrypoint
services/ledger-writer/  Dockerfile (code lives in runtime/)
agents/                  the starter party
ledger/                  page templates + seed brain (an Obsidian vault)
bin/tavern               owner CLI
compose.yaml             the whole tavern
```

## Design decisions

- **The model writes text; the runtime acts.** Blocks are parsed and validated by Go code. Models never get credentials to tavern-core, Redis or git.
- **Go for everything we write** — small static binaries, distroless images, low memory next to six Python agents.
- **Redis Streams as the bus** — consumer groups give at-least-once delivery with pending lists that survive restarts; AOF for durability.
- **Hermes as the agent** — tools, MCP, approvals and session store come from upstream; we pin behaviour through its config, not by forking.
- **Postgres only for the Ledger** — the rest of the tavern fits in Redis.
- **One tavern per Compose project** — multi-tenant hosting is a separate product (Hireling Cloud), not a mode of this repository.
