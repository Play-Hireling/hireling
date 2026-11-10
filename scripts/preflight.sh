#!/bin/sh
# preflight — check the host before `docker compose up`.
#
# On Ubuntu with Docker Engine from Docker's apt repository, iptables-legacy
# can sit beside the nft backend Docker programs, with FORWARD policy DROP.
# Compose bridges are named br-*, not docker0, so that policy drops packets
# between containers. tavern-core then logs `dial tcp …:6379: i/o timeout`
# and the first `up` exits 1.
#
# This script does not change iptables and it does not download anything.
# When it finds that case it exits 1 and prints one command: a single ACCEPT
# on the legacy FORWARD chain for br+ to br+, inserted only if it is missing.
# That does not flush rules, does not change the FORWARD policy, does not
# touch iptables-nft, and does not stop ufw or firewalld. Docker's nftables
# rules stay in place and still filter forwarded traffic.
#
# Exit 0 when this host is not in that case (including when iptables-legacy
# is not installed). PREFLIGHT_SKIP_SBIN=1 skips /usr/sbin and /sbin so the
# test fixtures are the only binaries consulted.
set -eu

say() { printf '%s\n' "$*"; }
# Diagnostics inside command substitutions must not mix with iptables output.
say_err() { printf '%s\n' "$*" >&2; }

# The same command is printed here and in docs/getting-started/quickstart.md.
fix='sudo iptables-legacy -C FORWARD -i br+ -o br+ -j ACCEPT 2>/dev/null || sudo iptables-legacy -I FORWARD 1 -i br+ -o br+ -j ACCEPT'

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
      if [ "$(id -u)" -eq 0 ]; then
        say_err "$out"
        return 1
      fi
      if ! command -v sudo >/dev/null 2>&1; then
        say_err "preflight: cannot read iptables-legacy FORWARD (need root) and sudo is not installed."
        return 1
      fi
      say_err "preflight: reading iptables-legacy needs root."
      sudo "$LEGACY" -S FORWARD
      return $?
      ;;
  esac
  say_err "preflight: iptables-legacy -S FORWARD failed:"
  say_err "$out"
  return 1
}

if ! DUMP=$(dump_legacy); then
  exit 1
fi

policy=$(printf '%s\n' "$DUMP" | awk 'NR==1 && $1=="-P" && $2=="FORWARD" { print $3; exit }')
if [ "$policy" != "DROP" ]; then
  say "preflight: ok (iptables-legacy FORWARD policy is ${policy:-unknown}, not DROP)"
  exit 0
fi

if printf '%s\n' "$DUMP" | grep -F -q -- '-i br+ -o br+ -j ACCEPT'; then
  say "preflight: ok (iptables-legacy FORWARD is DROP, and br+ to br+ is already accepted)"
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

say "preflight: iptables-legacy FORWARD policy is DROP."
if [ "$backend" = "nft" ]; then
  say "Docker's iptables command is the nft backend (iptables --version says nf_tables)."
else
  say "This script could not tell whether the iptables command is nft or legacy. Run: iptables --version"
  say "If that line contains nf_tables, Docker is programming the other table and the legacy policy below is what drops Compose traffic."
fi
say "Compose networks use bridges named br-*, not docker0. Packets between containers miss the legacy docker0 rules and hit this DROP, so tavern-core cannot dial Redis (dial tcp …:6379: i/o timeout) and the first docker compose up exits 1."
say ""
say "This does not turn the firewall off. Docker's nftables rules stay in place and still filter forwarded traffic. The command adds one ACCEPT on the unused legacy table, only for interfaces named br-* (Compose bridges), and only if that rule is missing. It does not flush rules, does not change the FORWARD policy, does not touch INPUT or OUTPUT, does not touch iptables-nft, and does not stop ufw or firewalld."
say ""
say "Current iptables-legacy FORWARD:"
printf '%s\n' "$DUMP"
say ""
say "Inspect the table Docker programs, then apply the one command and run this script again (it must exit 0 before docker compose up):"
say "  sudo iptables-nft -S FORWARD"
say "  $fix"
exit 1
