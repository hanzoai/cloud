#!/usr/bin/env bash
# What an agent pays to call an operation, per door.
#
# Every other lane measures work the machine does. This measures the tax on
# asking it to: one operation, registered once, called over each way this server
# offers to reach it. An agent's loop is call, read, decide, call again, so this
# number is multiplied by every step of every task.
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
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$DATA" "$BIN"; }
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
python3 - "$P" "$N" <<'PY'
import json, statistics, sys, time, urllib.request

port, n = sys.argv[1], int(sys.argv[2])
base = f"http://127.0.0.1:{port}"

def timed(req):
    t = time.perf_counter()
    with urllib.request.urlopen(req, timeout=10) as r:
        r.read()
    return (time.perf_counter() - t) * 1000

def rest():
    return urllib.request.Request(f"{base}/v1/tasks", method="GET")

def mcp():
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                       "params": {"name": "tasks_list", "arguments": {}}}).encode()
    return urllib.request.Request(f"{base}/mcp", data=body,
                                  headers={"Content-Type": "application/json"}, method="POST")

def plane():
    return urllib.request.Request(f"{base}/.well-known/zip/op/tasks_list",
                                  data=b"", method="POST")

# INTERLEAVED, not one door then the next. Measured blocked, the three rows
# disagreed about which door was fastest on every run — the drift between phases
# on a busy machine is larger than the difference being measured. Round-robin
# puts every door in the same conditions on every iteration, which is the only
# way the comparison means anything.
samples = {"REST": [], "MCP": [], "plane": []}
makers = {"REST": rest, "MCP": mcp, "plane": plane}
for _ in range(20):                       # warm every door
    for m in makers.values():
        timed(m())
for _ in range(n):
    for name, m in makers.items():
        samples[name].append(timed(m()))

print(f"{'door':<10} {'min':>8} {'p50':>8} {'p90':>8}   (ms, n={n}, interleaved)")
for name in ("REST", "MCP", "plane"):
    xs = sorted(samples[name])
    print(f"{name:<10} {xs[0]:>8.2f} {statistics.median(xs):>8.2f} {xs[int(len(xs)*0.90)-1]:>8.2f}")
