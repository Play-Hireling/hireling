#!/bin/sh
# GBrain entrypoint (E1). Runs under tini as UID 10001.
#
# 1. Clone the brain repo (the markdown source of truth) on first start.
# 2. Create the brain in Postgres on first start; migrate on later starts.
# 3. Register the checkout as the `ledger` source (once).
# 4. Loop in the background: git pull, then `gbrain sync --no-pull`.
# 5. exec `gbrain serve --http` on :3131 for the tavern network.
#
# GBrain never sees the GitHub token: our loop clones and pulls with a
# credential helper that reads LEDGER_GIT_TOKEN from the environment, and
# GBrain only indexes the local checkout. Nothing secret is written to disk.
set -eu

log() { printf '%s gbrain: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "$*"; exit 1; }

[ -n "${GBRAIN_DATABASE_URL:-}" ] || die "GBRAIN_DATABASE_URL is not set (service env)"
[ -n "${LEDGER_REPO_URL:-}" ] || die "LEDGER_REPO_URL is not set (service env)"
case "$LEDGER_REPO_URL" in
  https://*@*) die "LEDGER_REPO_URL must not carry credentials; use LEDGER_GIT_TOKEN" ;;
  https://*) ;;
  *) die "LEDGER_REPO_URL must be an https URL" ;;
esac

export HOME="${GBRAIN_HOME:-/data}"
dir="${LEDGER_DIR:-/ledger/brain-repo}"
source_id="${LEDGER_SOURCE:-ledger}"
every="${LEDGER_SYNC_SECONDS:-120}"

# git with the token from the environment only (no ~/.git-credentials).
lgit() {
  if [ -n "${LEDGER_GIT_TOKEN:-}" ]; then
    git -c credential.helper= \
        -c 'credential.helper=!f() { echo username=x-access-token; echo "password=$LEDGER_GIT_TOKEN"; }; f' \
        "$@"
  else
    git "$@"
  fi
}

if [ ! -d "$dir/.git" ]; then
  log "cloning the brain repo into $dir"
  GIT_TERMINAL_PROMPT=0 lgit clone --quiet "$LEDGER_REPO_URL" "$dir" || die "clone failed (check LEDGER_REPO_URL and LEDGER_GIT_TOKEN)"
else
  # The repo may have moved (owner/org rename) while the volume kept the old
  # origin: point the existing checkout at $LEDGER_REPO_URL before pulling.
  origin="$(git -C "$dir" remote get-url origin 2>/dev/null || true)"
  if [ -n "$origin" ] && [ "$origin" != "$LEDGER_REPO_URL" ]; then
    log "origin moved from $origin to $LEDGER_REPO_URL; updating the checkout"
    git -C "$dir" remote set-url origin "$LEDGER_REPO_URL" || die "could not update origin"
  fi
fi

if [ ! -f "$HOME/.gbrain/config.json" ]; then
  log "creating the brain (Postgres, keyword search, no embeddings)"
  gbrain init --non-interactive --no-embedding --json >/dev/null
else
  log "brain exists; applying migrations"
  gbrain init --migrate-only >/dev/null
fi

# Keyless: no background LLM extraction (there is no Anthropic/OpenAI key here).
gbrain config set facts.extraction_enabled false >/dev/null 2>&1 || log "warning: could not turn off facts extraction"

if ! gbrain sources list --json 2>/dev/null | grep -q "\"$source_id\""; then
  log "registering $dir as source '$source_id'"
  gbrain sources add "$source_id" --path "$dir" --name "Tavern Ledger" --federated
fi

# E2: one read-only token per hireling that reads the brain, limited to the
# ledger source. Written to the gbrain-tokens volume (/tokens/<id>.token),
# which the hirelings mount read-only at /run/gbrain. Minted once; delete a
# file (or revoke the token) and restart to rotate it.
tokens="${GBRAIN_TOKENS_DIR:-/tokens}"
if [ -n "${HIRELING_LEDGER_READERS:-}" ] && [ -d "$tokens" ]; then
  for id in $HIRELING_LEDGER_READERS; do
    f="$tokens/$id.token"
    [ -s "$f" ] && continue
    name="hireling-$id"
    gbrain auth revoke "$name" >/dev/null 2>&1 || true
    tok="$(gbrain auth create "$name" --scopes read 2>/dev/null | grep -o 'gbrain_[A-Za-z0-9_-]*' | head -n 1)"
    if [ -z "$tok" ]; then
      log "warning: could not mint the ledger token for $id"
      continue
    fi
    if ! gbrain auth rescope --token "$name" --sources "$source_id" >/dev/null 2>&1; then
      log "warning: could not limit $id's token to source $source_id; revoking it"
      gbrain auth revoke "$name" >/dev/null 2>&1 || true
      continue
    fi
    umask 022
    printf '%s\n' "$tok" > "$f.tmp" && mv "$f.tmp" "$f"
    log "ledger token minted for $id (read-only, source $source_id)"
  done
fi

sync_once() {
  if ! GIT_TERMINAL_PROMPT=0 lgit -C "$dir" pull --quiet --ff-only; then
    log "warning: git pull failed; indexing the checkout as it is"
  fi
  if gbrain sync --source "$source_id" --no-pull --json >/dev/null 2>"$HOME/sync.err"; then
    log "synced $(git -C "$dir" rev-parse --short HEAD)"
  else
    log "warning: sync failed: $(tail -n 3 "$HOME/sync.err" | tr '\n' ' ')"
  fi
}

sync_once
( while sleep "$every"; do sync_once; done ) &

log "serving MCP on :3131 (tavern network only)"
exec gbrain serve --http --port 3131 --bind 0.0.0.0 --fail-fast
