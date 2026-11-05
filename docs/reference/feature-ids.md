# Feature ids

Code comments and docs refer to features by short ids (`A5`, `B11`, …). This is the map. **Status** is for the engine in this repository.

## A — The hireling

| Id | Feature | Status |
| --- | --- | --- |
| A1 | One Hermes container per hireling; profile + prompt define it | ✅ |
| A2 | Model routing (V4-Flash / V4-Pro) and usage per call | ✅ |
| A3 | Turn reporting (`start` / `step` / `end`) to the Front Desk | ✅ |
| A4 | Command queue intake and acknowledgements | ✅ |
| A5 | Inbox loop on the bar; nothing lost across restarts | ✅ |
| A6 | Private memory on a named volume | ✅ |
| A7 | Common table `/tavern/shared` | ✅ |
| A8 | Sandbox: no socket, no privileges, approvals on, egress allowlist | ✅ |
| A9 | Secret redaction on every outbound path | ✅ |
| A10 | Persona and voice per hireling | 🗺 roadmap |
| A11 | A screen per hireling (computer use) | 🗺 roadmap |
| A12 | Hands layer 1: accounts through Composio | ✅ |
| A13 | Chat that feels instant: long poll, warm turns, light chat mode | 🟡 pickup + warm turns done; streaming next |

## B — Work

| Id | Feature | Status |
| --- | --- | --- |
| B1 | Request intake: an order becomes a plan in under a minute | ✅ |
| B2 | Planning and assignment by the planner | ✅ |
| B3 | Priorities P0–P3, ties by goal contribution | ✅ |
| B4 | Task leases with renewal and expiry sweep | ✅ |
| B5 | Interrupts with saved progress | ✅ |
| B6 | Handoffs approved by the planner | ✅ |
| B7 | Conflict resolution on shared resources | 🗺 |
| B8 | Hireling-to-hireling conversations mirrored to the Front Desk | ✅ |
| B9 | Help requests that don't jump the queue | 🗺 |
| B10 | Daily plan | 🗺 |
| B11 | Repeated failure escalates to the owner | ✅ |

## C — The quest

| Id | Feature | Status |
| --- | --- | --- |
| C1 | One quest record, quest bar in every turn | ✅ |
| C2 | Quest source reports one aggregate number | ✅ (`POST /goal/progress`) |
| C3 | Expected contribution declared by every task | ✅ |
| C4–C8 | Coins, budget follows results, levels, promotion, firing | 🗺 |

## D — Autonomy and limits

| Id | Feature | Status |
| --- | --- | --- |
| D1 | Policy gate: free / capped / owner, checked on every tool call | ✅ |
| D2 | Capped limits counted per day or week | ✅ |
| D3 | "Waiting on you" decisions | ✅ |
| D4 | Autonomy log | ✅ |
| D5 | Hard deny list | ✅ |
| D6 | Data rule (no finance/KYC/legal hosts, toolkits or Ledger pages) | ✅ |
| D7 | Token budget enforced (over budget → only P0/P1) | ✅ |
| D8 | Web-call budget | ✅ |
| D9 | Off-peak scheduling | ✅ |
| D11 | Autonomy first: ask only what is the owner's | ✅ |

## E — The Ledger

| Id | Feature | Status |
| --- | --- | --- |
| E1 | GBrain + Postgres, git repo as source of truth | ✅ |
| E2 | Per-hireling read-only access over MCP | ✅ |
| E3 | Every change is a commit authored by the hireling | ✅ |
| E4 | Page templates and grammar | ✅ |
| E5 | Learning promotion | 🗺 |
| E6 | Revert from history | ✅ (git) |
| E7 | Opens as an Obsidian vault with valid links | ✅ |
| E9 | Tavern night (overnight review) | 🗺 |

## F — The Caretaker (maintenance hireling)

F1 cron hygiene · F2 context pruning · F3 token-waste detection · F4 health watch · F5 night schedule · F6 daily report — all 🗺.

## G — Operations

| Id | Feature | Status |
| --- | --- | --- |
| G2 | One-file stack (`compose.yaml`) | ✅ |
| G3 | Redis with AOF persistence, internal only | ✅ |
| G4 | Per-hireling secrets as service environment | ✅ |
| G6 | Backups | 🗺 (see [deploy guide](../guides/deploy.md#backups)) |
| G7 | Cost panel | 🟡 API only |
| G8 | Multi-server taverns | 🗺 |
| G9 | Server flags (server + region on every turn) | ✅ reported |
| G10 | Rollback by image tag | ✅ `HIRELING_TAG` |

✅ shipped · 🟡 partly · 🗺 [roadmap](../roadmap.md)
