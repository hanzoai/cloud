#!/bin/sh
# One sweep over the packages that lift their doc comments, asked one of two ways.
#
#   zipdoc.sh check   — does the lifted prose still match its source? (the gate)
#   zipdoc.sh write   — lift it again (what `describe` needs before it composes)
#
# ONE INVOCATION. zipdoc takes package patterns — `zipdoc [-check] [packages]`, its own
# usage line — so the whole set goes in one call. Running it per package relinked the
# generator 121 times at about two seconds each, which was most of a gate that runs in
# front of every test.
#
# THE SET IS SELECTED, not `./...`, because `./...` reaches packages that lift nothing
# and the tool reports a missing zipdoc_gen.go for each of them — cmd/cloud,
# internal/planetest and plugin/todo among them. The directive is what says a package
# lifts prose, so the directive is what picks the set.
#
# ONE implementation because there were nearly two: the gate walks these packages to
# judge them and describe walks them to write them, and a second copy of "which packages
# lift prose" is how the two come to disagree about the set.
set -e

mode=${1:?usage: zipdoc.sh check|write}
GO=${GO:-go}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

case "$mode" in
  check) arg=-check ;;
  write) arg= ;;
  *) echo "zipdoc.sh: unknown mode '$mode' (check|write)" >&2; exit 2 ;;
esac

# Paths are normalised before sort -u because the same directory reached through
# `clients` and through `.` is two strings and would be handed over twice.
grep -rl '^//go:generate go run github.com/zap-proto/zip/cmd/zipdoc' \
     --include='*.go' clients cmd . 2>/dev/null \
  | grep -v '/\.' | xargs -n1 dirname | sed 's|^\./||' | sort -u | sed 's|^|./|' \
  | xargs $GO run github.com/zap-proto/zip/cmd/zipdoc $arg || exit 1
# `|| exit 1` because xargs answers 123 for "something I ran failed", and a gate whose
# exit code names the dispatcher rather than the refusal tells a reader nothing.
