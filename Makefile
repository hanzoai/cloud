# hanzoai/cloud — developer ergonomics for the unified Hanzo Cloud binary (HIP-0106).
# Targets are intentionally minimal; deploy artifacts (compose, helm) live in deploy/ and helm/.

GO              ?= go
BIN             ?= cloud
PKG             ?= ./cmd/cloud

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
# that pin the shipped storage posture (clients/kms concurrent-open, audit
# shareability) therefore skip in both targets. `make test-codec` below is the one
# that runs them, and needs a real libsqlcipher to do it.
CGO_ENABLED     ?= 0

# Every app the light host mounts, read from the generated manifest — the same
# list cmd/host links, so `make plugins` cannot build a set that differs from the
# one the host expects to find beside it.
APPS := $(shell sed -n 's/.*{Name: "\([^"]*\)".*/\1/p' manifest/apps.go)

# Linking a cloud binary is a multi-GiB act, and the Go LINKER writes its
# temporaries to TMPDIR (not GOTMPDIR). On a host where /tmp is a tmpfs that is
# RAM, so `make plugins` — 100+ links back to back — exhausts it. Point it at
# disk by default; override for a box where /tmp is real.
export TMPDIR ?= $(HOME)/.cache/go-tmp

.PHONY: help native webui deploy-ui agentskills build host ship plugins plugin generate openapi run smoke test test-cgo test-codec vet tidy docker docker-push clean monolith monolith-standalone

help: ## Show this help.
	@awk 'BEGIN{FS=":.*##";printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

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

deploy-ui: ## Build the monochrome ArgoCD dashboard bundle into clients/deploy/webui/dist (go:embed source). DEPLOY_DIR=<path to hanzoai/deploy>.
	@command -v yarn >/dev/null 2>&1 || { echo "yarn is required to build the deploy dashboard bundle"; exit 1; }
	@test -f "$(DEPLOY_DIR)/ui/package.json" || { echo "deploy checkout not found at $(DEPLOY_DIR) — set DEPLOY_DIR=<path to hanzoai/deploy on rebrand/hanzo-monochrome>"; exit 1; }
	@test -d "$(DEPLOY_DIR)/ui/node_modules" || (cd "$(DEPLOY_DIR)/ui" && yarn install --frozen-lockfile)
	cd "$(DEPLOY_DIR)/ui" && NODE_OPTIONS=--max-old-space-size=8192 yarn build
	# Overlay the fresh bundle, keeping only the tracked fallback (.gitignore +
	# index.html shell); the real 43MB bundle is build-time-only (gitignored).
	find clients/deploy/webui/dist -mindepth 1 -maxdepth 1 ! -name .gitignore -exec rm -rf {} +
	cp -r "$(DEPLOY_DIR)/ui/dist/app/." clients/deploy/webui/dist/
	@echo ">> embedded monochrome ArgoCD bundle into clients/deploy/webui/dist (index.html $$(wc -c < clients/deploy/webui/dist/index.html) bytes)"

agentskills: ## Regenerate the FULL agent-skills catalog into clients/agentskills/catalog (go:embed source) from the openapi SOT. OPENAPI_DIR=<path to openapi>.
	@test -f "$(OPENAPI_DIR)/skills.py" || { echo "openapi checkout not found at $(OPENAPI_DIR) — set OPENAPI_DIR=<path> or clone hanzoai/openapi"; exit 1; }
	# skills.py rewrites the whole catalog dir; the .gitignore keeps only the tiny
	# `ai` fallback tracked, so the full set is embedded at build but never committed.
	python3 "$(OPENAPI_DIR)/skills.py" --no-services --out clients/agentskills/catalog
	@echo ">> embedded FULL agent-skills catalog ($$(jq -r .skill_count clients/agentskills/catalog/hanzo/index.json) skills/brand)"

# THE DEFAULT BUILD IS THE HOST, and that is the whole point of the plugin model:
# nothing compiles together. `build` used to link all 103 subsystems into one
# 3108-package binary, so changing one line in one app relinked every other app
# with it. Measured on this tree with a fully warm cache, that relink is 9.5s and
# 3.8GiB of peak RSS — from cold it is minutes — for a change that touched one
# app. It is still available as `monolith` (last target in this file), which says
# why it survives. It is no longer what you get by typing `make build`.
#
# The loop this replaces it with is two commands, and neither grows as the fleet
# does. Measured here:
#
#   make build              # the router — 316 packages, 0.6s
#   make plugin APP=wallets # the ONE app you edited — 1.3s, recompile + relink
#
# then restart the host. `plugin` declares no prerequisites, so the second
# command never drags the first — or the monolith — along behind it.
build: host ## FAST PATH (default): build the light host into ./bin/host. Then `make plugin APP=<x>` for the app you are editing.

# THE LIGHT HOST links zip and the generated manifest and stops, because it knows
# only where each app lives and which paths it answers — never what the app does.
# The apps run as their own processes, started on the first request that reaches
# them, so the host's build does not grow when a subsystem does.
host: ## Build the light host into ./bin/host (links zip + the manifest, none of the apps).
	@mkdir -p bin $(TMPDIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$@ ./cmd/$@
	@echo ">> bin/host — $$(CGO_ENABLED=$(CGO_ENABLED) $(GO) list -deps ./cmd/host | wc -l) packages, $$(du -h bin/host | cut -f1)"

# Sequential and -p=2 on purpose: each link peaks in the GiBs, and building a
# hundred of them in parallel is how this OOMs a 128GiB box.
plugins: ## Build every app the manifest mounts into ./bin — the host's plugins. Slow by construction.
	@mkdir -p bin $(TMPDIR)
	@for a in $(APPS); do \
	  printf '>> %s\n' "$$a"; \
	  GOFLAGS=-p=2 CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$$a ./cmd/$$a || exit 1; \
	done
	@echo ">> $(words $(APPS)) plugins in ./bin"

# THE RELEASE LAYOUT, and the reason `plugins` above is a development target
# rather than a shipping one. A dedicated plugin is ~40MB of which ~35MB is the
# core every other plugin also links, so 108 of them are 4.5GB of duplicated
# code (already stripped — -s -w is the default LDFLAGS, there is no symbol win
# left in it). The unified binary is that core ONCE and serves any app via
# `cloud --enable=<name>`, which manifest.App.Plugin falls through to when no
# dedicated binary sits beside the host. Same contract, same child, 20x less to
# ship.
ship: host monolith ## Build the RELEASE layout into ./bin: the host + the one multi-call binary that serves all $(words $(APPS)) apps.
	@echo ">> ship: host $$(du -h bin/host | cut -f1) + cloud $$(du -h bin/cloud | cut -f1) = $$(du -ch bin/host bin/cloud | tail -1 | cut -f1) for $(words $(APPS)) apps"

plugin: ## Build ONE app into ./bin: make plugin APP=wallets.
	@test -n "$(APP)" || { echo "usage: make plugin APP=<name>"; echo "apps: $(APPS)"; exit 1; }
	@test -d cmd/$(APP) || { echo "no cmd/$(APP) — run 'make generate', or check the name against 'make plugin' with no APP"; exit 1; }
	@mkdir -p bin $(TMPDIR)
	GOFLAGS=-p=2 CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

# apps.Wire() is the single source of truth for the subsystem set. This derives
# BOTH artifacts from it in one parse — the per-app standalone mains and the
# host's manifest — so a subsystem added there cannot be missing from either.
# Idempotent: a no-op run leaves the tree clean, which is what lets CI diff it.
generate: ## Regenerate cmd/<app>/main.go + manifest/apps.go from apps.Wire().
	$(GO) run ./cmd/gen-app-cmds

# NOTE: cloud builds ONLY the `cloud` binary — the stateless unified API. The Go
# `hanzo` CLI (cmd/hanzo + cli/) is RETIRED: the shipped `hanzo` is the Rust CLI
# (~/work/hanzo/cli, `curl hanzo.sh`), which talks to this API over HTTP via its
# OpenAPI-generated command surface. The `code` wrapper (incl. the zen-tier 1M
# mechanism) now lives in the Rust CLI. cmd/hanzo + cli/ remain only as the
# reference for the still-to-port client-side tools (GPU fleet worker `link`,
# `runner`, `engine`, `security`) and are no longer built here.

# Builds the host plus EXACTLY the plugins it is told to mount — not all 106.
# The host resolves a plugin as a file beside itself (manifest.App.Plugin), so a
# name in RUN_ENABLE with no binary in ./bin is the one way this fails; building
# that same list here is what keeps the two in step.
RUN_ENABLE ?= iam,base,kms,gateway,o11y

run: host ## Run the host with iam,base,kms,gateway,o11y (matches README quickstart); builds just those plugins.
	@for a in $$(echo $(RUN_ENABLE) | tr ',' ' '); do $(MAKE) --no-print-directory plugin APP=$$a; done
	./bin/host --enable=$(RUN_ENABLE)

smoke: ## Build and run cmd/cloud-smoke (mount-time integration check).
	$(GO) run ./cmd/cloud-smoke

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
# built on full-text search (clients/code) cannot be tested at all. Carry the tag
# the shipped build carries, so the suite exercises the same schema surface.
TEST_TAGS := sqlite_fts5

test: ## Run unit + integration tests (pure-Go, with the FTS5 tag the image ships).
	$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" ./...

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

tidy: ## go mod tidy + verify go.sum.
	$(GO) mod tidy
	$(GO) mod verify

docker: ## Build the Docker image (uses repo Dockerfile, scratch final stage).
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker ## Push the Docker image to ghcr.io. Requires docker login.
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

clean: ## Remove built artifacts.
	rm -rf bin

native: ## Build the native flags evaluator staticlib (required for CGO=1 builds/tests).
	cargo build --release --manifest-path native/flags/Cargo.toml

# ---------------------------------------------------------------------------
# SLOW FALLBACK — everything linked together. Deliberately last, in the file and
# in `make help`, because typing it is a choice and it should look like one.
#
# Two reasons it still exists:
#   1. It is the SHIPPED artifact today. The Dockerfile builds ./cmd/cloud, and
#      the host+plugins layout cannot replace it until a plugin stops linking the
#      whole core: the 106 plugins weigh 5.3GB in ./bin against this one binary's
#      212MB, because each statically re-links the same ~650-package root. The
#      image gets ~25x bigger before the model pays off, so flipping the
#      Dockerfile waits on cutting that floor, not on this target.
#   2. It is the reference the plugin set is checked against: every app mounted
#      here through apps.Wire() is the same app cmd/<name> serves standalone, and
#      when the two disagree this is the one that is right.
# Delete it when neither is true — not before.
monolith: ## SLOW FALLBACK: link every subsystem into one ./bin/cloud (3108 packages, 212MB, 9.5s warm / minutes cold). Prefer `make build`.
	@mkdir -p bin $(TMPDIR)
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$(BIN) $(PKG)

monolith-standalone: webui monolith ## SLOW FALLBACK: the REAL 1-binary console — console build:embed → webui/dist → monolith.
