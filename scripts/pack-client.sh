#!/bin/sh
# Build a local hireling/client:local image from a playhireling self-host dist.
# Does not push, tag a release, or dispatch a workflow.
#
#   scripts/pack-client.sh /path/to/playhireling/dist-selfhost
set -eu

if [ $# -ne 1 ] || [ ! -f "$1/index.html" ]; then
  echo "usage: scripts/pack-client.sh <dist-selfhost-dir>" >&2
  exit 2
fi

root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
dist=$1
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT

mkdir -p "$ctx/dist" "$ctx/licenses"
cp -a "$dist"/. "$ctx/dist/"
cp "$root/NOTICE" "$root/LICENSE-CLIENT.md" "$root/THIRD-PARTY.md" "$ctx/licenses/"
cp "$root/client/Dockerfile" "$ctx/Dockerfile"

"$root/scripts/selfhost-bundle-gate.sh" "$ctx/dist" --expect-audio
"$root/scripts/selfhost-bundle-gate.sh" "$ctx/licenses"

rev=$(git -C "$root" rev-parse HEAD 2>/dev/null || echo unknown)
docker build --build-arg "REVISION=$rev" -t hireling/client:local "$ctx"
echo "built hireling/client:local (not pushed)"
