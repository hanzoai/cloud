#!/usr/bin/env bash
# postgres.sh COMMAND [ARG...]
#
# Runs COMMAND with CLOUD_TEST_SQL_DSN naming a throwaway PostgreSQL, the server
# internal/sqltest runs every ported store against beside SQLite. The server is the
# pinned official build (the zonky embedded-postgres-binaries on Maven Central,
# sha256 below), unpacked into a private run directory, started with a fresh
# cluster, and stopped and deleted when COMMAND returns. It needs curl, unzip, tar
# and xz, and no container runtime, package manager or root: the CI runner has no
# docker daemon, and a service cannot be declared in hanzo.yml.
#
# THE SERVER IS REACHABLE BY ITS OWNER AND NO ONE ELSE. It listens on no TCP port,
# only on a unix socket in a 0700 directory, so "trust" authentication admits
# whoever can open that directory and nobody on the host's network, however shared.
# Its role is `cloud`, which may log in and create databases and nothing else, the
# same shape as the production role: the seam refuses a superuser.
#
# THE DOWNLOAD IS VERIFIED EVERY RUN. The jar is cached in a directory only the
# caller can write, and its sha256 is checked before each unpack, so a cache that
# was altered after the first run is refused and not trusted.
#
# EVERYTHING DIES WITH THIS SCRIPT. The server and COMMAND are children with a
# parent-death signal, and a watcher that outlives the script removes the run
# directory, so a script killed with SIGKILL, which no trap can answer, leaves
# neither a server, a stray COMMAND nor a 100 MB directory behind.
#
# Linux only: it needs setpriv (util-linux), stat -c and a /var/tmp that is local.
#
#   scripts/postgres.sh go test ./apps/flags/...
set -euo pipefail

for tool in curl unzip tar xz setpriv flock; do
	command -v "$tool" >/dev/null 2>&1 && continue
	# A CI runner image may lack one; install what is missing if the host lets us.
	sudo -n apt-get update -qq && sudo -n apt-get install -y -qq curl unzip tar xz-utils util-linux ||
		{ echo "postgres.sh: needs curl, unzip, tar, xz and setpriv, and $tool is missing" >&2; exit 1; }
	break
done

version=18.6.0
case "$(uname -m)" in
x86_64 | amd64)
	arch=amd64
	sha=008ee189eaa7ca2b58bd01dd950291e638b998392cf982297acd4779ec898ac1
	;;
aarch64 | arm64)
	arch=arm64v8
	sha=5f54e016723afa6213925a8acb939bac52944b1d348f65c7a20f0cd2928c31be
	;;
*)
	echo "postgres.sh: no PostgreSQL $version build for $(uname -m)" >&2
	exit 1
	;;
esac

me=$(id -u)
cache=/var/tmp/hanzo-postgres-$me
install -d -m 0700 "$cache"
if [ "$(stat -c %u "$cache")" != "$me" ]; then
	echo "postgres.sh: $cache is not owned by uid $me; refusing to trust what is in it" >&2
	exit 1
fi
chmod 0700 "$cache"

jarname=embedded-postgres-binaries-linux-$arch-$version.jar
jar=$cache/$jarname
if [ ! -f "$jar" ]; then
	part=$(mktemp "$cache/fetch.XXXXXX")
	curl -fsSL --retry 3 -o "$part" \
		"https://repo1.maven.org/maven2/io/zonky/test/postgres/embedded-postgres-binaries-linux-$arch/$version/$jarname"
	mv "$part" "$jar"
fi
if ! echo "$sha  $jar" | sha256sum -c --quiet - >/dev/null 2>&1; then
	rm -f "$jar"
	echo "postgres.sh: $jarname does not match its pinned sha256; deleted, run again to fetch it afresh" >&2
	exit 1
fi

# THE RUN DIRECTORY'S NAME IS STABLE, because it is in the DSN and the DSN is an input
# `go test` hashes into its cache key: a name that changed every run (it was mktemp)
# made every package that opens a store a cache MISS on every run, so a package no
# commit touched was recompiled-and-rerun anyway. Concurrent invocations on one host
# take distinct slots (flock), so each still has a server of its own at a name that
# repeats from run to run.
slots=/var/tmp/hanzo-pg-slots-$me
install -d -m 0711 "$slots" # traverse-only: a root runner's server runs as nobody, below this
slot=
for i in 0 1 2 3 4 5 6 7; do
	exec 9>"$slots/$i.lock"
	if flock -n 9; then slot=$i; break; fi
	exec 9>&-
done
[ -n "$slot" ] || { echo "postgres.sh: all 8 slots are in use" >&2; exit 1; }
run=$slots/run-$slot # 0700: the socket below is reachable through nothing else
rm -rf "$run"
install -d -m 0700 "$run"
mkdir "$run/pg"
unzip -p "$jar" '*.txz' | tar -xJ -C "$run/pg"
pg=$run/pg/bin

# PostgreSQL will not run as root. A root runner drops to nobody for the server alone;
# COMMAND keeps the caller's identity.
if [ "$me" = 0 ]; then
	chown -R 65534 "$run"
	server_as=(setpriv --reuid=65534 --regid=65534 --clear-groups --pdeathsig SIGINT)
else
	server_as=(setpriv --pdeathsig SIGINT)
fi

server=
stop() {
	if [ -n "$server" ]; then
		kill -INT "$server" 2>/dev/null || true
		wait "$server" 2>/dev/null || true
	fi
	rm -rf "$run"
}
trap stop EXIT
trap 'exit 130' INT
trap 'exit 143' TERM HUP

"${server_as[@]}" "$pg/initdb" -D "$run/data" -U postgres -A trust --no-sync --no-locale -E UTF8 >"$run/initdb.log"
printf 'CREATE ROLE cloud LOGIN CREATEDB;\n' |
	"${server_as[@]}" "$pg/postgres" --single -D "$run/data" postgres >"$run/role.log" 2>&1 ||
	{ cat "$run/role.log" >&2; exit 1; }

# No TCP (listen_addresses=''), the socket in the run directory and only its owner's,
# and fsync off because this cluster lives for one command.
#
# NO AUTOVACUUM. internal/sqltest drops each test's database WITH (FORCE) as `cloud`,
# which may terminate only its own role's sessions. An autovacuum worker that has
# attached to that database belongs to no role `cloud` holds, so the drop answered
# "permission denied to terminate process" (SQLSTATE 42501) and failed whichever
# postgres subtest it landed on: about fifteen different tests a run, a different
# fifteen each time. A database that lives for one test has nothing to vacuum.
"${server_as[@]}" "$pg/postgres" -D "$run/data" \
	-c listen_addresses= -c unix_socket_directories="$run" -c unix_socket_permissions=0700 \
	-c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=500 \
	-c autovacuum=off \
	>"$run/log" 2>&1 &
server=$!

for _ in $(seq 1 300); do
	grep -q 'ready to accept connections' "$run/log" 2>/dev/null && break
	if ! kill -0 "$server" 2>/dev/null; then
		echo "postgres.sh: the server did not start" >&2
		cat "$run/log" >&2
		exit 1
	fi
	sleep 0.1
done
if ! grep -q 'ready to accept connections' "$run/log"; then
	echo "postgres.sh: the server was not ready after 30s" >&2
	cat "$run/log" >&2
	exit 1
fi

# What a trap cannot do. If this script is killed outright, the server and COMMAND are
# told by the kernel (parent-death signal), and this watcher, which is not killed with
# it, waits for the server to go and then removes the run directory.
parent=$$
(
	while kill -0 "$parent" 2>/dev/null && [ -d "$run" ]; do sleep 1; done
	if [ -d "$run" ]; then
		for _ in $(seq 1 100); do kill -0 "$server" 2>/dev/null || break; sleep 0.1; done
		kill -KILL "$server" 2>/dev/null || true
		rm -rf "$run"
	fi
) >/dev/null 2>&1 </dev/null &

# COMMAND runs in the background and this script waits for it, because a shell does not
# run a trap while a foreground command is running: a SIGTERM to the script (a CI
# timeout, a supervisor) would otherwise wait for the whole test run to end before
# anything was stopped or removed. Waiting returns at once on a trapped signal.
# Standard input is handed through, which a background command would not get.
export CLOUD_TEST_SQL_DSN="postgres://cloud@/postgres?host=$run&sslmode=disable"
setpriv --pdeathsig SIGTERM "$@" <&0 &
command=$!
trap 'kill -INT "$command" 2>/dev/null; wait "$command" 2>/dev/null; exit 130' INT
trap 'kill -TERM "$command" 2>/dev/null; wait "$command" 2>/dev/null; exit 143' TERM HUP
status=0
wait "$command" || status=$?
exit "$status"
