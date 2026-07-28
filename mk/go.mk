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

# Each link peaks in the GiBs. Unbounded parallelism is how a fleet target OOMs
# a 128GiB box.
export GOFLAGS ?= -p=2

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
