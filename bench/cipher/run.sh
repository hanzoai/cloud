#!/usr/bin/env bash
# What is on the disk.
#
# `self/` measures that a build which cannot encrypt refuses rather than
# pretending. This is the other half: write a value through the API and look for
# it in the files.
#
# Three builds, because they do not agree. cgo is on by default on macOS and
# that build links ordinary SQLite, so it cannot encrypt and says so instead of
# writing a plaintext store with a key set. The pure-Go build can. So can a cgo
# build told to link libsqlcipher. A refusal is as much a result here as a
# ciphertext header, and the run reports it as one.
#
# The control is the development path, which is explicitly not encrypted. It has
# to find the canary or the search is not searching: a store is a binary file and
# grep declines to read one unless told, which is how the first control run here
# reported a clean zero for a file that plainly contained the string.
set -euo pipefail
cd "$(dirname "$0")/../.."
. bench/host.sh

say() { printf '%-30s %s\n' "$1" "$2"; }

host

FLAGS_C=$(pkg-config --cflags sqlcipher 2>/dev/null || echo "-I/opt/homebrew/opt/sqlcipher/include")
FLAGS_L=$(pkg-config --libs sqlcipher 2>/dev/null || echo "-L/opt/homebrew/opt/sqlcipher/lib -lsqlcipher")

PURE=$(mktemp -u); CGO=$(mktemp -u); CIPHER=$(mktemp -u); PID=""
cleanup() {
  [ -n "$PID" ] && { kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true; }
  rm -f "$PURE" "$CGO" "$CIPHER"
}
trap cleanup EXIT
export GOWORK=off

CGO_ENABLED=0 go build -o "$PURE" ./cmd/cloud
go build -o "$CGO" ./cmd/cloud
CGO_ENABLED=1 CGO_CFLAGS="$FLAGS_C" CGO_LDFLAGS="$FLAGS_L" go build -tags libsqlite3 -o "$CIPHER" ./cmd/cloud

# A value nothing else could have written, so a hit is this run's rather than a
# coincidence in someone else's page.
CANARY="canary-$(head -c 8 /dev/urandom | xxd -p)"
hits() { LC_ALL=C grep -ral "$CANARY" "$1" 2>/dev/null | wc -l | tr -d ' '; }
headers() { for f in "$1"/*.db; do head -c 15 "$f" | LC_ALL=C tr -c '[:print:]' '.'; echo; done | sort -u; }

# Start it, write the canary, stop it, look. Prints one row whether it ran or
# refused — a build that will not start with a key is the answer, not an error.
probe() {
  local bin=$1 label=$2 port=$3 dir; dir=$(mktemp -d)
  CLOUD_LISTEN=":$port" CLOUD_ZAP_LISTEN="127.0.0.1:$((port + 1500))" \
  CLOUD_HEALTH_LISTEN="127.0.0.1:$((port + 1000))" CLOUD_ADMIN_LISTEN="127.0.0.1:$((port + 500))" \
  CLOUD_DATA_DIR="$dir" "$bin" serve >"$dir/log" 2>&1 & PID=$!
  local up=no
  for _ in $(seq 1 300); do
    curl -s -m 2 "http://127.0.0.1:$((port + 1000))/healthz" 2>/dev/null | grep -q . && { up=yes; break; }
    kill -0 "$PID" 2>/dev/null || break
    sleep 0.1
  done
  if [ "$up" != yes ]; then
    # The last line, not the first: the same refusal appears earlier as a warn
    # inside a JSON log record, where the quotes truncate it.
    say "$label" "refuses — $(grep 'cek:' "$dir/log" | tail -1 | sed 's/.*cek: //' | cut -c1-72)"
    PID=""; rm -rf "$dir"; return
  fi
  curl -s -o /dev/null -m 8 -X POST "http://127.0.0.1:$port/v1/base/collections/notes" \
    -H 'Content-Type: application/json' -d "{\"doc\":{\"body\":\"$CANARY\"}}"
  kill "$PID" 2>/dev/null; wait "$PID" 2>/dev/null || true; PID=""
  say "$label" "$(ls "$dir"/*.db | wc -l | tr -d ' ') stores · $(headers "$dir" | wc -l | tr -d ' ') distinct headers · canary in $(hits "$dir") files"
  CONTROL_DIR="$dir"
}

echo "── With a master key ──"
export CLOUD_KMS_MASTER_KEY_REF="$(openssl rand -base64 32)"
unset CLOUD_DEV_UNENCRYPTED || true
probe "$PURE" "CGO_ENABLED=0" 18120; rm -rf "${CONTROL_DIR:-}"
probe "$CGO" "CGO_ENABLED=1, as built" 18130; rm -rf "${CONTROL_DIR:-}"
probe "$CIPHER" "CGO_ENABLED=1 -tags libsqlite3" 18140; rm -rf "${CONTROL_DIR:-}"

echo
echo "── The control: the path that says it does not encrypt ──"
unset CLOUD_KMS_MASTER_KEY_REF
export CLOUD_DEV_UNENCRYPTED=1
probe "$CIPHER" "CLOUD_DEV_UNENCRYPTED=1" 18150
CH=$(hits "${CONTROL_DIR:-/nonexistent}")
rm -rf "${CONTROL_DIR:-}"
[ "${CH:-0}" -eq 0 ] && {
  echo "the search found nothing on a store that is not encrypted; the zeros above are not evidence" >&2
  exit 1
}
exit 0
