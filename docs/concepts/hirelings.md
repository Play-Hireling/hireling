# Hirelings

A hireling is **one container** running two things:

- **[Hermes Agent](https://hub.docker.com/r/nousresearch/hermes-agent)** (Nous Research) — the agent itself: model client, tools (files, terminal, web), MCP, sessions.
- **`hireling`** — a small Go program from this repository that is the container's command. It turns the profile into Hermes' config, feeds Hermes one turn at a time from the Front Desk and the bar, applies what the answer asks for, and records the cost.

The model never talks to tavern-core, Redis or the Front Desk. It writes text; the runtime decides what that text is allowed to do.

## What defines a hireling

```text
agents/<id>/
  profile.yaml   who it is and what it may do   (reference: ../reference/profile.md)
  prompt.md      how it works                   (its job, hard rules, habits)
```

The profile sets:

- **Model** — `deepseek-v4-flash` or `deepseek-v4-pro`, plus an optional OpenRouter fallback.
- **Budget** — tokens per day (mana) and web calls per day.
- **Autonomy** — actions that are `free`, `capped` (with limits) or `owner_decides`. Five actions are always the owner's, whatever the profile says: `payment`, `contract`, `customer_data`, `delete`, `production_critical`.
- **Network** — the hosts it may reach through the egress proxy.
- **Hands** — which of the owner's accounts it may use through Composio.
- **Ledger** — which areas of the shared memory it reads and writes.
- **Runtime** — polling, turn time-to-live, escalation threshold, chat reasoning effort.

## Memory, three layers

| Layer | Scope | Lives in | Survives |
| --- | --- | --- | --- |
| Hermes session store | This hireling, recent turns | `/opt/data/state.db` | Restarts and redeploys (named volume) |
| Private notes and transcripts | This hireling | `/opt/data/notes`, `/opt/data/turns` | Same |
| Common table | Everyone, files for current work | `/tavern/shared` | Same |
| Tavern Ledger | Everyone, long-term knowledge | git repo + GBrain | Forever, versioned |

## The starter party

| Id | Planner | Model | tokens/day | Capped | Extra hosts |
| --- | --- | --- | --- | --- | --- |
| `innkeeper` | yes | V4-Pro | 100k | propose a hire / a firing: 1 per week each | — |
| `scriptwriter` | | V4-Flash | 150k | publish a draft to the CMS: 3/day | — |
| `social-media` | | V4-Flash | 150k | publish on owned channels: 3/day | — |
| `dev` | | V4-Flash | 150k | deploy a non-critical service: 2/day | GitHub, PyPI, npm, Go proxy |
| `paid-traffic` | | V4-Flash | 150k | ad spend in USD: **0/day** until you raise it | — |
| `test` | | V4-Flash | 50k | — | — |

Profiles are the source of truth — open `agents/<id>/profile.yaml` for the exact lists.

## Safety inside the container

- Hermes approvals are **on** with `single_query_mode: deny`: a dangerous command is denied, never left waiting.
- A deny list covers sockets, privilege escalation and secret dumping.
- Hermes' file tools may only write under `/opt/data` and `/tavern/shared`.
- No capabilities beyond the five the image needs to step down to UID 10000; `no-new-privileges`.
- Everything that leaves the container (turn reports, answers, bar messages, Ledger pages) passes through the redactor: values of every secret in `profile.secrets` and credential-shaped strings are masked.

Read next: [The bar](the-bar.md).
