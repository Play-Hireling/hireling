# tavern-core

The tavern's state and the owner's Front Desk, in one small Go service on Redis.

- **Front Desk** (`/api/*`, bearer tokens) — orders, turn reports, long-poll command queue, "Waiting on you" decisions, conversations, status. [API](../../docs/api/front-desk.md)
- **Tasks** — create, lease/renew/finish/release, handoffs with approval, failure counting and escalation, priority queue per hireling, lease sweeper. [API](../../docs/api/tavern-core.md#tasks)
- **Quest** — one shared goal; only the source moves the number.
- **Usage and budgets** — per hireling, per UTC day.
- **Policy gate** — free / capped / owner for an action and a target.

```bash
cd services/tavern-core
go test ./...
TAVERN_AGENTS_DIR=../../agents FRONTDESK_OWNER_TOKEN=dev FRONTDESK_AGENT_TOKEN=dev go run ./cmd/tavern-core
# no REDIS_URL = in-memory store, for development
```

**License:** [FSL-1.1-ALv2](LICENSE.md) — see [LICENSING.md](../../LICENSING.md). It imports the Apache-2.0 `runtime/profile` package.
