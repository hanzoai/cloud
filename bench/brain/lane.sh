#!/usr/bin/env bash
# One reader, every row. Categories go outermost so a partial lane holds whole
# comparisons — the shipped retriever, the ceiling and the baseline on multi-hop
# — before any single-hop question is asked. Resumable: run.mjs skips what it has.
#
#   ./lane.sh <reader> [workers] [k ...]        e.g. ./lane.sh gemma4:31b 2 20 10
set -euo pipefail
cd "$(dirname "$0")"
reader=${1:?reader}; workers=${2:-3}; shift 2 || shift $#
ks=("$@"); [ ${#ks[@]} -gt 0 ] || ks=(20)
for k in "${ks[@]}"; do
  for cat in 1 2 3 4; do
    for policy in cer oracle single; do
      node run.mjs --policy="$policy" --k="$k" --reader="$reader" --workers="$workers" --cats="$cat"
    done
  done
done
