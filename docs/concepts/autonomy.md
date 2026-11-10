# Autonomy and decisions

Hirelings are useful because they act without asking. They are safe because some things they can never do alone. Hireling makes the line explicit, per action, per hireling.

## Three levels

| Level | Meaning | Example |
| --- | --- | --- |
| **free** | Act and log it | draft, research, write a shared file, message a hireling |
| **capped** | Act within a limit set in the profile | publish on owned channels 3/day; ad spend 0 USD/day until you raise it |
| **owner decides** | Stop, prepare, open a decision, wait | payments, contracts, customer data, deleting, production changes |

Five actions are **always** the owner's — the hard deny list: `payment`, `contract`, `customer_data`, `delete`, `production_critical`. A profile that lists one of them as `free` or `capped` is **rejected at load time**; one that forgets them gets them added back.

## Waiting on you

When an action is the owner's, the hireling ends its answer with an `ask` block:

````markdown
```ask
{"question": "Publish the launch post Friday 9:00 on X and LinkedIn?",
 "options": ["Publish Friday 9:00", "Hold", "Change copy"],
 "context": "Draft: /tavern/shared/T-3/post.md — 212 characters, image brief attached.",
 "urgency": "normal"}
```
````

The runtime validates it (question ≤ 300 characters, 2–6 options of ≤ 80, context ≤ 1 000, urgency `normal` or `high`), opens the card on the Front Desk, parks the task as `waiting_owner`, and ends the turn with *"Waiting on you: … (decision d-…)"*.

```bash
./bin/tavern decisions
./bin/tavern choose d-9f2c… "Hold"
```

Your choice comes back to that hireling as a command — `Decision: <question> → chosen: <option> (decision <id>)` — and it continues from there. **No answer means wait.** Nothing times out into a yes.

### Autonomy first (D11)

A loud hand only works if it is rare, so hirelings ask you only what is yours. Every `ask` names the gate `action` it is about and puts the hireling's recommendation first, with `why`:

```json
{"question": "Merge PR #7 into main?", "options": ["Merge it", "Not now"],
 "why": "CI green, reviewed", "action": "merge_pr", "context": "2 files."}
```

The card leads with *"Recommended: Merge it — CI green, reviewed"*. Before it opens, tavern-core decides (`POST /asks`):
- **Yours.** The action is Free, or Capped with uses left in its window, and the gate blocked nothing in that turn. No card opens. The hireling gets a note on its own inbox — decide it, do it, the Autonomy Log records it. This happens once per task and action. If it asks again, the card opens and says so.
- **Joined.** The task already has a card waiting on you. The new question joins it and goes back to the hireling with your answer. One card per task.
- **Ask.** The card opens.

When you answer, tavern-core counts whether you picked the recommended option. `GET /asks` shows, per hireling, asks per task and **could have decided alone** (the share of cards where you simply agreed). A high rate means it asks what it could have decided. If tavern-core cannot be reached, the card opens: when in doubt, you see it.

## Repeated failure escalates to you

If a hireling fails the same task several turns in a row (`runtime.escalate_after`, default 3), the runtime stops retrying and opens a decision by itself with four options: **Retry · Reassign · Drop · Change brief**. The task is `blocked` with reason `needs_human` until you answer.

## What enforces the limits

Be precise about what is a hard guarantee today and what relies on the model following its prompt.

| Guarantee | Enforced by | Status |
| --- | --- | --- |
| Only hosts in `network.allow` (+ model APIs) are reachable | egress proxy + internal Docker network | **Enforced** |
| Cloud metadata, localhost, finance/KYC/legal hosts can never be allowed | profile validation | **Enforced** |
| Hard deny list can't be made free or capped | profile validation | **Enforced** |
| Dangerous shell commands (sockets, privilege escalation, secret dumps) | Hermes approvals with deny-on-no-human | **Enforced** |
| Writes only under `/opt/data` and `/tavern/shared` | Hermes `HERMES_WRITE_SAFE_ROOT` | **Enforced** for file tools |
| Accounts: only listed toolkits; no deletions; no remote workbench | Composio session config | **Enforced** |
| Only the owner/planner assign work to others | tavern-core (handoffs start blocked) | **Enforced** |
| Only the quest source moves the quest number | tavern-core | **Enforced** |
| Secrets never leave the container in text | redactor on every outbound path | **Enforced** (pattern + known values) |
| Every tool call is checked before it runs | `hireling gate`, a Hermes `pre_tool_call` hook (D1) | **Enforced** (fail-closed) |
| The hireling asks before an owner-decided action | the gate blocks it and the `ask` block opens the card | **Enforced** — a blocked action does not run |
| Capped counts (e.g. 3 posts/day) | `POST /actions/check` counts each use per UTC day or ISO week (D2) | **Enforced** — past the cap the action is the owner's |
| Every free/capped action is logged | Autonomy Log in tavern-core (D4) | **Enforced** — an action that cannot be logged does not run |
| Off-peak routines | P3 bar work waits out DeepSeek's peak hours (D9) | **Enforced** — at most 4 h; P0–P2 never wait |
| Daily web-call budget | the gate hook counts `web_search`/`web_extract` against `web_calls_per_day` (D8) | **Enforced** — past it the call is blocked (`budget`) until 00:00 UTC; not a card for you |
| Daily token budget | recorded per turn; over `tokens_per_day` the runtime runs only P0/P1 (D7) | **Enforced** — your commands and answers, P0/P1 tasks and alerts run; other bar work waits, unacked, until 00:00 UTC. A P2/P3 turn that crosses the budget is cut between tool calls (at most one model call late) |

The defence against a model that ignores its prompt is that the dangerous capabilities are not there to use: no route to the host, no account toolkit, no delete tool, no money. Give a hireling a capability only when its profile and your comfort allow it.

## The gate on every tool call

Hermes runs `hireling gate` before every tool call (a `pre_tool_call` shell hook, fail-closed). The hook classifies the call:

- **local work** — reading, searching, files in the hireling's own volume, tests — runs at once, with no network call;
- **anything that acts on the outside world** — `git push`, opening a PR, an HTTP write, publishing a package, a Composio `MULTI_EXECUTE`, the browser, the desktop, cron, or a tool it does not know — is asked of tavern-core.

MCP tools are named `mcp__<server>__<tool>` (the single-underscore `mcp_<server>_<tool>` is accepted too). GBrain **reads** (`search`, `query`, `get_page`, `recall`, `context_pack` and the other read verbs) are local; any other GBrain tool (`put_page`, `capture`, `remember`, `forget`, or one a newer GBrain adds) goes to tavern-core like any outside action, because hireling writes go through ledger-writer. Composio tools follow the Composio rules above; tools of any other MCP server are external actions named after the tool.

The gate also refuses any call that touches Hermes' `config.yaml`, `.env`, the consent allowlist or the hireling prompt, so the model cannot switch the gate off.

## The policy gate

tavern-core answers *"may this hireling do this action on this target?"*:

```bash
curl -s -X POST http://127.0.0.1:8080/actions/check \
  -H "Authorization: Bearer $FRONTDESK_OWNER_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"agent":"social-media","action":"publish_owned_channel","target":"x.com","use":true}'
# {"level":"capped","reason":"profile: capped","limit":3,"period":"day","used":1,"counted":true}
```

Levels: `free`, `capped`, `owner`. Unknown agents and unknown actions are `owner`. Targets that look like finance, KYC or legal systems are always `owner` (the data rule).

With `"use": true` the check is the real thing, just before the action runs:

- `free` runs;
- `capped` counts the use in its window (UTC day or ISO week, D2) and runs while under the cap;
- at the cap, or for an `owner` action, it is blocked — and the block is remembered so it can ride on the hireling's next `ask` card.

**Your yes is the way through.** When you approve the card, each action it carried gets **one use within 24 hours** for that hireling only; the next check consumes it. Everything lives in tavern-core, so a hireling cannot write itself a permission: the card is tied to one open decision of that hireling before you answer, the Front Desk writes on the decision what a yes grants (`Choosing "Yes" grants dev one use of: merge_pr.`), and tavern-core grants only when the Front Desk shows that **you** chose that option (only the owner token can answer). An answered decision takes no new card, so an old yes is never reused. Read that line before you approve: it, not the hireling's question, says what the yes allows.

A cap of `0` means *none without you*, never "unlimited". A cap given in dollars (for example `ad_spend_usd`) cannot be counted from a tool call, so every spend is yours until the gate can read the amount.

## The Autonomy Log

Every action the gate saw is recorded (D4) with who, what, why (the model's own `thought` when the tool carries one, else the turn's topic), the outcome (`done` or `blocked`), the cap in use, and the turn and task it happened in. The log joins the turn's usage record to show the cost of that turn.

```bash
curl -s 'http://127.0.0.1:8080/autonomy?day=2026-11-05&agent=social-media'
# {"day":"2026-11-05","done":2,"blocked":1,"entries":[…]}
```

Read next: [The Ledger](ledger.md).
