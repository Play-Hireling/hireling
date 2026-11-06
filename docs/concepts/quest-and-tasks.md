# Quest and tasks

## The quest

A tavern has **one** shared goal: the quest. It is a title, a target, a current value, a unit (three-letter code: `USD`, `EUR`, …), a period and the **contribution kinds** a task may declare.

```bash
./bin/tavern quest
```

```json
{"title":"Monthly revenue","target":5000,"current":1250,"currency":"USD","source":"owner",
 "kinds":["revenue","leads","posts","videos","support"],"percent":25,
 "bar":"Quest: Monthly revenue — 25.0% [██░░░░░░░░] 1,250/5,000 USD",
 "expected_open":{"posts":3},"expected_done":{"videos":1}}
```

- **Who defines it:** the owner or the planner (`PUT /goal`). Seed it on first boot with `GOAL_*` variables.
- **Who moves the number:** only the quest's `source` (default `owner`) with `POST /goal/progress`. Hirelings can never change it — not even the Innkeeper.
- **What hirelings see:** every turn opens with the quest bar, so plans aim at it.
- **One aggregate number only.** Never statements, invoices or customer records.

## Tasks

```json
{"id":"T-2","title":"60-second script for CSV export","owner":"scriptwriter","requested_by":"innkeeper",
 "priority":"P1","status":"leased","parent":"T-1",
 "goal_contribution":{"kind":"videos","expected":1,"note":"launch video"},
 "lease_expires_at":"2026-11-04T15:17:11Z","brief":"…"}
```

| Field | Rule |
| --- | --- |
| `priority` | `P0` (now) … `P3` (someday). Default `P3`. |
| `goal_contribution.kind` | Required. One of the quest's kinds. |
| `goal_contribution.expected` | ≥ 0 — how much of the kind this task should produce. `support` work declares 0. |
| `status` | `queued` → `leased` → `done` / `failed`; or `blocked` (waiting for approval or for you), `waiting_owner`, `cancelled` |

### Priorities

Each batch of inbox messages is handled in the order of `GET /agents/<id>/queue`: P0 > P1 > P2 > P3, ties broken by the contribution that is closer to the quest (revenue before leads before posts…). A message without a task counts as P2.

### Leases

A hireling **leases** a task before working on it: 15 minutes, renewed on every keepalive step (a third of the turn TTL). When the turn ends the runtime finishes the task as `done` or `failed`, or releases it as `waiting_owner` when the answer opened a decision. An expired lease goes back to the queue by itself (tavern-core sweeps every 30 s) and is logged as `expired`.

### Interrupts

P2 and P3 turns are interruptible. Your order (always P0) interrupts them; so does a P0 task you or the Innkeeper created. The partial transcript is saved to `/tavern/shared/<task>/progress-<time>.md`, the task goes back to the queue, and the hireling later continues from that file. P0 and P1 turns are never interrupted.

### Handoffs

A worker that needs another hireling ends its answer with a `request` block. tavern-core creates the task **blocked** (the requester is not an approver) and the runtime sends a `handoff` to the Innkeeper. The Innkeeper approves (→ `queued`, owner notified) or denies (→ `cancelled`, requester told why). Both land on the timeline with who decided.

### The daily plan

Every morning (the planner's `daily_plan.at`) tavern-core puts the day's plan on the quest board: each hireling's open tasks in the order they will be taken, what waits on you, and the handoffs to approve. The Innkeeper then takes one short turn on it and leaves a note of at most five lines on the board: what matters most today, what is at risk, and what is stuck. If it needs new work, it adds tasks; it never edits existing ones. See [`daily_plan`](../reference/profile.md).

Read next: [Autonomy and decisions](autonomy.md).
