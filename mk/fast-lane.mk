# mk/fast-lane.mk — the CHEAP lane. Aganior the canonical CI's 50-minute steps
# (go-vet ~22m, app-contract ~35–53m on one runner) into a ~1–5 minute incremental
# answer, and the plugin fleet's artifact so a release is not bloated by it.
#
#	i. **An `affected` gate:**
#	Dirs that are exactly what this push changed (source files measured by cmd/cone
#	dirs, generous + dep closure). A push touching nothing (merge commit that is a
#	merge of a commit ahead and a tree change) empties the set: nothing to vet or
#	test and the line exits 0 fast.
#
#	  ii. **A `dist` target** that builds ONLY the affected app's binary, as
#	  dist/<os>-<arch>, and place it somewhere `docker cp && apply` can find.
#
# Both reuse the EXACT machinery of mk/plugin.mk / mk/fleet.mk — this file invents
# no rule of its own. Exhaustive checks are cheap on the slow lane they may be
# slow globally; on the fast lane, the critical path is small.
#
# OPT-IN: the canonical hanzo.yml test: steps stay exhaustive and slow (the
# tree's safety net). CI picks the fast lane by consuming the target that matches
# the slice it wants; cron authors behind it keep the exhaustive steps too.

FLEET_FAST_GO_ENV := GOPRIVATE='github.com/hanzoai/*,github.com/hanzo-inc/*,zap-proto.io/*' \
	GONOPROXY=none GOPROXY='https://proxy.golang.org|direct' GOWORK=off CGO_ENABLED=1
CONSOLE_CSRF_KEY ?= ZGV2LWNzcmYta2V5LWZvci10ZXN0cy1vbmx5LTAwMDA=

FAST_PRE := \
	export GOCACHE=$${GOCACHE:-/var/tmp/hanzo-go/build}; \
	export GOMODCACHE=$${GOMODCACHE:-/var/tmp/hanzo-go/mod}; \
	mkdir -p "$$GOCACHE" "$$GOMODCACHE"; \
	$(FLEET_FAST_GO_ENV)

.PHONY: fast-affected fast-fleet fast-info
fast-affected: ## vet+unit ONLY what this push touched (union of cmd/cone dirs vs main).
	@set -euo pipefail; \
	base=$$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main); \
	dirs=$$($(GO) run ./cmd/cone dirs "$$base"); \
	[ -n "$$dirs" ] || { echo "affected: NO dirs — nothing to vet. PASS fast."; exit 0; }; \
	echo "affected: $$dirs"; export GOFLAGS=$$GOFLAGS; export CONSOLE_CSRF_KEY=$(CONSOLE_CSRF_KEY); \
	$(FAST_PRE); \
	go vet $$dirs; \
	go test -timeout 10m -tags 'sqlite_fts5 sqlite_math_functions' $$dirs

fast-fleet: ## describe the apps this push touched, from fleet.mk's own fan.
	@set -euo pipefail; \
	base=$$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main); \
	keys=$$($(GO) run ./cmd/cone dirs $$base | tr ' ' '\n' | grep -oE '^apps/[a-z0-9_-]+$$' | cut -d'/' -f2); \
	[ -n "$$keys" ] || { echo "fast-fleet: nothing in apps/ touched. PASS."; exit 0; }; \
	echo "fast-fleet: $$keys"; \
	for a in $$keys; do $(MAKE) --no-print-directory describe/$$a; done

fast-info:
	@echo "apps:       $$(ls apps | wc -l)"
	@echo "affected:   $$($(GO) run ./cmd/cone dirs "$$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main)" | tr ' ' '\n' | wc -l) dirs"
