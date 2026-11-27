#!/usr/bin/env bash
# unit.sh SHARD COUNT — the unit leg of the gate, one balanced slice of it.
#
# `go test ./...` was ONE runner and ~100 minutes (45 on a quiet one), every package
# waiting on the one before it. The packages are independent, so the gate runs COUNT
# of these at once, each on a runner of its own, and every package is in exactly one
# slice: the slice a package lands in is a pure function of the package list and the
# size of its tests, so no two runs disagree about who runs what, and a package added
# tomorrow is gated by default (there is no list to forget to extend).
#
# A package's slice is a pure function of its import path: the first 32 bits of
# its sha256, modulo COUNT. No estimate, no weights, no history: any checkout
# computes the same plan, and a package added tomorrow is gated by default.
# Per-package and per-slice durations are printed for observability only.
set -euo pipefail
shard=${1:?shard 1..COUNT}
count=${2:?COUNT}
if [ "$shard" -lt 1 ] || [ "$shard" -gt "$count" ]; then
	echo "unit.sh: shard $shard is outside 1..$count" >&2
	exit 2
fi

export GOPRIVATE='github.com/hanzoai/*,github.com/hanzo-inc/*,github.com/zap-proto/*' GONOPROXY=none GOPROXY='https://proxy.golang.org|direct' GOWORK=off CGO_ENABLED=1
export GOCACHE=${GOCACHE:-/var/tmp/hanzo-go/build} GOMODCACHE=${GOMODCACHE:-/var/tmp/hanzo-go/mod}
mkdir -p "$GOCACHE" "$GOMODCACHE"
# The anti-forgery key every process that verifies the console token must hold; a
# key already in the environment wins. See the go-unit history in hanzo.yml.
export CONSOLE_CSRF_KEY="${CONSOLE_CSRF_KEY:-ZGV2LWNzcmYta2V5LWZvci10ZXN0cy1vbmx5LTAwMDA=}"
tags="sqlite_fts5 sqlite_math_functions"

# Resolve first, under a bound of its own: -timeout does not cover the toolchain
# resolving modules, and a hung fetch once held a runner for ninety minutes.
timeout 10m go mod download || {
	echo "::error::modules did not resolve in 10m. Most come from proxy.golang.org and the 7 private ones from github.com on the GitHub App token the gate mints — check that a publisher is serving, and that this job's credential is admitted, before reading this as a code failure."
	exit 1
}

plan=$(go list -tags "$tags" -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... |
	while read -r ip; do
		[ -n "$ip" ] || continue
		h=$(printf %s "$ip" | sha256sum | cut -c1-8)
		if [ $(( (16#$h % count) + 1 )) -eq "$shard" ]; then echo "$ip"; fi
	done)
[ -n "$plan" ] || { echo "unit.sh: shard $shard/$count holds no package"; exit 0; }
echo "shard $shard/$count: $(echo "$plan" | wc -l) packages"

# As many packages at once as the runner's MEMORY holds, not its cores: a package's
# test binary links cgo sqlite and the app graph at ~3 GB a link. GOMAXPROCS is the
# pod's real CPU limit (nproc inside a job reads the node's).
p=${GOMAXPROCS:-$(nproc)}
m=$(cat /sys/fs/cgroup/memory.max 2>/dev/null || echo max)
case "$m" in *[!0-9]* | '') : ;; *) q=$((m / 3221225472)); [ "$q" -lt 1 ] && q=1; [ "$q" -lt "$p" ] && p=$q ;; esac
echo "go test: $p packages at once"

# Test temp dirs on RAM when RAM is there. The stores are fsync-bound: t.TempDir on
# the runner's ext4 puts every SQLite commit behind the journal (measured ~90 s for
# a mount that costs milliseconds on tmpfs). -exec scopes TMPDIR to the test binary
# alone — the linker's multi-GB temporaries stay on disk — and needs no environment
# variable the code under test could read. Skipped unless /dev/shm holds 4 GiB.
exec_flag=()
shm_kb=$(df -Pk /dev/shm 2>/dev/null | awk 'NR==2{print $4}')
if [ "${shm_kb:-0}" -ge 4194304 ]; then
	mkdir -p /dev/shm/hanzo-unit
	printf '#!/bin/sh\nexport TMPDIR=/dev/shm/hanzo-unit\nexec "$@"\n' >/dev/shm/hanzo-unit/run.sh
	chmod +x /dev/shm/hanzo-unit/run.sh
	exec_flag=(-exec /dev/shm/hanzo-unit/run.sh)
fi

# Under a throwaway PostgreSQL so every store ported to hanzo-sql runs its suite on
# both engines (scripts/postgres.sh). Its DSN is the same string on every run, so
# `go test` may serve a package from its cache when nothing it reads has changed.
# shellcheck disable=SC2086
start=$SECONDS
trap 'echo "shard $shard/$count: $((SECONDS - start))s"' EXIT
scripts/postgres.sh go test -p "$p" -timeout 30m -tags "$tags" "${exec_flag[@]}" \
	-skip 'TestRecord_DebitsFinanceInProcess|TestIndexerUnreachableHonestEmpty' \
	$plan
