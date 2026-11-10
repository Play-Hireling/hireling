# Quickstart clean run

Record of the first clean run of the self-host quickstart (`docs/getting-started/quickstart.md`) on Linux with Docker. The log is [quickstart-run.log](quickstart-run.log). No real API key, token or password is in that log. `.env` was created by `./scripts/init-env.sh` during the run and was not committed.

This replaces the earlier **79s** figure. That number was a later run on the same day (2026-11-10T18:05:32Z to 18:06:51Z) that reused base images already on disk and a firewall that had already been fixed. It was not a first run from zero images.

## Machine

- 2026-11-10. Linux 6.12.113+ x86_64, Ubuntu 24.04.5. Docker Engine 29.9.3, Compose v5.7.0. `iptables` is `iptables v1.8.10 (nf_tables)`.
- Clone of commit `7e940446ec4debf7b89fb17ab16d9ef1a49d11dd`, tree `181a24f491faa0ab2b7b3fee2532822e27da939f`, in `/tmp/qs-clean` (`git clone` of that commit, not a dirty tree).
- Before the clock: `docker system prune -af --volumes` and `docker builder prune -af`. `docker images` listed nothing (`IMAGES_BEFORE 0`). The builder cache reported `Total: 0B`.
- The key was a placeholder written by a non-interactive stand-in for `${EDITOR:-nano} .env`. A real DeepSeek key was not used. That is enough to boot and accept an order; the model turn itself cannot succeed.

## Firewall, before the clock

`iptables-legacy` `FORWARD` policy was `DROP`, and `iptables --version` reported `nf_tables`. On a user-defined bridge (not `docker0`), `ping` between two containers lost 100% of packets until one legacy `ACCEPT` was inserted. Policy stayed `DROP`. nft `FORWARD` was not modified. The timed run below started only after that. No further firewall change was made during the clock.

The log's firewall transcript names `br-hireling`. An earlier transcription wrote that interface as `br+`; that abbreviation was corrected in the log so it matches the rule `preflight.sh` prints.

The 95s figure is the image pull and compile (`docker compose up -d --build` was 94s of the 95s, from zero images and an empty builder cache). Pinning the tavern bridge to `br-hireling` does not change that build, so this number still stands. The log of that run is unchanged. The rule to use now is only `-i br-hireling -o br-hireling`, which is what `preflight.sh` prints. A later check on the same machine, with `FORWARD` still `DROP` and that rule absent: ping on a bridge created with `com.docker.network.bridge.name=br-hireling` lost 100% of packets; `sudo ./scripts/preflight.sh` exited 1 and the only suggested rule was `br-hireling`; after that command, the same ping had 0% loss, the policy was still `DROP`, and `sudo ./scripts/preflight.sh` exited 0. Without root, `./scripts/preflight.sh` exited 2 and did not run `sudo`.

## Timed run

Wall clock from `./scripts/init-env.sh` through the post-order `./bin/tavern status`: **95s** (2026-11-10T18:47:55Z to 2026-11-10T18:49:30Z).

| Step | Result | Time |
| --- | --- | --- |
| `./scripts/init-env.sh` | exit 0, tokens written, values not printed | 0s |
| `${EDITOR:-nano} .env` | exit 0, placeholder `DEEPSEEK_API_KEY` (non-interactive stand-in; value not printed) | 0s |
| `./scripts/preflight.sh` | exit 0, the tavern-bridge accept was already in place | 0s |
| `docker compose up -d --build` | exit 0, from zero images and an empty builder cache | 94s |
| `docker compose ps -a` | redis, tavern-core, egress healthy; tavern-init exited 0; three agents up | 1s |
| `curl http://127.0.0.1:8080/healthz` | HTTP 200 `{"status":"ok",...}` | 0s |
| `curl http://127.0.0.1:8080/` | HTTP 200, title `Tavern`, 10705 bytes | 0s |
| `./bin/tavern health` | same JSON, exit 0 | 0s |
| `./bin/tavern status` | six agents, `quest` null, no decisions, exit 0 | 0s |
| `./bin/tavern decisions` | `{"decisions":[]}`, exit 0 | 0s |
| `docker compose exec -u hermes agent-innkeeper hireling check` | exit 0, `frontdesk  ok (queue readable)`, ledger off, and one log line `FRONTDESK_ALLOW_HTTP=1; plain http to a non-loopback host is allowed` | 0s |
| `./bin/tavern order innkeeper "…"` | exit 0, command `c-f3934f46058b` accepted | 0s |
| `./bin/tavern status` again | Innkeeper `pending` 1 on that order; `quest` still null | 0s |

The status in that same second still had the order queued (`pending` 1), not yet in phase `start`. A later read, outside the 95s, showed the Innkeeper turn in phase `start` with that topic; the turn's `at` is 2026-11-10T18:49:32Z.

`docker compose up` did not need a second invocation. There was no hidden manual step during the clock.

Agent logs after the order: **0** lines containing `FRONTDESK_URL must be https://`. The three `frontdesk:` lines are the startup notice that `FRONTDESK_ALLOW_HTTP=1`, one per hireling.

## Owner panel

With the owner token and no quest, the page at `http://127.0.0.1:8080/` showed `no quest set yet`, the party (including the Innkeeper's order), and `no open decisions`. It did not show `could not read the tavern`. Chromium recorded 0 console errors, 0 warnings and 0 page errors at 1440×900 and at 390×844, including a second refresh after 5 seconds. One verbose console note remained: the password field is not inside a form.
