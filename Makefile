# hanzoai/cloud — developer ergonomics for the unified Hanzo Cloud binary (HIP-0106).
# Targets are intentionally minimal; deploy artifacts (compose, helm) live in deploy/ and helm/.

GO              ?= go
BIN             ?= cloud
PKG             ?= ./cmd/cloud
DOCKER_IMAGE    ?= ghcr.io/hanzoai/cloud
DOCKER_TAG      ?= dev
LDFLAGS         ?= -s -w
# Path to a hanzoai/console2 checkout used to build the embedded console bundle.
CONSOLE2_DIR    ?= ../console2

# The shipped binary is pure Go (Dockerfile: CGO_ENABLED=0 → scratch). Default all
# build/test targets to that mode so `make build`/`make test` exercise exactly
# what prod runs — and, critically, register the ONE "sqlite" driver exactly once:
# cloud's stores use github.com/hanzoai/sqlite (its !cgo backend IS modernc), and
# the embedded deps (ai/base/commerce/o11y/orm/tasks) that import modernc directly
# then resolve to the SAME package → a single registration. A plain CGO_ENABLED=1
# build instead links the fork's mattn backend ALONGSIDE those modernc importers
# and panics at init ("sql: Register called twice for driver sqlite"); `make
# test-cgo` proves the cgo path via the fork's `sqlite_purego` opt-out tag, which
# forces the fork to modernc too so the whole binary registers "sqlite" once.
CGO_ENABLED     ?= 0

.PHONY: help webui build build-standalone run smoke test test-cgo test-race vet tidy docker docker-push clean

help: ## Show this help.
	@awk 'BEGIN{FS=":.*##";printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z_-]+:.*##/{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

webui: ## Build the real console2 static bundle into webui/dist (go:embed source). CONSOLE2_DIR=<path to console2>.
	@command -v npm >/dev/null 2>&1 || { echo "npm is required to build the console bundle"; exit 1; }
	@test -f "$(CONSOLE2_DIR)/package.json" || { echo "console2 checkout not found at $(CONSOLE2_DIR) — set CONSOLE2_DIR=<path>"; exit 1; }
	@test -d "$(CONSOLE2_DIR)/node_modules" || (cd "$(CONSOLE2_DIR)" && npm install --no-audit --no-fund)
	cd "$(CONSOLE2_DIR)" && NEXT_TELEMETRY_DISABLED=1 NODE_OPTIONS=--max-old-space-size=8192 npm run build:embed
	# Overlay the fresh static export onto webui/dist, keeping only the tracked
	# fallbacks (.gitignore + assets/.gitkeep); the real bundle is build-time-only.
	find webui/dist -mindepth 1 -maxdepth 1 ! -name .gitignore ! -name assets -exec rm -rf {} +
	cp -r "$(CONSOLE2_DIR)/out/." webui/dist/
	@echo ">> embedded real console2 bundle into webui/dist (index.html $$(wc -c < webui/dist/index.html) bytes)"

build: ## Build the unified cloud binary into ./bin/cloud (embeds whatever webui/dist holds — run `webui` first for the real console).
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS)" -o bin/$(BIN) $(PKG)

build-standalone: webui build ## Build the REAL 1-binary console: console2 build:embed → webui/dist → go build.

run: build ## Run with iam,base,kms,gateway,o11y enabled (matches README quickstart).
	./bin/$(BIN) --enable=iam,base,kms,gateway,o11y --brand=hanzo --domain=api.hanzo.ai

smoke: ## Build and run cmd/cloud-smoke (mount-time integration check).
	$(GO) run ./cmd/cloud-smoke

test: ## Run unit + integration tests (pure-Go, exactly as prod ships).
	CGO_ENABLED=$(CGO_ENABLED) $(GO) test ./...

test-cgo: ## Prove the cgo build works too — forces the fork's pure-Go backend via -tags sqlite_purego so the embedded modernc importers don't double-register "sqlite".
	CGO_ENABLED=1 $(GO) test -tags sqlite_purego ./...

test-race: ## Run tests under the race detector. -race REQUIRES CGO=1, which links the fork's mattn "sqlite" alongside the embedded modernc importers and panics at init ("sql: Register called twice for driver sqlite"); the sqlite_purego tag forces the fork to modernc too, so the whole binary registers "sqlite" exactly once — the ONE way to race-test the cloud (fixes clients/{graph,kmssvc,o11y}).
	CGO_ENABLED=1 $(GO) test -race -tags sqlite_purego ./...

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
