#!/usr/bin/env bash
#
# orphans — every published image must have a git tag naming its commit.
#
# WHY THIS EXISTS. hanzo.yml states the release invariant as "main push → build →
# smoke → tag → pin → prove live", and every job in cicd.yml upholds its own link
# in that chain. Nothing upheld the chain ITSELF. Each job can only see the
# release it is running; an image published by a lane that never entered cicd.yml
# is invisible to all of them, and that is not hypothetical — it is how
# v1.801.478, 479, 480 and 484 came to exist with no tag in any repo, while 480
# served production. The registry is the only place that knows what was actually
# published, so the registry is what has to be asked.
#
# The check is deliberately lane-agnostic. It does not ask WHO built an image or
# whether some workflow succeeded; it compares what is published against what is
# tagged. A gate phrased in terms of a lane can only catch that lane, and the
# lane that caused this outage was the one nobody thought to instrument.
#
# TAGS ARE READ FROM BOTH REPOS, and the union is what counts. cloud is canonical
# on git.hanzo.ai, but cicd.yml claims its version by creating refs/tags/v<N>
# through the GitHub API on hanzoai/cloud, so receipts exist in two namespaces.
# Asking only one would paint every release red from the other, and a gate that
# is always red is a gate someone turns off. Asking for the union answers the
# question actually worth asking — "does a receipt for this image exist anywhere"
# — and stays true whichever way the namespace split is later resolved.
#
# WHAT IS NOT A FAILURE. The images already published untagged cannot be fixed by
# this script, and pretending otherwise would make it red forever on day one.
# They are recorded, one per line with the reason, in .hanzo/orphans.txt — DATA,
# not a code path, so accepting one is a reviewable diff and the rule below stays
# a single rule. An entry there is a statement that the image is known and
# unreconstructable, not that untagged images are tolerated: anything published
# from now on that is not tagged is red.
#
#   orphans.sh
#
# Exit 0 = every published image is tagged or recorded. Exit 1 = at least one is
# neither (the release invariant is broken). Exit 2 = a source could not be READ,
# which is not the same answer as "nothing was found" — collapsing those two is
# how a verifier comes to pass by accident, so they get separate codes and the
# caller can tell an outage from a clean run.
#
# Test seams, mirroring githubAPIBase in apps/platform/release.go — set to a file
# and that source is read from it instead of the network, so the comparison can
# be exercised with no registry, no tokens and no network at all:
#   ORPHANS_IMAGES    published image tags, one per line
#   ORPHANS_TAGS      git tags, one per line
#   ORPHANS_ACCEPTED  path to the recorded-orphans file

set -euo pipefail

IMAGE_PATH="${ORPHANS_IMAGE_PATH:-hanzoai/cloud}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ACCEPTED="${ORPHANS_ACCEPTED:-${HERE}/../orphans.txt}"

# A published version, e.g. v1.801.480. Anchored at both ends: a tag that merely
# CONTAINS a version (v1.801.480-rc1, sha-abc123) is not the release name this
# invariant is about, and matching it loosely would invent orphans that the
# release chain never claimed a number for.
SEMVER='^v[0-9]+\.[0-9]+\.[0-9]+$'

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# ── what is published ────────────────────────────────────────────────────────
# The registry is paginated and the Link header is the ONLY way to know there is
# more; stopping at the first page silently truncates the published set, and a
# truncated set is a green run that proved nothing about the images it never saw.
if [ -n "${ORPHANS_IMAGES:-}" ]; then
  [ -r "$ORPHANS_IMAGES" ] || { echo "orphans: cannot read ORPHANS_IMAGES=$ORPHANS_IMAGES" >&2; exit 2; }
  grep -E "$SEMVER" "$ORPHANS_IMAGES" | sort -u > "$work/images" || true
else
  if [ -n "${GHCR_USER:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
    TOKEN="$(curl -fsSL --max-time 30 -u "$GHCR_USER:$GHCR_TOKEN" \
      "https://ghcr.io/token?scope=repository:${IMAGE_PATH}:pull&service=ghcr.io" | jq -r '.token // empty')" || TOKEN=""
  else
    TOKEN="$(curl -fsSL --max-time 30 \
      "https://ghcr.io/token?scope=repository:${IMAGE_PATH}:pull&service=ghcr.io" | jq -r '.token // empty')" || TOKEN=""
  fi
  [ -n "$TOKEN" ] || { echo "orphans: no ghcr pull token for ${IMAGE_PATH} — cannot read what is published" >&2; exit 2; }

  : > "$work/images.raw"
  page="https://ghcr.io/v2/${IMAGE_PATH}/tags/list?n=1000"
  while [ -n "$page" ]; do
    if ! curl -fsSL --max-time 60 -D "$work/hdr" -H "Authorization: Bearer $TOKEN" "$page" > "$work/body"; then
      echo "orphans: cannot list tags for ${IMAGE_PATH} — refusing to report a clean run against a registry that did not answer" >&2
      exit 2
    fi
    jq -r '.tags[]? // empty' < "$work/body" >> "$work/images.raw"
    page="$(tr -d '\r' < "$work/hdr" | sed -n 's/^[Ll]ink: *<\([^>]*\)>.*rel="next".*/\1/p' | head -1)"
    [ -n "$page" ] && page="https://ghcr.io${page}"
  done
  grep -E "$SEMVER" "$work/images.raw" | sort -u > "$work/images" || true
fi
[ -s "$work/images" ] || { echo "orphans: no published versions found for ${IMAGE_PATH} — that is not a clean run, it is a read that returned nothing" >&2; exit 2; }

# ── what is tagged ───────────────────────────────────────────────────────────
# `git ls-remote` failing and a repo having no tags produce the same empty list,
# so each remote is checked for the READ succeeding, separately from what it
# returned. At least one remote must answer: a network that answered nothing
# would otherwise make every published image look untagged and turn a broken
# gate into 400 spurious failures.
if [ -n "${ORPHANS_TAGS:-}" ]; then
  [ -r "$ORPHANS_TAGS" ] || { echo "orphans: cannot read ORPHANS_TAGS=$ORPHANS_TAGS" >&2; exit 2; }
  grep -E "$SEMVER" "$ORPHANS_TAGS" | sort -u > "$work/tags" || true
else
  : > "$work/tags.raw"
  answered=0
  for remote in "https://git.hanzo.ai/hanzoai/cloud" "https://github.com/hanzoai/cloud"; do
    url="$remote"
    case "$remote" in
      *github.com*) [ -n "${GH_PAT:-}" ] && url="https://x-access-token:${GH_PAT}@github.com/hanzoai/cloud" ;;
      *git.hanzo.ai*) [ -n "${FORGE_TOKEN:-}" ] && url="https://x-access-token:${FORGE_TOKEN}@git.hanzo.ai/hanzoai/cloud" ;;
    esac
    if git ls-remote --tags "$url" > "$work/ls" 2>/dev/null; then
      answered=$((answered + 1))
      sed 's|.*refs/tags/||; s|\^{}$||' < "$work/ls" >> "$work/tags.raw"
    else
      echo "orphans: ${remote} did not answer — its receipts are not in this comparison" >&2
    fi
  done
  [ "$answered" -gt 0 ] || { echo "orphans: no tag remote answered — refusing to call every published image an orphan on a failed read" >&2; exit 2; }
  grep -E "$SEMVER" "$work/tags.raw" | sort -u > "$work/tags" || true
fi

# ── what is already known and recorded ───────────────────────────────────────
if [ -r "$ACCEPTED" ]; then
  sed 's/#.*//' "$ACCEPTED" | awk '{print $1}' | grep -E "$SEMVER" | sort -u > "$work/accepted" || true
else
  : > "$work/accepted"
fi

comm -23 "$work/images" "$work/tags" > "$work/untagged"
comm -23 "$work/untagged" "$work/accepted" > "$work/new"

if [ -s "$work/new" ]; then
  while read -r v; do
    echo "::error::${v} is published but no git tag names it — an image no commit can be traced to is not a release. Tag it at the commit it was built from, or record it in .hanzo/orphans.txt with the reason it cannot be."
  done < "$work/new"
  echo "orphans: $(wc -l < "$work/new") published image(s) with no receipt; $(wc -l < "$work/accepted") previously recorded" >&2
  exit 1
fi

echo "orphans: $(wc -l < "$work/images") published, $(wc -l < "$work/tags") tagged, $(wc -l < "$work/accepted") recorded — every published image has a receipt"
