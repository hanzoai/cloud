# Hanzo Cloud — the unified binary

One Go binary mounts every Hanzo-native subsystem. Deployment config (`--enable`,
brand, data dir) selects which mount; the artifact is identical across every
white-label surface. See `README.md` for the product view; this file is the
agent-facing architecture note.

## HIP-0106 composition root — the ONE way to wire a subsystem

The binary is moving off a global mutable registry to an **explicit composition
root**. This is the target every subsystem converges on. There is exactly one way.

### What is being DELETED (end state — no compat, no facade)

- `cloud.Deps` — the 12-field god-struct passed to every `Mount`.
- `cloud.Register` / `cloud.Registry` / `cloud.MountAll` — the global mutable
  registry + `init()`-side-effect registration + `any`-typed `MountFunc`.
- `subsystems/subsystems.go` — the blank-import bundle that pulls every subsystem
  into the graph for its `init()`.
- Per-subsystem `init()` + the `func(app any, deps cloud.Deps) error` shim.

### The canonical shape every subsystem exposes

A subsystem is ONE package. It imports `github.com/zap-proto/zip` (+ `cloud/types`
for shared client interfaces, + `cloud/clients/principal` for the tenant gate) and
**never** `github.com/hanzoai/cloud` (the root package). It exposes:

```go
// New constructs the subsystem's own client/store (in-proc value). Present only
// for subsystems that OWN a resource other subsystems consume (kms, base, ...).
func New(cfg Config) (*Client, error)

// Deps is the NARROW dependency surface THIS subsystem declares — only the 2-3
// things it uses, not a shared god-struct. Scalars (Logger, DataDir, Brand) are
// fields; a dependency on another subsystem is that subsystem's client INTERFACE
// (types.KMSClient, types.CommerceClient, ...), so in-proc value and ZAP-RPC are
// the same static type.
type Deps struct { ... }

// Mount wires the subsystem's /v1/<name>/* routes onto the shared app. No init,
// no registry, no global state.
func Mount(app *zip.App, deps Deps) error
```

Rules:
- `Deps` is per-subsystem and minimal. A subsystem that needs only a logger + a
  data dir declares exactly those (see `metrics.Deps`). A provider subsystem
  carries the store `New` returns (see `kms.Deps.Store`).
- A consumer's cross-subsystem dep is the **interface** from `cloud/types`
  (`KMSClient`, `CommerceClient`, ...). The composition root passes either the
  in-proc value (co-resident) or a ZAP-RPC client (split deploy) — same type, the
  subsystem never branches on transport.
- Standalone still works: a subsystem's own `main` constructs its `Deps` and calls
  `Mount` on a fresh `zip.App` — it wires just itself.

### The composition root (serve.go — the SINGLE place the graph is visible)

`Serve` is the one composition root, shared by `cmd/cloud` and `cmd/hanzo`:

1. `cfg := LoadConfig()` — build Config from env/flags.
2. Construct each provider client ONCE: `store, err := kms.New(kms.Config{...})`
   (in-proc value), or a ZAP-RPC client per `cfg.<Sub>ZAPAddr`.
3. `app := zip.New(...)` + the canonical middleware pipeline.
4. For each ENABLED subsystem, in order, `sub.Mount(app, sub.Deps{...})` — passing
   the constructed clients explicitly. Enable-gating is `cfg.Enabled("<name>")`;
   order is explicit call order (route namespaces are disjoint, so order matters
   only where one subsystem consumes another's `New` value — construct-before-Mount
   makes that a compile-time data dependency, not a registry ordering hint).
5. Wire shutdown explicitly: the root that opened a resource closes it
   (`store.Close()`), within the shutdown deadline.

### Transitional bridge (Phase 1 ONLY — deleted when the last subsystem converts)

Two references are converted (`kms`, `metrics`); the other ~34 still use the
registry. To keep the binary building mid-migration, serve.go currently keeps:

- `BuildDeps` + `MountAll` + `Registry` + `Deps` for the not-yet-converted set.
- `deps.KMS = kmsStore` — a bridge line: the explicitly-constructed kms store is
  also injected into the god-struct so the registry consumers (authz/commerce/ai)
  still reach a live client.

Both are marked `TRANSITIONAL` in serve.go and disappear the moment the last
subsystem is converted. **No compat/facade survives the end state.**

## Fan-out checklist (per subsystem — the mechanical diff)

For each remaining subsystem (base, ai, authz, vfs, commerce, licensing, o11y +
the in-repo `clients/*`):

1. Drop the `hanzoai/cloud` import; swap `github.com/hanzoai/zip` →
   `github.com/zap-proto/zip` if still on the old zip.
2. Replace the `cloud.Deps` param with a package-local `Deps` struct holding only
   the fields the Mount actually reads (cross-subsystem deps → `types.*Client`).
3. Delete the `init()` + `cloud.Register(...)`; keep `Mount(app *zip.App, deps Deps)`.
4. Move construction (any `New`) into serve.go; add an explicit
   `if cfg.Enabled("<name>") { <sub>.Mount(app, <sub>.Deps{...}) }` in order.
5. Remove the blank import from `subsystems/subsystems.go`.
6. External modules: bump a **patch** version (never > v1), update cloud's `go.mod`
   pin (drop the local `replace`).
7. Prove: `go build ./... && go vet ./...` + the subsystem's tests + the boot-parity
   smoke (`cmd/cloud/main_test.go` serves every `/v1/*` it does today).

When the registry is empty, delete `deps.go`, `build.go`'s
Register/Registry/MountAll/BuildDeps, `subsystems/`, and the `deps.KMS =` bridge.

## Converted references (Phase 1 proof)

- `clients/kms` — merged the former `clients/kmsembed` (store core, `New`) + the
  former `clients/kms` HTTP mount into ONE package: `New` + `Mount(app, kms.Deps)`,
  imports zip+types+principal only, no `init`. Proven cloud-free via
  `go list -deps ./clients/kms | grep -x github.com/hanzoai/cloud` → empty.
- `github.com/hanzoai/metrics` — `Mount(app, metrics.Deps{Logger,DataDir,Brand})`,
  no cloud import, no `init`. (Wired via a local `replace` in the proof worktree;
  the real landing bumps metrics a patch and drops the replace.)
