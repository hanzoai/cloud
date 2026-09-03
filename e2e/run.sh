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
#   make boot                     # build and boot everything, no specs, stays up
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
# The three brokers bind well-known ports — NATS 4222, Kafka 9092, AMQP 5672 —
# and those are the ONLY listeners in the process that a second instance cannot
# share. Left at their defaults, a run on a host that already has a cloud up
# loses all three plugins to "address already in use", and the host reports it
# as "exited before listening", which reads like the plugin is broken.
PUBSUB_PORT="${PUBSUB_PORT:-14222}"
KAFKA_PORT="${KAFKA_PORT:-19092}"
AMQP_PORT="${AMQP_PORT:-15672}"

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
# Minted per run, like the two credentials below, and for the same reason: a
# password written into a file is a password on every disk that file reaches and
# in every copy of this repository's history, forever, for an instance whose data
# directory is deleted on exit. It is printed once at the end instead.
#
# A person or a local chat client that wants to sign in with a value it already
# knows passes it — `PASSWORD=… ./run.sh` — which is what the estate convention
# is for. Stating it here would commit it; taking it from the caller does not.
PASSWORD="${PASSWORD:-$(head -c 18 /dev/urandom | base64 | tr -d '=+/')!aA1}"
CLIENT_ID=hanzo-console
# Fresh per run, unless the caller already has one. BOOT_ONLY leaves the instance
# up for somebody else to talk to, and talking to it needs the credential it was
# seeded with — a caller that cannot know it can only reach the unauthenticated
# surface. `:-` and not an override: a suite that sets nothing still gets a
# random secret that exists only inside a data dir deleted on exit.
SERVICE_TOKEN="${SERVICE_TOKEN:-$(head -c 32 /dev/urandom | base64 | tr -d '=+/')}"
CLIENT_SECRET="${CLIENT_SECRET:-$(head -c 32 /dev/urandom | base64 | tr -d '=+/')}"

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
# A bound port is not a warning here: this instance would come up unable to serve
# on an address it needs, which is worth stopping for. It is not a statement that
# one cloud per host is the limit — every port here is derived from one number
# (dev/up.sh takes them from the project's `port`, and each is overridable), so a
# second instance is a second port block. Measured: four coexisting on this host.
# The per-process ingest engine now takes an ephemeral port, so it is not checked.
for p in "$HTTP_PORT" "$HEALTH_PORT" "$ZAP_PORT" "$TASKS_GATED_PORT"; do
  if ss -ltn "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    holder="$(ss -ltnp "sport = :$p" 2>/dev/null | grep -oP 'pid=\K[0-9]+' | head -1)"
    fail "port $p is in use by pid ${holder:-?}. Either stop it, or run this instance on a different port block — every port here is derived from one number, so a second cloud is a second block (dev/up.sh: the project's \`port\`; here: HTTP_PORT, HEALTH_PORT, ZAP_PORT, TASKS_GATED_PORT)."
  fi
done
# The suite is only needed by the half of this script that runs specs. A local
# boot has no use for it, and refusing one for its absence is refusing the thing
# that works because the thing beside it is missing.
[ -n "${BOOT_ONLY:-}" ] || [ -d "$SUITE" ] ||
  fail "Playwright suite not found at $SUITE — set SUITE=<path to universe/e2e>"

# ── build ────────────────────────────────────────────────────────────────────
# The host AND every app. `make build` is the light host alone, which loads apps
# as plugins from ./bin — so with only the host present every app route answers
# 503 and the suite fails on a product that is fine. Parallel because the fleet is
# 112 independent links; warm runs are seconds.
say "building the host and $(make -s -n apps 2>/dev/null | grep -c 'go build' || echo '?') app binaries"
make -j"$(nproc 2>/dev/null || echo 4)" ship >/dev/null

# The console is a PUBLISHED SITE RELEASE, not a compiled-in bundle (webui/console.go
# carries no bytes; webui/release reads CLOUD_CONSOLE_ORG + CLOUD_CONSOLE_SITE). A
# fresh data dir holds no release, so /login/oauth/authorize answers
# 503 "console unavailable: the bundle has no index.html" and anything that has to
# SIGN IN cannot run.
#
# The note used to name `make webui CONSOLE_DIR=../console`. There is no such
# target — the Makefile says so in as many words, and deleting it is what moved the
# console off this binary's build. Naming a target that cannot be run sent the
# reader looking for it; these are the steps that work.
#
# Publishing needs a USER bearer, which comes from the sign-in this is trying to
# make possible — so on a throwaway boot it is a chicken and egg, and the honest
# thing is to say which specs that costs rather than to imply a one-liner.
if [ ! -s webui/dist/index.html ] || [ "$(wc -c < webui/dist/index.html)" -lt 20000 ]; then
  say "note: no console release — every spec that SIGNS IN will skip."
  say "      the login page is a site release, not a compiled-in bundle:"
  say "        cd ../console && NEXT_PUBLIC_CLOUD_URL=$BASE NEXT_PUBLIC_IAM_URL=$BASE npm run build:embed"
  say "        hanzo sites publish <slug> --source out/     # needs a user bearer"
  say "        CLOUD_CONSOLE_SITE=<slug> ./e2e/run.sh"
fi

# ── boot ─────────────────────────────────────────────────────────────────────
# The master key seals this run's stores. It is random because the data dir is
# fresh: a key that cannot reopen what it never wrote is not a problem, and no
# long-lived key needs to exist in the tree. (A pinned key + a stale data dir is
# the ONE combination that breaks — "unwrap DEK: message authentication failed".)
export CLOUD_KMS_MASTER_KEY_REF="$(head -c 32 /dev/urandom | base64 -w0)"
# The anti-forgery key is ONE key for the whole boot. A CSRF token is minted by
# GET /v1/account/csrf in one process and checked in another, so an app that
# resolves a key of its own can never accept a token anybody else minted — and
# every app that checks one REFUSES TO MOUNT rather than hold a private key.
# Random for the same reason the master key is: nothing outlives this run.
export CONSOLE_CSRF_KEY="$(head -c 32 /dev/urandom | base64 -w0)"
export CLOUD_DATA_DIR="$DATA_DIR"
export CLOUD_ZAP_LISTEN=":$ZAP_PORT"
export CLOUD_TASKS_GATED_PORT="$TASKS_GATED_PORT"
export CLOUD_PUBSUB_PORT="$PUBSUB_PORT"
export CLOUD_KAFKA_PORT="$KAFKA_PORT"
export CLOUD_AMQP_PORT="$AMQP_PORT"
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
# And the ISSUER, because a browser does not take the address it was given — it
# asks. @hanzo/iam builds its authorize URL from `authorization_endpoint` in
# /.well-known/openid-configuration, and cloud derives that document's issuer
# from the BRAND unless this says otherwise. Left to the brand, an instance on
# loopback publishes `https://hanzo.id` for every endpoint, so a local chat
# client pointed here with VITE_HANZO_IAM signs in against PRODUCTION and comes
# back with a token this instance's JWKS cannot verify. Setting it makes the
# instance its own issuer: what it stamps, what it validates, and what it
# advertises are then one address.

# The same discovery, for the ai plugin, which is a separate module with its own
# validator: it reads IAM_ENDPOINT (then IAM_ISSUER) and refuses to guess a trust
# anchor, so with neither set it skips JWKS entirely and parses an empty
# certificate — every completion answers 401 "iam: not valid PEM", which reads as
# a key fault and is a missing address.
# iam pins its issuer from IAM_ISSUER, which it requires to be https — so on a
# loopback boot the only way to say "you are your own issuer" is the dev opt-in
# it publishes for exactly this. Unset, iam falls back to the constant
# `https://hanzo.id` and every endpoint in its discovery document names
# production. Host-relative means `iss` echoes the host the request ARRIVED on,
# so reach this instance as $BASE spells it — 127.0.0.1, not localhost — or the
# `iss` iam stamps and the one cloud validates against are two strings.
export IAM_DEV_HOST_RELATIVE=1
export CLOUD_IAM_ISSUER="${CLOUD_IAM_ISSUER:-$BASE}"
export IAM_ENDPOINT="$BASE"
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
# The app set is a property of the BINARY, not of this script: cmd/cloud mounts
# what manifest.Apps lists and resolves each plugin as a file beside itself, so
# `--enable` was removed (ecafb31c5) and stating the set here a second time is
# what took devnet down twice. Build the plugins this suite needs with
# `make plugin APP=<name>`; the host mounts whichever ones it finds.
# ── the identity store and its signing key ──────────────────────────────────
# IAM will not CREATE either, deliberately: a missing volume is a fault, not an
# empty identity service, and a key minted in-process would die with the process
# and differ between replicas under one kid. Both refusals are right in a
# deployment and both make a fresh directory unservable — which is why this
# script provides them. It is the one caller that KNOWS the directory is new,
# because it made it a moment ago with mktemp, and that is exactly the case those
# refusals distinguish from a volume that failed to mount.
#
# The store only has to EXIST and be a real SQLite file; the ORM migrates it and
# the seed fills it. A zero-byte file is not one — SQLite writes no header until
# something is written — so this creates a table to force it.
mkdir -p "$DATA_DIR/iam" "$DATA_DIR/signing"
python3 - "$DATA_DIR/iam/iam.db" <<'PYEOF'
import sqlite3, sys
c = sqlite3.connect(sys.argv[1])
c.execute("CREATE TABLE IF NOT EXISTS _bootstrap(x)")
c.commit()
c.close()
PYEOF

# One PEM per signing Cert, each file named for the Cert with NO extension — the
# name IS the JWKS kid, which is how the thing that mounts the key and the thing
# that verifies the token agree without a mapping table. The names come from
# init_data.json rather than a list here, so the seed stays the one source.
export IAM_SIGNING_KEYS="$DATA_DIR/signing"
for cert in $(python3 -c '
import json, sys
d = json.load(open(sys.argv[1]))
print(" ".join(c["name"] for c in d.get("certs", []) if c.get("name")))
' "$initDataFile"); do
  openssl genrsa -out "$IAM_SIGNING_KEYS/$cert" 2048 2>/dev/null
  chmod 0400 "$IAM_SIGNING_KEYS/$cert"
done
say "identity: store staged, $(ls "$IAM_SIGNING_KEYS" | wc -w) signing key(s) mounted"

./bin/cloud --brand=hanzo --listen=":$HTTP_PORT" >"$LOG" 2>&1 &
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
# ONE deadline for all of them, not sixty seconds each in turn. The apps come up
# concurrently, so waiting on them serially costs the SUM of the misses: measured
# on this repo, 124 apps of which most never bind a plane socket at all — they
# are lazy and start on first use — which made this loop the longest thing in a
# local boot by an order of magnitude, an hour of waiting for sockets that were
# never coming.
#
# It also reported an app absent for the crime of being checked early. ai, base
# and billing were each named here and each had a socket by the time the loop
# reached the letter c.
deadline=$(( $(date +%s) + 120 ))
missing="$WANT"
while [ -n "$missing" ] && [ "$(date +%s)" -lt "$deadline" ]; do
  still=""
  for app in $missing; do
    [ -S "$DATA_DIR/run/$app.sock" ] || still="$still $app"
  done
  missing="$(echo $still)"
  [ -n "$missing" ] && sleep 1
done
# An app with no socket after that is LAZY, not broken — it binds on first use.
# Naming them is worth doing; calling it a failure is not.
[ -n "$missing" ] && say "lazy (no socket until first use): $(echo $missing | wc -w) apps"
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

# BOOT_ONLY stops here, with a fully built and seeded instance still up. It is
# the "boot the whole thing locally" path: everything above — the binary, all
# 125 apps, isolated ports, a fresh data dir, identity — is exactly what a local
# run wants, and the specs below are the other half of e2e, needing the universe
# suite that a plain local boot does not have.
if [ -n "${BOOT_ONLY:-}" ]; then
  KEEP=1
  say "up:   $BASE"
  say "      health :$HEALTH_PORT · zap :$ZAP_PORT · pubsub :$PUBSUB_PORT · kafka :$KAFKA_PORT · amqp :$AMQP_PORT"
  say "      data $DATA_DIR · log $LOG"
  say "sign in as z@hanzo.ai / $PASSWORD"
  say "stop: kill $CLOUD_PID"
  exit 0
fi

say "running the suite against $BASE"
cd "$SUITE"
[ -d node_modules ] || npm ci --no-audit --no-fund
npm run --silent test:local -- ${E2E_ARGS:-}
