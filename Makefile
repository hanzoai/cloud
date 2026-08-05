# hanzoai/cloud — developer ergonomics for the unified Hanzo Cloud binary (HIP-0106).
# Targets are intentionally minimal; deploy artifacts (compose, helm) live in deploy/ and helm/.

GO              ?= go

# cloud is a STANDALONE Go module — a self-contained deploy unit (its own go.mod,
# Dockerfile, binary). It is intentionally NOT a member of the parent
# ~/work/hanzo/go.work workspace (that workspace deliberately excludes the heavy
# modules; adding cloud would merge its k8s/otel graph with o11y's and reintroduce
# koanf/ugorji import ambiguities). But `go` auto-discovers that parent go.work
# whenever a dev builds from inside this tree, which shadows cloud's own
# replace/exclude directives (oxy pin, ugorji monolith exclude, k8s staging pins)
# and breaks `go build ./...`. Force module mode so make targets build EXACTLY
# what CI/Docker build (fresh checkout, no parent go.work). Overridable via
# `make GOWORK=... <target>` for the rare cross-module case.
export GOWORK := off
DOCKER_IMAGE    ?= ghcr.io/hanzoai/cloud
DOCKER_TAG      ?= dev
LDFLAGS         ?= -s -w

# What a binary REPORTS when asked. `git describe` is the source: the tag when
# the build is one, the sha when it is not, `-dirty` when the tree is not
# committed. It is APPENDED to LDFLAGS at each cmd/ target rather than folded
# into the LDFLAGS default, so `make LDFLAGS=...` keeps overriding exactly what
# it always did and still cannot produce an unstamped binary.
#
# Empty is a legitimate value — a release that builds from an export with no
# .git has nothing to describe. Empty stamps nothing, and cmd/hanzo's
# resolveVersion then answers from the metadata the toolchain embeds by itself.
VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null)
# Path to a hanzoai/console checkout used to build the embedded console bundle.
CONSOLE_DIR    ?= ../console
# Path to a hanzoai/openapi checkout — the SOT the agent-skills catalog is generated from.
OPENAPI_DIR    ?= ../openapi

# The shipped binary is NOT pure Go. Dockerfile builds /cloud with
#   CGO_ENABLED=1 go build -tags "libsqlite3 sqlite_fts5"
# (CGO_ENABLED=0 there builds only the /smoke helper), so production links the live
# libsqlcipher codec: databases are encrypted in place, per-commit, and shareable by
# a second opener. The default below is pure Go for a DIFFERENT reason — it
# registers the ONE "sqlite" driver exactly once:
# cloud's stores use github.com/hanzoai/sqlite (its !cgo backend IS modernc), and
# the embedded deps (ai/base/commerce/o11y/orm/tasks) that import modernc directly
# then resolve to the SAME package → a single registration. A plain CGO_ENABLED=1
# build instead links the fork's mattn backend ALONGSIDE those modernc importers
# and panics at init ("sql: Register called twice for driver sqlite"); `make
# test-cgo` proves the cgo path via the fork's `sqlite_purego` opt-out tag, which
# forces the fork to modernc too so the whole binary registers "sqlite" once.
#
# CONSEQUENCE, worth knowing before trusting a green run: neither target links
# libsqlcipher, so NEITHER exercises the engine the image ships. Without the codec,
# cek falls back to the pure-Go envelope, whose properties differ — a store is
# single-writer and durable at close rather than in-place and per-commit. The tests
# that pin the shipped storage posture (apps/kms concurrent-open, audit
# shareability) therefore skip in both targets. `make test-codec` below is the one
# that runs them, and needs a real libsqlcipher to do it.
CGO_ENABLED     ?= 0

# Every app the light host mounts, read from the generated manifest — the same
# list cmd/cloud links and the multi-call binary serves.
APPS := $(shell sed -n 's/.*{Name: "\([^"]*\)".*/\1/p' manifest/apps.go)

# The binary each app builds to. Named targets (not a loop) so make can schedule
# them in parallel and build exactly the one you ask for.
APP_BINS := $(addprefix bin/,$(APPS))

.PHONY: help webui deploy-ui skills build cloud hanzo ship apps $(APP_BINS) plugin generate describe run dev smoke test test-fast test-cgo test-codec vet lint tidy docker docker-push compose clean e2e

help: ## Show this help.
	@awk 'BEGIN{FS=":.*##";printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

webui: ## Build the real console static bundle into webui/dist (go:embed source). CONSOLE_DIR=<path to console>.
	@command -v npm >/dev/null 2>&1 || { echo "npm is required to build the console bundle"; exit 1; }
	@test -f "$(CONSOLE_DIR)/package.json" || { echo "console checkout not found at $(CONSOLE_DIR) — set CONSOLE_DIR=<path>"; exit 1; }
	@test -d "$(CONSOLE_DIR)/node_modules" || (cd "$(CONSOLE_DIR)" && npm install --no-audit --no-fund)
	cd "$(CONSOLE_DIR)" && NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=8192 npm run build:embed
	# Overlay the fresh static export onto webui/dist, keeping only the tracked
	# fallbacks (.gitignore + assets/.gitkeep); the real bundle is build-time-only.
	find webui/dist -mindepth 1 -maxdepth 1 ! -name .gitignore ! -name assets -exec rm -rf {} +
	cp -r "$(CONSOLE_DIR)/out/." webui/dist/
	@echo ">> embedded real console bundle into webui/dist (index.html $$(wc -c < webui/dist/index.html) bytes)"

deploy-ui: ## Build the monochrome ArgoCD dashboard bundle into apps/deploy/webui/dist (go:embed source). DEPLOY_DIR=<path to hanzoai/deploy>.
	@command -v yarn >/dev/null 2>&1 || { echo "yarn is required to build the deploy dashboard bundle"; exit 1; }
	@test -f "$(DEPLOY_DIR)/ui/package.json" || { echo "deploy checkout not found at $(DEPLOY_DIR) — set DEPLOY_DIR=<path to hanzoai/deploy on rebrand/hanzo-monochrome>"; exit 1; }
	@test -d "$(DEPLOY_DIR)/ui/node_modules" || (cd "$(DEPLOY_DIR)/ui" && yarn install --frozen-lockfile)
	cd "$(DEPLOY_DIR)/ui" && NODE_OPTIONS=--max-old-space-size=8192 yarn build
	# Overlay the fresh bundle, keeping only the tracked fallback (.gitignore +
	# index.html shell); the real 43MB bundle is build-time-only (gitignored).
	find apps/deploy/webui/dist -mindepth 1 -maxdepth 1 ! -name .gitignore -exec rm -rf {} +
	cp -r "$(DEPLOY_DIR)/ui/dist/app/." apps/deploy/webui/dist/
	@echo ">> embedded monochrome ArgoCD bundle into apps/deploy/webui/dist (index.html $$(wc -c < apps/deploy/webui/dist/index.html) bytes)"

skills: ## Regenerate the FULL agent-skills catalog into apps/skills/catalog (go:embed source) from the openapi SOT. OPENAPI_DIR=<path to openapi>.
	@test -f "$(OPENAPI_DIR)/skills.py" || { echo "openapi checkout not found at $(OPENAPI_DIR) — set OPENAPI_DIR=<path> or clone hanzoai/openapi"; exit 1; }
	# skills.py rewrites the whole catalog dir; the .gitignore keeps only the tiny
	# `ai` fallback tracked, so the full set is embedded at build but never committed.
	python3 "$(OPENAPI_DIR)/skills.py" --no-services --out apps/skills/catalog
	@echo ">> embedded FULL agent-skills catalog ($$(jq -r .skill_count apps/skills/catalog/hanzo/index.json) skills/brand)"

# THE DEFAULT BUILD IS THE HOST, and that is the whole point of the plugin model:
# nothing compiles together. The fused binary linked all 112 subsystems into one
# ~3040-package graph, so changing one line in one app relinked every other app
# with it — 9.5s and 3.8GiB of peak RSS warm, minutes cold, for a one-app change.
# That binary is GONE: there is no target that links the fleet, by design.
#
# The loop that replaces it is two commands, and neither grows as the fleet does.
# Measured here:
#
#   make build              # the router — ~395 packages, 0.6s
#   make plugin APP=wallets # the ONE app you edited — 1.3s, recompile + relink
#
# then restart the host. `plugin` declares no prerequisites, so the second command
# never drags the first along behind it.
build: cloud ## FAST PATH (default): build the light host into ./bin/cloud. Then `make plugin APP=<x>` for the app you are editing.

# THE LIGHT HOST links zip and the generated manifest and stops, because it knows
# only where each app lives and which paths it answers — never what the app does.
# The apps run as their own processes, started on the first request that reaches
# them, so the host's build does not grow when a subsystem does. The binary is
# named cloud — it IS the one real binary, and its ENTRYPOINT the image ships.
cloud: ## Build the light host into ./bin/cloud (links zip + the manifest, none of the apps).
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS) -X github.com/hanzoai/cloud.Version=$(VERSION)" -o bin/$@ ./cmd/$@
	@echo ">> bin/cloud — $$(CGO_ENABLED=$(CGO_ENABLED) $(GO) list -deps ./cmd/cloud | wc -l) packages, $$(du -h bin/cloud | cut -f1)"

# THE RELEASE LAYOUT: the light host plus one dedicated binary per app, all in
# ./bin. The host loads each app as a plugin (manifest.App.Plugin resolves a file
# beside it), so shipping is one directory — host + its plugins — with no fused
# binary at all. Each per-app link is its OWN graph (the one subsystem, not the
# fleet), so this is $(words $(APPS)) independent lean builds and not the mega
# link that used to dominate a release. Slow by count, never by any single link.
ship: cloud apps ## Build the release layout into ./bin: the light host + one binary per app.
	@echo ">> ship: cloud + $(words $(APPS)) per-app plugins in ./bin ($$(du -sh bin | cut -f1))"

# EVERY app, as $(words $(APPS)) independent targets rather than one loop, so make
# schedules them: `make -j apps` runs as many links at once as you allow, and a
# single app named on the command line builds only itself. The recipe is stated
# once and `plugin` calls it, so there is one way to build an app binary.
#
# Each link keeps GOFLAGS=-p=2: N concurrent builds each spawning NPROC compilers
# is how a parallel build turns into a thrash. Two per link, N links, is the shape
# that actually finishes.
apps: $(APP_BINS) ## Build every app binary into ./bin. Parallelise: make -j apps.
	@echo ">> apps: $(words $(APPS)) binaries in ./bin ($$(du -sh bin | cut -f1))"

$(APP_BINS): bin/%:
	@test -d plugin/$* || { echo "no plugin/$* — run 'make generate', or check the name against 'make plugin' with no APP"; exit 1; }
	@mkdir -p bin
	GOFLAGS=-p=2 CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o $@ ./plugin/$*

plugin: ## Build ONE app into ./bin: make plugin APP=wallets.
	@test -n "$(APP)" || { echo "usage: make plugin APP=<name>"; echo "apps: $(APPS)"; exit 1; }
	@echo "$(APPS)" | tr ' ' '\n' | grep -qx "$(APP)" || { echo "no app named $(APP) — the manifest is the list; run 'make generate' after adding a row, or check the name against 'make plugin' with no APP"; exit 1; }
	@$(MAKE) --no-print-directory bin/$(APP)

# manifest/apps.go is the hand-authored source of truth for the subsystem set.
# This scaffolds a plugin/<app>/main.go for any manifest app that lacks one and
# validates the two are in bijection (every app has a command, every command is
# an app). Idempotent: a no-op run leaves the tree clean, which is what lets CI
# diff it. It does NOT rewrite existing mains — those are source.
generate: ## Scaffold missing plugin/<app>/main.go and validate the manifest.Apps bijection.
	$(GO) run ./plugin/gen-app-cmds

# NOTE: the shipped API is the light host plus one binary per app (there is no
# fused `cloud` binary anymore). The `hanzo` name is served by two binaries: the
# Rust fabric CLI (~/work/hanzo/cli, `curl hanzo.sh`), which talks to this API
# over HTTP via its OpenAPI-generated command surface, and cmd/hanzo here, the
# CLIENT-ONLY control binary over cli/ that delegates every verb it does not
# register to that Rust CLI. cmd/hanzo links cli and nothing else — no app, no
# host — so it is not part of the API build above; it is its own target.
#
# That it had NO target is how it shipped reporting "dev": the version ldflag
# was documented in cmd/hanzo/main.go and written nowhere, so every build — the
# installed one included — answered `hanzo --version` with the placeholder. The
# stamp lives here now, and resolveVersion covers whoever still runs a bare
# `go build ./cmd/hanzo`.
hanzo: ## Build the control CLI into ./bin/hanzo (links cli and nothing else).
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS) -X main.version=$(VERSION)" -o bin/$@ ./cmd/$@

# Builds the host plus the plugins you want to exercise locally — not all 106.
# The host mounts what manifest.Apps lists and resolves each plugin as a file
# beside itself (manifest.App.Plugin); a lazy one with no binary simply never
# starts, and a Required one fails loudly. So this list is a BUILD list, not a
# mount list — the binary has never taken one, and stating the app set a second
# time is what took devnet down twice.
RUN_PLUGINS ?= iam,base,kms,gateway,o11y

run: cloud ## Run the host, building the plugins in RUN_PLUGINS (iam,base,kms,gateway,o11y).
	@for a in $$(echo $(RUN_PLUGINS) | tr ',' ' '); do $(MAKE) --no-print-directory plugin APP=$$a; done
	./bin/cloud

# dev and lint are the names every repo in the fleet answers to. They are ALIASES
# of the two targets that already do the work, never copies of them, so each of
# those two things still has exactly one recipe.
dev: run ## Alias for run.

smoke: ## Build and run the smoke prober (mount-time integration check).
	$(GO) run ./plugin/smoke

# The ONE end-to-end target: builds this repo's binary, boots it on isolated ports
# with a fresh data dir, seeds identity through the same operator upsert production
# uses, and drives it with the real Playwright suite (universe/e2e) — a real login,
# a real cross-tenant refusal, and a real SMTP delivery through the drip engine.
# Needs no cluster and no network. SUITE=<path> if universe is not a sibling.
e2e: ## Boot the binary locally and run the Playwright e2e suite against it.
	@E2E_ARGS="$(E2E_ARGS)" ./e2e/run.sh

# The console's IAM/cloud origins are NEXT_PUBLIC_* — inlined at BUILD time — so a
# bundle built for production points its login at hanzo.id and its reads at
# api.hanzo.ai. This rebuilds it against the loopback instance so the UI specs
# exercise the local binary end to end. It OVERWRITES webui/dist with a
# localhost-pinned bundle: run plain `make webui` before shipping anything.
E2E_ORIGIN ?= http://127.0.0.1:18080
e2e-ui: ## Rebuild the console pointed at the local instance, then run e2e.
	NEXT_PUBLIC_IAM_URL=$(E2E_ORIGIN) NEXT_PUBLIC_CLOUD_URL=$(E2E_ORIGIN) \
	NEXT_PUBLIC_IAM_CLIENT_ID=hanzo-cloud NEXT_PUBLIC_IAM_APP_NAME=hanzo-cloud \
	NEXT_PUBLIC_IAM_ORG_NAME=hanzo $(MAKE) webui
	@E2E_ARGS="$(E2E_ARGS)" ./e2e/run.sh

# The data plane has no plaintext-at-rest mode: cek refuses to open a store without
# a master key, on every build. The server makes that a boot decision (serve.go); a
# test run has no boot, so the suite declares its own dev posture HERE — once, for
# every package — instead of each package carrying a copy. A key already in the
# environment always wins, so CI's real key is never overridden.
DEV_KMS_KEY := AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
TEST_ENV = CLOUD_KMS_MASTER_KEY_REF="$${CLOUD_KMS_MASTER_KEY_REF:-$(DEV_KMS_KEY)}"

# The release image builds with -tags "libsqlite3 sqlite_fts5" (see Dockerfile).
# libsqlite3 needs cgo and the C library, but sqlite_fts5 does not — and without it
# any store whose migration declares an FTS5 table fails to open, so a subsystem
# built on full-text search (apps/code) cannot be tested at all. Carry the tag
# the shipped build carries, so the suite exercises the same schema surface.
TEST_TAGS := sqlite_fts5

test: ## Run unit + integration tests (pure-Go, with the FTS5 tag the image ships).
	# The lifted prose is COMMITTED (zipdoc_gen.go) because bare `go build` cannot
	# regenerate it; -check writes nothing and goes red when a lift no longer
	# matches its source, which is the drift being committed makes possible.
	# Per PACKAGE, not ./...: the checker must load exactly the way `go generate`
	# does, one package at a time — whole-module loading extracts differently
	# (zap-proto/zip zipdoc: single-vs-module load divergence) and a gate must
	# never disagree with the generator it polices.
	@set -e; for d in $$(grep -rl '^//go:generate go run github.com/zap-proto/zip/cmd/zipdoc' --include='*.go' clients cmd . 2>/dev/null | xargs -n1 dirname | sort -u); do 	  (cd $$d && $(GO) run github.com/zap-proto/zip/cmd/zipdoc -check) || { echo "$$d/zipdoc_gen.go is stale — run: go generate -run zipdoc ./$$d/..."; exit 1; }; 	done
	$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" ./...
	# The drift gate: regenerate the document FROM SOURCE and fail on any diff.
	# The weave above proves the subsets compose; this proves they are still the
	# routes. Only the second one catches a route added without regenerating.
	$(MAKE) -f mk/fleet.mk surface-check

# The inner loop. Everything `test` runs EXCEPT the drift gate, which rebuilds one
# binary per app and dominates the wall clock.
#
# It announces the skip on every run, for the same reason the gate names its kafka
# exemption out loud: a skip nobody sees is how a gate becomes decorative. This is
# the convenience, never the contract — CI runs the real gate (hanzo.yml,
# app-contract), and nothing in the docs points here as the default.
test-fast: ## Everything `test` runs except the spec drift gate. Inner loop only — CI runs `test`.
	@echo ">> test-fast: NOT checking spec drift (openapi.yaml + plugin/*/openapi.json)."
	@echo ">>            a route added without regenerating will pass here and fail CI."
	@echo ">>            the real gate:  make -f mk/fleet.mk surface-check"
	@set -e; for d in $$(grep -rl '^//go:generate go run github.com/zap-proto/zip/cmd/zipdoc' --include='*.go' clients cmd . 2>/dev/null | xargs -n1 dirname | sort -u); do \
	  (cd $$d && $(GO) run github.com/zap-proto/zip/cmd/zipdoc -check) || { echo "$$d/zipdoc_gen.go is stale — run: go generate -run zipdoc ./$$d/..."; exit 1; }; \
	done
	$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" ./...

# THE spec, in three steps, in the only order they work in:
#
#   1. zipdoc lifts the doc comments off every typed handler into zipdoc_gen.go.
#      Go drops comments at compile time, so this build-time pass is the ONLY way
#      prose and examples reach the document. `-run zipdoc` picks the directives
#      out of ./... by name, so a typed op added anywhere is covered and no
#      unrelated generator fires.
#   2. each app describes ITSELF: `<app> describe` mounts that one subsystem and
#      projects its own router into plugin/<app>/openapi.json (mk/fleet.mk — one lean
#      binary per app, no fused build and no mega link). It no longer writes an MCP
#      catalogue beside it: the door asks the subsystems (package fleet).
#   3. the weave composes those subsets into openapi.yaml (openapi/weave.go),
#      refusing when two apps claim one path or one schema name. There is no
#      monolith left to read: the woven document IS the published spec.
#
# openapi.yaml is a golden file: written here, and verified two different ways —
# and the difference between them is the whole lesson.
#
# The WEAVE (openapi-weave, run by `make test`) proves the subsets COMPOSE: no two
# apps claiming one path, no two claiming one schema name. It compares the subsets
# to the golden they weave into. Both are derived artifacts, and nothing in that
# comparison forces either back to the routes — so they agree with each other
# while both are wrong. This comment used to claim the weave caught a route added
# without regenerating. It does not, and plugin/ingress proved it: eight paths
# were added, the subset was never regenerated, the golden was woven from that
# same stale subset, `make test` stayed green, and the entire ingress API was
# missing from the spec every SDK is generated from.
#
# The DRIFT GATE (surface-check) is the one that catches that: it REGENERATES
# from source and fails on any diff. It is the expensive half — one binary per
# app — and it is in `make test` anyway, because the cheap half is exactly the
# check that passed while the published document was missing an entire API.
describe: ## Regenerate every app's projections, then weave them into openapi.yaml.
	$(GO) generate -run zipdoc ./...
	$(MAKE) -f mk/fleet.mk describe-apps
	$(MAKE) -f mk/fleet.mk openapi-weave OUT=openapi.yaml
	@echo ">> openapi.yaml — $$(grep -c '^  /' openapi.yaml) paths. The MCP tool list is NOT an artifact: POST /v1/mcp asks every subsystem."

test-cgo: ## Prove the cgo build works too — forces the fork's pure-Go backend via -tags sqlite_purego so the embedded modernc importers don't double-register "sqlite".
	$(TEST_ENV) CGO_ENABLED=1 $(GO) test -tags "sqlite_purego $(TEST_TAGS)" ./...

# The only target that builds what the image builds (Dockerfile: CGO_ENABLED=1,
# -tags "libsqlite3 sqlite_fts5"). The other two link no codec, so cek falls back to
# the pure-Go envelope and the tests pinning the shipped storage posture — a store
# shareable by a second opener, durable per-commit rather than at close — skip
# instead of running.
#
# The tag alone is not enough: it selects the C engine, but the codec is a RUNTIME
# probe of the libsqlcipher that engine links. A csqlite built against plain SQLite
# compiles and passes the one-engine guard while CodecLinked() stays false, so the
# storage tests would still quietly skip. SQLITE_REQUIRE_CODEC=1 — the same
# assertion the Dockerfile makes before it builds /cloud — turns that into a
# failure, so this target either exercises the shipped engine or says it cannot.
# It therefore FAILS on a machine without SQLCipher, which is the honest result.
test-codec: ## Run the suite against the engine the image ships (cgo + a real libsqlcipher).
	SQLITE_REQUIRE_CODEC=1 $(TEST_ENV) CGO_ENABLED=1 $(GO) test -tags "libsqlite3 $(TEST_TAGS)" ./...

vet: ## go vet across the module.
	CGO_ENABLED=$(CGO_ENABLED) $(GO) vet ./...

lint: vet ## Alias for vet.

# Not part of `test`: it rewrites source, so it runs deliberately, alone. It is how a
# new assertion earns its place — break the property, watch the test go RED. An anchor
# that no longer matches is a hard FAILURE here, never a skip, so a refactor that
# outruns a guard says so instead of quietly reading as a pass.
mutate: ## Mutation-test the guarded properties: break each one, prove its test goes red.
	scripts/mutate.py $(MUTANT)

tidy: ## go mod tidy + verify go.sum.
	$(GO) mod tidy
	$(GO) mod verify

docker: ## Build the Docker image (uses repo Dockerfile, scratch final stage).
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker ## Push the Docker image to ghcr.io. Requires docker login.
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

# COMPOSE is the check the v1.801.425/.426 outage needed and nobody had. zip
# refuses to compose a program whose middleware could never run, and it refuses at
# BOOT — so fifteen plugins built, linked, passed vet and unit tests, and then
# crash-looped in production. `go build` cannot see it; only running the binary can.
#
# SURVIVAL is the signal, and it is the only honest one. A compose panic is fatal,
# so a process still alive when the timeout kills it (rc 124) composed. Grepping
# the log for a success line does NOT work: `"message":"zip new"` is printed
# BEFORE composition, and reading it as a pass is exactly how a broken build was
# twice reported shipped.
#
# Each app gets a writable data dir and its OWN four ports. Without them it dies on
# `mkdir /var/lib/cloud/orgs` or on binding :8080/:9653/:9090/:8081 long before it
# reaches the router — and an early death looks like silence, which reads as a pass.
# CONCURRENT, because the timeout is the cost and it is paid per app: run one at a
# time and $(words $(APPS)) apps take most of an hour, which is a check nobody runs
# — and a check nobody runs is how all of this reached production. Each app already
# has its own dir and its own port block, so they do not contend; xargs -P just
# stops them queueing. Failures are written to files rather than raced onto stdout.
COMPOSE_DIR  ?= .compose
COMPOSE_JOBS ?= 8
compose: apps ## Prove every app binary BOOTS — the compose check `go build` cannot do.
	@rm -rf $(COMPOSE_DIR) && mkdir -p $(COMPOSE_DIR)
	@printf '%s\n' $(APPS) | nl -ba | xargs -P$(COMPOSE_JOBS) -n2 sh -c '\
	  a=$$1; d=$(COMPOSE_DIR)/$$1; p=$$((41000 + $$0 * 10)); mkdir -p $$d/rt; \
	  out=$$(CLOUD_DATA_DIR=$$d ZIP_RUNTIME_DIR=$$d/rt \
	         CLOUD_LISTEN=:$$p CLOUD_ZAP_LISTEN=:$$((p+1)) \
	         CLOUD_HEALTH_LISTEN=:$$((p+2)) CLOUD_ADMIN_LISTEN=:$$((p+3)) \
	         timeout 25 ./bin/$$a 2>&1); rc=$$?; \
	  if printf "%s" "$$out" | grep -q "does not compose"; then \
	    { echo "PANIC $$a"; printf "%s\n" "$$out" | grep -E "zip: (the group|GET|POST|PUT|PATCH|DELETE)" | sed "s/^/    /" | head -4; } > $$d.fail; \
	  elif [ $$rc -ne 124 ]; then \
	    echo "DIED  $$a (rc=$$rc): $$(printf "%s" "$$out" | tail -1 | cut -c1-140)" > $$d.fail; \
	  fi'
	@set -- $(COMPOSE_DIR)/*.fail; \
	if [ -e "$$1" ]; then cat $(COMPOSE_DIR)/*.fail; n=$$(ls $(COMPOSE_DIR)/*.fail | wc -l); \
	  rm -rf $(COMPOSE_DIR); echo ">> compose FAILED: $$n of $(words $(APPS)) apps"; exit 1; \
	else rm -rf $(COMPOSE_DIR); echo ">> compose: $(words $(APPS)) apps boot"; fi

clean: ## Remove built artifacts.
	rm -rf bin $(COMPOSE_DIR)
