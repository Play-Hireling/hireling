# The tavern

A **tavern** is one team of hirelings working for one owner toward one goal, on one machine (or one Compose project). Everything in Hireling is named after a tavern because the product is also a game — the [pixel-art client](https://playhireling.com/play/) draws exactly these pieces — but each name maps to something concrete.

| In the tavern | Concretely | Where |
| --- | --- | --- |
| **The owner** (Executive Director) | You. The only human in the loop | `bin/tavern`, the Front Desk API |
| **Hirelings** | AI agents, one container each | `agents/<id>/`, `agent-<id>` services |
| **The Innkeeper** | The one hireling that plans and approves | `planner: true` in its profile |
| **The Front Desk** | Where you give orders and answer decisions; where hirelings report turns | `tavern-core` `/api/*` |
| **The bar** | Messages between hirelings and the event log | Redis Streams `inbox:<id>`, `bar`, `events` |
| **The common table** | Files everyone can read and write | volume `tavern-shared` → `/tavern/shared` |
| **A hireling's desk drawer** | Private memory and transcripts | volume `tavern-<id>` → `/opt/data` |
| **The quest** | The single shared business goal | `GET /goal` |
| **The task board** | Tasks with owners, priorities, leases | `GET /tasks` |
| **Mana** | Each hireling's daily token budget | `budget.tokens_per_day` |
| **Waiting on you** | Decisions only the owner can make | `GET /api/decisions` |
| **The Tavern Ledger** | Shared long-term memory | GBrain + a git repo |
| **The door** | The only way out to the internet | `egress` proxy |

## Principles

1. **One goal.** Every task declares how it moves the quest. Work that moves nothing is visible as such.
2. **One manager.** Only the Innkeeper (and you) set priorities and assign work. Hirelings ask; the Innkeeper decides.
3. **Isolation by default.** A hireling cannot read another's drawer, cannot reach a host its profile does not list, and holds only its own secrets.
4. **Limits are enforced by code, not by the prompt.** The runtime and tavern-core refuse what a profile does not allow, whatever the model writes.
5. **Silence means wait.** An unanswered decision never turns into a "yes".
6. **Everything is attributable.** Every message, task change, decision and Ledger page carries the hireling that made it.
7. **Data outside the tavern stays outside.** Customer, KYC, financial and legal data never enter a hireling, a prompt, a log or the Ledger.

## Lifecycle of a turn

```text
order / bar message ──► lock (one turn at a time per hireling)
   ──► Front Desk: start ──► Hermes turn (prompt + quest + context)
   ──► answer blocks applied by the runtime (plan / request / decision / ask / ledger)
   ──► usage recorded ──► Front Desk: end (+ ack done | rejected)
```

While the model writes, the runtime reports the answer text so far as a `step`.
The owner panel follows it live with `GET /api/turn/stream` (Server-Sent Events),
so the first words show up before the turn ends; the plain `POST /api/turn` JSON
and `GET /api/status` polling stay exactly as they were.

Read next: [Hirelings](hirelings.md).
