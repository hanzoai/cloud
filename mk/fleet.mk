# Fleet-wide targets: the same per-app contract, applied to the whole set.
#
# Included from the repo root (`include mk/fleet.mk`), or run directly as
# `make -f mk/fleet.mk <target>`. It defines no build logic of its own — every
# recipe here delegates to mk/plugin.mk, so "how an app is built" stays in
# exactly one file no matter how many apps a command touches.

ROOT := $(abspath $(dir $(firstword $(MAKEFILE_LIST)))..)
include $(ROOT)/mk/go.mk

# The apps, read from the Makefiles themselves. Those are generated from
# apps.Wire() alongside cmd/<app>/main.go, so globbing them reads the same single
# source of truth the mains do — no second list to fall out of step.
APPDIRS := $(patsubst %/Makefile,%,$(wildcard $(ROOT)/clients/*/Makefile))

# Three apps in the manifest have a cmd/<app> here and no source directory: they
# are external modules (hanzoai/authz, hanzoai/licensing, hanzoai/metrics) wired
# into apps.Wire() by import. There is nothing for a per-app Makefile to sit
# beside, so they are named here and run through the SAME mk/plugin.mk recipe by
# name instead of by location. When those repos publish their own subsets this
# list goes away and the loop below with it.
EXTERNAL := authz licensing metrics

.PHONY: openapi-weave openapi-apps

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

openapi-apps: ## Regenerate EVERY app's own spec subset (one binary per app; slow by construction).
	@for d in $(APPDIRS); do $(MAKE) --no-print-directory -C $$d openapi || exit 1; done
	@for a in $(EXTERNAL); do $(MAKE) --no-print-directory -f $(ROOT)/mk/plugin.mk ROOT=$(ROOT) APPS=$$a openapi || exit 1; done
	@echo ">> $$(ls $(ROOT)/cmd/*/openapi.json | wc -l) app subsets"
