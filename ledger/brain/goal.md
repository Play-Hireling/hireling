---
title: The quest
type: goal
owner: owner
tags: [goal, quest]
---

# The quest

> The tavern's one shared goal. tavern-core holds the record (`GET /goal`); only the source posts the number, as one aggregate figure — no statements, no customer data. Every task declares how it moves it.

## State
- **Target:** as set in tavern-core (`PUT /goal`; seeded from `GOAL_*` on first boot)
- **Source:** one trusted reporter (for example a finance system), one aggregate number
- **Contribution kinds:** revenue, leads, posts, videos, support — closest to the goal first; at the same priority, the closer kind wins (B3)

## How work moves it
- **revenue:** money that lands in the goal's number; only the source reports it.
- **leads:** people who asked to talk or signed up — the step before revenue.
- **posts / videos:** published pieces on owned channels that bring leads.
- **support:** work that only enables other tasks (expected 0).

## Open Threads
- Source and refresh rate of the number.

## See Also
- [Agents](agents/README.md)
- [Projects](projects/README.md)
- [Decisions](decisions/README.md)

---

## Timeline
- **2026-11-04** | seed — Page created with the tavern.
