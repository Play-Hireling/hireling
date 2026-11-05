# Answer blocks

A hireling acts on the tavern by ending its answer with **one fenced block**. The runtime parses it, validates it and does the work; the model never calls tavern-core, Redis or the Front Desk itself. A block that fails validation creates nothing, and the answer says why.

| Block | Who | Creates |
| --- | --- | --- |
| `plan` | the planner | a parent task + one task per entry, briefs on the common table, requests on the bar |
| `request` | any non-planner | a **blocked** task + a handoff to the planner |
| `decision` | the planner | approves or denies a requested task |
| `ask` | anyone | a "Waiting on you" decision for the owner |
| `ledger <path>` | anyone with `ledger.write` | a Ledger page committed as the hireling (max 3 per turn) |

## `plan`

````markdown
```plan
{"summary": "Launch content for CSV export",
 "tasks": [
  {"title": "Write a 60-second launch video script", "owner": "scriptwriter", "priority": "P1",
   "goal": {"kind": "videos", "expected": 1, "note": "launch video"},
   "brief": "Audience: existing users. Hook in 3 s. Due Thursday. Inputs: /tavern/shared/T-1/spec.md"},
  {"title": "Write the launch post for X and LinkedIn", "owner": "social-media", "priority": "P1",
   "goal": {"kind": "posts", "expected": 2, "note": "two channels"},
   "brief": "Use the script's hook. Publishing needs the owner."}
 ]}
```
````

Rules: owners must be hirelings of this tavern; `goal.kind` is required and must be one of the quest's kinds (`support` with `expected: 0` for enabling work); at most 12 tasks.

## `request`

````markdown
```request
{"to": "dev", "title": "Export a sample CSV from staging", "priority": "P2",
 "brief": "Need a real-looking file for the video by Wednesday. Put it in /tavern/shared/T-2/.",
 "goal": {"kind": "support", "expected": 0, "note": "enables the launch video"}}
```
````

## `decision`

````markdown
```decision
{"task": "T-7", "approve": true, "note": "Yes — staging only, no customer rows."}
```
````

## `ask`

````markdown
```ask
{"question": "Publish the launch post Friday 9:00 on X and LinkedIn?",
 "options": ["Publish Friday 9:00", "Hold", "Change copy"],
 "context": "Draft: /tavern/shared/T-3/post.md",
 "urgency": "normal"}
```
````

Question ≤ 300 characters, 2–6 options of ≤ 80, context ≤ 1 000, urgency `normal` or `high`.

## `ledger`

Four backticks so the page may contain code fences; the path follows the tag:

`````markdown
````ledger learnings/dev/retry-registry-502.md
---
title: Retry the registry on 502
type: learning
owner: dev
tags: [learning, ci]
---

# Retry the registry on 502

> The package registry returns 502 under load; one retry after 10 s works.

## State
- **Seen:** 3 times on CI.
````
`````

The page must follow its template in `ledger/templates/` and the path must be inside the hireling's `ledger.write` areas.
