# Quickstart

A tavern with three hirelings — **Innkeeper**, **Scriptwriter** and **Dev** — on your machine, in about ten minutes.

## What you need

- **Docker** with Compose v2 (`docker compose version`).
- **~2 GB of free RAM** for the default party (each hireling is capped at 512 MB; idle they use ~150 MB). The `full` profile adds two more.
- A **DeepSeek API key** — [platform.deepseek.com](https://platform.deepseek.com/). A day of light work for three hirelings usually costs cents.
- Linux or macOS. On Windows, use WSL 2.

## 1. Get the code and the secrets

```bash
git clone https://github.com/Play-Hireling/hireling.git
cd hireling
./scripts/init-env.sh
```

`init-env.sh` creates `.env` from `.env.example` and fills random values for the Front Desk tokens (`FRONTDESK_OWNER_TOKEN`, `FRONTDESK_AGENT_TOKEN`) and the Ledger. Running it again keeps what is there.

Open `.env` and paste your key:

```dotenv
DEEPSEEK_API_KEY=sk-...
```

## 2. Start the tavern

```bash
docker compose up -d --build
docker compose ps -a
```

The first build pulls the Hermes Agent image and compiles three Go binaries (a few minutes). You should see `redis`, `tavern-core` and `egress` **healthy**, `tavern-init` **exited (0)** and three `agent-*` containers **running**.

Check one hireling from the inside (output abbreviated):

```bash
docker compose exec -u hermes agent-innkeeper hireling check
```

```text
profile  innkeeper (front desk agent innkeeper, model deepseek-v4-pro, server local/)
fallback off (no OPENROUTER_API_KEY or disabled in the profile)
usage    /opt/data/usage → tavern-core http://tavern-core:8080
planner  on, party [dev scriptwriter …]
quest    not set
bar      inbox:innkeeper (group hireling)
hands    off (COMPOSIO_API_KEY unset)
ledger   off (no GBrain token …)
frontdesk  ok (queue readable)
```

## 3. Give your first order

```bash
./bin/tavern order innkeeper "Plan a launch post and a 60-second video script for our new feature, due Friday"
```

The Innkeeper plans (usually under 30 seconds), creates one task per hireling, files a brief for each on the common table and messages the owners on the bar. Watch it:

```bash
./bin/tavern status        # each hireling's current turn and last answer
./bin/tavern tasks         # the task board
docker compose logs -f agent-innkeeper agent-scriptwriter
```

When a hireling needs you — publishing, spending, anything irreversible — it opens a decision:

```bash
./bin/tavern decisions
./bin/tavern choose d-4eeddbec1182 "Publish Friday 9:00"
```

The work itself lands on the common table (`/tavern/shared` in every container):

```bash
docker compose exec agent-scriptwriter ls -R /tavern/shared
```

Next: [Your first order, step by step](first-order.md).

## Optional profiles

| Profile | Adds | Turn on |
| --- | --- | --- |
| `full` | Social Media, Paid Traffic | `COMPOSE_PROFILES=full` in `.env` |
| `test` | The smoke-test hireling | `COMPOSE_PROFILES=test` |
| `brain` | The Tavern Ledger: Postgres + GBrain + ledger-writer | see [Turn on the Ledger](../guides/ledger.md) |

Combine them: `COMPOSE_PROFILES=full,brain`.

## Set a quest (optional, recommended)

Every task declares how it moves the tavern's single goal. Seed one on first boot in `.env`:

```dotenv
GOAL_TITLE=Monthly revenue
GOAL_TARGET=5000
GOAL_CURRENCY=USD
GOAL_KINDS=revenue,leads,posts,videos,support
```

…or set it any time (only the owner or the planner may define it; the currency is a three-letter code):

```bash
curl -X PUT -H 'X-Hireling: owner' -H 'Content-Type: application/json' \
  -d '{"title":"Monthly revenue","target":5000,"currency":"USD"}' http://127.0.0.1:8080/goal
./bin/tavern quest
```

See [Quest and tasks](../concepts/quest-and-tasks.md).

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `required variable DEEPSEEK_API_KEY is missing` | Paste the key in `.env`. |
| `run ./scripts/init-env.sh` | The Front Desk tokens are empty. |
| A hireling restarts in a loop with `s6-applyuidgid` | Your Docker drops capabilities differently; keep the `cap_add` list from `compose.yaml`. |
| `frontdesk HTTP 401` in `hireling check` | The hireling's token doesn't match tavern-core's: restart both after editing `.env` (`docker compose up -d`). |
| Turns take ~20 s instead of ~3 s | The Hermes gateway did not start on a fresh volume; `HERMES_GATEWAY_BOOTSTRAP_STATE=running` must be in the hireling's environment. |
| `egress deny <agent> <host>` in `docker compose logs egress` | Working as designed: add the host to the hireling's `network.allow` ([profile reference](../reference/profile.md#network)). |
| Ran `hireling once` and usage stopped saving | Use `docker compose exec -u hermes …`; running as root leaves root-owned files on the volume. |

Stop everything with `docker compose down` (volumes and memory survive). `docker compose down -v` deletes them.
