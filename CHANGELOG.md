# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/) (0.x: anything may change between minors).

## [Unreleased]

### Upgrading
- Rebuild the hirelings together with tavern-core (`docker compose up -d --build`): `POST /actions/answer` now needs `agent`, which older runtimes do not send, so with an old runtime your yes on a blocked action grants nothing (400) until it is rebuilt.
- tavern-core now refuses to start when `FRONTDESK_OWNER_TOKEN` is shorter than 32 characters or equal to a hireling's token. A box set up before this change may have a shorter one: run `./scripts/init-env.sh` again: it now replaces a Front Desk token shorter than 32 characters with 48 random hex characters and keeps the others. Then restart the box (`docker compose up -d`) and use the new owner token for `bin/tavern` and the Tavern MCP server.

### Added
- **Your answer is proven** (D1/D3): a gate card is tied to one open decision of its hireling before you answer, and the Front Desk writes on the decision what a yes grants. `POST /actions/answer` takes `agent` and grants only when the Front Desk shows that you chose the decision's first option; the body's `choice` and the card's `approve` no longer count, an answered decision takes no new card (no reuse of an old yes), and a card pending under another hireling's name is refused. Without a Front Desk nothing is granted (503).
- **D7 inside a turn**: a P2/P3 turn that crosses `tokens_per_day` is cut between tool calls by the gate hook; the model is told to wrap up. Each turn's priority travels with that turn, so your command never inherits a routine's label.
- **Ledger history and revert** (E6): ledger-writer serves `GET /history?path=&limit=` and `POST /revert {commit, reason}`; the undo is a new commit signed by the asking hireling, limited to its `ledger.write` areas (a planner curates any page of `brain/`). See [the Ledger](docs/concepts/ledger.md#history-and-revert).
- **Tavern MCP server** (`services/tavern-mcp`): orders, status, decisions (read and choose), tasks and the quest as MCP tools and resources over the Front Desk API, on stdio for `claude mcp add` ([guide](docs/guides/claude-code-mcp.md)). It reads the owner token from `FRONTDESK_OWNER_TOKEN` and calls only `/api/*`.
- **Owner reads under `/api/*`** in tavern-core, with the owner token: `GET /api/tasks`, `/api/tasks/{id}`, `/api/quest`, and `GET /api/decisions/{id}` (open or answered). A proxy that forwards only `/api/*` now serves the whole owner console; the internal `/tasks` and `/goal` are unchanged and stay on the Compose network.
- tavern-core refuses to start with an owner token shorter than 32 characters or equal to a hireling's token, and the internal write routes take `application/json` only (415 otherwise: no cross-site `text/plain` POST from a browser on the same machine).
- **Autonomy gate** (D1): `hireling gate`, a fail-closed Hermes `pre_tool_call` hook, checks every tool call with tavern-core before it runs; blocked actions open an `ask` card and your yes grants one use each.
- **Capped limits** (D2): uses counted per UTC day or ISO week; past the cap the action is yours. A cap of `0` means none without you.
- **Autonomy Log** (D4): every action the gate saw, with who, what, why, outcome, the turn and its cost (`GET /autonomy`).
- First public release of the engine: `hireling` runtime, tavern-core, egress proxy, ledger-writer, GBrain image, starter party of six hirelings.
- **Front Desk** in tavern-core (`/api/*`): orders, turn reports, long-poll command queue, "Waiting on you" decisions, hireling-to-hireling conversations, status.
- `bin/tavern` owner CLI and `scripts/init-env.sh`.
- `compose.yaml` "tavern in a box" with profiles `full`, `test` and `brain`.
- Documentation: concepts, guides, API and CLI reference, security model, licensing.

### Fixed
- ledger-writer replays a push that lost a race (someone pushed between its fetch and its push) for page writes too, and the replay works in the container: it rebases as `ledger-writer`, since the container has no git identity of its own. The Ledger history names only the files in the caller's areas (the rest are counted as `hidden`).
- The redactor also masks URLs with credentials in any scheme (`postgres://`, `redis://`, `amqp://`, `mysql://`), whole PEM private keys, full JWTs and more API key shapes; Ledger pages go through it before ledger-writer.
- The redactor also masks URLs with credentials in any scheme (`postgres://`, `redis://`, `amqp://`, `mysql://`), PEM private keys (whole, or cut without their END line), full JWTs, environment-style `…_API_KEY=`/`…_TOKEN=` lines and more key shapes (Hugging Face, GitLab, AWS temporary, Twilio, Stripe test, Slack webhooks); Ledger pages go through it before ledger-writer.
- The gate reads MCP tool names as Hermes writes them, `mcp__<server>__<tool>` (the single-underscore form still works). Before, GBrain and Composio calls under the double-underscore name fell through to "external action". Only GBrain **reads** are local now: a GBrain write tool goes to tavern-core like any outside action.

### Changed
- CI runs the Redis-backed stores (tasks, asks, autonomy, policy grants) against a real Redis (`REDIS_TEST_URL`), and the tavern client has its own tests.
