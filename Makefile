# Hanzo Cloud — OSS dev edition.
#
# `make dev` is the whole first run: build the binary, mint a local encryption
# key if there isn't one, serve on http://127.0.0.1:8080. No cluster, no
# network, no Rust toolchain, nothing to configure.

# Default to the pure-Go SQLite backend, which encrypts with no C library
# installed. A default cgo build links ordinary SQLite, cannot encrypt, and
# refuses to start with the key `make dev` mints (#3). A cgo build encrypts only
# with -tags libsqlite3 linked against libsqlcipher. Override with
# CGO_ENABLED=1 make build ...
CGO_ENABLED ?= 0
export CGO_ENABLED

BIN      := cloud
HOST_BIN := bin/host
DEVDIR   := .dev
KEYFILE  := $(DEVDIR)/master.key
PORT     ?= 8080

# The dev run binds LOOPBACK, not every interface. An unauthenticated caller
# shares the local namespace (see README), which is the right trade for a
# machine-local tool and the wrong one for anything reachable.
DEV_ENV := CLOUD_LISTEN=127.0.0.1:$(PORT) \
           CLOUD_DATA_DIR=$(DEVDIR)/data \
           CLOUD_ENABLE_STAGED=functions

.PHONY: all build host plugin dev run test vet native hooks clean

all: build

# The default build links no Rust. The native feature-flag evaluator is behind
# `-tags flags_native`; see clients/flags/engine.go for why it is a tag and not
# a cgo check.
build:
	go build -o $(BIN) ./cmd/cloud

# Local network host daemon (`hanzo network start` looks for bin/host and tells
# the user to run `make host plugin`; #4). All subsystems are wired in-binary via
# apps.Wire(), so `plugin` is a no-op alias kept for CLI compatibility.
host:
	@mkdir -p $(dir $(HOST_BIN))
	go build -o $(HOST_BIN) ./cmd/host

plugin: host

# Rebuild with the native evaluator: make native && make build TAGS=flags_native
TAGS ?=
ifneq ($(TAGS),)
build host: export GOFLAGS += -tags=$(TAGS)
endif

# The native evaluator links Rust over cgo (engine.go is `cgo && flags_native`).
# Under the CGO_ENABLED=0 default the tag would select engine_stub.go without a
# word, so stop instead.
comma := ,
ifneq ($(filter flags_native,$(subst $(comma), ,$(TAGS))),)
ifeq ($(CGO_ENABLED),0)
$(error TAGS=flags_native needs cgo: CGO_ENABLED=1 make build TAGS=flags_native, and add libsqlite3 linked against libsqlcipher if the binary must encrypt)
endif
endif

dev: build $(KEYFILE)
	@echo "→ http://127.0.0.1:$(PORT)"
	$(DEV_ENV) CLOUD_KMS_MASTER_KEY_REF=$$(cat $(KEYFILE)) ./$(BIN)

run: dev

# One local key, minted once, kept out of git. cloud encrypts its SQLite stores
# at rest and refuses to open them without a key, so local development gets a
# real key rather than an exemption.
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

# Refuses any push to origin that carries non-public history or imports.
# One command, and git remembers it.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-push guard active"

clean:
	rm -f $(BIN) $(HOST_BIN)
	rm -rf $(DEVDIR) bin
