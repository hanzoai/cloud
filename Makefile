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

.PHONY: help native webui deploy-ui agentskills build build-standalone run smoke e2e test test-cgo test-codec vet tidy docker docker-push clean

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

build: ## Build the unified cloud binary into ./bin/cloud (embeds whatever webui/dist holds — run `webui` first for the real console).
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$(BIN) $(PKG)

build-standalone: webui build ## Build the REAL 1-binary console: console build:embed → webui/dist → go build.

# NOTE: cloud builds ONLY the `cloud` binary — the stateless unified API. The Go
# `hanzo` CLI (cmd/hanzo + cli/) is RETIRED: the shipped `hanzo` is the Rust CLI
# (~/work/hanzo/cli, `curl hanzo.sh`), which talks to this API over HTTP via its
# OpenAPI-generated command surface. The `code` wrapper (incl. the zen-tier 1M
# mechanism) now lives in the Rust CLI. cmd/hanzo + cli/ remain only as the
# reference for the still-to-port client-side tools (GPU fleet worker `link`,
# `runner`, `engine`, `security`) and are no longer built here.

run: build ## Run with iam,base,kms,gateway,o11y enabled (matches README quickstart).
	./bin/$(BIN) --enable=iam,base,kms,gateway,o11y --brand=hanzo --domain=api.hanzo.ai

smoke: ## Build and run cmd/cloud-smoke (mount-time integration check).
	$(GO) run ./cmd/cloud-smoke

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
