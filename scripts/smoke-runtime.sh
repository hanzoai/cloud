#!/usr/bin/env bash
# smoke-runtime.sh — boot the cloud binary and probe the mounted HTTP surface.
#
# Complements cmd/cloud-smoke (which exercises the in-process mount path).
# This script exercises the REAL ./cmd/cloud binary end-to-end: build, boot
# with the "default safe" --enable list, curl the endpoints that should return
# 200/401 per the HIP-0106 contract, kill the process, exit non-zero if any
# endpoint regresses.
#
# Intended for: CI smoke gating + local "does this clone actually serve?".
#
# Usage:
#   scripts/smoke-runtime.sh                       # build, boot, probe, teardown
#   PORT=8090 scripts/smoke-runtime.sh             # alternate port
#   KEEP_RUNNING=1 scripts/smoke-runtime.sh        # leave the binary up after probes
#   BIN=./bin/cloud scripts/smoke-runtime.sh       # reuse an already-built binary

set -euo pipefail

PORT="${PORT:-8088}"
LISTEN="${LISTEN:-127.0.0.1:${PORT}}"
BIN="${BIN:-./bin/cloud}"
DATA_DIR="${DATA_DIR:-$(mktemp -d -t cloud-smoke.XXXXXX)}"
LOG_FILE="${LOG_FILE:-${DATA_DIR}/cloud-smoke.log}"
KEEP_RUNNING="${KEEP_RUNNING:-0}"
BOOT_TIMEOUT="${BOOT_TIMEOUT:-30}"

# IAM env vars are required by the kms subsystem even when iam is disabled,
# since kms validates inbound JWTs against the live hanzo.id IAM.
export IAM_URL="${IAM_URL:-https://hanzo.id}"
export IAM_ISSUER="${IAM_ISSUER:-https://hanzo.id}"
export IAM_AUDIENCE="${IAM_AUDIENCE:-kms}"
export IAM_KEYS_URL="${IAM_KEYS_URL:-https://hanzo.id/v1/iam/.well-known/jwks}"

# Default safe enable list — omits iam (panics on boot until hanzoai/iam ships
# the v1.19.2 fix for the orphan RunAuthzCommand route) and ingress/commerce
# (not yet wired into the default smoke set).
ENABLE="${ENABLE:-base,kms,gateway,o11y,mcp,ai,licensing,plans,pricing,amqp,metrics,vfs,authz}"

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
log()   { printf '[smoke-runtime] %s\n' "$*" >&2; }

cleanup() {
  local rc=$?
  if [[ "${KEEP_RUNNING}" != "1" ]]; then
    if [[ -n "${CLOUD_PID:-}" ]] && kill -0 "${CLOUD_PID}" 2>/dev/null; then
      log "stopping cloud (pid=${CLOUD_PID})"
      kill "${CLOUD_PID}" 2>/dev/null || true
      wait "${CLOUD_PID}" 2>/dev/null || true
    fi
    if [[ -d "${DATA_DIR}" && "${DATA_DIR}" == /tmp/cloud-smoke.* ]]; then
      rm -rf "${DATA_DIR}"
    fi
  else
    log "KEEP_RUNNING=1: cloud (pid=${CLOUD_PID:-?}) left up; logs at ${LOG_FILE}"
  fi
  exit "${rc}"
}
trap cleanup EXIT INT TERM

if [[ ! -x "${BIN}" ]]; then
  log "building ${BIN}"
  mkdir -p "$(dirname "${BIN}")"
  go build -ldflags='-s -w' -o "${BIN}" ./cmd/cloud
fi

log "data dir: ${DATA_DIR}"
log "log file: ${LOG_FILE}"
log "starting: ${BIN} --brand=smoke --data-dir=${DATA_DIR} --enable=${ENABLE} --listen=${LISTEN}"

"${BIN}" \
  --brand=smoke \
  --domain=cloud.smoke.local \
  --data-dir="${DATA_DIR}" \
  --enable="${ENABLE}" \
  --listen="${LISTEN}" \
  > "${LOG_FILE}" 2>&1 &
CLOUD_PID=$!

# Wait for the listener to come up. We poll /healthz; if it doesn't bind
# within BOOT_TIMEOUT, dump the log tail and exit non-zero.
log "waiting for ${LISTEN} (timeout ${BOOT_TIMEOUT}s)…"
for i in $(seq 1 "${BOOT_TIMEOUT}"); do
  if curl -fsS -o /dev/null "http://${LISTEN}/healthz" 2>/dev/null; then
    log "listener up after ${i}s"
    break
  fi
  if ! kill -0 "${CLOUD_PID}" 2>/dev/null; then
    red "cloud exited before listener came up. Last log lines:"
    tail -40 "${LOG_FILE}" >&2 || true
    exit 1
  fi
  sleep 1
done

if ! curl -fsS -o /dev/null "http://${LISTEN}/healthz" 2>/dev/null; then
  red "timed out waiting for /healthz on ${LISTEN}. Last log lines:"
  tail -40 "${LOG_FILE}" >&2 || true
  exit 1
fi

# Probe matrix. Format: "<expected_status> <path>".
# Values match the verified runtime contract per the HIP-0106 runbook:
#   /healthz                  200  — process health probe
#   /v1/models                200  — catalog endpoint (no auth)
#   /v1/plans                 200  — plansvc catalog (goja-hosted)
#   /v1/pricing               200  — pricingsvc catalog (goja-hosted)
#   /v1/base/collections      401  — base subsystem alive, auth-gated
PROBES=(
  "200 /healthz"
  "200 /v1/models"
  "200 /v1/plans"
  "200 /v1/pricing"
  "401 /v1/base/collections"
)

failed=0
for probe in "${PROBES[@]}"; do
  expected="${probe%% *}"
  path="${probe#* }"
  actual="$(curl -sS -o /dev/null -w '%{http_code}' "http://${LISTEN}${path}" || echo 'ERR')"
  if [[ "${actual}" == "${expected}" ]]; then
    green "  OK   ${path}  ->  ${actual}"
  else
    red "  FAIL ${path}  ->  expected=${expected} actual=${actual}"
    failed=$((failed + 1))
  fi
done

if [[ "${failed}" -gt 0 ]]; then
  red "${failed} probe(s) failed. Tail of cloud log:"
  tail -60 "${LOG_FILE}" >&2 || true
  exit 1
fi

green "all ${#PROBES[@]} probes passed."
