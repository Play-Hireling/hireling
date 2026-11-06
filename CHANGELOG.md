# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses [Semantic Versioning](https://semver.org/) (0.x: anything may change between minors).

## [Unreleased]

### Added
- **Tavern MCP server** (`services/tavern-mcp`): orders, status, decisions (read and choose), tasks and the quest as MCP tools and resources over the Front Desk API, on stdio for `claude mcp add` ([guide](docs/guides/claude-code-mcp.md)).
- **Autonomy gate** (D1): `hireling gate`, a fail-closed Hermes `pre_tool_call` hook, checks every tool call with tavern-core before it runs; blocked actions open an `ask` card and your yes grants one use each.
- **Capped limits** (D2): uses counted per UTC day or ISO week; past the cap the action is yours. A cap of `0` means none without you.
- **Autonomy Log** (D4): every action the gate saw, with who, what, why, outcome, the turn and its cost (`GET /autonomy`).
- First public release of the engine: `hireling` runtime, tavern-core, egress proxy, ledger-writer, GBrain image, starter party of six hirelings.
- **Front Desk** in tavern-core (`/api/*`): orders, turn reports, long-poll command queue, "Waiting on you" decisions, hireling-to-hireling conversations, status.
- `bin/tavern` owner CLI and `scripts/init-env.sh`.
- `compose.yaml` "tavern in a box" with profiles `full`, `test` and `brain`.
- Documentation: concepts, guides, API and CLI reference, security model, licensing.
