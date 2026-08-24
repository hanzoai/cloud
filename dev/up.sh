#!/usr/bin/env bash
# Bring the whole cloud up for one local project.
#
# The project is declared in hanzo.config.js and nothing else. The stack it runs
# on is the SAME binary that serves api.hanzo.ai — one host, every capability,
# each started on the first request that reaches it. There is no local-only
# build and no reduced mode: what answers here is what answers in production.
#
# Boot is e2e/run.sh with BOOT_ONLY, which is the one place that knows how to
# bring this binary up and seed an identity. Repeating that here would be a
# second boot path, and the two would agree only until someone edited one.
set -euo pipefail
cd "$(dirname "$0")/.."

CONFIG="${1:-dev/hanzo.config.js}"
[ -f "$CONFIG" ] || { echo "no $CONFIG — declare the project first"; exit 1; }

CFG="$(node dev/config.mjs "$CONFIG")"
name=$(printf '%s' "$CFG" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).name))')
org=$(printf '%s' "$CFG"  | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).org))')
serve=$(printf '%s' "$CFG"| node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).serve))')
port=$(printf '%s' "$CFG" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>console.log(JSON.parse(s).port))')
root="$(cd "$(dirname "$CONFIG")" && cd "$(dirname "$serve")" 2>/dev/null && pwd)/$(basename "$serve")"
[ -d "$root" ] || { echo "serve: $root does not exist"; exit 1; }

echo ">> $name.localhost:$port  org=$org  serving $root"

# The apex is what makes <name>.localhost a SITE rather than a path on the
# console: apps/sites reads CLOUD_SITES_APEX and routes any <slug>.<apex> to the
# project of that slug. run.sh never sets it, so this reaches the binary.
export CLOUD_SITES_APEX=localhost
# ONE number in the config, a contiguous block from it. Every listener in this
# process is a port somebody else's instance may already hold — the three
# brokers bind well-known ones — so deriving them all from the project's port
# is what lets two projects, or a project and a test run, be up at once.
export HTTP_PORT="$port"
export HEALTH_PORT=$((port + 1))
export ZAP_PORT=$((port + 2))
export TASKS_GATED_PORT=$((port + 3))
export PUBSUB_PORT=$((port + 4))
export KAFKA_PORT=$((port + 5))
export AMQP_PORT=$((port + 6))
# Chosen HERE so this script can talk to what it booted. run.sh randomises when
# the caller sets nothing, which is still what every suite gets.
export CLIENT_SECRET="${CLIENT_SECRET:-$(head -c 32 /dev/urandom | base64 | tr -d '=+/')}"
export SERVICE_TOKEN="${SERVICE_TOKEN:-$(head -c 32 /dev/urandom | base64 | tr -d '=+/')}"
BOOT_ONLY=1 ./e2e/run.sh

BASE="http://127.0.0.1:$port"

# The identity run.sh seeds. A user token, not the service token: a project
# belongs to whoever created it, and the service token belongs to nobody.
# -f would turn a refusal into an exit code and throw the body away — the same
# trade that made the KMS step unable to say why it failed. Keep the body.
login=$(curl -sS -X POST "$BASE/v1/iam/oauth/token" \
  -H 'content-type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=password' \
  --data-urlencode 'username=z' \
  --data-urlencode 'password=***REMOVED***' \
  --data-urlencode 'client_id=hanzo-console' \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  )
tok=$(printf '%s' "$login" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);process.stdout.write(j.access_token||j.accessToken||"")}catch{}})')
[ -n "$tok" ] || { echo "sign-in as $org/z failed: $login"; exit 1; }

curl -sS -X POST "$BASE/v1/projects" -H "Authorization: Bearer $tok" \
  -H 'content-type: application/json' -d "{\"slug\":\"$name\",\"name\":\"$name\"}" >/dev/null || true

echo ">> up:  $BASE"
echo ">> api: curl $BASE/v1            # every capability, each started on first use"
echo ">> app: curl -H 'Host: $name.localhost' $BASE/"

# Serving the FILES is the one thing this host cannot do alone. apps/projects
# keeps a site's bytes in object storage and reads them back through the same
# client in both places, so there is no from-disk mode to fall back to — without
# a store the deploy answers 503 and the address answers "site not found".
# Say that here, once, rather than let it surface as a 404 nobody can explain.
if ! curl -sS "$BASE/v1/s3/health" 2>/dev/null | grep -q '"ok"'; then
  echo ">> note: $name.localhost answers 404 until object storage is configured."
  echo ">>       The API above is complete without it; only a site's BYTES need a store."
  echo ">>       Point S3_ACCESS_KEY/S3_SECRET_KEY/S3_ENDPOINT at one and re-run."
fi
