#!/usr/bin/env sh
# Fills the random secrets of .env (creating it from .env.example if needed).
# Existing non-empty values are kept, so it is safe to run again — except a
# Front Desk token shorter than 32 characters, which tavern-core refuses: it
# is replaced (restart the box afterwards: docker compose up -d).
set -eu
cd "$(dirname "$0")/.."
[ -f .env ] || cp .env.example .env

rand() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }

fill() {
  key=$1
  min=${2:-1}
  current=$(sed -n "s|^$key=||p" .env | tail -n 1 | tr -d '\r')
  if [ "${#current}" -ge "$min" ]; then
    echo "kept   $key"
  else
    [ -n "$current" ] && echo "short  $key (${#current} characters, at least $min needed): replacing it"
    value=$(rand)
    if grep -q "^$key=" .env; then
      tmp=$(mktemp)
      sed "s|^$key=.*|$key=$value|" .env >"$tmp" && cat "$tmp" >.env && rm -f "$tmp"
    else
      printf '%s=%s\n' "$key" "$value" >>.env
    fi
    echo "wrote  $key"
  fi
}

fill FRONTDESK_OWNER_TOKEN 32
fill FRONTDESK_AGENT_TOKEN 32
fill LEDGER_DB_PASSWORD
fill GBRAIN_ADMIN_BOOTSTRAP_TOKEN
chmod 600 .env 2>/dev/null || true

if ! grep -q '^DEEPSEEK_API_KEY=..*' .env; then
  echo
  echo "Next: paste your DeepSeek key into .env (DEEPSEEK_API_KEY=…), then: docker compose up -d --build"
fi
