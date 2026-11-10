# Front Desk API

The Front Desk is where the **owner** talks to the tavern and where **hirelings** report what they are doing. It is served by tavern-core under `/api/*` (reference implementation: `services/tavern-core/internal/frontdesk`) and consumed by the runtime (`runtime/internal/frontdesk`), `bin/tavern` and the game client.

Any server that implements this contract can stand in for it.

## Authentication

`Authorization: Bearer <token>` on every call.

| Token | Variable | May call |
| --- | --- | --- |
| Owner | `FRONTDESK_OWNER_TOKEN` | `/api/status`, `/api/command`, `/api/decisions` (GET), `/api/decisions/{id}` (GET), `/api/decisions/{id}/choose`, `/api/interactions` (GET), `/api/tasks`, `/api/tasks/{id}`, `/api/quest` |
| Hireling (own) | `FRONTDESK_TOKEN_<ID>` | Only its own `agent`/`from` |
| Hireling (shared) | `FRONTDESK_AGENT_TOKEN` | Any hireling of the tavern |

Unknown agents are `401`. Tokens are compared in constant time. tavern-core refuses to start when `FRONTDESK_OWNER_TOKEN` is shorter than 32 characters or equal to `FRONTDESK_AGENT_TOKEN` or a hireling's `FRONTDESK_TOKEN_<ID>` (that hireling would be the owner); `./scripts/init-env.sh` writes 48 random hex characters for each. Behind a proxy that adds its own auth (e.g. Cloudflare Access), the runtime also sends `CF-Access-Client-Id` / `CF-Access-Client-Secret` when configured.

## Owner endpoints

### `POST /api/command` — give an order

```json
{"agent": "innkeeper", "text": "Plan a launch post for Friday"}
```
→ `200 {"command": {"id": "c-…", "agent": "innkeeper", "text": "…", "at": "…"}}`. Text ≤ 4 000 characters. Wakes the hireling's long poll.

### `GET /api/status` — the whole tavern at a glance

```json
{"agents": [
   {"id": "innkeeper", "pending": 0,
    "turn": {"agent": "innkeeper", "phase": "end", "topic": "Plan a launch post…", "result": "Planned T-1 …", "server": "local", "at": "…"},
    "lastDone": {"id": "c-…", "result": "done", "note": "…", "answer": "…"}}],
 "decisions": [ …open decisions… ],
 "interactions": [ …recent conversations… ],
 "quest": null,
 "at": "…"}
```

`quest` is the same object as [`GET /api/quest`](#the-task-board-and-the-quest-read-only), or `null` when no quest is set. The owner panel reads it here, so a missing quest is an empty state on this 200. `GET /api/quest` is still 404 until a quest is set. The response sends `Cache-Control: no-store` so a poll cannot reuse a stale party or quest.

### `GET /api/decisions` — waiting on you
→ `{"decisions": [{"id": "d-…", "from": "social-media", "question": "…", "options": ["…"], "context": "…", "urgency": "normal", "at": "…"}]}`

### `GET /api/decisions/{id}` — one decision, open or answered
→ `{"decision": {"id": "d-…", "from": "…", "question": "…", "options": ["…"], "chosen": "…", "at": "…"}}`. `chosen` is set once the owner has answered. `404` for an unknown id (the desk keeps the last 200 decisions).

### `POST /api/decisions/{id}/choose`
```json
{"option": "Publish Friday 9:00"}
```
Case-insensitive match against the offered options (`400` otherwise, `409` if already decided). Queues a command for the requesting hireling with `decisionId` set and text `Decision: <question> → chosen: <option> (decision <id>)`.

### The task board and the quest (read-only)

tavern-core serves the owner's view of the board and the quest next to the desk, with the owner token. They are the same data as the internal `GET /tasks`, `GET /tasks/{id}` and `GET /goal` ([tavern-core API](tavern-core.md)), wrapped in an object:

| Call | Answer |
| --- | --- |
| `GET /api/tasks` | `{"tasks": [ …every task… ]}` |
| `GET /api/tasks/{id}` | `{"task": {…}}` · `404` unknown |
| `GET /api/quest` | `{"quest": {"title", "target", "current", "percent", "bar", "kinds", …}}` · `404` until a quest is set |

A hireling token gets `401` here, as on every owner endpoint.

## Hireling endpoints

### `POST /api/turn` — report a turn

```json
{"agent": "scriptwriter", "phase": "start", "with": "innkeeper", "topic": "Launch video script", "screen": "code", "server": "local", "region": ""}
{"agent": "scriptwriter", "phase": "step", "step": "Drafting hook 2 of 3"}
{"agent": "scriptwriter", "phase": "end", "result": "Script on the table · 41.2k tokens · US$ 0.006", "waitingOnOwner": ""}
```
`phase` ∈ `start | step | end`. A `step` keeps the topic of its turn. Texts are cut (topic/step 200, result 500, waitingOnOwner 300).

### `GET /api/commands/{agent}[?wait=N]` — the order queue

→ `{"commands": [{"id": "c-…", "text": "…", "decisionId": "d-…"}], "waited": true}`

Oldest first. With `wait` (seconds, capped at 25) and an empty queue the server holds the request until an order arrives or the time runs out (`waited: true`). A server without long poll answers at once with `waited: false`, and the runtime falls back to `poll_seconds`.

### `POST /api/commands/{agent}/ack`

```json
{"id": "c-…"}                                                     // received: leaves the queue
{"id": "c-…", "result": "done", "note": "≤200", "answer": "≤4000"} // finished
{"id": "c-…", "result": "rejected", "note": "why"}
```

### `POST /api/decisions` — open a "Waiting on you" card

```json
{"from": "social-media", "question": "≤300", "options": ["2 to 6, ≤80 each"], "context": "≤1000", "urgency": "normal"}
```
→ `{"decision": {"id": "d-…", …}}`

### Conversations

| Call | Body | Notes |
| --- | --- | --- |
| `GET /api/interactions` | — | Recent hireling-to-hireling conversations (owner or hireling token) |
| `POST /api/interaction` | `{"from", "to", "text"}` | Opens one (`text` ≤ 300) |
| `POST /api/interaction/{id}/reply` | `{"agent", "reply"}` | Only the recipient; once |

## Storage

One JSON document in Redis (`tavern:frontdesk`), written after every change; the last 200 finished commands, interactions and decisions are kept. tavern-core is a single instance per tavern.
