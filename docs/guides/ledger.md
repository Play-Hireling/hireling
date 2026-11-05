# Turn on the Ledger

The Ledger needs three things: a **git repository** for the pages, a **token** that can push to it, and the Compose profile **`brain`** (Postgres + GBrain + ledger-writer).

## 1. Create the brain repository

Create an empty repository on your git host (private is fine), e.g. `you/tavern-ledger`, and seed it from this repo's `ledger/` folder:

```bash
cp -r ledger /tmp/tavern-ledger && cd /tmp/tavern-ledger
git init -b main && git add . && git commit -m "seed: tavern ledger"
git remote add origin https://github.com/you/tavern-ledger.git && git push -u origin main
```

## 2. A token with the smallest scope

On GitHub: a **fine-grained personal access token** with access to **only that repository** and **Contents: Read and write**. Nothing else.

## 3. Configure

In `.env`:

```dotenv
COMPOSE_PROFILES=brain            # or full,brain
LEDGER_REPO_URL=https://github.com/you/tavern-ledger.git   # https, no credentials in the URL
LEDGER_GIT_TOKEN=github_pat_…
# LEDGER_DB_PASSWORD and GBRAIN_ADMIN_BOOTSTRAP_TOKEN were filled by ./scripts/init-env.sh
```

## 4. Start and check

```bash
docker compose up -d --build
docker compose logs -f gbrain        # clone → create brain → register source → serve on :3131
docker compose restart agent-innkeeper agent-scriptwriter agent-dev   # pick up their read tokens
docker compose exec -u hermes agent-dev hireling check
# ledger   gbrain http://gbrain:3131/mcp, read-only (areas …), writes learnings/dev/* through the runtime (writer http://ledger-writer:8090)
```

Ask a hireling something the Ledger knows (*"What is our quest?"*) and it should answer citing `goal.md`. Ask it to record a learning, and a commit appears in your repo authored by that hireling, indexed by GBrain on its next sync (`LEDGER_SYNC_SECONDS`, default 120).

## How the pieces trust each other

- **GBrain** never sees the git token: its entrypoint clones and pulls with a credential helper that reads `LEDGER_GIT_TOKEN` from the environment; GBrain only indexes the local checkout.
- **Hirelings** get a **read-only** GBrain token each (minted into the `gbrain-tokens` volume, mounted read-only at `/run/gbrain`).
- **ledger-writer** is the only writer. It identifies the caller by container name, checks `ledger.write` areas and the page grammar, then commits as the hireling.

## Backups

The pages are in git — that *is* the backup. Postgres only holds the index; if you lose it, `docker compose down` + remove the `ledger-db` and `gbrain-data` volumes rebuilds it from the repo.
