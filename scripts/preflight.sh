#!/bin/sh
# preflight — check the host before `docker compose up`.
#
# On Ubuntu with Docker Engine from Docker's apt repository, iptables-legacy
# can sit beside the nft backend Docker programs, with FORWARD policy DROP.
# The tavern network's bridge is fixed in compose.yaml as br-hireling.
# Without an ACCEPT for that exact interface, packets between containers are
# dropped and tavern-core logs `dial tcp …:6379: i/o timeout`.
#
# This script does not change iptables, does not run sudo, and does not
# download anything. Exit codes:
#   0  ok to start Compose (legacy is absent, is not the stale DROP case,
#      or br-hireling to br-hireling is already accepted)
#   1  the stale DROP case was read and br-hireling is not accepted; the
#      fix command is printed. Not a pass.
#   2  the table could not be read (usually: not root). The diagnostic
#      commands and the fix are printed. Not a pass. Re-run as root:
#      sudo ./scripts/preflight.sh
#
# The fix is one ACCEPT for -i br-hireling -o br-hireling, inserted only
# when it is missing. It does not flush rules, does not change the FORWARD
# policy, does not match br0, br-lan, or br-wan, and does not stop ufw or
# firewalld. Docker's nftables rules stay in place.
#
# PREFLIGHT_SKIP_SBIN=1 skips /usr/sbin and /sbin so the test fixtures are
# the only binaries consulted.
set -eu

say() { printf '%s\n' "$*"; }
say_err() { printf '%s\n' "$*" >&2; }

# Printed for the operator to run. This script never executes it.
# The same text is in docs/getting-started/quickstart.md and docs/guides/deploy.md.
fix='sudo iptables-legacy -C FORWARD -i br-hireling -o br-hireling -j ACCEPT 2>/dev/null || sudo iptables-legacy -I FORWARD 1 -i br-hireling -o br-hireling -j ACCEPT'

lookup() {
  name=$1
  found=$(command -v "$name" 2>/dev/null || true)
  if [ -n "$found" ]; then
    printf '%s\n' "$found"
    return 0
  fi
  if [ "${PREFLIGHT_SKIP_SBIN:-}" = "1" ]; then
    return 1
  fi
  for dir in /usr/sbin /sbin; do
    if [ -x "$dir/$name" ]; then
      printf '%s\n' "$dir/$name"
      return 0
    fi
  done
  return 1
}

cannot_read() {
  say "preflight: cannot read iptables-legacy FORWARD (exit 2). This is not a pass."
  say "This script does not run sudo. Read the table yourself, then re-run this script as root (sudo ./scripts/preflight.sh):"
  say "  sudo iptables-legacy -S FORWARD"
  say "  sudo iptables-nft -S FORWARD"
  say "  iptables --version"
  say "If the policy is DROP, Docker's iptables is nft, and br-hireling to br-hireling is not accepted, apply this and re-run the script as root. It checks before inserting, does not flush, and does not change the policy. It names only the tavern bridge from compose.yaml, not br0, br-lan, or br-wan:"
  say "  $fix"
  exit 2
}

if ! LEGACY=$(lookup iptables-legacy); then
  say "preflight: ok (iptables-legacy is not installed)"
  exit 0
fi

dump_legacy() {
  out=$("$LEGACY" -S FORWARD 2>&1) && {
    printf '%s\n' "$out"
    return 0
  }
  case $out in
    *ermission*|*not\ permitted*|*must\ be\ root*)
      return 2
      ;;
  esac
  say_err "preflight: iptables-legacy -S FORWARD failed:"
  say_err "$out"
  return 2
}

rc=0
DUMP=$(dump_legacy) || rc=$?
if [ "$rc" -ne 0 ]; then
  cannot_read
fi

policy=$(printf '%s\n' "$DUMP" | awk 'NR==1 && $1=="-P" && $2=="FORWARD" { print $3; exit }')
if [ "$policy" != "DROP" ]; then
  say "preflight: ok (iptables-legacy FORWARD policy is ${policy:-unknown}, not DROP)"
  exit 0
fi

if printf '%s\n' "$DUMP" | grep -F -q -- '-i br-hireling -o br-hireling -j ACCEPT'; then
  say "preflight: ok (iptables-legacy FORWARD is DROP, and br-hireling to br-hireling is already accepted)"
  exit 0
fi

backend=unknown
if IPT=$(lookup iptables); then
  ver=$("$IPT" --version 2>/dev/null || true)
  case $ver in
    *nf_tables*) backend=nft ;;
    *legacy*) backend=legacy ;;
  esac
fi

if [ "$backend" = "legacy" ]; then
  say "preflight: ok (iptables itself is the legacy backend, so Docker programs this table)"
  exit 0
fi

say "preflight: iptables-legacy FORWARD policy is DROP (exit 1). This is not a pass."
if [ "$backend" = "nft" ]; then
  say "Docker's iptables command is the nft backend (iptables --version says nf_tables)."
else
  say "This script could not tell whether the iptables command is nft or legacy. Run: iptables --version"
  say "If that line contains nf_tables, Docker is programming the other table and the legacy policy below is what drops traffic on the tavern bridge."
fi
say "compose.yaml pins that bridge to br-hireling. Packets between tavern containers use that interface, not docker0, br0, br-lan, or br-wan, so they hit this DROP and tavern-core cannot dial Redis (dial tcp …:6379: i/o timeout). The first docker compose up then exits 1."
say ""
say "This does not turn the firewall off. Docker's nftables rules stay in place and still filter forwarded traffic. The command adds one ACCEPT on the unused legacy table, only for -i br-hireling -o br-hireling, and only if that rule is missing. It does not flush rules, does not change the FORWARD policy, does not touch INPUT or OUTPUT, does not touch iptables-nft, and does not stop ufw or firewalld."
say ""
say "Current iptables-legacy FORWARD:"
printf '%s\n' "$DUMP"
say ""
say "Inspect the table Docker programs, then apply the one command and run this script again as root. It must exit 0 before docker compose up:"
say "  sudo iptables-nft -S FORWARD"
say "  $fix"
exit 1
