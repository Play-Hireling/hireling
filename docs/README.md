# Hireling documentation

Hireling runs a small team of AI agents — **hirelings** — on your own server, as one **tavern**: a manager that plans, workers that do, a message bar, a shared table of files, a shared memory and a front desk where you give orders and answer decisions.

New here? Read in this order: **Quickstart → Your first order → The tavern → Autonomy and decisions.**

## Getting started
- [Quickstart](getting-started/quickstart.md) — a running tavern in ten minutes.
- [Your first order](getting-started/first-order.md) — follow one request from your terminal to finished files.

## Concepts
- [The tavern](concepts/tavern.md) — the pieces and how they fit.
- [Hirelings](concepts/hirelings.md) — what one agent is: container, profile, prompt, memory, budget.
- [The bar](concepts/the-bar.md) — how hirelings talk: inboxes, requests, replies, handoffs, events.
- [Quest and tasks](concepts/quest-and-tasks.md) — the shared goal, priorities, leases, interrupts.
- [Autonomy and decisions](concepts/autonomy.md) — free, capped, the owner decides; "Waiting on you".
- [The Ledger](concepts/ledger.md) — shared memory in git, searched over MCP.

## Guides
- [Add a hireling](guides/add-a-hireling.md)
- [Models and providers](guides/models.md)
- [Give hirelings accounts (Composio)](guides/accounts.md)
- [Turn on the Ledger](guides/ledger.md)
- [Run your tavern with Claude Code](guides/claude-code.md)
- [Deploy to a server](guides/deploy.md)

## Reference
- [`bin/tavern` and `hireling` CLIs](reference/cli.md)
- [Configuration (environment variables)](reference/configuration.md)
- [Profile schema (`agents/<id>/profile.yaml`)](reference/profile.md)
- [Answer blocks (plan, request, decision, ask, ledger)](reference/blocks.md)
- [Feature ids](reference/feature-ids.md)
- [Front Desk API](api/front-desk.md)
- [tavern-core API](api/tavern-core.md)

## Project
- [Architecture](architecture.md)
- [Security model](security.md)
- [Roadmap](roadmap.md)
- [FAQ](faq.md)
- [Glossary](glossary.md)
- [Licensing](../LICENSING.md) · [Contributing](../CONTRIBUTING.md) · [Security policy](../SECURITY.md)
