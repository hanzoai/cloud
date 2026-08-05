# Fleet-wide targets: the same per-app contract, applied to the whole set.
#
# Included from the repo root (`include mk/fleet.mk`), or run directly as
# `make -f mk/fleet.mk <target>`. It defines no build logic of its own — every
# recipe here delegates to mk/plugin.mk, so "how an app is built" stays in
# exactly one file no matter how many apps a command touches.

# lastword, not firstword: firstword is whichever makefile make STARTED with, so
# running `make -f mk/fleet.mk` resolved the root correctly while `include
# mk/fleet.mk` from the root Makefile resolved to the repo's PARENT and broke the
# go.mk include below. lastword is this file in both cases, which is what makes the
# header's "included at the root it changes nothing" actually true.
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))..)
include $(ROOT)/mk/go.mk

# The apps, read from the Makefiles themselves. Those sit alongside each app's
# plugin/<app>/main.go, so globbing them reads the same single source of truth
# the mains do — no second list to fall out of step.
APPDIRS := $(patsubst %/Makefile,%,$(wildcard $(ROOT)/apps/*/Makefile))

# Three apps in the manifest have a plugin/<app> here and no source directory: they
# are external modules (hanzoai/authz, hanzoai/licensing, hanzoai/metrics) wired
# into apps.Wire() by import. There is nothing for a per-app Makefile to sit
# beside, so they are named here and run through the SAME mk/plugin.mk recipe by
# name instead of by location. When those repos publish their own subsets this
# list goes away and the loop below with it.
EXTERNAL := authz licensing metrics

# Apps that cannot project their own document without a live dependency. kafka's
# Mount is fail-closed on a pubsub broker (apps/kafka/kafka.go), and a document is
# a projection of ROUTES — it should not need a running message bus — so this is a
# defect in the app, not a property of the gate. It is exempt BY NAME and prints
# itself when skipped, rather than being silently passed over, and the exemption
# is cheap to hold honest: kafka's subset declares zero paths, so it contributes
# nothing to the fleet spec and has no drift it could hide. It goes away when the
# adaptor moves out to hanzoai/stream.
OPENAPI_NEEDS_BROKER := kafka

# Apps with NO STANDALONE MOUNT, DERIVED from the manifest rather than named.
#
# A coresident app is middleware on a SIBLING's router (manifest.App.Coresident):
# it claims no prefix, and mounting it alone is refused by construction — "zen
# installed middleware at /v1, outside the prefixes it owns". describe mounts one
# app alone, so there is nothing here for it to project. That is a property of
# coresidency, not a defect like kafka's broker, and like kafka the subset is
# empty and therefore has no drift it could hide.
#
# Read from apps.go the same way the root Makefile reads APPS, because the ONE
# place an app declares it is coresident must also be the place this loop learns
# it. Named here instead, the next coresident app would go missing exactly the way
# zen did — and zen went missing INVISIBLY, by having no Makefile at all, so the
# glob below simply never saw it and nothing said so.
CORESIDENT := $(shell sed -n 's/.*{Name: "\([^"]*\)".*Coresident: true.*/\1/p' $(ROOT)/manifest/apps.go)

.PHONY: openapi-weave describe-apps surface-check

# FIRST, so a bare `make -f mk/fleet.mk` runs the two-second check and not the
# twelve-minute rebuild. (Included at the root it changes nothing: the default
# goal is still the root Makefile's own first target.)
#
# The composition proof: weave the subsets and check the result against the
# fully-mounted golden. It links no subsystem and mounts nothing, so it is cheap
# enough to run on every push — see openapi/weave_test.go for what it refuses.
# OUT=<path> also writes the woven document (the artifact the SDK repos pull).
openapi-weave: ## Weave the per-app subsets into the fleet spec and prove it equals openapi.yaml. OUT=<path> to write it.
	@$(GO) test -count=1 $(ROOT)/openapi $(if $(OUT),-weave="$(abspath $(OUT))")

# The exemptions are honoured HERE as well as in the gate, because the gate's
# own failure message says "fix: make describe" — and that fix routed through this
# loop, which mounted kafka, which fails closed without a live broker. So the one
# command told to repair a red gate could not run at all. Each exemption is
# defined once and read everywhere it applies; a repair path that skipped fewer
# apps than the gate would be the same bug again.
describe-apps: ## Regenerate EVERY app's own spec subset (one binary per app; slow by construction).
	@set -e; for d in $(APPDIRS); do \
	  a=$$(basename $$d); \
	  case " $(OPENAPI_NEEDS_BROKER) " in \
	    *" $$a "*) echo ">> skip $$a — needs a live broker to mount (OPENAPI_NEEDS_BROKER)"; continue;; \
	  esac; \
	  case " $(CORESIDENT) " in \
	    *" $$a "*) echo ">> skip $$a — coresident: middleware on a sibling's router, no standalone mount to project"; continue;; \
	  esac; \
	  $(MAKE) --no-print-directory -C $$d describe; \
	done
	@for a in $(EXTERNAL); do $(MAKE) --no-print-directory -f $(ROOT)/mk/plugin.mk ROOT=$(ROOT) APPS=$$a describe || exit 1; done
	@echo ">> $$(ls $(ROOT)/plugin/*/openapi.json | wc -l) app subsets"

# The drift gate. It REGENERATES FROM SOURCE and fails on any diff, which is the
# whole difference between it and openapi-weave.
#
# openapi-weave compares two COMMITTED artifacts — the subsets and the golden they
# weave into. Both are derived, and nothing forces either back to the routes, so
# they agree with each other while both are wrong. That is not hypothetical: it is
# how plugin/ingress lost eight paths (/v1/ingress/routes, /services, /middlewares,
# /tls, /status and their :id forms). Routes were added, the subset was never
# regenerated, the golden was woven from that same stale subset, the weave passed,
# and the entire ingress API was absent from openapi.yaml — and therefore from every
# SDK generated off it, so no Python, Go or TS caller could reach it at all.
#
# The same blindness hid a second thing: main once carried documents generated by
# one zip version against a go.mod pinning an older one that could not produce
# them. Everything agreed with everything, and the next regeneration would have
# silently dropped the parameter examples, the derived required-ness and the $ref
# sharing. Two independent failures, one cause — so the fix is not a better
# comparison between derived things, it is regenerating the derived thing.
#
# It checks with `git status --porcelain`, not `git diff`: a NEW app produces a
# NEW subset, which is untracked and therefore invisible to a diff — the failure
# that matters most is exactly the one a diff would miss.
surface-check: ## Regenerate every subset + the fleet spec FROM SOURCE and fail on any diff. The drift gate.
	@set -e; \
	for d in $(APPDIRS); do \
	  a=$$(basename $$d); \
	  case " $(OPENAPI_NEEDS_BROKER) " in \
	    *" $$a "*) echo ">> skip $$a — needs a live broker to mount (OPENAPI_NEEDS_BROKER)"; continue;; \
	  esac; \
	  case " $(CORESIDENT) " in \
	    *" $$a "*) echo ">> skip $$a — coresident: middleware on a sibling's router, no standalone mount to project"; continue;; \
	  esac; \
	  out=$$($(MAKE) --no-print-directory -C $$d describe 2>&1) \
	    || { echo "$$out"; echo "!! $$a cannot project its own document — an app that cannot describe itself is the bug"; exit 1; }; \
	done; \
	for a in $(EXTERNAL); do \
	  out=$$($(MAKE) --no-print-directory -f $(ROOT)/mk/plugin.mk ROOT=$(ROOT) APPS=$$a describe 2>&1) \
	    || { echo "$$out"; echo "!! $$a cannot project its own document"; exit 1; }; \
	done
	@out=$$($(MAKE) --no-print-directory -f $(ROOT)/mk/fleet.mk openapi-weave OUT=$(ROOT)/openapi.yaml 2>&1) \
	  || { echo "$$out"; echo "!! the weave refused; nothing was written"; exit 1; }
	@stale=$$(git -C $(ROOT) status --porcelain -- openapi.yaml openapi/floor.json plugin/); \
	if [ -n "$$stale" ]; then \
	  echo "$$stale"; \
	  git -C $(ROOT) diff --stat -- openapi.yaml plugin/; \
	  echo ""; \
	  echo "STALE: regenerating the document from source produced something other than what is"; \
	  echo "committed. The list above is published surface — routes that exist and are"; \
	  echo "undocumented, or documented and gone. The SDK repos pull this file, so a route"; \
	  echo "missing here is a route no generated client can reach."; \
	  echo ""; \
	  echo "  fix:  make describe  # then commit openapi.yaml and plugin/*/openapi.json"; \
	  echo ""; \
	  exit 1; \
	fi
	@echo ">> openapi.yaml regenerated from source and unchanged — $$(grep -c '^  /' $(ROOT)/openapi.yaml) paths"
