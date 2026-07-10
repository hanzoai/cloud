#!/bin/sh
# fetch-plans.sh — pull the latest @hanzo/plans tarball from npm and
# extract its JSON files into ./api/billing/plans/.
#
# hanzoai/plans is published as @hanzo/plans on the public npm
# registry (the catalog is customer-facing marketing data, no reason
# to gate it). Commerce uses Go's go:embed at compile time, so we
# can't `npm install` at runtime — but we CAN curl the tarball at
# build time and unpack it. No git clone, no vendored copy.
#
# Env:
#   PLANS_VERSION  semver (default: tracks the highest published)
#
# Local dev: just run this script — public npm needs no auth.

set -eu

DEST=$(cd "$(dirname "$0")/.." && pwd)/api/billing/plans
TMP=$(mktemp -d)

# Fetch the registry metadata once. We read both the latest dist-tag (when no
# explicit PLANS_VERSION is requested) and the exact tarball URL for the
# resolved version from it — the registry is authoritative for the tarball
# path, so we never hand-build a URL that can 404 on a missing version.
curl -fsSL "https://registry.npmjs.org/@hanzo/plans" -o "$TMP/meta.json"

# Resolve version: explicit PLANS_VERSION wins, else the published "latest".
PLANS_VERSION=${PLANS_VERSION:-$(
  sed -n 's/.*"dist-tags":{[^}]*"latest":"\([^"]*\)".*/\1/p' "$TMP/meta.json"
)}
if [ -z "$PLANS_VERSION" ]; then
  echo "fetch-plans: could not resolve @hanzo/plans latest version" >&2
  exit 1
fi

# Extract the tarball URL for the resolved version from the metadata.
TARBALL=$(
  tr ',' '\n' < "$TMP/meta.json" \
    | grep -F "/plans-${PLANS_VERSION}.tgz" \
    | sed -n 's/.*"\(https:[^"]*plans-'"${PLANS_VERSION}"'\.tgz\)".*/\1/p' \
    | head -n1
)
if [ -z "$TARBALL" ]; then
  echo "fetch-plans: version ${PLANS_VERSION} not found in registry" >&2
  exit 1
fi

mkdir -p "$DEST"
rm -f "$DEST"/*.json

curl -fsSL "$TARBALL" -o "$TMP/plans.tgz"
tar -xzf "$TMP/plans.tgz" -C "$TMP"
cp "$TMP"/package/*.json "$DEST/"
rm -rf "$TMP"

echo "fetched @hanzo/plans@${PLANS_VERSION} -> $DEST ($(ls "$DEST" | wc -l | tr -d ' ') files)"
