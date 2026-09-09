#!/usr/bin/env zsh
# The lane, end to end, on the local open reader: rows that need no plan first,
# then everything once every question has a plan, then the test split.
set -u
cd "$(dirname "$0")"
plans() { node -e "console.log(Object.keys(require('../data/mab/plans.json')).length)"; }
echo "[chain] $(date +%H:%M) dev semantic+lexical"
node lane.mjs --split=dev --rows=semantic,lexical --reader=gemma4:31b --workers=1 2>&1 | grep -vE '^\s*$' | tail -2
until [ "$(plans)" -ge 760 ]; do sleep 60; done
echo "[chain] $(date +%H:%M) plans complete; no-reader on dev and test"
node lane.mjs --split=dev --rows=noreader 2>&1 | tail -1
node lane.mjs --split=test --rows=noreader 2>&1 | tail -1
echo "[chain] $(date +%H:%M) dev rows"
node lane.mjs --split=dev --rows=entities,timeline,hops,resolver,full,rrf --reader=gemma4:31b --workers=1 2>&1 | grep -vE '^\s*$' | tail -6
echo "[chain] $(date +%H:%M) test rows"
node lane.mjs --split=test --rows=full --reader=gemma4:31b --workers=1 2>&1 | tail -1
node lane.mjs --split=test --rows=semantic --reader=gemma4:31b --workers=1 2>&1 | tail -1
node lane.mjs --split=test --rows=resolver --reader=gemma4:31b --workers=1 2>&1 | tail -1
echo "[chain] $(date +%H:%M) done"; node table.mjs
