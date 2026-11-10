#!/bin/sh
# Exercises scripts/preflight.sh with stub iptables binaries. No root, no Docker.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
preflight=$root/scripts/preflight.sh
fix='sudo iptables-legacy -C FORWARD -i br+ -o br+ -j ACCEPT 2>/dev/null || sudo iptables-legacy -I FORWARD 1 -i br+ -o br+ -j ACCEPT'
fail=0

for file in "$preflight" "$root/docs/getting-started/quickstart.md"; do
  if ! grep -F -q -- "$fix" "$file"; then
    echo "missing the legacy FORWARD command in $file"
    fail=1
  fi
done

# The documented fix is one rule, not a flush and not a disabled firewall.
for banned in 'iptables-legacy -F' 'iptables-legacy -P FORWARD ACCEPT' 'ufw disable' 'curl | sh' 'curl |sh'; do
  if grep -F -q -- "$banned" "$preflight"; then
    echo "preflight.sh contains banned text: $banned"
    fail=1
  fi
done

run_case() {
  name=$1
  want=$2
  chain=$3
  version=$4
  have_ipt=$5
  dir=$(mktemp -d)
  printf '%s\n' "$chain" >"$dir/chain.txt"
  cat >"$dir/iptables-legacy" <<'EOF'
#!/bin/sh
if [ "$1" = "-S" ] && [ "$2" = "FORWARD" ]; then
  cat "$STUB_CHAIN"
  exit 0
fi
echo "unexpected $*" >&2
exit 2
EOF
  if [ "$have_ipt" = "yes" ]; then
    printf '%s\n' "$version" >"$dir/version.txt"
    cat >"$dir/iptables" <<'EOF'
#!/bin/sh
if [ "$1" = "--version" ]; then
  cat "$STUB_VERSION"
  exit 0
fi
echo "unexpected $*" >&2
exit 2
EOF
    chmod +x "$dir/iptables"
  fi
  chmod +x "$dir/iptables-legacy"
  out=$dir/out
  err=$dir/err
  set +e
  STUB_CHAIN=$dir/chain.txt STUB_VERSION=$dir/version.txt PREFLIGHT_SKIP_SBIN=1 \
    PATH="$dir:/usr/bin:/bin" sh "$preflight" >"$out" 2>"$err"
  rc=$?
  set -e
  if [ "$rc" -ne "$want" ]; then
    echo "FAIL $name: exit $rc, want $want"
    echo "--- stdout ---"; cat "$out"
    echo "--- stderr ---"; cat "$err"
    fail=1
    return
  fi
  if [ "$want" -eq 1 ]; then
    if ! grep -F -q -- "$fix" "$out"; then
      echo "FAIL $name: stdout missing the fix command"
      cat "$out"
      fail=1
      return
    fi
    if ! grep -F -q -- "does not flush" "$out" || ! grep -F -q -- "does not stop ufw" "$out"; then
      echo "FAIL $name: explanation incomplete"
      cat "$out"
      fail=1
      return
    fi
  else
    if grep -F -q -- "$fix" "$out"; then
      echo "FAIL $name: printed the fix on a passing host"
      cat "$out"
      fail=1
      return
    fi
    if ! grep -F -q -- "preflight: ok" "$out"; then
      echo "FAIL $name: missing ok"
      cat "$out"
      fail=1
      return
    fi
  fi
  echo "ok $name (exit $rc)"
}

dir=$(mktemp -d)
set +e
PREFLIGHT_SKIP_SBIN=1 PATH="$dir:/usr/bin:/bin" sh "$preflight" >"$dir/out" 2>"$dir/err"
rc=$?
set -e
if [ "$rc" -ne 0 ] || ! grep -F -q "not installed" "$dir/out"; then
  echo "FAIL missing binary: exit $rc"
  cat "$dir/out" "$dir/err"
  fail=1
else
  echo "ok missing binary (exit $rc)"
fi

run_case "nft and DROP" 1 "-P FORWARD DROP" "iptables v1.8.10 (nf_tables)" yes
run_case "nft and ACCEPT" 0 "-P FORWARD ACCEPT" "iptables v1.8.10 (nf_tables)" yes
run_case "nft DROP but br+ already accepted" 0 "-P FORWARD DROP
-A FORWARD -i br+ -o br+ -j ACCEPT" "iptables v1.8.10 (nf_tables)" yes
run_case "legacy backend keeps DROP" 0 "-P FORWARD DROP
-A FORWARD -o docker0 -j ACCEPT" "iptables v1.8.10 (legacy)" yes
run_case "unknown backend and DROP" 1 "-P FORWARD DROP" "" no

if [ "$fail" -ne 0 ]; then
  echo "preflight tests failed"
  exit 1
fi
echo "preflight tests passed"
