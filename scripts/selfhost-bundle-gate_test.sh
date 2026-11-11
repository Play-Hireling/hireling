#!/bin/sh
# Proves the self-host bundle gate. A clean tree of the eight SFX passes.
# A planted packages/assets/withheld file fails. docs/provenance fails.
# Music, an extra audio file, another /api/ path and a denied string inside
# a PNG fail too.
set -eu

root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
gate=$root/scripts/selfhost-bundle-gate.sh

fail() {
  echo "FAIL $1" >&2
  exit 1
}

pass_dir=$(mktemp -d)
err=$(mktemp)
trap 'rm -rf "$pass_dir" "$err"' EXIT
mkdir -p "$pass_dir/assets/audio/sfx"
for name in ui_click reward_coins notify_bell parchment broom_sweep amb_fireplace door_open door_close; do
  printf 'sfx\n' > "$pass_dir/assets/audio/sfx/$name.mp3"
done
printf '%s\n' '<!doctype html><script src="app.js"></script>' > "$pass_dir/index.html"
printf '%s\n' 'fetch("/api/status");fetch("/api/command");fetch("/api/decisions/"+id+"/choose");' > "$pass_dir/app.js"
"$gate" "$pass_dir" --expect-audio >/dev/null || fail "clean bundle"

plant="$pass_dir/packages/assets/withheld/secret.txt"
mkdir -p "$(dirname "$plant")"
printf 'no\n' > "$plant"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "withheld was accepted"
fi
grep -F -q 'withheld' "$err" || fail "withheld rejection did not name withheld"
rm -rf "$pass_dir/packages"

mkdir -p "$pass_dir/docs/provenance/audio"
printf 'raw\n' > "$pass_dir/docs/provenance/audio/note.txt"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "docs/provenance was accepted"
fi
grep -F -q 'docs/provenance' "$err" || fail "provenance rejection did not name docs/provenance"
rm -rf "$pass_dir/docs"

mkdir -p "$pass_dir/assets/audio/music"
printf 'song\n' > "$pass_dir/assets/audio/music/vale_night.mp3"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "vale_night.mp3 in assets/audio/music/ was accepted"
fi
grep -F -q 'assets/audio/music/vale_night.mp3' "$err" || fail "music rejection did not name the planted track"
rm -rf "$pass_dir/assets/audio/music"

mkdir -p "$pass_dir/assets/extra"
printf 'ogg\n' > "$pass_dir/assets/extra/foo.ogg"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "foo.ogg outside the SFX allowlist was accepted"
fi
grep -F -q 'foo.ogg' "$err" || fail "audio rejection did not name foo.ogg"
rm -rf "$pass_dir/assets/extra"

printf 'extra\n' > "$pass_dir/assets/audio/sfx/extra.mp3"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "extra audio was accepted"
fi
rm -f "$pass_dir/assets/audio/sfx/extra.mp3"

printf '%s\n' 'fetch("/api/quest")' > "$pass_dir/app.js"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "/api/quest was accepted"
fi
grep -F -q '/api/quest' "$err" || fail "api rejection did not name /api/quest"
printf '%s\n' 'fetch("/api/status");fetch("/api/command");fetch("/api/decisions/"+id+"/choose");' > "$pass_dir/app.js"

printf 'pages.dev' > "$pass_dir/hero.png"
if "$gate" "$pass_dir" >/dev/null 2>"$err"; then
  fail "pages.dev inside a png was accepted"
fi
grep -F -q 'pages.dev' "$err" || fail "binary denial did not name pages.dev"
rm -f "$pass_dir/hero.png"

"$gate" "$pass_dir" --expect-audio >/dev/null || fail "bundle did not pass again after the plants were removed"
echo "selfhost bundle gate tests passed"
