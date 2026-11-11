#!/bin/sh
# Fail a self-host client bundle that is not safe to publish.
#
#   scripts/selfhost-bundle-gate.sh <directory> [--expect-audio]
#
# --expect-audio requires the eight allowlisted files to be present.
# Without it, any audio that is present must still be one of those eight.
# Anything under assets/audio/music/, any audio file outside the SFX
# allowlist, docs/provenance and packages/assets/withheld fail
# either way. Denied strings are scanned in text and in binaries
# (images, audio, wasm, fonts) with grep -a. /api/ may only be status,
# command, or decisions/{id}/choose.
set -eu

if [ $# -lt 1 ] || [ "$1" = "-h" ]; then
  echo "usage: scripts/selfhost-bundle-gate.sh <directory> [--expect-audio]" >&2
  exit 2
fi

dir=$1
expect=0
if [ "${2:-}" = "--expect-audio" ]; then
  expect=1
fi
if [ ! -d "$dir" ]; then
  echo "not a directory: $dir" >&2
  exit 1
fi

fail=0
note() {
  echo "$1" >&2
  fail=1
}

allow='assets/audio/sfx/ui_click.mp3
assets/audio/sfx/reward_coins.mp3
assets/audio/sfx/notify_bell.mp3
assets/audio/sfx/parchment.mp3
assets/audio/sfx/broom_sweep.mp3
assets/audio/sfx/amb_fireplace.mp3
assets/audio/sfx/door_open.mp3
assets/audio/sfx/door_close.mp3'

terms='cloudflareaccess
cf-access
CF_Authorization
pages.dev
pedrodapps
AsharFinance
hireling-cloud
localhost:8787
analytics.twitter
twq(
/api/coins
/api/treasury
/api/usage
solana
USDC
playhireling.com
escritorio
cloudflareinsights
ads-twitter
x-tavern-csrf'

list=$(find "$dir" -print | sed "s|^$dir/||")
if echo "$list" | grep -F -q 'docs/provenance'; then
  note "docs/provenance is in the bundle"
fi
if echo "$list" | grep -E -q '(^|/)withheld(/|$)'; then
  note "withheld is in the bundle"
fi

maps=$(find "$dir" -type f -name '*.map' | sed "s|^$dir/||" || true)
if [ -n "$maps" ]; then
  echo "$maps" | while IFS= read -r m; do
    [ -n "$m" ] && echo "sourcemap: $m" >&2
  done
  fail=1
fi

music=$(find "$dir" -type f -path '*/assets/audio/music/*' | sed "s|^$dir/||" || true)
if [ -n "$music" ]; then
  echo "$music" | while IFS= read -r m; do
    [ -n "$m" ] && echo "music: $m" >&2
  done
  fail=1
fi

audio=$(find "$dir" -type f \( -iname '*.mp3' -o -iname '*.ogg' -o -iname '*.wav' -o -iname '*.flac' -o -iname '*.m4a' -o -iname '*.aac' -o -iname '*.opus' -o -iname '*.weba' \) | sed "s|^$dir/||" | sort || true)
if [ -n "$audio" ]; then
  printf '%s\n' "$audio" | while IFS= read -r a; do
    [ -n "$a" ] || continue
    if ! printf '%s\n' "$allow" | grep -F -x -q "$a"; then
      echo "audio not allowlisted: $a" >&2
      exit 1
    fi
  done || fail=1
fi

if [ "$expect" -eq 1 ]; then
  printf '%s\n' "$allow" | while IFS= read -r line; do
    [ -n "$line" ] || continue
    if [ ! -f "$dir/$line" ]; then
      echo "missing allowlisted audio: $line" >&2
      exit 1
    fi
  done || fail=1
fi

if find "$dir" -type f -print | grep -q .; then
  printf '%s\n' "$terms" | while IFS= read -r term; do
    [ -n "$term" ] || continue
    hit=$(find "$dir" -type f -exec grep -a -l -F -i -e "$term" {} + 2>/dev/null || true)
    if [ -n "$hit" ]; then
      echo "denied term: $term" >&2
      printf '%s\n' "$hit" | sed 's/^/  /' >&2
      exit 1
    fi
  done || fail=1
fi

api_files=$(find "$dir" -type f \( -name '*.js' -o -name '*.mjs' -o -name '*.css' -o -name '*.html' -o -name '*.json' -o -name '*.svg' -o -name '*.txt' -o -name '*.webmanifest' -o -name '*.wasm' \) -print || true)
if [ -n "$api_files" ]; then
  printf '%s\n' "$api_files" | while IFS= read -r f; do
    [ -n "$f" ] || continue
    paths=$(grep -a -o -E '/api/[A-Za-z0-9._~-]*' "$f" || true)
    [ -n "$paths" ] || continue
    printf '%s\n' "$paths" | while IFS= read -r p; do
      [ -n "$p" ] || continue
      case $p in
        /api/status|/api/command|/api/decisions) ;;
        /api/decisions/*/choose) ;;
        *)
          echo "api path not allowlisted: $p in ${f#"$dir"/}" >&2
          exit 1
          ;;
      esac
    done || exit 1
  done || fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "selfhost bundle gate: failed ($dir)" >&2
  exit 1
fi
echo "selfhost bundle gate ok: $dir"
