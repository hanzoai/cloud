#!/usr/bin/env bash
# What an agent pays to call an operation, per door.
#
# Every other lane measures work the machine does. This measures the tax on
# asking it to: one operation, registered once, called over each way this server
# offers to reach it. An agent's loop is call, read, decide, call again, so this
# number is multiplied by every step of every task.
#
# Four doors, and the fourth is why the harness is Go. REST, MCP and the op-call
# plane are HTTP and a script can speak them; ZAP is not, and a Python client
# against three doors with a Go client against the fourth would put the client's
# language into the comparison. One client, four envelopes.
#
# The doors are not variants of one wire. Over REST the operation IS the address.
# Over MCP the address is a single agent endpoint and the operation is a name in
# the body. Over the op-call plane the name is in the path and the body is
# binary. Same handler, same process, three different envelopes — so the
# difference between these rows is the envelope and nothing else.
set -euo pipefail
cd "$(dirname "$0")/../.."
. bench/host.sh
export GOWORK=off

N=${N:-200}
P=${P:-18086}; ZP=${ZP:-19663}; HP=${HP:-19096}; AP=${AP:-18087}
DATA=$(mktemp -d); BIN=$(mktemp -u); PID=""
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$DATA" "$BIN" "$BIN.doors"; }
trap cleanup EXIT

go build -o "$BIN" ./cmd/cloud
export CLOUD_DEV_UNENCRYPTED=1
export CLOUD_LISTEN=":$P" CLOUD_ZAP_LISTEN="127.0.0.1:$ZP"
export CLOUD_HEALTH_LISTEN="127.0.0.1:$HP" CLOUD_ADMIN_LISTEN="127.0.0.1:$AP"
export CLOUD_DATA_DIR="$DATA"
"$BIN" serve >"$DATA/log" 2>&1 & PID=$!
for _ in $(seq 1 300); do
  curl -s -m 2 "http://127.0.0.1:$HP/healthz" 2>/dev/null | grep -q . && break
  sleep 0.1
done

# One READ, because a read is what an agent does most and it writes nothing that
# would make the second call different from the first.
host
go build -o "$BIN.doors" ./bench/doors/harness
"$BIN.doors" -base "http://127.0.0.1:$P" -zap "127.0.0.1:$ZP" -zap-name "ZAP tcp" -n "$N" ${BENCH_JSON:+-json "$BENCH_JSON"}

# ZAP over a unix socket is the in-cluster path, and a process serves ONE ZAP
# address, so it is a second phase rather than a fifth row of the first. The
# three HTTP doors are measured again with it: they are the control on drift, and
# the two ZAP rows are only comparable if REST reads the same in both phases.
echo
kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true
DATA2=$(mktemp -d)
CLOUD_DATA_DIR="$DATA2" CLOUD_ZAP_LISTEN="$DATA2/cloud.sock" "$BIN" serve >"$DATA2/log" 2>&1 & PID=$!
for _ in $(seq 1 300); do
  curl -s -m 2 "http://127.0.0.1:$HP/healthz" 2>/dev/null | grep -q . && break
  sleep 0.1
done
"$BIN.doors" -base "http://127.0.0.1:$P" -zap "$DATA2/cloud.sock" -zap-name "ZAP unix" -n "$N" ${BENCH_JSON:+-json "$BENCH_JSON"}
rm -rf "$DATA2"
