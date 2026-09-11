#!/usr/bin/env bash
# What is on the disk.
#
# `self/` says a plain build cannot encrypt and refuses rather than pretending.
# This is the other half: build one that can, write a value through the API, and
# look for that value in the files.
#
# The same binary runs it twice — once with a master key, once on the
# development path that is explicitly unencrypted. The second run is the
# control. Without it a zero means nothing: the write might never have reached
# the disk, or the search might not work on the file, and both of those have
# happened here.
set -euo pipefail
cd "$(dirname "$0")/../.."

say() { printf '%-34s %s\n' "$1" "$2"; }

# The store is SQLCipher and cgo links ordinary SQLite unless the build says so.
FLAGS_C=$(pkg-config --cflags sqlcipher 2>/dev/null || echo "-I/opt/homebrew/opt/sqlcipher/include")
FLAGS_L=$(pkg-config --libs sqlcipher 2>/dev/null || echo "-L/opt/homebrew/opt/sqlcipher/lib -lsqlcipher")

BIN=$(mktemp -u); A=$(mktemp -d); B=$(mktemp -d); PID=""
cleanup() {
  [ -n "$PID" ] && { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true; }
  rm -rf "$BIN" "$A" "$B"
}
trap cleanup EXIT

export GOWORK=off
CGO_ENABLED=1 CGO_CFLAGS="$FLAGS_C" CGO_LDFLAGS="$FLAGS_L" \
  go build -tags libsqlite3 -o "$BIN" ./cmd/cloud

# A value nothing else could have written, so a hit is this run's and not a
# coincidence in a page of someone else's data.
CANARY="canary-$(head -c 8 /dev/urandom | xxd -p)"

# `LC_ALL=C` and `-a`, because a store is a binary file and grep will otherwise
# decline to read it — which reads as "not found" and is not the same thing. The
# control is what caught that: it reported a clean zero for a file that plainly
# contained the string.
hits() { LC_ALL=C grep -ral "$CANARY" "$1" 2>/dev/null | wc -l | tr -d ' '; }
headers() {
  for f in "$1"/*.db; do
    head -c 15 "$f" | LC_ALL=C tr -c '[:print:]' '.'
    echo
  done | sort -u
}

# One run: start it, write the canary through two operations, stop it, look.
run() {
  local dir=$1 port=$2 zap=$3 health=$4 admin=$5
  CLOUD_LISTEN=":$port" CLOUD_ZAP_LISTEN="127.0.0.1:$zap" \
  CLOUD_HEALTH_LISTEN="127.0.0.1:$health" CLOUD_ADMIN_LISTEN="127.0.0.1:$admin" \
  CLOUD_DATA_DIR="$dir" "$BIN" serve >"$dir/log" 2>&1 & PID=$!
  for _ in $(seq 1 300); do
    curl -s -m 2 "http://127.0.0.1:$health/healthz" 2>/dev/null | grep -q . && break
    sleep 0.1
  done
  curl -s -o /dev/null -m 8 -X POST "http://127.0.0.1:$port/v1/tasks" \
    -H 'Content-Type: application/json' \
    -d "{\"kind\":\"bench_probe\",\"payload\":{\"note\":\"$CANARY\"}}"
  curl -s -o /dev/null -m 8 -X POST "http://127.0.0.1:$port/v1/base/collections/notes" \
    -H 'Content-Type: application/json' -d "{\"doc\":{\"body\":\"$CANARY\"}}"
  kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true; PID=""
}

# The one this lane is about. A master key, and the store encrypted at rest.
export CLOUD_KMS_MASTER_KEY_REF="$(openssl rand -base64 32)"
unset CLOUD_DEV_UNENCRYPTED || true
run "$A" 18110 19673 19111 18111
say "encryption" "$(grep -o 'data-plane encryption [A-Z]*' "$A/log" | head -1)"
say "stores written" "$(ls "$A"/*.db | wc -l | tr -d ' ')"
say "distinct headers" "$(headers "$A" | wc -l | tr -d ' ')"
headers "$A" | sed 's/^/                                   /'
say "files holding the canary" "$(hits "$A")"

# The control. Same binary, same writes, the development path that says plainly
# it is not encrypting.
unset CLOUD_KMS_MASTER_KEY_REF
export CLOUD_DEV_UNENCRYPTED=1
run "$B" 18112 19675 19113 18113
say "control: stores written" "$(ls "$B"/*.db | wc -l | tr -d ' ')"
say "control: distinct headers" "$(headers "$B" | wc -l | tr -d ' ')"
headers "$B" | sed 's/^/                                   /'
CH=$(hits "$B")
say "control: files holding it" "$CH"
[ "$CH" -eq 0 ] && {
  echo "the search found nothing on a store that is not encrypted; the zero above is not evidence" >&2
  exit 1
}
exit 0
