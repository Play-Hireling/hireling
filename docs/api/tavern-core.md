# tavern-core API

tavern-core is the tavern's state: tasks with leases, priorities, the quest, usage and budgets, and the policy gate. It also serves the [Front Desk](front-desk.md).

> **Internal API.** Everything below is meant for the internal Docker network. Callers identify themselves with the `X-Hireling: <id>` header (or an `agent` field in the body). `compose.yaml` publishes tavern-core on **127.0.0.1 only** — never expose this port to the internet. Put a reverse proxy with authentication in front if you need remote access, and expose only `/api/*`. The owner's read-only board and quest are also served under `/api/*` with the owner token (`GET /api/tasks`, `/api/tasks/{id}`, `/api/quest`, see the [Front Desk API](front-desk.md)).
>
> **Owner bearer.** Every `/tasks` route, `PUT /goal`, every `/actions/*` route (`/actions/check`, `/actions/pending`, `/actions/answer`), `POST /asks/answer`, `POST /goal/progress`, `POST /asks`, `POST /asks/opened` and `POST /agents/{id}/usage` require `Authorization: Bearer` set to `FRONTDESK_OWNER_TOKEN`, the same token the Front Desk uses. Missing or wrong token is `401`. The token in a query string is ignored. An empty bearer and an empty owner token do not authenticate. Hirelings and `bin/tavern tasks` send that header. `GET /goal`, `GET /asks`, `GET /agents/*` and `GET /autonomy` do not: they do not change state. `X-Hireling` is case-sensitive: the owner is exactly `owner`. Writes must be `Content-Type: application/json` (`415` otherwise), so a web page cannot post to them cross-site. Every JSON response sends `X-Content-Type-Options: nosniff`. HTML, including `/tavern/`, sends the tavern Content-Security-Policy (no `unsafe-eval`; `style-src` allows `'unsafe-inline'`).

## Health and agents

| Call | Returns |
| --- | --- |
| `GET /healthz` | `{"status":"ok","agents":[…]}` or `503` when Redis is down |
| `GET /agents` | Loaded hireling ids |
| `GET /agents/{id}/budget` | Profile budget + today's `tokens_used`, `cost_usd`, `turns`, `over_budget` (D7: today's tokens ≥ `tokens_per_day`), `resets_at` (next 00:00 UTC) and `web_calls_used` (D8) |
| `GET /agents/{id}/usage` | Today's totals and recent turns |
| `POST /agents/{id}/usage` | One turn's usage (the runtime posts it) |
| `GET /agents/{id}/queue` | The hireling's open tasks in priority order (P0 > … > P3, ties by goal contribution) |

## Policy and the Autonomy Log

| Call | Body | Returns |
| --- | --- | --- |
| `POST /actions/check` | `{"agent","action","target","use","why","turn","task"}` | `{"level":"free|capped|owner|budget","reason","limit","period","used","counted"}` — action `web_call` is counted against `web_calls_per_day` (D8) |
| `GET /agents/{id}/blocked?since=` | — | Actions the gate blocked for this hireling since an RFC 3339 time (to attach to its card) |
| `POST /actions/pending` | `{"decision","agent","actions":[{"action","target"}],"approve"}` | The pending card, tied to the decision: it must be **open**, opened by that hireling (`desk_id`) and carry no card yet. The Front Desk writes on the decision what a yes grants ("Choosing "…" grants … one use of: …") and the option that grants is the decision's **first plain yes** (Approve, Yes, OK …; none → `400`), never the card's `approve`. `403` for an unknown or foreign decision, `409` for an answered one or a second card, `400` when that line would not fit whole (it is never cut, so it always names every action), `503` without a Front Desk |
| `POST /actions/answer` | `{"decision","choice","agent"}` | `{"granted":[…]}` — one use of each action of the card when the Front Desk shows that the owner chose the option the card names (the decision's first plain yes: Approve, Yes, OK …); any other answer uses the card up and grants nothing. The body's `choice` is ignored. `403` for an open, unknown or foreign decision or one without a card, `400` without `agent` |
| `GET /autonomy?day=&agent=&limit=` | — | `{"day","done","blocked","entries":[…]}` — the Autonomy Log (D4) |
| `POST /asks` | `{"agent","task","turn","action","question","blocked"}` | `{"verdict":"yours|joined|ask","level","reason","decision","follow_ups"}` — D11, before a card opens |
| `POST /asks/opened` | `{"agent","decision","task","action","question","recommended"}` | The recorded card (one per task) |
| `POST /asks/answer` | `{"decision","choice"}` | `{"found","card":{…,"follow_ups","choice","picked_recommended"}}` — owner bearer. The choice is the body's `choice`. Only the owner may send it. It does not call Approvals.Confirm, which proves a gate grant, not this card |
| `GET /asks?agent=` | — | `{"open":[…],"stats":[{"agent","opened","joined","bounced","answered","picked_recommended","tasks","asks_per_task","could_have_decided_alone"}]}` |

With `"use": true` the check counts a capped use in its UTC day or ISO week window (D2), consumes a one-time grant from your approval, and remembers a block for the next card. A cap of `0` means none without you; a `*_usd` cap cannot be counted per call, so every spend is yours. An action that cannot be written to the Autonomy Log is blocked.

`entries` carry `at`, `agent`, `action`, `target`, `outcome` (`done`/`blocked`), `level`, `reason`, `used`, `limit`, `period`, `why`, `turn`, `task`, and, when listed, `turn_cost_usd` and `turn_tokens`.

## The quest

| Call | Body | Who |
| --- | --- | --- |
| `GET /goal` | — | anyone · `404` until set |
| `PUT /goal` | `{"title","target","currency","source","kinds","period","current"}` | owner or planner, with the owner bearer and `X-Hireling` exactly `owner` (or a planner id). Bearer without that header is 403. `Owner` is 403 |
| `POST /goal/progress` | `{"from","current"}` | the quest's `source` only, and the owner bearer |

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
