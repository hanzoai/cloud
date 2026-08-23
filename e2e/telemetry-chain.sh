#!/usr/bin/env bash
# The telemetry chain, end to end: does an event survive the whole path?
#
# Each stage is checked SEPARATELY so a failure names the broken link rather
# than reporting "telemetry is down". The stages are independent — the ingest
# endpoint can be healthy while the collector that drains it is dead, which is
# exactly the state this was written in.
set -uo pipefail
API="${API:-https://api.hanzo.ai}"
NS="${NS:-hanzo}"
POD="${POD:-datastore-0}"
fail=0
ok()   { printf "  \033[32mPASS\033[0m  %s\n" "$1"; }
bad()  { printf "  \033[31mFAIL\033[0m  %s\n" "$1"; fail=1; }

echo "== 1. the one ingest endpoint accepts an event =="
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/v1/event" \
  -H 'content-type: application/json' -d '{"event":"e2e.probe"}' 2>/dev/null)
ctl=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/v1/definitely-not-real-9137" \
  -H 'content-type: application/json' -d '{}' 2>/dev/null)
[ "$code" = "200" ] && [ "$ctl" = "404" ] \
  && ok "POST /v1/event -> $code (control $ctl proves it is routed)" \
  || bad "POST /v1/event -> $code, control -> $ctl"

echo "== 2. the OTLZ collector is actually listening =="
# 4317 traces, 4318 logs. Both must bind: they are separate receivers, and a
# collision on one kills the whole pipeline, taking the other down with it.
listening=$(kubectl exec -n "$NS" "$(kubectl get pods -n "$NS" -o name 2>/dev/null | grep -E 'pod/cloud-' | head -1 | cut -d/ -f2)" -- \
  sh -c 'cat /proc/net/tcp /proc/net/tcp6 2>/dev/null' 2>/dev/null \
  | awk '$4=="0A"{split($2,a,":"); print a[2]}' | sort -u \
  | while read h; do printf "%d " $((16#$h)); done)
for port in 4317 4318; do
  echo "$listening" | tr ' ' '\n' | grep -qx "$port" \
    && ok "cloud is listening on $port" \
    || bad "cloud is NOT listening on $port (collector died? check for 'address already in use')"
done

echo "== 3. telemetry is arriving, not just accepted =="
# The load-bearing check. Stage 1 can pass while nothing is stored: the endpoint
# 200s, the collector is dead, and the write never happens.
# Both signals live in the one event plane and date themselves with the same
# `time` column, so there is no per-probe expression left to carry. They used to
# be o11y_traces.o11y_index_v3 and o11y_logs.logs_v2; those databases are gone,
# which made this stage report the chain BROKEN unconditionally -- a check that
# always fails tells you nothing, and hides the outage it exists to catch.
for probe in "traces:event.span" "logs:event.log"; do
  name="${probe%%:*}"; tbl="${probe#*:}"
  mins=$(kubectl exec -n "$NS" "$POD" -- bash -lc \
    "hanzo-datastore client --host 127.0.0.1 --port 9000 --user \"\$DATASTORE_USER\" --password \"\$DATASTORE_PASSWORD\" \
     -q \"SELECT dateDiff('minute', max(time), now()) FROM $tbl\"" 2>/dev/null | tr -d '[:space:]')
  if [ -n "$mins" ] && [ "$mins" -lt 30 ] 2>/dev/null; then
    ok "$name last written ${mins}m ago"
  else
    bad "$name last written ${mins:-?}m ago (stale — nothing is draining into the store)"
  fi
done

echo "== 4. the product surfaces answer =="
for host in insights.hanzo.ai analytics.hanzo.ai sentry.hanzo.ai; do
  c=$(curl -s -o /dev/null -w '%{http_code}' -L "https://$host/" 2>/dev/null)
  [ "$c" = "200" ] && ok "$host -> $c" || bad "$host -> $c"
done

echo
[ "$fail" = "0" ] && echo "TELEMETRY CHAIN: healthy" || echo "TELEMETRY CHAIN: BROKEN (see FAIL above)"
exit $fail
