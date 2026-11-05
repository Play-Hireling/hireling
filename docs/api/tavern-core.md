# tavern-core API

tavern-core is the tavern's state: tasks with leases, priorities, the quest, usage and budgets, and the policy gate. It also serves the [Front Desk](front-desk.md).

> **Internal API.** Everything below is meant for the internal Docker network. Callers identify themselves with the `X-Hireling: <id>` header (or an `agent` field in the body); there is no token. `compose.yaml` publishes tavern-core on **127.0.0.1 only** — never expose this port to the internet. Put a reverse proxy with authentication in front if you need remote access, and expose only `/api/*`. Authenticated task APIs are on the [roadmap](../roadmap.md).

## Health and agents

| Call | Returns |
| --- | --- |
| `GET /healthz` | `{"status":"ok","agents":[…]}` or `503` when Redis is down |
| `GET /agents` | Loaded hireling ids |
| `GET /agents/{id}/budget` | Profile budget + today's `tokens_used`, `cost_usd`, `turns` |
| `GET /agents/{id}/usage` | Today's totals and recent turns |
| `POST /agents/{id}/usage` | One turn's usage (the runtime posts it) |
| `GET /agents/{id}/queue` | The hireling's open tasks in priority order (P0 > … > P3, ties by goal contribution) |

## Policy and the Autonomy Log

| Call | Body | Returns |
| --- | --- | --- |
| `POST /actions/check` | `{"agent","action","target","use","why","turn","task"}` | `{"level":"free|capped|owner","reason","limit","period","used","counted"}` |
| `GET /agents/{id}/blocked?since=` | — | Actions the gate blocked for this hireling since an RFC 3339 time (to attach to its card) |
| `POST /actions/pending` | `{"decision","agent","actions":[{"action","target"}],"approve"}` | The pending card |
| `POST /actions/answer` | `{"decision","choice"}` | `{"granted":[…]}` — on a yes, one use of each action |
| `GET /autonomy?day=&agent=&limit=` | — | `{"day","done","blocked","entries":[…]}` — the Autonomy Log (D4) |

With `"use": true` the check counts a capped use in its UTC day or ISO week window (D2), consumes a one-time grant from your approval, and remembers a block for the next card. A cap of `0` means none without you; a `*_usd` cap cannot be counted per call, so every spend is yours. An action that cannot be written to the Autonomy Log is blocked.

`entries` carry `at`, `agent`, `action`, `target`, `outcome` (`done`/`blocked`), `level`, `reason`, `used`, `limit`, `period`, `why`, `turn`, `task`, and, when listed, `turn_cost_usd` and `turn_tokens`.

## The quest

| Call | Body | Who |
| --- | --- | --- |
| `GET /goal` | — | anyone · `404` until set |
| `PUT /goal` | `{"title","target","currency","source","kinds","period","current"}` | owner or planner |
| `POST /goal/progress` | `{"from","current"}` | the quest's `source` only |

`currency` is a three-letter code (default `USD`). `kinds` default to `revenue, leads, posts, videos, support`. The response adds `percent`, `bar`, `expected_open` and `expected_done` (declared contributions by kind).

## Tasks

| Call | Body | Effect |
| --- | --- | --- |
| `GET /tasks` | — | All tasks |
| `GET /tasks/{id}` | — | One task |
| `POST /tasks` | `{"title","owner","priority","goal_contribution":{"kind","expected","note"},"parent","brief","attachments"}` | Create. From a non-approver for someone else → `blocked`, `needs_approval` |
| `POST /tasks/{id}/lease` | `{"agent"}` | Take it (15 min) |
| `POST /tasks/{id}/renew` | `{"agent"}` | Extend the lease |
| `POST /tasks/{id}/finish` | `{"agent","status":"done|failed"}` | Close it |
| `POST /tasks/{id}/release` | `{"agent","status":"queued|waiting_owner","progress","reason"}` | Give it back (`reason: interrupt` → `interrupted` event) |
| `POST /tasks/{id}/handoff` | `{"agent","to"}` | Change owner |
| `POST /tasks/{id}/approve` · `/deny` | `{"agent","note"}` | Planner/owner decides a requested task |
| `POST /tasks/{id}/fail` | `{"agent","error","max"}` | Count a failed turn; at `max` → `blocked`, `needs_human` |
| `POST /tasks/{id}/unblock` | `{"agent","action"}` | Owner's answer to an escalation: retry, reassign, drop, change brief |

Status codes: `404` unknown task or quest not set · `409` not the owner / not leased / already finished / not pending · `403` not an approver or not the quest source · `400` validation.

Every change lands on the `events` stream with who did it.
