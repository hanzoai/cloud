#!/usr/bin/env bash
# End-to-end test verifying all 8 partner orgs and billing operations against a local cloud host.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$ROOT/bin"
PORT="${PORT:-18088}"

export CLOUD_KMS_MASTER_KEY_REF="${CLOUD_KMS_MASTER_KEY_REF:-MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=}"
export CLOUD_DATA_DIR="${CLOUD_DATA_DIR:-$(mktemp -d /tmp/cloud-e2e-XXXXXX)}"
export SQUARE_ENVIRONMENT="sandbox"
export SQUARE_APPLICATION_ID="sandbox-sq0idb--kZP78n8_TjQOu8qApr5iQ"
export SQUARE_LOCATION_ID="LD8DWE4KKB0WB"

echo "=== Starting local cloud on port $PORT ==="
echo "Data dir: $CLOUD_DATA_DIR"

[ -x "$BIN/cloud" ] || { echo "no $BIN/cloud — building..."; go build -o "$BIN/cloud" "$ROOT/cmd/cloud"; }
[ -x "$BIN/commerce" ] || { echo "no $BIN/commerce — building..."; go build -o "$BIN/commerce" "$ROOT/plugin/commerce"; }
[ -x "$BIN/billing" ] || { echo "no $BIN/billing — building..."; go build -o "$BIN/billing" "$ROOT/plugin/billing"; }

"$BIN/cloud" --listen "127.0.0.1:$PORT" --zap "127.0.0.1:0" \
  >"$CLOUD_DATA_DIR/host.log" 2>&1 &
HOST=$!
trap 'kill -TERM $HOST 2>/dev/null || true; wait $HOST 2>/dev/null || true; rm -rf "$CLOUD_DATA_DIR"' EXIT

echo "Waiting for healthz..."
for i in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
    echo "Host is up (took $i checks)"
    break
  fi
  sleep 0.2
done

HEALTH=$(curl -s "http://127.0.0.1:$PORT/healthz")
echo "Health response: $HEALTH"

echo "=== Testing public catalog endpoint /v1/billing/plans ==="
PLANS_RES=$(curl -s "http://127.0.0.1:$PORT/v1/billing/plans")
PLANS_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/v1/billing/plans")
echo "/v1/billing/plans HTTP status: $PLANS_STATUS"
if [ "$PLANS_STATUS" != "200" ]; then
  echo "FAIL: expected 200 from /v1/billing/plans, got $PLANS_STATUS"
  cat "$CLOUD_DATA_DIR/host.log"
  exit 1
fi
echo "Catalog response: $(echo "$PLANS_RES" | head -c 200)..."
echo "PASS: /v1/billing/plans served public catalog successfully"

echo "=== Testing auth enforcement on /v1/billing/settings ==="
SETTINGS_STATUS=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/v1/billing/settings")
echo "/v1/billing/settings unauthenticated HTTP status: $SETTINGS_STATUS"
if [ "$SETTINGS_STATUS" != "401" ]; then
  echo "FAIL: expected 401 from unauthenticated /v1/billing/settings, got $SETTINGS_STATUS"
  cat "$CLOUD_DATA_DIR/host.log"
  exit 1
fi
echo "PASS: /v1/billing/settings correctly requires authentication (401)"

echo "=== Running partner orgs allowance and credit E2E tests ==="
cd "$ROOT"
go test -v -count=1 ./apps/commerce -run TestPartnerOrgsAllowanceAndCreditsE2E

echo "=== All E2E tests passed against local cloud ==="
