#!/usr/bin/env bash
# Seed the ONE published plan ladder: Go / Dev / Pro / Max.
#
# The catalog lives in commerce's store, not in code, so it is changed through the
# SuperAdmin CRUD that admin.hanzo.ai's plan editor drives — not by editing a
# marketing page. That direction matters: hanzo.ai reads GET /v1/billing/plans
# live, so the site follows the catalog automatically. Editing the page instead is
# how Pro came to be published at $49 while billing took $20.
#
#   PUBLIC READ   GET  /v1/billing/plans          (anonymous, 200)
#   ADMIN CRUD    GET/POST/PUT/DELETE /v1/plans/entries[/:slug]   (SuperAdmin)
#
# Safe to run destructively today because nothing has launched and there are no
# subscribers. That is the ONLY reason the retire step below is acceptable — with
# live subscriptions, repricing pro 20 -> 49 in place charges people 2.45x on
# their next renewal with no notice, and retiring a slug strands whoever is on it.
# If that ever changes, grandfather first and reprice second.
#
# Usage:
#   HANZO_ADMIN_TOKEN=<superadmin bearer> ./scripts/seed-plans.sh          # apply
#   HANZO_ADMIN_TOKEN=<…> DRY_RUN=1 ./scripts/seed-plans.sh                # show only
set -euo pipefail

API="${HANZO_API:-https://api.hanzo.ai}"
TOKEN="${HANZO_ADMIN_TOKEN:-}"
DRY="${DRY_RUN:-}"

[ -n "$TOKEN" ] || { echo "FATAL: set HANZO_ADMIN_TOKEN to a SuperAdmin bearer" >&2; exit 1; }

# Prices are CENTS, as the catalog stores them. Written once here so the ladder is
# stated in exactly one place.
read -r -d '' PLANS <<'JSON' || true
[
  {
    "slug": "go", "name": "Go", "category": "personal",
    "description": "Omnichannel agent hosting and the Hanzo desktop app, for builders getting started.",
    "price": 900, "priceAnnual": 900, "currency": "usd",
    "interval": "monthly", "intervalCount": 1, "trialPeriodDays": 0,
    "features": [
      "Hanzo Bot Gateway — host local agents across WhatsApp, Telegram, Discord and Slack",
      "Hanzo Chat & UI — desktop app layout with instant global model context",
      "$5 of foundational API gateway credits included"
    ],
    "limits": { "includedCreditsUsd": 5 }
  },
  {
    "slug": "dev", "name": "Dev", "category": "personal",
    "description": "Terminal-native pair programming in your own repository shell.",
    "price": 1900, "priceAnnual": 1900, "currency": "usd",
    "interval": "monthly", "intervalCount": 1, "trialPeriodDays": 0,
    "features": [
      "Hanzo Dev — full terminal-native pair programmer inside your local repo shell",
      "Medium reasoning dial — balanced latency-to-depth for standard code generation",
      "Unlimited local context — codebase indexing via the native desktop client",
      "250 compute/API credits per month"
    ],
    "limits": { "includedCredits": 250 }
  },
  {
    "slug": "pro", "name": "Pro", "category": "personal",
    "description": "Multi-agent orchestration, background review, and maximum reasoning depth.",
    "price": 4900, "priceAnnual": 4900, "currency": "usd",
    "interval": "monthly", "intervalCount": 1, "trialPeriodDays": 0,
    "features": [
      "Auto Drive orchestration — multi-agent teams executing repo milestones (/plan, /code, /solve)",
      "Auto Review — background watcher running changes in hidden git worktrees to suggest fixes silently",
      "High and XHigh reasoning dials — maximum context processing depth",
      "Headless browser CDP — the agent tests and screenshots its own front-end work",
      "1,000 compute/API credits per month"
    ],
    "limits": { "includedCredits": 1000 }
  },
  {
    "slug": "max", "name": "Max", "category": "personal",
    "description": "Enso orchestration, unlimited managed agents, and shared team workspaces.",
    "price": 9900, "priceAnnual": 9900, "currency": "usd",
    "interval": "monthly", "intervalCount": 1, "trialPeriodDays": 0,
    "features": [
      "Enso orchestration — dedicated routing through Hanzo's foundational framework model",
      "Unlimited managed agents — continuous background scheduling to unified team dashboards",
      "Shared team workspaces — RBAC, KMS-backed credentials, centralized billing",
      "2,500 compute/API credits per month",
      "Priority GPU inference queues"
    ],
    "limits": { "includedCredits": 2500 }
  }
]
JSON

# Everything not on the ladder. Retired rather than left showing, because a row in
# the catalog is a row the site can render and billing can charge.
RETIRE="developer plus team-max enterprise custom \
        world-free world-pro world-team world-enterprise \
        social-free social-pro social-team social-team-max social-enterprise \
        dns-free dns-pro dns-enterprise"

api() { curl -fsS -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' "$@"; }

echo "== current catalog =="
curl -fsS "${API}/v1/billing/plans" | python3 -c "
import sys,json
for p in json.load(sys.stdin): print(f\"  {p['category']:11} {p['slug']:20} \${(p.get('price') or 0)//100}\")"

if [ -n "$DRY" ]; then echo; echo "DRY_RUN — nothing sent."; echo "$PLANS" | python3 -m json.tool | head -20; exit 0; fi

echo
echo "== upserting the ladder =="
echo "$PLANS" | python3 -c "
import sys,json
for p in json.load(sys.stdin): print(json.dumps(p))
" | while read -r row; do
  slug=$(printf '%s' "$row" | python3 -c 'import sys,json;print(json.load(sys.stdin)["slug"])')
  # PUT updates an existing slug; POST creates. Try PUT, fall back to POST, so the
  # script is idempotent and safe to re-run.
  if api -X PUT "${API}/v1/plans/entries/${slug}" -d "$row" >/dev/null 2>&1; then
    echo "  updated ${slug}"
  elif api -X POST "${API}/v1/plans/entries" -d "$row" >/dev/null 2>&1; then
    echo "  created ${slug}"
  else
    echo "  FAILED ${slug}" >&2; exit 1
  fi
done

echo
echo "== retiring everything off the ladder =="
for slug in $RETIRE; do
  if api -X DELETE "${API}/v1/plans/entries/${slug}" >/dev/null 2>&1; then
    echo "  retired ${slug}"
  else
    echo "  absent  ${slug}"
  fi
done

echo
echo "== resulting catalog =="
curl -fsS "${API}/v1/billing/plans" | python3 -c "
import sys,json
rows=json.load(sys.stdin)
for p in rows: print(f\"  {p['category']:11} {p['slug']:20} \${(p.get('price') or 0)//100}\")
want={'go':9,'dev':19,'pro':49,'max':99}
got={p['slug']:(p.get('price') or 0)//100 for p in rows}
bad=[s for s,v in want.items() if got.get(s)!=v] + [s for s in got if s not in want]
print()
print('  LADDER CLEAN' if not bad else '  MISMATCH: '+', '.join(bad))
"
