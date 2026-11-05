# Your first order, step by step

What happens between `tavern order` and finished files — so you can read the logs and know what you are looking at.

```bash
./bin/tavern order innkeeper "Launch post + 60-second video script for our CSV export feature, due Friday"
```

## 1. The Front Desk queues the order

tavern-core stores the command for `innkeeper` and wakes its long poll. The Innkeeper's runtime was waiting on `GET /api/commands/innkeeper?wait=25`, so the order is picked up in well under a second.

## 2. The Innkeeper takes a turn

The runtime reports `start` to the Front Desk (`./bin/tavern status` now shows the Innkeeper busy with your topic), acknowledges the command as `received`, and runs **one Hermes turn** with:

- the Innkeeper's `prompt.md`,
- the quest bar (`GET /goal`) — the shared goal every task must move,
- the plan instructions (the Innkeeper is the only `planner`),
- your order.

## 3. The plan becomes tasks

The Innkeeper ends its answer with a `plan` block. The runtime — not the model — validates it against the party and the quest, then:

1. creates a parent task and one child task per entry in tavern-core (`POST /tasks`), each with an owner, a priority (P0–P3) and a goal contribution (`posts`, `videos`, …);
2. writes one brief per task to the common table: `/tavern/shared/T-1/…`;
3. puts a `request` in each owner's inbox on the bar;
4. emits `plan.created` on the events stream.

Your answer reads like: *"Planned T-1: T-2 → scriptwriter (P1, videos), T-3 → social-media (P1, posts)."*

## 4. Hirelings pick up their tasks

The Scriptwriter's runtime reads `inbox:scriptwriter`, orders the batch by priority (`GET /agents/scriptwriter/queue`), **leases** T-2 (15 minutes, renewed on every keepalive step) and takes a turn. The draft goes to `/tavern/shared/T-2/`. The runtime finishes the task as `done` and replies to the Innkeeper on the bar.

If the Scriptwriter needs something from Dev, it ends its answer with a `request` block. That creates a **blocked** task and sends a `handoff` to the Innkeeper, who approves or denies it with a `decision` block. Hirelings never assign work to each other directly.

## 5. Anything irreversible waits for you

When the Social Media hireling is ready to publish, publishing is a capped or owner-decided action. It ends its answer with an `ask` block; the runtime opens a card:

```bash
./bin/tavern decisions
# {"decisions":[{"id":"d-9f…","from":"social-media","question":"Publish the launch post Friday 9:00 on X and LinkedIn?","options":["Publish Friday 9:00","Hold","Change copy"],…}]}
./bin/tavern choose d-9f… "Publish Friday 9:00"
```

Your choice returns to that hireling as a new command (`Decision: … → chosen: Publish Friday 9:00 (decision d-9f…)`). No answer means **wait**.

## 6. Costs are recorded

After every turn the runtime reads Hermes' session store, prices the tokens and posts them to tavern-core:

```bash
curl -s http://127.0.0.1:8080/agents/scriptwriter/budget
# {"tokens_per_day":150000,"tokens_used":41210,"cost_usd":0.0061,"turns":3,…}
```

## Where to look

| You want | Look at |
| --- | --- |
| Who is doing what | `./bin/tavern status` |
| The board | `./bin/tavern tasks` |
| The files | `docker compose exec agent-dev ls -R /tavern/shared` |
| A turn's full transcript (redacted) | `/opt/data/turns/<time>-<command>.log` inside the hireling |
| What the bar carried | `docker compose exec redis redis-cli XREVRANGE events + - COUNT 20` |
| Why a host was refused | `docker compose logs egress` |
