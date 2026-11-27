# Rust data-plane implementation remains in hanzoai/engine; cloud owns composition.
HERE := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))
ENGINE_SOURCE ?= $(abspath $(HERE)/../../../engine)
CARGO_TARGET_DIR ?= $(ENGINE_SOURCE)/target
POOL ?= $(abspath $(HERE)/../../deploy/engine-pool.yaml)
HOST ?= 127.0.0.1
PORT ?= 8000

.PHONY: build test run
build:
	cd "$(ENGINE_SOURCE)" && cargo build --locked --manifest-path "$(ENGINE_SOURCE)/Cargo.toml" --target-dir "$(CARGO_TARGET_DIR)" -p hanzo-router --features proxy

test:
	cd "$(ENGINE_SOURCE)" && cargo test --locked --manifest-path "$(ENGINE_SOURCE)/Cargo.toml" --target-dir "$(CARGO_TARGET_DIR)" -p hanzo-router --features proxy

run: build
	"$(CARGO_TARGET_DIR)/debug/hanzo-router" --host "$(HOST)" --port "$(PORT)" --config "$(POOL)"
