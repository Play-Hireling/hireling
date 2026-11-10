# Quickstart clean run

Record of running the self-host quickstart from the README on a Linux machine with Docker and no prior Hireling state. The complete log of the successful run is [quickstart-run.log](quickstart-run.log). No real API key, token or password is in that log. `.env` was created by `./scripts/init-env.sh` during the run and was not committed.

## Machine

- Linux 6.12, x86_64, Docker Engine 29.1.3, Compose v2.40.3.
- Before the successful run: no `.env`, no `hireling/*` images, no Hireling volumes, and the Compose build cache removed (`docker builder prune -af`).
- Upstream images already on disk were reused (golang, redis, busybox, and Hermes layers still in the containerd store). The first attempt, below, started from zero local images and did pull them.

The key was a placeholder written by a non-interactive stand-in for `${EDITOR:-nano} .env`. A real DeepSeek key was not used. That is enough to boot the tavern and accept an order; the model turn itself cannot succeed.

## Successful run

Wall clock from `./scripts/init-env.sh` through the post-order status: **79s** (2026-11-10T18:05:32Z to 2026-11-10T18:06:51Z).

| Step | Result | Time |
| --- | --- | --- |
| `./scripts/init-env.sh` | exit 0, tokens written, values not printed | 0s |
| `${EDITOR:-nano} .env` | exit 0, placeholder `DEEPSEEK_API_KEY` | 0s |
| `docker compose up -d --build` | exit 0 | 70s |
| `docker compose ps -a` | redis, tavern-core, egress healthy; tavern-init exited 0; three agents up | 0s |
| `curl http://127.0.0.1:8080/healthz` | HTTP 200 `{"status":"ok",...}` | 0s |
| `curl http://127.0.0.1:8080/` | HTTP 200, title `Tavern`, 10456 bytes | 0s |
| `./bin/tavern health` | same JSON, exit 0 | 0s |
| `./bin/tavern status` | six agents, no decisions, exit 0 | 1s |
| `./bin/tavern decisions` | `{"decisions":[]}`, exit 0 | 0s |
| `docker compose exec -u hermes agent-innkeeper hireling check` | exit 0, `frontdesk  ok (queue readable)`, ledger off (brain profile not on) | 0s |
| `./bin/tavern order innkeeper "…"` | exit 0, command `c-803142a1e04e` accepted | 0s |
| `./bin/tavern status` again | Innkeeper turn phase `start` on that order | 0s |

Compose printed one warning and continued: `Docker Compose is configured to build using Bake, but buildx isn't installed`. The build used the default docker driver.

Agent logs after the order: **0** lines containing `FRONTDESK_URL must be https://`, **0** `frontdesk:` errors, and the command was picked up.

## Failures on the way

### 1. `$EDITOR .env` with `EDITOR` unset

- Step: the previous README line `$EDITOR .env`, run in `bash -lc`.
- Result: exit 127, `bash: line 1: .env: command not found`, about 1s.
- Cause: an unset `EDITOR` expands to nothing, so the shell executes `.env`.
- Fix: README and the quickstart now use `${EDITOR:-nano} .env`, and the troubleshooting table says to use `vi .env` when nano is missing.

### 2. `docker compose up` left tavern-core unhealthy

- Step: first `docker compose up -d --build` on this machine, from zero images. The image build itself finished.
- Result: exit 1 after 141s. `dependency failed to start: container hireling-tavern-core-1 is unhealthy`. redis and egress were healthy, tavern-init had exited 0, and the three agents stayed in `Created`. `hireling check` then failed in 0s with `service "agent-innkeeper" is not running`.
- Cause: not the Compose file. This machine had both iptables backends. `iptables-nft` accepted traffic on the Compose bridge, and `iptables-legacy` had `FORWARD` policy `DROP` with an accept only for `docker0`. Container-to-container packets on the tavern network were dropped, so tavern-core logged `dial tcp 172.18.0.3:6379: i/o timeout` while `redis-cli ping` inside the redis container returned `PONG`. Host-to-container still worked, which is why the published port reset connections instead of refusing them.
- Fix on this machine only: flush the stale `iptables-legacy` filter and nat tables so the nft rules are the only filter. A normal Docker Engine install has one backend; the quickstart troubleshooting table describes the symptom. No Compose change.

After that flush, a later curl got HTTP 200 from `/healthz` (the wait loop in the first attempt reached it on try 18, 101s after the failed up, once the daemon could dial Redis). The agents were still not started, because Compose had already given up.

### 3. Hirelings refused the Front Desk URL

- Step: `docker compose up -d` after the firewall fix, then `docker compose logs agent-innkeeper`.
- Result: containers stayed up, and every poll logged `frontdesk: FRONTDESK_URL must be https://`. `hireling check` could not report `frontdesk ok`.
- Cause: Compose sets `FRONTDESK_URL=http://tavern-core:8080`. The runtime client allowed plain `http://` only for the prefixes `http://127.0.0.1` and `http://localhost`, so the internal DNS name was rejected and the hireling never read the queue. The Tavern MCP server already had `FRONTDESK_ALLOW_HTTP=1` for this case; the runtime did not.
- Fix: the runtime parses the URL, allows loopback IPs (`127.0.0.1`, `::1`) without the flag, and allows any other `http://` host only when `FRONTDESK_ALLOW_HTTP=1`. Compose sets that flag next to the internal URL. The name `localhost` is no longer treated as loopback. The clean run above is after this fix.

## GBrain

The default quickstart does not start GBrain. `hireling check` reports the ledger off until the `brain` profile is on, which also needs a git repository. That is expected.

The image does build. On this machine `docker build -f services/gbrain/Dockerfile -t hireling/gbrain:local .` exited 0 and printed `gbrain 0.60.474.0`. CI's image job and the release workflow both build it now. Neither pushes it.
