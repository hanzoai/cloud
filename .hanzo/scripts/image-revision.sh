#!/usr/bin/env bash
#
# image-revision — print the commit an image was built from.
#
# WHY THIS EXISTS. A release is meant to be a receipt: the tag, the commit and
# the image all name each other, and any one of them can be checked against the
# other two after the fact. Two of those links are cheap — the git tag names a
# commit, and universe pins repo:tag@digest. The third, image -> commit, is
# readable ONLY from the image's own `org.opencontainers.image.revision` label,
# and nothing in the fleet read it, so nothing noticed when it stopped being
# true.
#
# It had stopped being true for a whole class of images. cloud's Dockerfile
# declares `ARG REVISION=unknown`, and the label takes that default unless a
# builder passes it. The docker/build-push-action builder happens to overwrite the
# label from the outside (its `labels:` input is applied after the Dockerfile's
# own LABEL), so ITS images were fine. The platform builder — buildctl, via
# buildFrontendCmd in apps/platform/k8s.go — passes build-arg:VERSION and
# build-arg:GIT_VERSION but no REVISION, so every image it published carried
# `revision=unknown` and could not be traced to a commit at all.
#
# That is exactly how the two v1.801.410 images became indistinguishable without
# a byte-level diff: one labelled 1b8b76ed (the real release), one labelled
# `unknown` (the builder that overwrote the tag 12 minutes later). With the label
# truthful on both builders, "which commit is this image" is one call, and the
# tag -> commit -> image triangle closes.
#
#   image-revision.sh <image-path> <ref> [bearer-token]
#     image-path  the path under the registry host, e.g. hanzoai/cloud
#     ref         a tag or a sha256: digest
#     token       a ghcr pull token; fetched anonymously when omitted
#
# Prints the revision on stdout. Exits non-zero (printing nothing) when the
# image cannot be read, so a caller can distinguish "no label" (empty output,
# exit 0) from "could not look" (exit 1) — the two demand opposite handling and
# collapsing them is how a verifier comes to pass by accident.

set -euo pipefail

IMAGE_PATH="${1:?usage: image-revision.sh <image-path> <ref> [token]}"
REF="${2:?usage: image-revision.sh <image-path> <ref> [token]}"
TOKEN="${3:-}"

ACCEPT='application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.list.v2+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.oci.image.manifest.v1+json'

if [ -z "$TOKEN" ]; then
  if [ -n "${GHCR_USER:-}" ] && [ -n "${GHCR_TOKEN:-}" ]; then
    TOKEN="$(curl -fsSL --max-time 30 -u "$GHCR_USER:$GHCR_TOKEN" \
      "https://ghcr.io/token?scope=repository:${IMAGE_PATH}:pull&service=ghcr.io" | jq -r '.token // empty')"
  else
    TOKEN="$(curl -fsSL --max-time 30 \
      "https://ghcr.io/token?scope=repository:${IMAGE_PATH}:pull&service=ghcr.io" | jq -r '.token // empty')"
  fi
fi
[ -n "$TOKEN" ] || { echo "image-revision: no ghcr pull token for ${IMAGE_PATH}" >&2; exit 1; }

fetch_manifest() {
  curl -fsSL --max-time 30 -H "Authorization: Bearer $TOKEN" -H "Accept: $ACCEPT" \
    "https://ghcr.io/v2/${IMAGE_PATH}/manifests/$1"
}

MANIFEST="$(fetch_manifest "$REF")" || { echo "image-revision: cannot read ${IMAGE_PATH}:${REF}" >&2; exit 1; }

# A multi-arch tag is an INDEX, and an index carries no config blob and so no
# labels. Descend to the amd64 child — the only platform this fleet publishes —
# rather than reporting "no label" for every multi-arch image, which would make
# the verifier silently vacuous exactly where it matters most.
if printf '%s' "$MANIFEST" | jq -e 'has("manifests")' >/dev/null 2>&1; then
  CHILD="$(printf '%s' "$MANIFEST" | jq -r '
    (.manifests[] | select(.platform.architecture == "amd64" and .platform.os == "linux") | .digest),
    (.manifests[0].digest)' | head -1)"
  [ -n "$CHILD" ] || { echo "image-revision: index for ${IMAGE_PATH}:${REF} names no manifest" >&2; exit 1; }
  MANIFEST="$(fetch_manifest "$CHILD")" || { echo "image-revision: cannot read child ${CHILD}" >&2; exit 1; }
fi

CONFIG="$(printf '%s' "$MANIFEST" | jq -r '.config.digest // empty')"
[ -n "$CONFIG" ] || { echo "image-revision: ${IMAGE_PATH}:${REF} has no config descriptor" >&2; exit 1; }

curl -fsSL --max-time 30 -H "Authorization: Bearer $TOKEN" \
  "https://ghcr.io/v2/${IMAGE_PATH}/blobs/${CONFIG}" \
  | jq -r '.config.Labels["org.opencontainers.image.revision"] // ""' \
  | sed 's/^unknown$//'
