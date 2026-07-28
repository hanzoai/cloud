#!/usr/bin/env bash
# Publish every repo in the GitHub org `hanzo-templates` as a live demo at
# https://<slug>.hanzo.app, owned by the `hanzo` org so it is editable in
# console, with the Hanzo runtime injected.
#
# ONE path in: POST /v1/projects (the project) + POST /v1/projects/<slug>/deploy
# with a tar.gz of the built site — the SAME artifact route the console's
# one-click deploy uses, so the demos get the identical publishSite lifecycle
# (versioned deployment -> S3 origin -> first-come host bind -> edge purge).
# The JSON manifest route (/v1/sites/deploy) is deliberately NOT used: its
# `content` is a JSON string, so a template's images/fonts/video cannot survive
# it, and a demo with broken images is not a demo.
#
# Credentials come from the environment, never this file: HANZO_TOKEN, or an
# IAM grant from IAM_CLIENT_ID/IAM_CLIENT_SECRET (+ IAM_USERNAME/IAM_PASSWORD
# for the human grant). Source them from KMS.
set -uo pipefail

ORG=${ORG:-hanzo}
API=${API:-https://api.hanzo.ai}
IAM=${IAM:-https://hanzo.id}                 # OIDC issuer; token_endpoint is /v1/iam/oauth/token
GHORG=${GHORG:-hanzo-templates}
W=${W:-$HOME/.cache/hanzo-templates}
LOG=$W/deploy.log
# The live edge rejects a request body over 4 MiB, so an artifact must fit under
# it; prep.py re-encodes heavy media in place until it does.
CAP=${CAP:-4000000}

mkdir -p "$W"
log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$LOG"; }

# ── auth ─────────────────────────────────────────────────────────────────────
# The org a deploy lands in is NOT the X-Org-Id header — cloud's identity
# boundary (SanitizeIdentity) strips that and re-mints it from the validated
# token, so the token itself has to belong to `hanzo`: a member's `orgs` claim
# (home org first) or a client_credentials app owned by the org.
TOKEN=${HANZO_TOKEN:-}
if [ -z "$TOKEN" ]; then
  [ -n "${IAM_CLIENT_ID:-}" ] || { log "FATAL: set HANZO_TOKEN or IAM_CLIENT_ID/IAM_CLIENT_SECRET"; exit 1; }
  if [ -n "${IAM_USERNAME:-}" ]; then
    GRANT=(--data-urlencode "grant_type=password" --data-urlencode "username=$IAM_USERNAME" --data-urlencode "password=${IAM_PASSWORD:-}")
  else
    GRANT=(--data-urlencode "grant_type=client_credentials")
  fi
  TOKEN=$(curl -s -X POST "$IAM/v1/iam/oauth/token" \
    --data-urlencode "client_id=$IAM_CLIENT_ID" --data-urlencode "client_secret=${IAM_CLIENT_SECRET:-}" \
    "${GRANT[@]}" --max-time 30 | jq -r '.access_token // empty')
fi
[ -n "$TOKEN" ] || { log "FATAL: could not mint an IAM token"; exit 1; }
HDR=(-H "Authorization: Bearer $TOKEN" -H "X-Org-Id: $ORG")

# Refuse to run against the wrong tenant: one org-scoped read proves the token's
# effective org before anything is written.
curl -s -o /dev/null -w '%{http_code}' "$API/v1/sites" "${HDR[@]}" --max-time 30 | grep -q 200 \
  || { log "FATAL: token is not scoped to org '$ORG' (GET /v1/sites refused)"; exit 1; }
log "auth ok: org=$ORG"

# ── the repo list ────────────────────────────────────────────────────────────
gh repo list "$GHORG" --limit 300 --json name,isArchived \
  | jq -r '.[] | select(.isArchived|not) | .name' > "$W/repos.txt"
log "templates: $(wc -l < "$W/repos.txt")"

ok=0; fail=0; skip=0
while read -r slug; do
  [ -n "$slug" ] || continue
  d=$W/src/$slug; art=$W/art/$slug.tgz
  mkdir -p "$W/src" "$W/art"
  [ -d "$d" ] || gh repo clone "$GHORG/$slug" "$d" -- --depth 1 -q 2>/dev/null || { log "CLONE-FAIL $slug"; fail=$((fail+1)); continue; }

  # Build (when needed), inject the runtime, and pack — one tool, prep.py.
  r=$(python3 "$(dirname "$0")/prep.py" "$d" "$art" "$CAP" 2>/dev/null | tail -1)
  case "$r" in OK*) : ;; *) log "NO-SITE $slug"; skip=$((skip+1)); continue ;; esac

  curl -s -o /dev/null -X POST "$API/v1/projects" "${HDR[@]}" -H 'Content-Type: application/json' \
    -d "{\"slug\":\"$slug\",\"name\":\"$slug\",\"framework\":\"static\"}" --max-time 40
  code=$(curl -s -o "$W/art/$slug.resp" -w '%{http_code}' -X POST "$API/v1/projects/$slug/deploy" \
    "${HDR[@]}" -H 'Content-Type: application/gzip' --data-binary @"$art" --max-time 300)
  if [ "$code" = "200" ]; then
    log "OK $slug ${r#OK } -> https://$slug.hanzo.app"; ok=$((ok+1))
  else
    log "DEPLOY-FAIL $slug http=$code $(head -c 120 "$W/art/$slug.resp" | tr -d '\n')"; fail=$((fail+1))
  fi
done < "$W/repos.txt"

log "DONE ok=$ok fail=$fail skip=$skip"
