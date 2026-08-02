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
# Retiring is ARCHIVING, never deleting (the Shopify product model). An archived
# plan leaves the public catalog and can no longer be bought — the three purchase
# entrypoints refuse it — but the row survives, so an invoice or renewal that
# recorded the slug still resolves and prices itself. Un-archiving restores it.
# This script used to DELETE those rows, which was only defensible because nothing
# had launched; it is no longer the tradeoff being made.
#
# Repricing is still destructive in the way archiving is not: pro 20 -> 49 in place
# charges an existing subscriber 2.45x on their next renewal with no notice. Today
# there are no subscribers. When there are, grandfather first and reprice second.
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
    "price": 900, "priceAnnual": 900, "currency": "usd", "status": "active",
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
    "price": 1900, "priceAnnual": 1900, "currency": "usd", "status": "active",
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
    "price": 4900, "priceAnnual": 4900, "currency": "usd", "status": "active",
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
    "price": 9900, "priceAnnual": 9900, "currency": "usd", "status": "active",
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

# `team` is deliberately NOT retired: TeamEnterpriseStrip reads its price and seat
# minimum live from this catalog, so archiving it blanks the seat price on the
# pricing page.

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
echo "== archiving everything off the ladder =="
for slug in $RETIRE; do
  # PUT status=archived. Absent body fields keep their stored value (UpdateEntry
  # loads the row first), so this changes the row's lifecycle and nothing else —
  # the price and history stay exactly as they were.
  if api -X PUT "${API}/v1/plans/entries/${slug}" -d '{"status":"archived"}' >/dev/null 2>&1; then
    echo "  archived ${slug}"
  else
    echo "  absent   ${slug}"
  fi
done

echo
echo "== resulting catalog =="
curl -fsS "${API}/v1/billing/plans" | python3 -c "
import sys,json
rows=json.load(sys.stdin)
for p in rows: print(f\"  {p['category']:11} {p['slug']:20} \${(p.get('price') or 0)//100}\")
want={'go':9,'dev':19,'pro':49,'max':99}
# The ladder IS the 'personal' category, so that is what is checked. 'team' lives
# on and is read live by the pricing page's seat strip; it is not off-ladder.
got={p['slug']:(p.get('price') or 0)//100 for p in rows if p['category']=='personal'}
bad=[f'{s}(want \${v}, got \${got.get(s)})' for s,v in want.items() if got.get(s)!=v]
bad+= [f'{s}(unexpected)' for s in got if s not in want]
print()
print('  LADDER CLEAN' if not bad else '  MISMATCH: '+', '.join(bad))
"
