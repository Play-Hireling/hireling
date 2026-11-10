# Deploy to a server

The same `compose.yaml` runs on a laptop or a VPS. This guide covers what changes when the tavern lives on a server.

## Size the box

| Party | RAM | vCPU |
| --- | --- | --- |
| Default (3 hirelings) | 2 GB | 1–2 |
| `full` (5 hirelings) | 4 GB | 2 |
| `full,brain` | 6–8 GB | 4 |

Each hireling is capped at 512 MB and ~0.5 CPU; idle it uses ~150 MB, during a turn up to ~400 MB. Pick a region close to the accounts your hirelings will log into (fewer "suspicious login" prompts).

## Install

```bash
# Ubuntu/Debian
curl -fsSL https://get.docker.com | sh
git clone https://github.com/Play-Hireling/hireling.git /opt/hireling && cd /opt/hireling
./scripts/init-env.sh && ${EDITOR:-nano} .env
./scripts/preflight.sh
docker compose up -d --build
```

## Reaching the Front Desk remotely

tavern-core listens on **127.0.0.1** only. Choose one:

1. **SSH tunnel** (simplest): `ssh -L 8080:127.0.0.1:8080 you@server`, then `./bin/tavern status` locally.
2. **Private network** (Tailscale, WireGuard): bind `TAVERN_PORT` to the private address by editing the `ports:` line, never `0.0.0.0`.
3. **Reverse proxy with TLS** (Caddy, Traefik, Cloudflare Tunnel): expose **only** `/api/*` (the Front Desk has bearer auth); keep the internal task API private. `tavern status`, `order`, `decisions` and `choose` work remotely; `quest`, `tasks` and `health` need the tunnel.

Caddy example (only the Front Desk is routed):

```caddy
tavern.example.com {
  @desk path /api/*
  handle @desk { reverse_proxy 127.0.0.1:8080 }
  handle { respond 404 }
}
```

## Updates and rollback

```bash
git pull && docker compose up -d --build        # rebuild changed images
```

`HIRELING_TAG` is the local tag Compose puts on every image it builds (`local` when unset). `HIRELING_TAG=v0.1.0 docker compose up -d --build` rebuilds from this tree and tags the result; it does not pull a registry.

Release images are built by [`.github/workflows/release.yml`](../../.github/workflows/release.yml) on a `v*` tag (and from the Actions tab). **Nothing is pushed** until a maintainer turns publishing on. The publish job stays skipped while the repository variable `RELEASE_PUBLISH` does not exist. To enable it later, without changing the workflow:

1. Settings → Secrets and variables → Actions → Variables → New variable: name `RELEASE_PUBLISH`, value `true`.
2. Actions → Release images → Run workflow, and set **publish** to true.

A tag push still only builds. The images, once published, are `ghcr.io/<owner>/hireling-runtime`, `hireling-tavern-core`, `hireling-egress`, `hireling-ledger-writer` and `hireling-gbrain`, tagged with the git tag. Compose keeps building `hireling/<name>:<HIRELING_TAG>` locally; pointing it at GHCR is a separate change after the first publish.

The Hermes base image is `nousresearch/hermes-agent:latest`; pin it by digest in `runtime/Dockerfile` for reproducible builds.

## Backups

| What | Where | How |
| --- | --- | --- |
| Tasks, quest, usage, Front Desk | volume `redis-data` (AOF) | `docker run --rm -v hireling_redis-data:/d -v $PWD:/b busybox tar czf /b/redis.tgz -C /d .` |
| Hireling memory and transcripts | volumes `tavern-<id>` | same pattern |
| Common table | volume `tavern-shared` | same pattern |
| Ledger | your git repo | it is already versioned; Postgres is only an index |

## Platforms

Anything that runs Docker Compose works: a plain VPS, [Dokploy](https://dokploy.com) or Coolify (point the app at this repo and `compose.yaml`; set the variables in the panel), or Docker Desktop. Kubernetes manifests are not provided yet.

## Operating checklist

- [ ] `.env` is `chmod 600` and not in git.
- [ ] Port 8080 is not reachable from the internet (`curl http://<public-ip>:8080/healthz` must fail).
- [ ] Each hireling's `network.allow` is as small as it can be.
- [ ] `paid-traffic` ad cap is still 0 unless you decided otherwise.
- [ ] Backups of `redis-data` and `tavern-*` volumes are scheduled.
- [ ] You know where decisions show up (`./bin/tavern decisions`) and check them daily.
