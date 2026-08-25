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
# Deploying a site is METERED — $1.00 a deploy by default, and an org with no
# balance is refused 402. That is the money plane working. A local stack has no
# biller and no way to fund one that is not deliberately hard (IAM forbids a
# reserved-org client on the public token endpoint, which is what a SuperAdmin
# grant would need), so it takes the free tier the code already names: a fee of
# 0 is un-gated, and it is operator configuration rather than a bypass.
export CLOUD_HOSTING_FEE_CENTS="${CLOUD_HOSTING_FEE_CENTS:-0}"
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
export PASSWORD="${PASSWORD:-***REMOVED***}"
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
  --data-urlencode "password=$PASSWORD" \
  --data-urlencode 'client_id=hanzo-console' \
  --data-urlencode "client_secret=$CLIENT_SECRET" \
  )
tok=$(printf '%s' "$login" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);process.stdout.write(j.access_token||j.accessToken||"")}catch{}})')
[ -n "$tok" ] || { echo "sign-in as $org/z failed: $login"; exit 1; }

curl -sS -X POST "$BASE/v1/projects" -H "Authorization: Bearer $tok" \
  -H 'content-type: application/json' -d "{\"slug\":\"$name\",\"name\":\"$name\"}" >/dev/null || true

# The project's files. `serve` is a directory of bytes and the site plane keeps
# them in object storage, so this is where the local stack needs a store — see
# the note below when it has none.
files=$(node -e '
const {readdirSync,readFileSync,statSync}=require("fs"), {join,relative}=require("path");
const root=process.argv[1], out=[];
(function walk(d){ for (const e of readdirSync(d,{withFileTypes:true})) {
  const p=join(d,e.name);
  if (e.isDirectory()) walk(p);
  else if (statSync(p).size <= 8*1024*1024) out.push({path:relative(root,p).split("\\").join("/"),content:readFileSync(p,"utf8")});
} })(root);
process.stdout.write(JSON.stringify({slug:process.argv[2],files:out}));
' "$root" "$name")
deploy=$(curl -sS -X POST "$BASE/v1/projects/sites/deploy" -H "Authorization: Bearer $tok" \
  -H 'content-type: application/json' --data-binary "$files")
case "$deploy" in
  *'"status":"live"'*) echo ">> deployed: $(printf '%s' "$deploy" | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{try{const j=JSON.parse(s);process.stdout.write(j.files.length+" file(s)")}catch{process.stdout.write("?")}})')" ;;
  *) echo ">> deploy did not go live: $deploy" ;;
esac

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
