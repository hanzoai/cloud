#!/usr/bin/env bash
# grant-hunter-pro.sh — one-line wrapper for gifting Hunter a World Pro
# subscription. Fill in HUNTER_EMAIL and execute.
#
# Preconditions:
#   1. Build the CLI once:     `go build -o commerce-grant ./cmd/grant`
#   2. Export commerce env:    source deploy/.env.production (or prod kubeconfig)
#   3. IAM client credentials: IAM_CLIENT_ID / IAM_CLIENT_SECRET in env (or KMS)
#   4. COMMERCE_GRANT_ALLOW=true gates accidental runs in the wrong environment.
#
# The CLI:
#   - resolves hunter@example.com → IAM owner/name
#   - auto-creates the commerce org and plan records if missing
#   - creates one active manual_gift subscription valid for 12 months
#
# The operation is idempotent: rerunning extends the period_end if longer.

set -euo pipefail

# TODO(zach): replace __REPLACE__ with Hunter's real IAM email before running.
HUNTER_EMAIL="__REPLACE__"

if [[ "$HUNTER_EMAIL" == "__REPLACE__" ]]; then
  echo "ERROR: edit this script and set HUNTER_EMAIL to Hunter's real address" >&2
  exit 1
fi

: "${IAM_CLIENT_ID:?IAM_CLIENT_ID must be set (see KMS /hanzo-commerce/admin/grant-token)}"
: "${IAM_CLIENT_SECRET:?IAM_CLIENT_SECRET must be set}"

export COMMERCE_GRANT_ALLOW=true
export IAM_ISSUER="${IAM_ISSUER:-https://hanzo.id}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${SCRIPT_DIR}/../commerce-grant"

if [[ ! -x "$BIN" ]]; then
  echo "Building commerce-grant..."
  (cd "${SCRIPT_DIR}/.." && go build -o commerce-grant ./cmd/grant)
fi

"$BIN" \
  --email "$HUNTER_EMAIL" \
  --plan world-pro \
  --months 12 \
  --reason "beta gift from Zach" \
  --by "zach@hanzo.ai"
