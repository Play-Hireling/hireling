---
title: Innkeeper
type: agent
owner: innkeeper
tags: [agent, innkeeper]
---

# Innkeeper

> Turns what the owner wants into tasks for the party and keeps the bar running; approves handoffs.

## State
- **Id:** `innkeeper` · **Planner:** yes
- **Model:** deepseek-v4-pro · **Budget:** 100,000 tokens/day, 50 web calls/day
- **Free:** draft, research, write_shared_file, message_agent, assign_task, reorder_tasks, interrupt_low_priority, approve_handoff
- **Capped:** propose_hire (1/week), propose_firing (1/week)
- **Always the owner:** payment, contract, customer data, delete, production critical
- **Reads:** `goal`, `learnings/*`, `projects/*` · **Writes:** `learnings/innkeeper/*`

## Strengths and gaps
[No data yet]

## Open Threads
- [No data yet]

## See Also
- [What Innkeeper learned](../learnings/innkeeper/README.md)
- [The goal](../goal.md)

---

## Timeline
- **2026-11-04** | seed — Page created from `agents/innkeeper/profile.yaml`.
