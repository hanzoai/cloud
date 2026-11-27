# Hanzo Cloud (OSS) — light host + per-app ZAP plugin fleet (HIP-0106).
#
# Local cluster & coding lifecycle follows the canonical Hanzo CLI §6 contract:
#   make up / down           -> hanzo up / hanzo down
#   make start / stop        -> start / stop this repo's Cloud workload (keeps shared runtime warm)
#   make start-vm / stop-vm  -> hanzo start / hanzo stop  (local microVM + k3s runtime)
#   make dev / debug         -> hanzo dev / hanzo dev --debug
#   make logs                -> project-scoped Cloud workload logs
#   make clean               -> remove build artifacts only

.DEFAULT_GOAL := help

GO              ?= go
export GOWORK   := off

DOCKER_IMAGE    ?= ghcr.io/hanzoai/cloud-oss
DOCKER_TAG      ?= main
HANZO           ?= hanzo
HANZO_CLOUD_IMAGE ?= ghcr.io/hanzoai/cloud-oss:main

LDFLAGS         ?= -s -w
DEBUG           ?= 0
ifeq ($(DEBUG),1)
override LDFLAGS :=
DEBUG_GCFLAGS = -gcflags="all=-N -l"
else
DEBUG_GCFLAGS =
endif

VERSION         ?= $(shell git describe --tags --always --dirty 2>/dev/null)
REVISION        ?= $(shell git describe --always --abbrev=40 --match='' --dirty 2>/dev/null)
STAMP            = -X github.com/hanzoai/cloud/sdk/env.version=$(VERSION) -X github.com/hanzoai/cloud/sdk/env.revision=$(REVISION)

CGO_ENABLED     ?= 0
TAGS            := $(if $(filter 1,$(CGO_ENABLED)),libsqlite3 sqlite_fts5 sqlite_math_functions,sqlite_fts5 sqlite_math_functions)
ifeq ($(CGO_ENABLED),1)
export CGO_CFLAGS  := -DSQLITE_HAS_CODEC -DSQLITE_USE_URI=1 -I/usr/include/sqlcipher
export CGO_LDFLAGS := -lsqlcipher
endif
TEST_TAGS       := sqlite_fts5 sqlite_math_functions

DEV_KMS_KEY     := AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
DEV_CSRF_KEY    := ZGV2LWNzcmYta2V5LWZvci10ZXN0cy1vbmx5LTAwMDA=
TEST_ENV         = CLOUD_KMS_MASTER_KEY_REF="$${CLOUD_KMS_MASTER_KEY_REF:-$(DEV_KMS_KEY)}" CONSOLE_CSRF_KEY="$${CONSOLE_CSRF_KEY:-$(DEV_CSRF_KEY)}"

APPS            := $(shell sed -n '/Coresident: *true/d; s/.*{Name: "\([^"]*\)".*/\1/p' manifest/apps.go)
COMPOSE_DIR     ?= .compose
COMPOSE_JOBS    ?= 8
LOGS_WAIT       ?= 15s

include mk/fleet.mk

.PHONY: help build cloud ship apps plugin generate hanzo up down start stop start-vm stop-vm down-vm restart status dev debug boot dev-stop smoke logs zipdoc-check closure closure-check test test-fast vet lint tidy docker docker-push compose hooks clean

help: ## Show available targets.
	@awk 'BEGIN{FS=":.*##";printf "\nUsage: make <target>\n\nTargets:\n"} /^[a-zA-Z0-9_-]+:.*##/{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# ── Build targets ──────────────────────────────────────────────────────────────

build: cloud ## Build the light host into ./bin/cloud (default fast path).

cloud: ## Build the light host into ./bin/cloud.
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(DEBUG_GCFLAGS) -tags "$(TAGS)" -ldflags="$(LDFLAGS) $(STAMP)" -o bin/$@ ./cmd/$@
	@echo ">> bin/cloud — $$(CGO_ENABLED=$(CGO_ENABLED) $(GO) list -deps ./cmd/cloud | wc -l) packages, $$(du -h bin/cloud | cut -f1)"

PLUGIN_BUILD = CGO_ENABLED=$(CGO_ENABLED) $(GO) build $(if $(filter 1,$(DEBUG)),,-trimpath) $(DEBUG_GCFLAGS) -tags "$(TAGS)" -ldflags="$(LDFLAGS)" -o bin/

apps: ## Build every OSS app plugin binary into ./bin.
	@mkdir -p bin
	$(PLUGIN_BUILD) $(addprefix ./plugin/,$(APPS))
	@echo ">> apps: $(words $(APPS)) binaries in ./bin ($$(du -sh bin | cut -f1))"

ship: cloud apps ## Build the full release layout into ./bin (host + all OSS plugins).
	@echo ">> ship: cloud + $(words $(APPS)) per-app plugins in ./bin ($$(du -sh bin | cut -f1))"

plugin: ## Build ONE app plugin into ./bin: make plugin APP=iam.
	@test -n "$(APP)" || { echo "usage: make plugin APP=<name>"; echo "apps: $(APPS)"; exit 1; }
	@echo "$(APPS)" | tr ' ' '\n' | grep -qx "$(APP)" || { echo "no app named $(APP) in manifest/apps.go"; exit 1; }
	@mkdir -p bin
	$(PLUGIN_BUILD) ./plugin/$(APP)

generate: ## Scaffold missing plugin/<app>/main.go and validate manifest bijection.
	$(GO) run ./plugin/gen-app-cmds

hanzo: ## Build the client-only control CLI into ./bin/hanzo.
	@mkdir -p bin
	CGO_ENABLED=$(CGO_ENABLED) $(GO) build -ldflags="$(LDFLAGS) -X main.version=$(VERSION)" -o bin/$@ ./cmd/$@

# ── Canonical §6 Local Lifecycle & Dev Targets ─────────────────────────────────

up: ## Bring up the local Hanzo Cloud stack (hanzo up).
	@$(HANZO) up

down: ## Tear down local workloads while keeping the shared k3s runtime warm (hanzo down).
	@$(HANZO) down

start-vm: ## Start the local Hanzo microVM + k3s runtime without deploying workloads (hanzo start).
	@$(HANZO) start

stop-vm: ## Gracefully shut down the local Hanzo microVM + k3s runtime (hanzo stop).
	@$(HANZO) stop

down-vm: stop-vm ## Alias for stop-vm.

start: ## Start this repo's Cloud workload on the local runtime (defaults to ghcr.io/hanzoai/cloud-oss:main).
	@set -eu; \
	  img="$(HANZO_CLOUD_IMAGE)"; \
	  case "$$img" in \
	    oci.hanzo.ai/hanzoai/*|oci.hanzo.ai/hanzo-inc/*|ghcr.io/hanzoai/*|ghcr.io/hanzo-inc/*) ;; \
	    *) echo "Expected a Hanzo-owned image (got $$img)" >&2; exit 1 ;; \
	  esac; \
	  echo "Starting local Hanzo Cloud workload with $$img"; \
	  exec $(HANZO) up --no-ui --cloud "$$img"

stop: ## Stop this repo's Cloud workload without killing the shared k3s runtime or sibling services.
	@kc="$(HOME)/.kube/hanzo.yaml"; \
	if [ -f "$$kc" ]; then \
	  KUBECONFIG="$$kc" $(HANZO) k8s delete deployment/cloud service/cloud -n hanzo --ignore-not-found; \
	else \
	  echo "Local kubeconfig not initialized; nothing to stop."; \
	fi

restart: ## Restart this repo's Cloud workload on the shared runtime.
	@$(MAKE) --no-print-directory stop
	@$(MAKE) --no-print-directory start

status: ## Inspect local cluster and workload status.
	@$(HANZO) up status

dev: ## Launch Hanzo Dev coding session in this repository (hanzo dev).
	@$(HANZO) dev

debug: ## Launch Hanzo Dev coding session with debug logging enabled (hanzo dev --debug).
	@$(HANZO) dev --debug

BOOT_APPS       ?= base kms iam o11y audit
BOOT_LISTEN     ?= 127.0.0.1:8080
BOOT_ZAP_LISTEN ?= 127.0.0.1:9653

boot: cloud ## Build and boot a local loopback OSS Cloud host + core plugins (BOOT_APPS=...).
	@set -eu; \
	  mkdir -p bin .dev/data; \
	  if [ -f .dev/cloud.pid ] && kill -0 "$$(cat .dev/cloud.pid)" 2>/dev/null; then \
	    echo "Local Cloud already running (pid $$(cat .dev/cloud.pid)) on http://$(BOOT_LISTEN)"; \
	    exit 0; \
	  fi; \
	  if [ ! -f .dev/master.key ]; then \
	    (umask 077 && openssl rand -base64 32 > .dev/master.key); \
	  fi; \
	  $(PLUGIN_BUILD) $(addprefix ./plugin/,$(BOOT_APPS)); \
	  CLOUD_LISTEN="$(BOOT_LISTEN)" \
	  CLOUD_ZAP_LISTEN="$(BOOT_ZAP_LISTEN)" \
	  CLOUD_DATA_DIR=".dev/data" \
	  CLOUD_KMS_MASTER_KEY_REF="$$(cat .dev/master.key)" \
	  CONSOLE_CSRF_KEY="$${CONSOLE_CSRF_KEY:-$(DEV_CSRF_KEY)}" \
	  ./bin/cloud >.dev/cloud.log 2>&1 & \
	  pid=$$!; echo "$$pid" > .dev/cloud.pid; \
	  for i in $$(seq 1 40); do \
	    if curl -fsS "http://$(BOOT_LISTEN)/healthz" >/dev/null 2>&1; then \
	      echo "Hanzo Cloud (OSS) ready on http://$(BOOT_LISTEN) (pid $$pid, logs: .dev/cloud.log)"; \
	      exit 0; \
	    fi; \
	    if ! kill -0 "$$pid" 2>/dev/null; then \
	      echo "Cloud failed to start; tail of .dev/cloud.log:" >&2; \
	      tail -n 20 .dev/cloud.log >&2; \
	      rm -f .dev/cloud.pid; \
	      exit 1; \
	    fi; \
	    sleep 0.25; \
	  done; \
	  echo "Timed out waiting for http://$(BOOT_LISTEN)/healthz (check .dev/cloud.log)" >&2; \
	  exit 1

dev-stop: ## Stop a local loopback instance started with `make boot`.
	@set -eu; \
	  if [ -f .dev/cloud.pid ]; then \
	    pid="$$(cat .dev/cloud.pid)"; \
	    if kill -0 "$$pid" 2>/dev/null; then \
	      kill "$$pid" 2>/dev/null || true; \
	      for i in $$(seq 1 20); do \
	        kill -0 "$$pid" 2>/dev/null || break; \
	        sleep 0.2; \
	      done; \
	      echo "Stopped local Cloud (pid $$pid)"; \
	    fi; \
	    rm -f .dev/cloud.pid; \
	  else \
	    echo "No local Cloud pid file (.dev/cloud.pid) found."; \
	  fi

logs: ## Stream project-scoped logs for the local Cloud workload (never uses ambient kubeconfig).
	@test -f "$(HOME)/.kube/hanzo.yaml" || { echo "Local kubeconfig missing; run make start or make up" >&2; exit 1; }
	@set -eu; \
	  kc="$(HOME)/.kube/hanzo.yaml"; \
	  waiting_reason=$$(KUBECONFIG="$$kc" $(HANZO) k8s get pods -n hanzo -l app=cloud \
	    -o jsonpath='{.items[0].status.containerStatuses[0].state.waiting.reason}' 2>/dev/null || true); \
	  phase=$$(KUBECONFIG="$$kc" $(HANZO) k8s get pods -n hanzo -l app=cloud \
	    -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true); \
	  if [ -z "$$phase" ] || [ "$$phase" = "Pending" ] || [ "$$waiting_reason" = "ContainerCreating" ] || [ "$$waiting_reason" = "PodInitializing" ]; then \
	    echo "Cloud pod is still starting ($${waiting_reason:-$${phase:-not scheduled yet}}); waiting up to $(LOGS_WAIT)..."; \
	    KUBECONFIG="$$kc" $(HANZO) k8s wait --for=condition=Ready pod -l app=cloud -n hanzo --timeout="$(LOGS_WAIT)" >/dev/null 2>&1 || true; \
	  fi; \
	  KUBECONFIG="$$kc" $(HANZO) k8s logs deployment/cloud -n hanzo --tail=100

# ── Verification, OpenAPI & Image Targets ──────────────────────────────────────

smoke: ## Run the mount-time smoke prober.
	$(GO) run ./plugin/smoke

zipdoc-check: ## Verify lifted handler doc comments match source.
	@sh mk/zipdoc.sh check
	@echo ">> zipdoc: every lifted file matches its source"

closure: ## Write openapi/closure.json witness for all describable OSS apps.
	@$(GO) run ./cmd/closure -write -describable="$(DESCRIBABLE)"

closure-check: ## Verify committed OpenAPI subsets match current dependency graph.
	@$(GO) run ./cmd/closure -describable="$(DESCRIBABLE)"

test: ## Run unit + integration tests across the OSS module.
	$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -tags "$(TEST_TAGS)" ./...

test-fast: ## Run unit tests in short mode.
	$(TEST_ENV) CGO_ENABLED=$(CGO_ENABLED) $(GO) test -short -tags "$(TEST_TAGS)" ./...

vet: ## Run go vet across the OSS module.
	CGO_ENABLED=$(CGO_ENABLED) $(GO) vet -tags "$(TEST_TAGS)" ./...

lint: vet ## Alias for vet.

tidy: ## Run go mod tidy + verify against public Go proxy.
	GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org $(GO) mod tidy
	$(GO) mod verify

docker: ## Build the local OSS Cloud image (ghcr.io/hanzoai/cloud-oss:main).
	docker build --build-arg VERSION="$(VERSION)" --build-arg REVISION="$(REVISION)" -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

docker-push: docker ## Push the OSS Cloud image to GHCR.
	docker push $(DOCKER_IMAGE):$(DOCKER_TAG)

compose: apps ## Boot every OSS app plugin standalone and verify router composition.
	@rm -rf $(COMPOSE_DIR) && mkdir -p $(COMPOSE_DIR)
	@printf '%s\n' $(APPS) | xargs -P$(COMPOSE_JOBS) -n1 sh -c '\
	  a=$$0; d=$(COMPOSE_DIR)/$$0; mkdir -p $$d/rt; \
	  out=$$(CLOUD_DATA_DIR=$$d ZIP_RUNTIME_DIR=$$d/rt \
	         CLOUD_LISTEN=:0 CLOUD_ZAP_LISTEN=:0 \
	         CLOUD_HEALTH_LISTEN=:0 CLOUD_ADMIN_LISTEN=:0 \
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

hooks: ## Activate repository git hooks (.githooks/pre-push public-boundary guard).
	git config core.hooksPath .githooks
	@echo "pre-push guard active"

clean: ## Remove build artifacts only (preserves local data/runtime state).
	rm -rf bin dist $(COMPOSE_DIR)
