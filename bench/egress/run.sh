#!/usr/bin/env bash
# What leaves the machine.
#
# `self/` measures whether you can have the software. This measures whether
# having it means anyone else hears about it.
#
# Two observers, because one of them can miss.
#
# A LISTENER STANDING IN FOR THE INTERNET. Go's net/http honours HTTP_PROXY and
# HTTPS_PROXY, so a process pointed at `catcher.py` reaches that socket instead
# of anywhere real, and every attempt is recorded with what it asked for. It
# cannot be missed by timing: the connection is either accepted or it never
# happened.
#
# ITS SOCKETS, SAMPLED. The proxy only sees a client that honours the variables,
# so the process is also watched directly with lsof. That one CAN miss — a
# connection that opens and closes between two samples is invisible, and this
# run proves it does miss by reporting how many it caught of traffic it knows
# was there. Read it as corroboration, never as the proof.
#
# Both end with a control: the same observer, pointed at something that does
# reach out. A zero from an observer that cannot see is not evidence.
set -euo pipefail
cd "$(dirname "$0")/../.."
. bench/host.sh

say() { printf '%-34s %s\n' "$1" "$2"; }

host

sockets() {
  lsof -nP -i -a -p "$1" -F n 2>/dev/null | sed -n 's/^n//p' || true
}

watch_sockets() {
  local pid=$1 until_ts=$2 seen=$3
  while [ "$(python3 -c 'import time;print(int(time.time()))')" -lt "$until_ts" ]; do
    sockets "$pid" >>"$seen"
    sleep 0.1
  done
}

# A peer on loopback is this machine talking to itself — the curls below, the
# health probe, the catcher. Anything else is egress. The catcher's own port is
# loopback, so an attempt to phone home shows up in ITS log, not here.
remote_only() {
  grep -F -- '->' "$1" 2>/dev/null | sed 's/.*->//' | sort -u |
    grep -vE '^(127\.0\.0\.1|\[?::1\]?|localhost)[:.]' || true
}

P=${P:-18090}; ZP=${ZP:-19663}; HP=${HP:-19091}; AP=${AP:-18091}; CP=${CP:-18099}
DATA=$(mktemp -d); BIN=$(mktemp -u); PID=""; WATCH=""; CATCH=""
cleanup() {
  for p in "$WATCH" "$CATCH" "$PID"; do
    [ -n "$p" ] || continue
    kill "$p" 2>/dev/null
    # Reaped here, or the shell reports the signal over the last row of output.
    wait "$p" 2>/dev/null || true
  done
  rm -rf "$DATA" "$BIN"
}
trap cleanup EXIT

export GOWORK=off
go build -o "$BIN" ./cmd/cloud

: >"$DATA/caught"
python3 bench/egress/catcher.py "$CP" "$DATA/caught" >"$DATA/catcher.out" 2>&1 & CATCH=$!
for _ in $(seq 1 100); do grep -q ready "$DATA/catcher.out" 2>/dev/null && break; sleep 0.05; done

export CLOUD_DEV_UNENCRYPTED=1
export CLOUD_LISTEN=":$P" CLOUD_ZAP_LISTEN="127.0.0.1:$ZP"
export CLOUD_HEALTH_LISTEN="127.0.0.1:$HP" CLOUD_ADMIN_LISTEN="127.0.0.1:$AP"
export CLOUD_DATA_DIR="$DATA"
# Everything except the ports this run is driving. NO_PROXY has to name them or
# the binary's own health probe would be recorded as an attempt to leave.
export HTTP_PROXY="http://127.0.0.1:$CP" HTTPS_PROXY="http://127.0.0.1:$CP"
export http_proxy="$HTTP_PROXY" https_proxy="$HTTPS_PROXY" ALL_PROXY="$HTTP_PROXY"
export NO_PROXY="127.0.0.1:$P,127.0.0.1:$ZP,127.0.0.1:$HP,127.0.0.1:$AP"
export no_proxy="$NO_PROXY"

"$BIN" serve >"$DATA/log" 2>&1 & PID=$!
for _ in $(seq 1 300); do
  curl -s -m 2 --noproxy '*' "http://127.0.0.1:$HP/healthz" 2>/dev/null | grep -q . && break
  sleep 0.1
done

UNTIL=$(python3 -c 'import time;print(int(time.time())+12)')
watch_sockets "$PID" "$UNTIL" "$DATA/seen" & WATCH=$!

for _ in $(seq 1 20); do
  curl -s -o /dev/null -m 8 --noproxy '*' "http://127.0.0.1:$P/v1/tasks"
  curl -s -o /dev/null -m 8 --noproxy '*' -X POST "http://127.0.0.1:$P/mcp" \
    -H 'Content-Type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
  curl -s -o /dev/null -m 8 --noproxy '*' -X POST "http://127.0.0.1:$P/.well-known/zip/op/tasks_list"
done
wait "$WATCH" 2>/dev/null || true; WATCH=""

say "attempts to leave" "$(sort -u "$DATA/caught" | wc -l | tr -d ' ')"
sort -u "$DATA/caught" | sed 's/^/                                   /'

LISTENERS=$(sort -u "$DATA/seen" | grep -vF -- '->' | wc -l | tr -d ' ')
OUT=$(remote_only "$DATA/seen")
say "listeners seen" "$LISTENERS"
say "connections caught by sampling" "$(grep -cF -- '->' "$DATA/seen" || true)"
say "peers off this machine" "$([ -z "$OUT" ] && echo 0 || echo "$OUT" | wc -l | tr -d ' ')"
[ -n "$OUT" ] && echo "$OUT" | sed 's/^/                                   /'
[ "$LISTENERS" -eq 0 ] && { echo "no socket of any kind was seen on the server; it was not being watched" >&2; exit 1; }

# The controls, in the same run, so a broken observer fails the lane instead of
# reporting a clean one.
curl -s -m 6 -o /dev/null -x "http://127.0.0.1:$CP" https://pkg.hanzo.ai/ 2>/dev/null || true
sleep 0.5
CAUGHT_NOW=$(sort -u "$DATA/caught" | wc -l | tr -d ' ')
say "control: the catcher saw" "$CAUGHT_NOW attempt(s) after one caller"
[ "$CAUGHT_NOW" -eq 0 ] && { echo "the catcher recorded nothing for a caller that used it; the zero above is not evidence" >&2; exit 1; }

curl -s -m 10 -o /dev/null --noproxy '*' https://pkg.hanzo.ai/ & CPID=$!
UNTIL=$(python3 -c 'import time;print(int(time.time())+3)')
watch_sockets "$CPID" "$UNTIL" "$DATA/control" 2>/dev/null || true
wait "$CPID" 2>/dev/null || true
CN=$(remote_only "$DATA/control" | wc -l | tr -d ' ')
say "control: sampling saw" "$CN peer(s)"
[ "$CN" -eq 0 ] && { echo "sampling saw nothing it should have seen" >&2; exit 1; }
exit 0
