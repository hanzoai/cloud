#!/usr/bin/env bash
# One reader, every row. Categories go outermost so a partial lane holds whole
# comparisons — the shipped retriever, the frozen engine, the ceiling and the
# baseline on multi-hop — before any single-hop question is asked. Resumable:
# run.mjs skips what it has. A row that fails to start is reported and skipped,
# so one broken policy does not stop the lane.
#
#   ./lane.sh <reader> [workers] [k ...]        e.g. ./lane.sh gemma4:31b 2 20 10
set -uo pipefail
cd "$(dirname "$0")"
reader=${1:?reader}; workers=${2:-3}; shift 2 || shift $#
ks=("$@"); [ ${#ks[@]} -gt 0 ] || ks=(20)
for k in "${ks[@]}"; do
  for cat in 1 2 3 4; do
    for policy in cer context oracle single; do
      node run.mjs --policy="$policy" --k="$k" --reader="$reader" --workers="$workers" --cats="$cat" \
        || echo "lane $reader: $policy k=$k cat=$cat did not finish (exit $?)" >&2
    done
  done
done
