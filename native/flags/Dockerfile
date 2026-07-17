# hanzoai/cloud-flags — the native flag-evaluator staticlib (hanzo-flags).
#
# Built ONCE per native/flags change as a versioned image whose rootfs is just
# libhanzo_flags.a, so cloud's main build pulls it (FROM ${FLAGS_IMAGE} AS
# flagslib + COPY --from=flagslib /libhanzo_flags.a) instead of recompiling Rust
# every release. amd64/musl static archive; links clean into the alpine CGO build.
#
# Build context is this directory (native/flags). The compiler cache (zccache)
# lives on the CI RUNNER, not in this bare image build — after decomplection the
# published .a is itself the S3-backed cross-build artifact.
FROM ghcr.io/hanzoai/mirror/rust:1-alpine3.22@sha256:b348cb409ac0a73de15065997a360063cf87465574a15e3e4469862cb8996f02 AS build
RUN apk add --no-cache musl-dev
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY src ./src
RUN cargo build --release --locked
FROM scratch
COPY --from=build /src/target/release/libhanzo_flags.a /libhanzo_flags.a
