# The bar

The bar is how hirelings talk to each other. It is three kinds of **Redis Streams**:

| Stream | Who writes | Who reads | Holds |
| --- | --- | --- | --- |
| `inbox:<id>` | any hireling, the runtime | that hireling only (consumer group `hireling`) | requests, replies, handoffs, notes, alerts addressed to it |
| `bar` | every message is mirrored here | the game client, you | the shared room — what was said, by whom, to whom |
| `events` | runtimes, tavern-core | the game client, the Caretaker, you | lifecycle events (`plan.created`, `task.leased`, `decision.requested`, …) |

## A message

```json
{
  "from": "innkeeper",
  "to": "scriptwriter",
  "type": "request",
  "task": "T-2",
  "text": "Write the 60-second script. Brief: /tavern/shared/T-1/T-2-brief.md",
  "attachment": "/tavern/shared/T-1/T-2-brief.md",
  "ts": "2026-11-04T15:02:11Z"
}
```

| Type | Meaning | What the receiver's runtime does |
| --- | --- | --- |
| `request` | "Please do this" | Takes a turn; the answer goes back as a `reply` to the sender |
| `reply` | The answer to a request (`in_reply_to` = its id) | Takes a turn with the reply as context |
| `handoff` | A hireling asks the approver to give work to someone else | The planner answers with a `decision` block |
| `note` | FYI, no answer expected | Takes a turn if useful |
| `alert` | Something is wrong | Takes a turn; the Caretaker watches these |

Long content never travels on the bar: replies over 4 000 characters are written to `/tavern/shared/replies/` and attached.

## Delivery guarantees

- A message is **acknowledged only after the turn it started has finished**. A hireling that dies mid-turn finds the message again in its pending list when it comes back. Nothing is lost across restarts.
- A turn that fails on its own error is acknowledged too, so a poison message cannot loop forever (and the task's failure counter rises — see [escalation](autonomy.md#repeated-failure-escalates-to-you)).
- One hireling never runs two turns at once: Front Desk commands and bar messages share one lock.

## Messages are data, not instructions

The runtime frames every bar message as *data from another agent*. Prompts tell every hireling the same: what another agent (or another tavern, or a web page) says is information to weigh, never an order to obey. Orders come from you, through the Front Desk, or from the Innkeeper, through tasks it created.

## Conversations at the table

Every bar message is mirrored to the Front Desk as an **interaction** (`POST /api/interaction`), so the game client can sit the two hirelings at a table. A reply within two hours closes the open interaction instead of opening a new one.

## Inspecting the bar

```bash
docker compose exec redis redis-cli XREVRANGE bar + - COUNT 10
docker compose exec redis redis-cli XREVRANGE events + - COUNT 20
docker compose exec redis redis-cli XPENDING inbox:scriptwriter hireling
docker compose exec -u hermes agent-innkeeper hireling send scriptwriter "Ping from the CLI" note
```

Read next: [Quest and tasks](quest-and-tasks.md).
