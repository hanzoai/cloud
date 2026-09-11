#!/usr/bin/env bash
# Can anyone fetch what this binary is built from?
#
# `run.sh` counts private module paths in go.mod, which is a property of a file.
# This asks the public Go proxy for every module the binary actually needs, with
# no credential: a module that answers is one anybody can fetch, and a module
# that does not is a dependency only we can resolve.
#
# It ends by asking for a module that does not exist. A probe that answers 200
# for everything would report a clean sweep for a tree full of private
# dependencies, so the run fails unless the absent one is refused.
set -uo pipefail
cd "$(dirname "$0")/../.."
. bench/host.sh
export GOWORK=off

say() { printf '%-34s %s\n' "$1" "$2"; }

host

# The Go proxy lowercases an uppercase letter and marks it with a bang, so a
# module path is not its own URL.
esc() {
  python3 -c 'import sys, re; print(re.sub(r"[A-Z]", lambda m: "!" + m.group(0).lower(), sys.argv[1]))' "$1"
}

ask() { curl -s -o /dev/null -w '%{http_code}' --max-time 20 "https://proxy.golang.org/$(esc "$1")/@v/list"; }

mods=$(go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/cloud 2>/dev/null | sort -u | grep -v '^$')
total=$(echo "$mods" | wc -l | tr -d ' ')

ok=0; refused=0
while read -r m; do
  [ -z "$m" ] && continue
  if [ "$(ask "$m")" = "200" ]; then ok=$((ok + 1)); else refused=$((refused + 1)); echo "  not public: $m"; fi
done <<<"$mods"

say "modules the binary needs" "$total"
say "fetchable with no credential" "$ok"
say "not" "$refused"

control=$(ask "github.com/hanzoai/a-module-that-does-not-exist")
say "control: an absent module" "$control"
[ "$control" = "200" ] && {
  echo "the proxy answered for a module that does not exist; the sweep above is not evidence" >&2
  exit 1
}
[ "$refused" -gt 0 ] && exit 1
exit 0
