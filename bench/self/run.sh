#!/usr/bin/env bash
# What it costs to run this yourself.
#
# Every other lane measures what the software does. This one measures whether you
# can have it at all: build the binary from this repo, start it, ask it what it
# serves, and reach one operation through each door it opens. No account, no
# image registry, no private module.
#
# Re-run it. The numbers in README.md came from this script and nothing else.
set -euo pipefail
cd "$(dirname "$0")/../.."
. bench/host.sh

say() { printf '%-34s %s\n' "$1" "$2"; }

host

# A free port, because the point is that this runs beside whatever you already
# have running.
P=${P:-18080}; ZP=${ZP:-19653}; HP=${HP:-19090}; AP=${AP:-18081}
DATA=$(mktemp -d); BIN=$(mktemp -u); PID=""
# An unset PID must not reach kill: `kill 0` signals the whole process group,
# which takes down the shell that ran this and leaves no output to read.
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null; rm -rf "$DATA" "$BIN"; }
trap cleanup EXIT

# This repository stands alone, and the point of the lane is that it does. A
# workspace file in a parent directory would pull in whatever else is checked
# out beside it and fail on a toolchain none of this needs.
export GOWORK=off

say "private modules required" "$(grep -cE 'hanzo-inc|hanzoai/(iam|kms|commerce)' go.mod || true)"

t0=$(python3 -c 'import time;print(time.time())')
go build -o "$BIN" ./cmd/cloud
t1=$(python3 -c 'import time;print(time.time())')
say "build from source" "$(python3 -c "print('%.0f s' % ($t1-$t0))")"
say "binary" "$(du -h "$BIN" | cut -f1)"

# The store refuses to open unencrypted, which is the right default, so a run
# from source has to say which it wants. This lane asks for the dev path and
# NOT for a key: a plain `go build` links ordinary SQLite rather than
# libsqlcipher, so a key here would be a request the binary correctly refuses.
# See README.md — it is the one row where running it yourself costs something.
export CLOUD_DEV_UNENCRYPTED=1
export CLOUD_LISTEN=":$P" CLOUD_ZAP_LISTEN="127.0.0.1:$ZP"
export CLOUD_HEALTH_LISTEN="127.0.0.1:$HP" CLOUD_ADMIN_LISTEN="127.0.0.1:$AP"
export CLOUD_DATA_DIR="$DATA"

t0=$(python3 -c 'import time;print(time.time())')
"$BIN" serve >"$DATA/log" 2>&1 & PID=$!
# Asked of the HEALTH port, not the API port. The API port answers the console's
# shell for an unrouted path, so a 200 there says the listener is up and nothing
# about whether the program is ready — measuring against it reads 0.6s where the
# honest number is half that.
for _ in $(seq 1 300); do
  curl -s -m 2 "http://127.0.0.1:$HP/healthz" 2>/dev/null | grep -q . && break
  sleep 0.1
done
t1=$(python3 -c 'import time;print(time.time())')
say "boot to a healthy answer" "$(python3 -c "print('%.1f s' % ($t1-$t0))")"

curl -s -m 10 "http://127.0.0.1:$P/.well-known/openapi.json" -o "$DATA/api.json"
python3 - "$DATA/api.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
paths = d.get("paths", {})
ops = sum(1 for p in paths.values() for m in p if m in ("get","post","put","patch","delete"))
print("%-34s %d over %d paths" % ("operations served by default", ops, len(paths)))
PY

# One operation, every door it opens. A door that does not answer is the row that
# matters, so each is asked separately rather than counted.
say "REST" "$(curl -s -o /dev/null -w '%{http_code}' -m 8 "http://127.0.0.1:$P/v1/tasks")"
say "MCP tools/list" "$(curl -s -m 8 -X POST "http://127.0.0.1:$P/mcp" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["result"]["tools"]), "tools")')"
say "op-call plane" "$(curl -s -o /dev/null -w '%{http_code}' -m 8 -X POST "http://127.0.0.1:$P/.well-known/zip/op/tasks_list")"
