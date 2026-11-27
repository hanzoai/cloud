#!/usr/bin/env bash
# sdk.sh — prove sdk/ builds ALONE as the public module github.com/hanzoai/cloud.
#
# sdk/ is the tree that moves to the OSS repository. This copies its non-test Go
# files into an empty directory, names the module github.com/hanzoai/cloud,
# rewrites github.com/hanzoai/cloud/sdk/<pkg> to github.com/hanzoai/cloud/<pkg>,
# lets the toolchain pick the requirements from this repo's go.mod, and builds and
# vets every package there. An sdk package that reaches anything outside sdk/ —
# an app, internal/..., a private module — cannot resolve in that directory, so
# this fails exactly when the SDK would not build on its own.
#
# Tests are left out: an SDK test may still run against this repo's test kits
# (sdk/exceptions.txt names each), and those move with it later.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$(mktemp -d)}
export GOWORK=off GOFLAGS=-mod=mod GOPRIVATE='github.com/hanzoai/*,github.com/hanzo-inc/*,github.com/zap-proto/*' GONOPROXY=none
rm -rf "$out/sdk" && mkdir -p "$out/sdk"
(cd "$root/sdk" && find . -type f ! -name '*_test.go' ! -name '*.txt' ! -name 'LLM.md' ! -name 'AGENTS.md' ! -name 'CLAUDE.md' ! -name 'GEMINI.md' -print0 | while IFS= read -r -d '' f; do
	mkdir -p "$out/sdk/$(dirname "$f")" && cp "$f" "$out/sdk/$f"; done)
cd "$out/sdk"
find . -name '*.go' -print0 | xargs -0 perl -pi -e 's#"github\.com/hanzo-inc/cloud/sdk/#"github.com/hanzoai/cloud/#g; s#"github\.com/hanzo-inc/cloud/sdk"#"github.com/hanzoai/cloud"#g'
if grep -rln '"github.com/hanzo-inc/' --include='*.go' .; then
	echo "sdk.sh: the files above import this private repository or another private module" >&2
	exit 1
fi
# This repo's requirements, renamed; tidy keeps only what the SDK imports.
sed '1s#^module .*#module github.com/hanzoai/cloud#' "$root/go.mod" > go.mod
cp "$root/go.sum" go.sum
go mod tidy
go build ./...
go vet ./...
echo "OK: sdk/ builds alone as github.com/hanzoai/cloud ($(go list ./... | wc -l | tr -d ' ') packages) in $out/sdk"
