#!/bin/sh
# One sweep over every package that lifts its doc comments, asked one of two ways.
#
#   zipdoc.sh check   — does the lifted prose still match its source? (the gate)
#   zipdoc.sh write   — lift it again (what `describe` needs before it composes)
#
# BUILT ONCE, ASKED IN PARALLEL. The //go:generate directive says `go run`, so both
# callers used to relink the generator once per package — 121 links at ~1.9s each,
# serially, in front of every test on a repo taking sixty-odd commits an hour. The
# directive stays as it is, because a directive naming a module is what lets anyone
# run `go generate ./...` without this file; it is the SWEEPS that stop paying for
# the same binary 121 times.
#
# ONE implementation because there were nearly two: the gate walks these packages to
# judge them and describe walks them to write them, and a second copy of "which
# packages lift prose" is how the two come to disagree about the set.
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

why=$(mktemp -d)
trap 'rm -rf "$why"' EXIT
bin="$why/zipdoc"

$GO build -o "$bin" github.com/zap-proto/zip/cmd/zipdoc || {
  echo "zipdoc: the generator itself does not build — that is the failure, not the prose" >&2
  exit 1
}

# The packages that lift prose. Paths are normalised before sort -u because the same
# directory reached through `clients` and through `.` is two strings and was walked twice.
dirs=$(grep -rl '^//go:generate go run github.com/zap-proto/zip/cmd/zipdoc' \
         --include='*.go' clients cmd . 2>/dev/null \
       | grep -v '/\.' | xargs -n1 dirname | sed 's|^\./||' | sort -u)

printf '%s\n' "$dirs" | xargs -P "$(nproc)" -I{} sh -c \
  'cd "{}" && "$0" '"$arg"' > "$1/$(echo {} | tr / _).out" 2>&1 || echo {} >> "$1/stale"' \
  "$bin" "$why"

stale=$(cat "$why/stale" 2>/dev/null | sort -u | tr '\n' ' ')
[ -z "$stale" ] && exit 0

if [ "$mode" = write ]; then
  echo "" >&2
  echo "zipdoc could not lift these packages:" >&2
  for d in $stale; do
    echo "    $d" >&2
    sed -e 's/^/        /' "$why/$(echo "$d" | tr / _).out" 2>/dev/null | tail -8 >&2
  done
  exit 1
fi

# EVERY stale package, never the first: stopping at one cost the release train six red
# runs in a day, because the run after the fix found the next one and said the same word.
#
# AND WHY, not only which. Three different failures used to arrive as one: the prose
# genuinely drifted, the generator could not BUILD, or zipdoc REFUSED the package (a
# router it cannot resolve statically, which it is designed to refuse rather than file
# prose under a path that does not exist). Those have three different remedies, and the
# report named one — `go generate` — which repairs only the first.
echo ""
echo "STALE: the lifted prose no longer matches the source it was lifted from."
echo "Go drops comments at compile time, so zipdoc_gen.go is the ONLY path from a"
echo "typed handler's doc comment to /v1/openapi.json — a stale lift ships a binary"
echo "that describes itself wrongly."
echo ""
for d in $stale; do
  echo "    $d/zipdoc_gen.go"
  sed -e 's/^/        /' "$why/$(echo "$d" | tr / _).out" 2>/dev/null | tail -8
done
echo ""
echo "  fix:"
printf '    go generate -run zipdoc'
for d in $stale; do printf ' ./%s/...' "$d"; done
echo ""
echo ""
exit 1
