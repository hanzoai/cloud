#!/usr/bin/env bash
# run.sh — boot THIS repo's cloud binary locally and drive it with the real
# Playwright suite. The whole of `make e2e`.
#
#   build the binary → boot on isolated ports with a fresh data dir
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
# The cluster-reachable tasks listener. Isolated like every other port here so a
# run does not collide with a dev stack — or another agent — already on this host.
TASKS_GATED_PORT="${TASKS_GATED_PORT:-19998}"

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
# A bound port is not a warning here. 9999 in particular: it is the ONE
# cluster-reachable tasks listener and still a compile-time constant, so a leftover
# instance holding it stops this one from serving durable work to its consumers.
# The per-process ingest engine now takes an ephemeral port, so it is not checked.
for p in "$HTTP_PORT" "$HEALTH_PORT" "$ZAP_PORT" "$TASKS_GATED_PORT"; do
  if ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    holder="$(ss -ltnp "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)"
    fail "port $p is in use by pid ${holder:-?} — another cloud instance is up. Stop it (\`kill -9 ${holder:-<pid>}\`). The gated tasks port $TASKS_GATED_PORT is a compile-time constant, so two instances can never coexist on one host."
  fi
done
[ -d "$SUITE" ] || fail "Playwright suite not found at $SUITE — set SUITE=<path to universe/e2e>"

# ── build ────────────────────────────────────────────────────────────────────
# The host AND every app. `make build` is the light host alone, which loads apps
# as plugins from ./bin — so with only the host present every app route answers
# 503 and the suite fails on a product that is fine. Parallel because the fleet is
# 112 independent links; warm runs are seconds.
say "building the host and $(make -s -n apps 2>/dev/null | grep -c 'go build' || echo '?') app binaries"
make -j"$(nproc 2>/dev/null || echo 4)" ship >/dev/null

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
export CLOUD_TASKS_GATED_PORT="$TASKS_GATED_PORT"
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

# The prepaid gate only enforces on a kind that COSTS something: ResourceMeter.Gate
# short-circuits to allow for costCents<=0, and every todo fee defaults to 0. Price
# it here so spec 136 exercises a real refusal, and hand the SAME number to the suite
# so the fee has one source of truth rather than two that can drift.
export CLOUD_TODO_FEE_CENTS=500
export E2E_TODO_FEE_CENTS="$CLOUD_TODO_FEE_CENTS"

say "booting cloud on $BASE (data: $DATA_DIR)"
# commerce carries the money plane the billing surface reads, and todo supplies a
# gated create that depends on nothing but the local store — so a 402 in spec 136 is
# the billing gate and not some absent upstream answering first.
./bin/cloud --enable=iam,base,kms,marketing,notify,billing,commerce,todo --brand=hanzo --listen=":$HTTP_PORT" >"$LOG" 2>&1 &
CLOUD_PID=$!

# Readiness is the HOST answering /healthz on its own app port. Liveness belongs to
# the host rather than to any app: it must answer while every plugin is still cold,
# or a lazily-started fleet fails its probe before the first real request arrives.
# There is no separate health listener in the light host — waiting on one is
# waiting on a port nothing binds, which reads as "cloud never came up" when in
# fact it came up fine. Not a sleep; if the process dies we say so immediately.
for i in $(seq 1 90); do
  kill -0 "$CLOUD_PID" 2>/dev/null || { tail -30 "$LOG"; fail "cloud exited during boot"; }
  if curl -fsS "$BASE/healthz" 2>/dev/null | grep -q '"status":"ok"'; then
    say "ready in ${i}s"
    break
  fi
  [ "$i" = 90 ] && { tail -30 "$LOG"; fail "cloud did not become ready in 90s"; }
  sleep 1
done

# WAKE THE FLEET FIRST. Apps are their own processes now and the host starts each
# on the first request that reaches its prefix, so every assertion below — the IAM
# seed, the drip engine, commerce's embed — is about a process that does not exist
# until something asks it for something. Checking the log before that reads a boot
# that has not happened yet and fails a stack that is fine.
#
# The prefixes come from the host's OWN "loaded plugin" lines rather than a list
# kept here: the host already knows what it routes where, and a second copy would
# be the thing that goes stale when an app moves. The response does not matter —
# 401, 403 and 404 all mean the child answered, which is the point.
WANT="$(grep -o '"name":"[^"]*","prefix"' "$LOG" | cut -d'"' -f4 | sort -u)"
say "waking $(echo "$WANT" | wc -w) apps"
grep -o '"prefix":"[^"]*"' "$LOG" | cut -d'"' -f4 | sort -u | while read -r pfx; do
  curl -fsS -o /dev/null --max-time 30 "$BASE$pfx" >/dev/null 2>&1 || true
done

# WAIT for them, do not just poke them. An app's unix socket is created by
# rpc.Listen once it is actually serving, so the socket IS the readiness signal —
# the same fact Dial relies on ("a socket means the app is up"). Polling it beats
# a sleep and beats grepping the log, because it is the thing the next caller will
# itself depend on.
#
# commerce is the one that makes this necessary: it runs migrations and seeds a
# catalog before it listens, which takes longer than the host's start timeout, and
# a spec that asks billing for a balance in that window gets "no such file or
# directory" for a ledger that is merely still booting.
for app in $WANT; do
  for _ in $(seq 1 60); do
    [ -S "$DATA_DIR/run/$app.sock" ] && break
    sleep 1
  done
  [ -S "$DATA_DIR/run/$app.sock" ] || say "note: $app never served its socket — specs touching it will fail"
done
say "up: $(ls "$DATA_DIR/run" 2>/dev/null | wc -l) sockets"

# The seed is non-fatal inside cloud (a missing file only WARNS), so verify it
# actually applied — an unseeded IAM has no signing cert and can issue no token.
grep -q '"message":"iam seed applied"' "$LOG" || {
  grep 'iam seed' "$LOG" || true
  fail "IAM seed did not apply — $initDataFile"
}
say "drip engine: $(grep -c 'email drip engine live' "$LOG") live"

# commerce degrades to a fail-closed 503 on its own prefixes rather than crashing the
# binary, which is right for production and useless for a test run: every money
# assertion would fail against a plane that was never up, and the reason would be 200
# lines back in the log. Name it here instead.
if grep -q 'commerce embed failed' "$LOG"; then
  grep -o '"err":"[^"]*"' <(grep 'commerce embed failed' "$LOG") | head -1
  fail "commerce did not boot — the money plane is fail-closed 503 and specs 135/136 cannot pass"
fi

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
