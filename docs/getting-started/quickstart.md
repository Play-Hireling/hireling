# Quickstart

Self-host the engine and the panel. In about ten minutes you get tavern-core, three hirelings (**Innkeeper**, **Scriptwriter** and **Dev**), the owner panel, and the orders API (`./bin/tavern` and `http://127.0.0.1:8080/api/`). The default commands below do not start the pixel-art tavern. That game is not in this repository. A clean run of those commands, from zero images, is recorded at 95 s in [the evidence log](../evidence/quickstart-run.md). The public demo is at [playhireling.com/play](https://playhireling.com/play/). The optional [`game` profile](#optional-pixel-art-client) serves a client you build yourself at `/tavern/`.

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

`init-env.sh` creates `.env` from `.env.example` and fills random values for the Front Desk tokens (`FRONTDESK_OWNER_TOKEN`, `FRONTDESK_AGENT_TOKEN`) and the Ledger. Running it again keeps what is there. `.env` is git-ignored. Never commit it.

Open `.env` and set your key. `$EDITOR` is often unset on a fresh machine, and a bare `$EDITOR .env` then tries to execute `.env`. Use:

```bash
${EDITOR:-nano} .env
```

If `nano` is not installed, run `vi .env` instead. The line you want is:

```dotenv
DEEPSEEK_API_KEY=sk-paste-your-key-here
```

That value is a placeholder. Replace it with your own key. An empty value makes Compose refuse to start.

> **A real provider key is required for an order to leave `start`.** `sk-paste-your-key-here`, `sk-placeholder-…`, or any other placeholder lets the box boot and the checks below pass, but `./bin/tavern status` stays in phase `start`: the hireling retries the provider and does not plan. `hireling check` prints `provider key missing/invalid` when the key is empty, a placeholder, or the provider answers 401 or 403. That line does not include the key. `hireling check --offline` skips the probe.

## 2. Check the host firewall, then start the engine

```bash
./scripts/preflight.sh
docker compose up -d --build
docker compose ps -a
```

`preflight.sh` does not change the firewall, does not run `sudo`, and does not download anything. Exit 0 means this host is not in the stale-firewall case: continue. Exit 2 means it could not read `iptables-legacy` (on Ubuntu that read needs root). It prints the diagnostic commands and the fix, and it has not passed. Run `sudo ./scripts/preflight.sh` so the same script can see the table. Exit 1 means it read the table and found the stale case below.

On Ubuntu with Docker Engine from Docker's apt repository, `iptables-legacy` can have `FORWARD` policy `DROP` beside the nft backend Docker programs. `compose.yaml` pins the tavern bridge to `br-hireling` (the only network where containers talk to each other). Packets on that bridge miss the legacy `docker0` rules and are dropped, so tavern-core logs `dial tcp …:6379: i/o timeout` and the first `up` exits 1. `br0`, `br-lan`, and `br-wan` are host bridges and are not part of the fix.

If the script exits 1, it prints the current legacy chain and this command. Run it, then run `sudo ./scripts/preflight.sh` again. Do not start Compose until the script exits 0.

```bash
sudo iptables-legacy -C FORWARD -i br-hireling -o br-hireling -j ACCEPT 2>/dev/null || sudo iptables-legacy -I FORWARD 1 -i br-hireling -o br-hireling -j ACCEPT
```

That does not turn the firewall off. Docker's nftables rules stay in place and still filter forwarded traffic. The command adds one `ACCEPT` on the unused legacy table, only for `-i br-hireling -o br-hireling`, and only if that rule is missing. It does not flush rules, does not change the `FORWARD` policy, does not touch `INPUT` or `OUTPUT`, does not touch `iptables-nft`, and does not stop ufw or firewalld.

The first build pulls the Hermes Agent image and compiles three Go binaries (a few minutes). You should see `redis`, `tavern-core` and `egress` **healthy**, `tavern-init` **exited (0)** and three `agent-*` containers **running**.

The engine is up when both of these succeed. They do not call DeepSeek.

```bash
curl -fsS http://127.0.0.1:8080/healthz
# {"agents":["dev","innkeeper","paid-traffic","scriptwriter","social-media","test"],"status":"ok"}

curl -fsS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/
# 200 — the owner panel. Open http://127.0.0.1:8080/ in a browser on this machine.
# Party, decisions and the quest bar. Not the pixel-art tavern.
```

`./bin/tavern health` prints the same JSON. The port is bound to `127.0.0.1` only.

GBrain does not start with this command. The Ledger is the optional `brain` profile ([Turn on the Ledger](../guides/ledger.md)) and needs a git repository of your own. `hireling check` reports `ledger off` until that profile is on. That is expected here.

Check one hireling from the inside (output abbreviated):

```bash
docker compose exec -u hermes agent-innkeeper hireling check
```

```text
profile  innkeeper (front desk agent innkeeper, model deepseek-v4-pro, server local/)
fallback off (no OPENROUTER_API_KEY or disabled in the profile)
usage    /opt/data/usage → tavern-core http://tavern-core:8080
planner  on, party [dev innkeeper paid-traffic scriptwriter social-media test]
quest    not set (PUT /goal on tavern-core, or GOAL_* env)
bar      inbox:innkeeper (group hireling)
hands    off (COMPOSIO_API_KEY unset)
ledger   off (no GBrain token at /run/gbrain/innkeeper.token — the gbrain service mints it when the brain profile is on)
frontdesk  ok (queue readable)
```

## 3. Give your first order

```bash
./bin/tavern order innkeeper "Plan a launch post and a 60-second video script for our new feature, due Friday"
```

The desk accepts the order at once (`{"command":{…}}`). With a real `DEEPSEEK_API_KEY`, `./bin/tavern status` moves on from phase `start` and the Innkeeper plans (usually under 30 seconds), creates one task per hireling, files a brief for each on the common table and messages the owners on the bar. **Without a real key the status stays in phase `start`** while the provider retries; the tavern stays up, and the plan does not run. Watch it:

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
curl -sS -X PUT \
  -H "Authorization: Bearer $FRONTDESK_OWNER_TOKEN" \
  -H 'X-Hireling: owner' \
  -H 'Content-Type: application/json' \
  -d '{"title":"Monthly revenue","target":5000,"currency":"USD"}' \
  http://127.0.0.1:8080/goal
./bin/tavern quest
```

The bearer is required: without it this call is 401. `X-Hireling: owner` still names who is defining the quest (the owner, or a planner's id); it does not replace the bearer. The comparison is exact: `Owner` and `OWNER` are 403. A bearer with no `X-Hireling: owner` header is also 403. Do not put the token in the query string.

See [Quest and tasks](../concepts/quest-and-tasks.md).

## Optional: pixel-art client

The default `docker compose up` does not start the client. Profile `game` serves a data-only image at `http://127.0.0.1:8080/tavern/` on the same origin as the API. No digest is published. Compose requires `HIRELING_CLIENT_IMAGE` (the whole file is interpolated). Without it, `docker compose --profile game` stops and names the variable. The local tag is `hireling/client:local`, from `scripts/pack-client.sh`. Publishing that image waits on the license holder, who is still [PENDENTE TITULAR]. Do not push one from this checkout.

Build the client from the `main` branch of `Play-Hireling/playhireling` (`npm run build:selfhost`), then pack it locally. Nothing is published:

```bash
scripts/pack-client.sh /path/to/playhireling/dist-selfhost
# writes hireling/client:local and does not push
```

```bash
HIRELING_CLIENT_IMAGE=hireling/client:local docker compose --profile game up -d --build
```

`docker buildx` is the recommended builder for `hireling/runtime:local`. Only `agent-innkeeper` builds that image. Open `http://127.0.0.1:8080/tavern/`, paste `FRONTDESK_OWNER_TOKEN` (it stays in `sessionStorage`; there is no remember option) and enter the tavern.

Give the order in the game at `/tavern/` when that screen accepts one. If it does not, use the CLI, which is the same order:

```bash
./bin/tavern order innkeeper "Plan a launch post for our new feature, due Friday"
```

The owner panel at `/` does not grow an order button for this. Answer a decision in the game, or with `./bin/tavern choose`.

If you already had a stack from before the bridge was named `br-hireling`, recreate the tavern network (`docker compose down`, and `docker network rm hireling_tavern` if it remains) so the name applies, then `docker compose up -d`.

The client image's license is [LICENSE-CLIENT.md](../../LICENSE-CLIENT.md). The public notice is [NOTICE](../../NOTICE) and [THIRD-PARTY.md](../../THIRD-PARTY.md). `docs/provenance` is not in the image and not in this repository.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `.env: command not found` right after `init-env.sh` | `$EDITOR` is unset, so the shell tried to run `.env`. Use `${EDITOR:-nano} .env` or `vi .env`. |
| `required variable DEEPSEEK_API_KEY is missing` | Set `DEEPSEEK_API_KEY` in `.env`. A placeholder boots the box; a real key is required for the turn to leave phase `start`. |
| `run ./scripts/init-env.sh` | The Front Desk tokens are empty. |
| `./scripts/preflight.sh` exits 2 | It could not read `iptables-legacy` and did not run `sudo`. Run `sudo ./scripts/preflight.sh`. Exit 2 is not a pass. |
| `./scripts/preflight.sh` exits 1, or `tavern-core` stays **unhealthy** and its logs say `dial tcp …:6379: i/o timeout`, while `docker compose exec redis redis-cli ping` prints `PONG` | `iptables-legacy` has `FORWARD` policy `DROP` while Docker programs nftables. Run the `sudo iptables-legacy -C FORWARD -i br-hireling -o br-hireling` command in the step above (the same one `preflight.sh` prints), then `sudo ./scripts/preflight.sh` again (it must exit 0) and `docker compose up -d`. That one rule does not flush the table, does not change the policy, and does not turn the firewall off. |
| `frontdesk: FRONTDESK_URL must be https://` in `docker compose logs agent-innkeeper` | The runtime refuses plain `http://` except to `127.0.0.1` or `::1`. Compose sets `FRONTDESK_ALLOW_HTTP=1` for `http://tavern-core:8080`. Keep that line; do not point a hireling at a public desk over `http://`. |
| A hireling restarts in a loop with `s6-applyuidgid` | Your Docker drops capabilities differently; keep the `cap_add` list from `compose.yaml`. |
| `frontdesk HTTP 401` in `hireling check` | The hireling's token doesn't match tavern-core's: restart both after editing `.env` (`docker compose up -d`). |
| Turns take ~20 s instead of ~3 s | The Hermes gateway did not start on a fresh volume; `HERMES_GATEWAY_BOOTSTRAP_STATE=running` must be in the hireling's environment. |
| `egress deny <agent> <host>` in `docker compose logs egress` | Working as designed: add the host to the hireling's `network.allow` ([profile reference](../reference/profile.md#network)). |
| Ran `hireling once` and usage stopped saving | Use `docker compose exec -u hermes …`; running as root leaves root-owned files on the volume. |
| Containers cannot reach each other after an older checkout | Recreate the tavern network so the bridge name `br-hireling` applies: `docker compose down`, then `docker compose up -d`. If the old network is still there, `docker network rm hireling_tavern` before the next `up`. |
| `hireling check` should not call the provider | `docker compose exec -u hermes agent-innkeeper hireling check --offline` skips the key probe. |

Stop everything with `docker compose down` (volumes and memory survive). `docker compose down -v` deletes them.
