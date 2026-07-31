# THE build contract for one app. Included by every apps/<app>/Makefile,
# which is otherwise just the app's name.
#
# Why one file instead of a target per app: `build` is the same command for every
# app, and so are `test`, `vet`, `openapi` and `clean`. A target written once per
# app is one place per app for them to disagree — and they would, because nobody
# edits a hundred files at once. Here the contract has ONE definition and each app
# supplies the ONE thing that actually varies: its name.
#
# It works from the repo root (`make -C apps/tasks openapi`) and from inside
# the app (`cd apps/tasks && make openapi`), because everything below is
# absolute and derived from the including Makefile's own location — never from
# the caller's cwd. That is not a convenience: task #49 extracts apps into their
# own repos, and an extracted apps/<app> + plugin/<app> + mk/ keeps these paths
# intact, so extraction is a move rather than a rewrite.
#
# APPS is a LIST and is never inferred from the directory name. Four packages
# are not named after the app they back (apps/zt → zero-trust, eval → evals,
# auditlog → audit, plugin → plugins) and apps/account backs TWO mounts — its
# self-service routes and the /v1/billing catch-all bridge. An inferred name
# would be right 99 times and silently wrong 5.

# Simply-expanded: resolved once, from THIS include's own position, so no later
# include can move them. A command-line ROOT= still wins (mk/fleet.mk uses that
# for the apps whose source is another module).
APPDIR := $(patsubst %/,%,$(dir $(abspath $(firstword $(MAKEFILE_LIST)))))
ROOT   ?= $(abspath $(APPDIR)/../..)

ifeq ($(strip $(APPS)),)
$(error APPS is unset — a Makefile including mk/plugin.mk must name the app(s) it backs)
endif

include $(ROOT)/mk/go.mk

BIN := $(ROOT)/bin

.DEFAULT_GOAL := help
.PHONY: help generate build test vet openapi clean

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
# It is a prerequisite of `build`, not of `openapi`, because the generated file
# is compiled INTO the binary — running it after the build would be too late.
#
# An external app's source is another module: nothing here to lift, and nothing
# writable to lift it into.
generate: ## Lift this app's typed-handler doc comments into zipdoc_gen.go.
	@ls $(APPDIR)/*.go >/dev/null 2>&1 || exit 0; \
	 $(GO) generate -run zipdoc $(APPDIR)/...

# One binary per app, into the shared ./bin the host resolves plugins from
# (manifest.App.Plugin looks for a file beside the host). Sequential: each link
# peaks in the GiBs.
build: generate ## Build this app's binary into <root>/bin.
	@mkdir -p $(BIN) $(TMPDIR)
	@for a in $(APPS); do \
	  echo ">> build $$a"; \
	  CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN)/$$a $(ROOT)/plugin/$$a || exit 1; \
	done

test: ## Run this app's tests.
	@$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" $(APPDIR)/...

vet: ## go vet this app and its entrypoint(s).
	@CGO_ENABLED=$(CGO_ENABLED) $(GO) vet $(APPDIR)/... $(foreach a,$(APPS),$(ROOT)/plugin/$(a))

# The app's OWN subset of the API document, from the app's OWN live router: the
# binary mounts one subsystem and projects it through the same
# openapi.FleetSpec the whole-fleet golden is projected through (openapi_dump.go).
# It is never sliced out of the fleet spec by prefix — that would make the fleet
# the source and the app a derivative, which is backwards and is exactly how a
# catch-all silently swallows a neighbour's routes.
#
# `build` first, because a spec generated from a stale binary is a lie.
#
# GIT_SSH_ADDR, CLOUD_PUBSUB_*: mounting is not free of side effects — apps/git
# opens a real SSH listener on a fixed :2222 and apps/pubsub binds the NATS client
# port on a fixed :4222, both FAIL-CLOSED — and a document is a projection of
# ROUTES, not a reason to contend for a port with a cloud already running on the
# box. Each app's own ephemeral-port knob (the same one its tests use: :0 for the
# listener, -1 for "pick a free port" in NATS) makes projecting a document
# independent of what else holds a port here. Without them the gate is red for an
# environmental reason on any box running a cloud — and a gate that cannot be run
# is a gate that stops being run.
#
# The binary is handed the PATH, never a redirect: a subsystem's dependencies
# write to stdout at mount (hanzoai/commerce prints a sqlite-vec warning and GORM
# debug lines), and `> file` splices those into the front of the document.
openapi: build ## Emit this app's own subset of the API document into plugin/<app>/openapi.json.
	@for a in $(APPS); do \
	  echo ">> openapi $$a"; \
	  GIT_SSH_ADDR=127.0.0.1:0 CLOUD_PUBSUB_HOST=127.0.0.1 CLOUD_PUBSUB_PORT=-1 \
	    $(BIN)/$$a openapi $(ROOT)/plugin/$$a/openapi.json || exit 1; \
	done

# Binaries only. plugin/<app>/openapi.json is a committed artifact, like the fleet's
# openapi.yaml — `clean` removes what a build wrote, not what a build publishes.
clean: ## Remove this app's built binary.
	@rm -f $(foreach a,$(APPS),$(BIN)/$(a))
