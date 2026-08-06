# THE build contract for one app. Included by every apps/<app>/Makefile,
# which is otherwise just the app's name.
#
# Why one file instead of a target per app: `build` is the same command for every
# app, and so are `test`, `vet`, `describe` and `clean`. A target written once per
# app is one place per app for them to disagree — and they would, because nobody
# edits a hundred files at once. Here the contract has ONE definition and each app
# supplies the ONE thing that actually varies: its name.
#
# It works from the repo root (`make -C apps/tasks describe`) and from inside
# the app (`cd apps/tasks && make describe`), because everything below is
# absolute and derived from the including Makefile's own location — never from
# the caller's cwd. That is not a convenience: task #49 extracts apps into their
# own repos, and an extracted apps/<app> + plugin/<app> + mk/ keeps these paths
# intact, so extraction is a move rather than a rewrite.
#
# APPS is a LIST and is never inferred from the directory name. Four packages
# are not named after the app they back (eval → evals,
# auditlog → audit, plugin → plugins), and it stays a LIST because a package
# backing two mounts is a shape the fleet has had and will have again —
# apps/account carried its self-service routes and the /v1/billing catch-all
# bridge that way until the bridge was retired. An inferred name would be right
# 100 times and silently wrong 4.

# Simply-expanded: resolved once, from THIS include's own position, so no later
# include can move them. A command-line ROOT= still wins (mk/fleet.mk uses that
# for the apps whose source is another module).
APPDIR := $(patsubst %/,%,$(dir $(abspath $(firstword $(MAKEFILE_LIST)))))
ROOT   ?= $(abspath $(APPDIR)/../..)

# Where the binary lands and what it is called. Two seams with one caller —
# mk/fleet.mk's `dist`, which builds each app for every platform into one flat
# directory, and a flat directory cannot hold two `billing`s. There is no second
# naming rule: SUFFIX is empty for every other caller, so `build` still writes
# exactly $(BIN)/<app>, and the publishable layout is this same recipe with the
# platform spelled into the name (hanzoai/ci's Go lane writes <name>-<os>-<arch>
# too, and manifest/release.go keys on that triple).
SUFFIX ?=

ifeq ($(strip $(APPS)),)
$(error APPS is unset — a Makefile including mk/plugin.mk must name the app(s) it backs)
endif

include $(ROOT)/mk/go.mk

BIN ?= $(ROOT)/bin

.DEFAULT_GOAL := help
.PHONY: help generate build test vet describe clean

help: ## Show this help.
	@awk 'BEGIN{FS=":.*##";printf "\n%s: make <target>\n\n", "$(APPS)"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# Go drops comments at compile time, so this build-time pass is the ONLY way a
# typed handler's prose and examples reach the document — zipdoc lifts them into
# zipdoc_gen.go, which registers them with zip.Describe at init. The files ARE
# committed today, deliberately: the root make targets and a bare `go build` do
# not run this step, and until every path regenerates (the Dockerfile now does,
# this per-app chain always has), an untracked file means a binary whose
# /v1/openapi.json is missing every description. `make test` runs zipdoc -check,
# so a lift that drifts from its source turns CI red instead of shipping stale.
#
# It is a prerequisite of `build`, not of `describe`, because the generated file
# is compiled INTO the binary — running it after the build would be too late.
#
# An external app's source is another module: nothing here to lift, and nothing
# writable to lift it into.
#
# GOOS/GOARCH are CLEARED for it. The directive is `go run
# github.com/zap-proto/zip/cmd/zipdoc` — a HOST tool — so inheriting a
# cross-compilation target builds an amd64 zipdoc on an arm64 box and then tries
# to exec it. A generator runs where make runs, always; the target platform is a
# property of the artifact, never of the tool that writes its source.
generate: ## Lift this app's typed-handler doc comments into zipdoc_gen.go.
	@ls $(APPDIR)/*.go >/dev/null 2>&1 || exit 0; \
	 GOOS= GOARCH= $(GO) generate -run zipdoc $(APPDIR)/...

# One binary per app, into the shared ./bin the host resolves plugins from
# (manifest.App.Plugin looks for a file beside the host). Sequential over $(APPS)
# because that list is at most two long — the fleet's parallelism is one level
# up, where the apps are (mk/fleet.mk), which is the level that can actually
# bound how many links run at once. Measured, the heaviest link in this repo is
# o11y at 1.67 GB resident; that number is what mk/fleet.mk divides the box by.
build: generate ## Build this app's binary into <root>/bin.
	@mkdir -p $(BIN) $(TMPDIR)
	@for a in $(APPS); do \
	  echo ">> build $$a$(SUFFIX)"; \
	  CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN)/$$a$(SUFFIX) $(ROOT)/plugin/$$a || exit 1; \
	done

test: ## Run this app's tests.
	@$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" $(APPDIR)/...

vet: ## go vet this app and its entrypoint(s).
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) vet $(APPDIR)/... $(foreach a,$(APPS),$(ROOT)/plugin/$(a))

# The app's OWN projection, from the app's OWN live router: the binary mounts one
# subsystem and projects it through the same openapi.FleetSpec the whole-fleet
# golden is projected through (describe.go). Never sliced out of the fleet spec by
# prefix — that would make the fleet the source and the app a derivative, which is
# backwards and is exactly how a catch-all silently swallows a neighbour's routes.
#
# It used to write mcp.json beside it — the same registry projected as MCP tools —
# and the argument was that generating them together kept them honest. They were
# BOTH stale by the same 353 ops for o11y, because the trigger was a go.mod bump in
# another repository. The tool catalogue is not generated any anymore: the host
# ASKS each subsystem for its tools at the moment it is asked (package fleet).
#
# `build` first, because a projection taken from a stale binary is a lie.
#
# GIT_SSH_ADDR: mounting is not free of side effects — apps/git opens a real
# SSH listener on a fixed :2222 — and a projection is a function of routes, not a
# reason to contend for a port with a cloud already running on the box. The same
# ephemeral-port convention the shared describe.go spec harness uses.
#
# The binary is handed the DIRECTORY, never a redirect: a subsystem's dependencies
# write to stdout at mount (hanzoai/commerce prints a sqlite-vec warning and GORM
# debug lines), and `> file` splices those into the front of the document.
describe: build ## Emit this app's own OpenAPI subset into plugin/<app>/.
	@for a in $(APPS); do \
	  echo ">> describe $$a"; \
	  GIT_SSH_ADDR=127.0.0.1:0 $(BIN)/$$a describe $(ROOT)/plugin/$$a || exit 1; \
	done

# Binaries only. plugin/<app>/openapi.json is a committed artifact, like the
# fleet's openapi.yaml — `clean` removes what a build wrote, not what it publishes.
clean: ## Remove this app's built binary.
	@rm -f $(foreach a,$(APPS),$(BIN)/$(a))
