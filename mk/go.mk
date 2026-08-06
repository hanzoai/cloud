# The Go toolchain contract — the settings every build in this repo shares.
#
# It is a separate file from the targets that use it because there are two
# includers: mk/plugin.mk (one app) and mk/fleet.mk (the whole set), with the
# root Makefile the third once it drops its copies. A setting that differed
# between them would make an app's binary differ from the fleet's for reasons
# nobody could see in a diff.

GO          ?= go
CGO_ENABLED ?= 0
LDFLAGS     ?= -s -w

# cloud is a STANDALONE module and is deliberately NOT in the parent go.work.
# `go` auto-discovers that workspace from anywhere under ~/work, which shadows
# this module's replace/exclude directives and breaks the build. Force module
# mode so a developer builds EXACTLY what CI and Docker build.
export GOWORK := off

# Linking a cloud binary is a multi-GiB act and the Go LINKER writes its
# temporaries to TMPDIR (not GOTMPDIR). Where /tmp is a tmpfs that is RAM, and a
# fleet-wide target is a hundred links back to back. Point it at disk.
export TMPDIR ?= $(HOME)/.cache/go-tmp
# ...and it must EXIST. `go` does not create TMPDIR; it stats it and dies
#   go: creating work dir: stat /root/.cache/go-tmp: no such file or directory
# On a dev box ~/.cache is already there, so this only ever bit CI — where it
# failed `test app-contract` and, through it, every cloud image since
# 2026-07-29. The last good build was 01:10 that day; the AI balance-reader fix
# sat un-shippable behind it while hanzo.app answered 503 balance_unavailable.
TMPDIR_READY := $(shell mkdir -p $(TMPDIR) && echo ok)

# ONE number, split two ways, because the two multiply. A fleet target runs J
# apps at once and each of those spawns P compilers, so the box sees J*P; a solo
# build is J=1 and gets the whole box. NPROC is the number, mk/fleet.mk derives
# JOBS from it, and this line derives P.
#
# P was a flat 2 for everyone, and that was the fleet's answer applied to the
# solo case that never had the problem: `go build` bounded to two compilers on a
# 20-core box. Measured on this repo, one app from an empty cache is 57.6s at
# -p=2 and 41.4s at -p=20 — a 28% tax on every developer rebuild and on every
# per-app CI job, paid to protect a fleet run that was never parallel to begin
# with. mk/fleet.mk states the fleet's P where the fleet's J is stated, which is
# the only place the product is actually known.
#
# It is stated rather than left to Go's default (which is also GOMAXPROCS)
# because `go env -w GOFLAGS=-p=6` in a developer's own config would otherwise
# decide it, and this file exists so that a developer builds EXACTLY what CI and
# Docker build.
NPROC := $(or $(NPROC),$(shell nproc 2>/dev/null || echo 4))
export GOFLAGS ?= -p=$(NPROC)

# The data plane has no plaintext-at-rest mode: cek refuses to open a store
# without a master key, on every build. The server makes that a boot decision; a
# test run has no boot, so the suite declares its dev posture once here. A key
# already in the environment always wins, so CI's real key is never overridden.
DEV_KMS_KEY := AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
TEST_ENV     = CLOUD_KMS_MASTER_KEY_REF="$${CLOUD_KMS_MASTER_KEY_REF:-$(DEV_KMS_KEY)}"

# The release image builds with -tags "libsqlite3 sqlite_fts5". sqlite_fts5 needs
# no cgo, and without it any store whose migration declares an FTS5 table fails
# to open — so a subsystem built on full-text search cannot be tested at all.
# Carry the tag the shipped build carries.
TEST_TAGS ?= sqlite_fts5
