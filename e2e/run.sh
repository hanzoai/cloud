#!/usr/bin/env bash
# run.sh — boot THIS repo's cloud binary locally and drive it with the real
# Playwright suite. The whole of `make e2e`.
#
#   build (rust staticlib + binary) → boot on isolated ports with a fresh data dir
#   → wait for readiness → seed identity → run the specs → tear down
#
# Exits non-zero if any step or any spec fails. Needs no cluster, no KMS, no
# network: everything it talks to, it started.
#
#   make e2e                      # the full run
#   make e2e E2E_ARGS=--headed    # watch the browser
#   KEEP=1 make e2e               # leave the binary up for poking afterwards
set -euo pipefail

cd "$(dirname "$0")/.."
ROOT="$PWD"
SUITE="${SUITE:-$ROOT/../universe/e2e}"

# Isolated by default so a run never collides with a dev instance on 8080.
HTTP_PORT="${HTTP_PORT:-18080}"
HEALTH_PORT="${HEALTH_PORT:-19090}"
ZAP_PORT="${ZAP_PORT:-19653}"
# NOT configurable: cloud.durableZAPPort is a compile-time constant, and the drip
# engine that delivers marketing mail is bound to it. See the preflight below.
TASKS_PORT=19999

DATA_DIR="${DATA_DIR:-$(mktemp -d -t hanzo-e2e.XXXXXX)}"
LOG="$DATA_DIR/cloud.log"
BASE="http://127.0.0.1:${HTTP_PORT}"

# ── test fixtures ────────────────────────────────────────────────────────────
# These are FIXTURES for a throwaway loopback instance that this script creates
# and destroys. They are not configuration, they are not secrets, and nothing
# here is a production credential: real deployments take every one of these from
# KMS. The service token is what the K8s operator would present; the client
# secret and password exist only inside this data dir, which is deleted on exit.
ORG=hanzo
OTHER_ORG=acme
PASSWORD='***REMOVED***'
CLIENT_ID=hanzo-console
SERVICE_TOKEN="$(head -c 32 /dev/urandom | base64 | tr -d '=+/')"
CLIENT_SECRET="$(head -c 32 /dev/urandom | base64 | tr -d '=+/')"

say()  { printf '\033[36m>> %s\033[0m\n' "$*"; }
fail() { printf '\033[31m!! %s\033[0m\n' "$*" >&2; exit 1; }

CLOUD_PID=""
cleanup() {
  local rc=$?
  if [ -n "$CLOUD_PID" ] && kill -0 "$CLOUD_PID" 2>/dev/null; then
    if [ "${KEEP:-0}" = "1" ]; then
      say "KEEP=1 — cloud still running: pid=$CLOUD_PID $BASE (data: $DATA_DIR)"
      return $rc
    fi
    # SIGTERM, then INSIST. The binary can linger in graceful shutdown while still
    # holding the tasks listeners (19999/9999) — and those are compile-time
    # constants, so a lingering process blocks the NEXT run with a port clash that
    # looks nothing like the real cause. Escalate rather than leave that behind.
    kill "$CLOUD_PID" 2>/dev/null || true
    for _ in $(seq 1 20); do
      kill -0 "$CLOUD_PID" 2>/dev/null || break
      sleep 0.5
    done
    kill -9 "$CLOUD_PID" 2>/dev/null || true
    wait "$CLOUD_PID" 2>/dev/null || true
  fi
  if [ "${KEEP:-0}" != "1" ]; then
    [ $rc -eq 0 ] && rm -rf "$DATA_DIR" || say "logs kept for triage: $LOG"
  fi
  return $rc
}
trap cleanup EXIT

# ── preflight ────────────────────────────────────────────────────────────────
# A bound port is not a warning here. 19999 in particular: if the embedded tasks
# engine cannot bind it, cloud logs one line and carries on with the drip engine
# IDLE — marketing mail is then never delivered and the mail spec fails with a
# timeout that looks like a product bug. Refuse to start instead.
for p in "$HTTP_PORT" "$HEALTH_PORT" "$ZAP_PORT" "$TASKS_PORT"; do
  if ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    holder="$(ss -ltnp "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)"
    fail "port $p is in use by pid ${holder:-?} — another cloud instance is up. Stop it (\`kill -9 ${holder:-<pid>}\`). The tasks ports $TASKS_PORT/9999 are compile-time constants, so two instances can never coexist on one host."
  fi
done
[ -d "$SUITE" ] || fail "Playwright suite not found at $SUITE — set SUITE=<path to universe/e2e>"

# ── build ────────────────────────────────────────────────────────────────────
# cargo lives in ~/.cargo/bin and is not on a default PATH; the staticlib it
# produces is what makes `go build ./...` succeed repo-wide.
export PATH="$HOME/.cargo/bin:$PATH"
command -v cargo >/dev/null || fail "cargo not found — install Rust (the native flags staticlib is required to link)"
say "building native/flags staticlib"
make native >/dev/null
say "building bin/cloud"
make build >/dev/null

# The console bundle is go:embed'd at COMPILE time. A fresh clone carries only the
# fallback shell, and the UI spec says so rather than pretending.
if [ "$(wc -c < webui/dist/index.html)" -lt 20000 ]; then
  say "note: webui/dist holds the FALLBACK shell — UI specs will skip."
  say "      build the real console first: make webui CONSOLE_DIR=../console"
fi

# ── boot ─────────────────────────────────────────────────────────────────────
# The master key seals this run's stores. It is random because the data dir is
# fresh: a key that cannot reopen what it never wrote is not a problem, and no
# long-lived key needs to exist in the tree. (A pinned key + a stale data dir is
# the ONE combination that breaks — "unwrap DEK: message authentication failed".)
export CLOUD_KMS_MASTER_KEY_REF="$(head -c 32 /dev/urandom | base64 -w0)"
export CLOUD_DATA_DIR="$DATA_DIR"
export CLOUD_ZAP_LISTEN=":$ZAP_PORT"
export CLOUD_HEALTH_LISTEN=":$HEALTH_PORT"
export initDataFile="$ROOT/e2e/init_data.json"   # camelCase: the key IAM reads
export IAM_SERVICE_TOKEN="$SERVICE_TOKEN"
# Key discovery MUST point at the IAM that signed the token — the one embedded in
# this very process. The issuer identity is left exactly as production
# (iss=https://hanzo.id, which cloud's validator also derives from the brand), so
# only KEY LOOKUP moves. Without this cloud fetches JWKS from the public hanzo.id,
# fails to find this run's `cert-signing` kid, silently drops the principal, and
# every org-scoped route answers "org scope required" — a 403 that reads exactly
# like a product bug.
export CLOUD_JWKS_URL="$BASE/v1/iam/.well-known/jwks"
export E2E_CLIENT_SECRET="$CLIENT_SECRET"        # ${VAR} substitution in init_data.json
export E2E_REDIRECT_URI="$BASE/auth/callback"

say "booting cloud on $BASE (data: $DATA_DIR)"
./bin/cloud --enable=iam,base,kms,marketing,notify --brand=hanzo --listen=":$HTTP_PORT" >"$LOG" 2>&1 &
CLOUD_PID=$!

# Readiness is the health LISTENER answering ok — not the HTTP port, which 404s
# /healthz, and not a sleep. If the process dies we say so immediately.
for i in $(seq 1 90); do
  kill -0 "$CLOUD_PID" 2>/dev/null || { tail -30 "$LOG"; fail "cloud exited during boot"; }
  if curl -fsS "http://127.0.0.1:${HEALTH_PORT}/healthz" 2>/dev/null | grep -q '"status":"ok"'; then
    say "ready in ${i}s"
    break
  fi
  [ "$i" = 90 ] && { tail -30 "$LOG"; fail "cloud did not become ready in 90s"; }
  sleep 1
done

# The seed is non-fatal inside cloud (a missing file only WARNS), so verify it
# actually applied — an unseeded IAM has no signing cert and can issue no token.
grep -q '"message":"iam seed applied"' "$LOG" || {
  grep 'iam seed' "$LOG" || true
  fail "IAM seed did not apply — $initDataFile"
}
say "drip engine: $(grep -c 'email drip engine live' "$LOG") live"

# ── seed identity ────────────────────────────────────────────────────────────
# init_data.json seeds identity CONFIG only (orgs/apps/certs) — by design it
# cannot create users. Users come through the same operator-driven upsert the
# Hanzo K8s operator reconciles against, so this seeds exactly the way production
# does, and the password is argon2id-hashed server-side. Never stored in plaintext.
seed_user() {
  local owner="$1" name="$2" email="$3" admin="${4:-false}"
  local out
  out="$(curl -fsS -X POST "$BASE/v1/iam/admin/users/upsert" \
    -H "Authorization: Bearer $SERVICE_TOKEN" -H 'content-type: application/json' \
    -d "{\"owner\":\"$owner\",\"name\":\"$name\",\"email\":\"$email\",\"password\":\"$PASSWORD\",\"isAdmin\":$admin}")"
  case "$out" in *'"status":"ok"'*) ;; *) fail "seed $owner/$name failed: $out" ;; esac
}
say "seeding identity"
seed_user "$ORG"       z      z@hanzo.ai      true
seed_user "$ORG"       ada    ada@hanzo.ai
seed_user "$ORG"       optout optout@hanzo.ai
seed_user "$OTHER_ORG" eve    eve@acme.com

# ── run ──────────────────────────────────────────────────────────────────────
export E2E_LOCAL_STACK=1
export E2E_CLOUD_URL="$BASE" E2E_CONSOLE_URL="$BASE" E2E_IAM_GRANT_URL="$BASE" E2E_BASE_DOMAIN="127.0.0.1:${HTTP_PORT}"
export E2E_TENANT_ORG="$ORG" E2E_OTHER_ORG="$OTHER_ORG"
export E2E_TENANT_USER=z E2E_LOCAL_USER=z E2E_LOCAL_OTHER_USER=eve
export E2E_TENANT_PASSWORD="$PASSWORD"
export E2E_TENANT_CLIENT_ID="$CLIENT_ID" E2E_IAM_MINT_CLIENT_SECRET="$CLIENT_SECRET"

say "running the suite against $BASE"
cd "$SUITE"
[ -d node_modules ] || npm ci --no-audit --no-fund
npm run --silent test:local -- ${E2E_ARGS:-}
