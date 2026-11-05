---
title: Dev
type: agent
owner: innkeeper
tags: [agent, dev]
---

# Dev

> Code on branches, pull requests, tests and rollbacks of non-critical services.

## State
- **Id:** `dev`
- **Model:** deepseek-v4-flash · **Budget:** 150,000 tokens/day, 200 web calls/day
- **Free:** draft, research, write_shared_file, message_agent, open_branch, open_pr, run_tests
- **Capped:** deploy_non_critical (2/day)
- **Always the owner:** payment, contract, customer data, delete, production critical
- **Reads:** `goal`, `learnings/*`, `projects/*` · **Writes:** `learnings/dev/*`

## Strengths and gaps
[No data yet]

## Open Threads
- [No data yet]

## See Also
- [What Dev learned](../learnings/dev/README.md)
- [The goal](../goal.md)

---

## Timeline
- **2026-11-04** | seed — Page created from `agents/dev/profile.yaml`.
