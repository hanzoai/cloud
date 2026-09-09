#!/usr/bin/env bash
# One reader, every row: the three retrieval policies at each k, in the order
# that makes a partial lane useful first (the shipped retriever, then the
# ceiling, then the baseline). Resumable: run.mjs skips what it has.
#
#   ./lane.sh <reader> [workers] [k ...]        e.g. ./lane.sh gemma4:31b 2 20 10
set -euo pipefail
cd "$(dirname "$0")"
reader=${1:?reader}; workers=${2:-3}; shift 2 || shift $#
ks=("$@"); [ ${#ks[@]} -gt 0 ] || ks=(20)
for k in "${ks[@]}"; do
  for policy in cer oracle single; do
    node run.mjs --policy="$policy" --k="$k" --reader="$reader" --workers="$workers"
  done
done
