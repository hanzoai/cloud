# Hanzo Cloud (OSS core)

The open-source core of Hanzo Cloud — the single Go binary that runs the
compute/backend control plane (KMS secrets, object storage, DNS/provisioning,
PaaS, code + exec, zero-trust sharing, audit, gateway edge, feature flags,
security, and the plugin runtime). Every subsystem mounts from one composition
root (`apps.Wire()`).

The proprietary product planes — identity, commerce, AI inference,
observability, the message bus, billing — are **injected** by the private build
through the `cloud/types` extension interfaces (`IAMClient`, `KMSClient`,
`AIClient`, `DurableEngine`, …). This binary links only public infrastructure
libraries. Where a plane is absent it mounts **fail-closed** and says so, rather
than pretending to work:

```
deps.Commerce: commerce enabled but no client factory registered — failing closed
s3 subsystem mounted fail-closed: S3_ADMIN_ACCESS_KEY not set (all ops 503 until provisioned)
```

## Run it locally

No Kubernetes, no cluster, no external services — a data directory is the whole
dependency.

```bash
GOWORK=off CGO_ENABLED=0 go build -o cloud ./cmd/cloud
CLOUD_DEV_UNENCRYPTED=1 ./cloud -data-dir /tmp/cloud -listen :8080
```

That boots ~32 subsystems and serves HTTP plus the ZAP transport.

**About `CLOUD_DEV_UNENCRYPTED`.** Stores open through `cek`, which encrypts at
rest. On a build linked against a SQLCipher codec a missing master key is
**fatal by design** — a capable binary never silently writes plaintext. That is
right for production and useless on a laptop, so this flag opts a local build
out of *only* the no-key case. It is loud (a warning every boot), it is a
separate variable from the key on purpose, and a malformed key still fails.
Never set it where real data lives.

To run encrypted locally instead, supply a real key — it is **base64 of exactly
32 bytes**, not hex:

```bash
CLOUD_KMS_MASTER_KEY_REF=$(openssl rand -base64 32) ./cloud -data-dir /tmp/cloud
```

### Flags

| flag | default | meaning |
|---|---|---|
| `-data-dir` | `/var/lib/cloud` | data root |
| `-listen` | `:8080` | HTTP listener |
| `-enable` | *(empty = all)* | comma-separated subsystem list |
| `-brand` | `hanzo` | white-label brand |
| `-domain` | `api.hanzo.ai` | primary domain |
| `-iam-issuer` | — | JWKS issuer |

## Build

```bash
GOWORK=off CGO_ENABLED=0 go build ./...   # pure-Go: stores open unencrypted
go test ./...
```

A CGO build linked against `libsqlcipher` is encryption-capable and then
requires a master key (or the dev flag above).

## Licence

Apache-2.0. See [LICENSE](LICENSE).
