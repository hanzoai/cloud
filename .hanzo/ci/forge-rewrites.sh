#!/usr/bin/env bash
#
# Route our own modules at the forge WHERE THE FORGE CAN SERVE THEM, and leave the
# rest on GitHub.
#
# WHY THIS IS PER-REPO AND NOT ONE PREFIX. `insteadOf` is a REWRITE, not a chain:
# git takes the longest match and dials that, so a single prefix rewrite of
# github.com/hanzoai/ -> git.hanzo.ai/hanzoai/ means GitHub is never tried. The
# comment beside the old prefix rewrite promised "GH_PAT remains the fallback for a
# module the forge has not mirrored", and git cannot provide that fallback — one
# repo the forge denies takes the whole build down.
#
# That is not hypothetical, and it has now happened on BOTH hosts to the SAME repo.
# The prefix rewrite exists because GitHub's ACL on hanzoai/zen drifted from its
# siblings: the token read ai, commerce, orm and account and answered
# `Repository not found` for zen alone, blocking nine consecutive releases. Routing
# to the forge escaped that — until the forge's own ACL denied zen the same way
# (`fatal: repository 'https://git.hanzo.ai/hanzoai/zen/' not found`), which took
# containment down and, because `go list -m` loads the whole module graph, reported
# it as an unrelated "zen is pinned at , below the streaming-fix floor".
#
# A private repo and a missing repo deny identically, so no probe can tell them
# apart — which is exactly why the decision has to be made per repo rather than
# assumed for a namespace. Each module go.mod names is asked whether THIS token can
# read it on the forge; the ones that answer yes are rewritten one repo at a time,
# and the ones that do not are left for the GitHub rewrite the caller installs
# after us. So an ACL drift on either host costs one module's address, never the
# build.
#
# go.sum is what makes choosing per repo safe: whichever host serves it, the zip
# must hash to the line already committed, or the build fails loudly.
set -euo pipefail

[ -n "${GIT_TOKEN:-}" ] || { echo "forge-rewrites: no GIT_TOKEN — every module stays on GitHub"; exit 0; }

mods=$(sed -nE 's|^[[:space:]]+github\.com/hanzoai/([A-Za-z0-9._-]+) .*|\1|p' go.mod | sort -u)
[ -n "$mods" ] || { echo "forge-rewrites: go.mod names no github.com/hanzoai module"; exit 0; }

on=""; off=""
for m in $mods; do
  code=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Authorization: token ${GIT_TOKEN}" \
    "https://git.hanzo.ai/v1/repos/hanzoai/${m}" 2>/dev/null || echo 000)
  if [ "$code" = "200" ]; then
    git config --global "url.https://x:${GIT_TOKEN}@git.hanzo.ai/hanzoai/${m}.insteadOf" \
      "https://github.com/hanzoai/${m}"
    on="$on $m"
  else
    off="$off ${m}(${code})"
  fi
done

echo "forge-rewrites: forge serves ->$on"
[ -z "$off" ] || echo "forge-rewrites: left on GitHub ->$off"
