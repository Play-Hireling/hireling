# runtime — the hireling container

One image for every hireling (feature **A1**). It is the official
[`nousresearch/hermes-agent`](https://hub.docker.com/r/nousresearch/hermes-agent)
image plus one static Go binary, `hireling`, which is the container's command.

```
hireling run            # the container's CMD: Front Desk queue (long poll) + bar loop, forever
hireling check          # validate the profile, write the Hermes config, ping the front desk
hireling once "<text>"  # run one command without the queue (smoke test)
hireling usage [1h]     # what the last period cost, from the Hermes store (A2)
hireling send <to> "<text>" [type] [task]   # put a message in another hireling's inbox (A5)
hireling quest          # print the quest bar (C1)
```

## What `hireling run` does

1. Loads `agents/<HIRELING_AGENT>/profile.yaml` + `prompt.md` (`runtime/profile`). The model
   named there is the only thing that picks DeepSeek V4-Flash or V4-Pro (**A2**).
   The hard deny list (**D5**) is always appended to `owner_decides`, and a profile that
   tries to make one of those actions *free* or *capped* is rejected.
2. Writes `$HERMES_HOME/config.yaml` (`internal/hermes`): provider `custom`, base URL
   `https://api.deepseek.com/v1`, `key_env: DEEPSEEK_API_KEY`, approvals **on**
   (`single_query_mode: deny`, so a dangerous command is denied instead of waiting for a
   human) and a deny list for sockets, privilege escalation and secret dumping (**A8**).
   The file never contains a secret. When `OPENROUTER_API_KEY` is set it also writes
   `fallback_providers: [{provider: openrouter, model: deepseek/deepseek-v4-<flash|pro>}]`:
   Hermes switches to the same model on OpenRouter when DeepSeek answers 429/5xx (after its
   retries) or 401/403/404, for that turn only — the next turn starts on DeepSeek again.
   `fallback.model` / `fallback.disabled` in the profile override it.
3. Waits on `GET /api/commands/<id>?wait=25` on the Front Desk (**A4**, long poll; a desk without
   long poll falls back to `runtime.poll_seconds`). For each command: `POST /api/turn start`, ack
   `received`, run one turn through Hermes' warm API server (`127.0.0.1:8642`; `hermes chat -q` is
   the fallback and `HIRELING_TURNS=cli` forces it), send a `step` every `turn_ttl_minutes / 3` so
   the turn stays alive, then `end` and ack `done` (or `rejected` with the error) — **A3**.
4. On the CLI path, `hermes chat -q` prints a transcript — the echoed `Query:`, thinking and tool calls between `────` rules, then `☤ Hermes` and the reply. `internal/hermes.Answer` keeps only the reply for the ack and the turn result — but that stdout is a terminal render that drops code fences, so after the turn the runtime takes the reply from Hermes' session store instead (`usage.Store.Answer`: last assistant message without tool calls in the sessions started since the turn began), which keeps the raw markdown the `plan`/`request`/`decision`/`ask` blocks need; `hermes.Answer` is the fallback; the full redacted transcript is saved to `$HERMES_HOME/turns/<time>-<command-id>.log` (private volume, audit).
5. After the turn, `internal/usage` reads the Hermes session store (`$HERMES_HOME/state.db`,
   tables `sessions` + `session_model_usage`: Hermes records every API call's tokens there, the
   `hermes chat -q` transcript carries none) for the sessions started since the turn began, prices
   them at DeepSeek's public rates (cache hit / cache miss / output, off-peak = half; the OpenRouter
   fallback is recorded but not priced) and records the result three ways (**A2**): one log line
   (`usage: 7 calls, 14924 in + 68096 cached, 1017 out (+313 reasoning), US$ 0.0032`), one JSON line
   in `$HERMES_HOME/usage/<day>.jsonl`, and `POST /agents/<id>/usage` on tavern-core (`TAVERN_URL`),
   which adds it to the day's counters shown by `GET /agents/<id>/budget`. The front desk result
   gets the short form appended: `… · 84.35k tokens · US$ 0.003`.
6. **The bar (A5)**: when `REDIS_URL` is set, a second loop reads `inbox:<id>` through the consumer
   group `hireling` (`internal/bar`): first the entries delivered to this consumer and never
   acknowledged (a turn cut by a restart), then a blocking `XREADGROUP` (20 s windows). A message
   is handled like a command — `start` to the front desk *with the sender*, one Hermes turn (the
   message is framed as data from another agent, never as instructions), `end` — and a `request`
   gets a `reply` in the sender's inbox (over 4000 characters: written under
   `/tavern/shared/replies/` and attached); a `handoff` is answered by the decision (below). `XACK` happens only after the turn, so nothing is lost
   across restarts; a turn that fails on its own error is acknowledged too, so it cannot loop. Every
   message is mirrored on `bar` and produces `message.received` / `message.handled` on `events`.
   Bar turns and front desk turns share one lock: a hireling never runs two turns at once.
7. **Planning (B2/B1)**: when the profile says `planner: true` (the Innkeeper), `internal/plan.Instructions`
   is appended to the prompt and the plan block at the end of an answer is applied by the runtime:
   validated against the party (`profile.ListIDs(/agents)`), parent task + one child per entry in
   tavern-core (`POST /tasks`, `parent`, `goal_contribution`), brief per task under
   `/tavern/shared/<parent>/`, a `request` on the bar to each owner, `plan.created` on `events`. The
   answer that reaches the owner lists the created ids instead of the JSON. The model never calls
   tavern-core or Redis.
8. **Conversations in the front desk (B8)**: every message a hireling puts on the bar is mirrored by
   `frontdesk.ReportChat`: an open interaction from the recipient to
   the sender in the last 2 h gets the message as its `reply` (the pair leaves the table), otherwise a
   new interaction is created (`POST /api/interaction`, both shown talking). Ids are the front desk's
   (`desk_id` of each profile), and a bar turn reports `start … with <desk id>`.
9. **Handoffs (B6)**: a non-planner may end an answer with a `request` block (`to`, `title`,
   `priority` P2 by default, `brief`). The runtime creates the task (`requested_by` = itself, `parent` =
   the task it was working on); tavern-core keeps it **blocked** because the requester is not an
   approver, and the runtime sends a `handoff` to the approver's inbox (the planner). The planner
   answers with a `decision` block (`task`, `approve`, `note`); the runtime calls
   `POST /tasks/{id}/approve|deny` (only approvers may), then tells the owner (`request`) and the
   requester (`note`). `requested` / `approved` / `denied` land on tavern-core's `events` with who
   decided — the timeline of owner changes.
10. **Waiting on you (D3)**: when an action is the owner's to decide (`owner_decides` in the profile,
   money, publishing, anything irreversible), any hireling ends its answer with an `ask` block
   (`question` ≤ 300, 2–6 `options` ≤ 80 each, `context` ≤ 1000, `urgency` normal|high). The
   runtime opens the card with `POST /api/decisions`, ends the
   turn with `waitingOnOwner` = the question and the answer "Waiting on you: … (decision <id>)",
   and emits `decision.requested` on the `events` stream. No answer means wait: the hireling does
   nothing more about it until the owner's choice comes back through its command queue
   (`Decision: <question> → chosen: <option>`, with `decisionId`), which is a normal command turn.
   An unusable block opens nothing and the answer says why.
11. **Quest (C1)**: with `TAVERN_URL` set, every turn opens with the quest bar from `GET /goal`
   (title, %, current/target, the contribution kinds a task may declare) so the model plans
   against the goal; the number itself is never the model's to change. `hireling quest` prints it,
   `hireling check` shows a `quest` line (`not set` until `PUT /goal` or `GOAL_*` seed it).
12. **Contribution per task (C3)**: `goal.kind` is required in every `plan` task and every `request`
   block, and must be one of the quest's kinds read with the bar (nil kinds = any non-empty kind);
   `expected` ≥ 0, `note` ≤ 140. Blocks that fail are reported in the answer and create nothing;
   tavern-core enforces the same rule on `POST /tasks`. Owners read `Moves the quest: …` in their
   task message.
13. **Priorities and leases (B3, B4)**: each batch of bar messages is handled in the order of
   `GET /agents/{id}/queue` (P0 > P1 > P2 > P3, ties by goal contribution; a message without a task
   counts as P2). A `request` about a task this hireling owns holds its lease: `lease` before the
   turn (under the turn lock, so waiting never eats it), `renew` on every keepalive step (a third of
   the turn TTL), then `finish done`, `finish failed`, or `release waiting_owner` when the answer
   opened a "Waiting on you" card. Events `task.leased/done/failed/waiting_owner` on `events`.
14. **Interrupts (B5)**: a P2/P3 task turn is interruptible. The owner's command (always P0) calls
   `Interrupt` before waiting for the turn lock; while the turn runs, a watcher looks at newer inbox
   entries every 3 s and interrupts on a P0 task requested by the owner or approved by the Innkeeper (a
   Flash hireling's P0 never does — "only the Innkeeper reorders or interrupts"). The Hermes process
   is cancelled; its partial transcript goes to `/tavern/shared/<task>/progress-<time>.md`, the task
   goes back to the queue (`release` with reason `interrupt` → `interrupted` event, progress on the
   task), and the request is re-sent to the hireling's own inbox telling it to continue from that
   file. P0 and P1 turns are never interrupted.
15. **Accounts through Composio (A12)**: when the profile lists `hands.toolkits` and
   `COMPOSIO_API_KEY` is set, the runtime opens (or reuses, `composio-session.json` on the volume)
   a Composio Tool Router session for the tavern's user id (`COMPOSIO_USER_ID`), limited to those
   toolkits, with destructive tools and the remote workbench off and connection removal disabled,
   and adds it to `config.yaml` as `mcp_servers.composio` (`x-api-key: ${COMPOSIO_API_KEY}` —
   Hermes expands it at connect time, the key never hits the disk). The prompt gets
   `composio.Instructions`: an account that is not connected is asked for with an `ask` block
   carrying the Connect Link (the owner authorizes on Composio's page, 10-minute link) — never a
   password or token. `hireling check` prints the `hands` line. No key or no toolkits = no hands.
16. Everything that leaves the container goes through `internal/redact` (**A9**): the values
   of every variable listed in `profile.secrets`, the front desk token and the Access
   secret are masked, plus credential-shaped strings; summaries are cut to 120 characters
   like the front desk does.

The Front Desk being down never blocks the work: failures are
logged and the turn goes on.

## Environment (set per service — G4)

| Variable | Purpose |
| --- | --- |
| `HIRELING_AGENT` | agent id, picks `/agents/<id>/` |
| `HIRELING_SERVER`, `HIRELING_REGION` | reported with every turn (flag in the tavern) |
| `FRONTDESK_URL` | `http://tavern-core:8080` |
| `FRONTDESK_TOKEN_<ID>` | this hireling's token (`FRONTDESK_TOKEN_TEST`); falls back to `FRONTDESK_AGENT_TOKEN` |
| `CF_ACCESS_CLIENT_ID`, `CF_ACCESS_CLIENT_SECRET` | optional: only when the Front Desk sits behind Cloudflare Access |
| `DEEPSEEK_API_KEY` | read by Hermes only |
| `OPENROUTER_API_KEY` | optional fallback provider, read by Hermes only |
| `COMPOSIO_API_KEY` | optional (A12): Composio project key; read by this binary (session) and Hermes (`${…}` in the MCP header) |
| `COMPOSIO_USER_ID` | Composio user whose connected accounts the tavern shares (default `hireling-tavern`) |
| `HERMES_HOME` | `/opt/data` (volume `tavern-<id>`) |
| `HERMES_WRITE_SAFE_ROOT` | `/opt/data:/tavern/shared` (image default, A7): the only places Hermes' file tools may write — the private volume and the common table |
| `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` | set by the Compose file to the `egress` proxy (A8): the `tavern` network has no route out; Hermes (`httpx`, env proxy policy) and this binary (Go `ProxyFromEnvironment`) both honour them |
| `REDIS_URL` | `redis://redis:6379/0` — the bar; unset = no inbox loop |
| `TAVERN_URL` | `http://tavern-core:8080` — where usage per turn is posted; unset = recorded on the volume only |

## Develop

```bash
cd runtime && go test ./... && go build ./cmd/hireling && cd ..
# from the repository root:
docker build -f runtime/Dockerfile -t hireling/runtime:local .
```

## Known gaps

- Budget enforcement (`tokens_per_day`, **D7**) holds P2/P3 bar work between turns and stops a P2/P3 turn between tool calls (the gate hook); the cut is at most one API call late.
- Decisions live on the Front Desk only; they are not mirrored into tavern-core's task timeline yet.

Full documentation: [../docs/README.md](../docs/README.md).
