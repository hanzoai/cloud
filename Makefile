# Hanzo Cloud — OSS dev edition.
#
# `make dev` is the whole first run: build the binary, mint a local encryption
# key if there isn't one, serve on http://127.0.0.1:8080. No cluster, no
# network, no Rust toolchain, nothing to configure.

BIN     := cloud
DEVDIR  := .dev
KEYFILE := $(DEVDIR)/master.key
PORT    ?= 8080

# The dev run binds LOOPBACK, not every interface. An unauthenticated caller
# shares the local namespace (see README), which is the right trade for a
# machine-local tool and the wrong one for anything reachable. Production sets
# CLOUD_LISTEN itself and is unaffected.
DEV_ENV := CLOUD_LISTEN=127.0.0.1:$(PORT) \
           CLOUD_DATA_DIR=$(DEVDIR)/data \
           CLOUD_ENABLE_STAGED=functions

.PHONY: all build dev run test vet native hooks clean

all: build

# The default build links no Rust. The native feature-flag evaluator is behind
# `-tags flags_native`; see clients/flags/engine.go for why it is a tag and not
# a cgo check.
build:
	go build -o $(BIN) ./cmd/cloud

# Rebuild with the native evaluator: make native && make build TAGS=flags_native
TAGS ?=
ifneq ($(TAGS),)
build: export GOFLAGS += -tags=$(TAGS)
endif

dev: build $(KEYFILE)
	@echo "→ http://127.0.0.1:$(PORT)"
	$(DEV_ENV) CLOUD_KMS_MASTER_KEY_REF=$$(cat $(KEYFILE)) ./$(BIN)

run: dev

# One local key, minted once, kept out of git. This is the SAME path production
# takes — cloud encrypts its SQLite stores at rest and refuses to open them
# without a key. Local development gets a real key rather than an exemption, so
# the thing you run on your laptop is the thing that runs in production.
# (CLOUD_DEV_UNENCRYPTED=1 still opts out if you want plaintext files to poke at.)
$(KEYFILE):
	@mkdir -p $(DEVDIR)
	@umask 077 && openssl rand -base64 32 > $(KEYFILE)
	@echo "minted $(KEYFILE)"

test:
	go test ./...

vet:
	go vet ./...

# The Rust feature-flag evaluator. Optional — nothing else needs it.
native:
	cargo build --release --manifest-path native/flags/Cargo.toml

# Refuses any push to origin that carries private-edition history or imports.
# One command, and git remembers it.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-push guard active"

clean:
	rm -f $(BIN)
	rm -rf $(DEVDIR)
