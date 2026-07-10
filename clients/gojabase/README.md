# `clients/gojabase` — the reusable read-write-Base goja host

`gojabase` is the **one-and-only-one-way** to run a Hanzo subsystem's
self-contained JS/TS business logic (a goja bundle exposing `globalThis.handle`)
in-process **with persistence over per-tenant Base/SQLite**. It is the
storage-bearing sibling of [`clients/goja`](../goja) (the pure JS engine that
`plans`/`pricing` use with a read-only catalog).

captable (#97) is the pilot. **esign (#100) and dataroom (#101) reuse this
package unchanged** — it carries ZERO domain logic (no cap table, no signatures,
no rooms), only the engine + the Base bridge.

## What a subsystem provides

```go
host, err := gojabase.New(gojabase.Config{
    Name:    "captable",          // names the goja host AND the data subdir
    Bundle:  bundleBytes,         // the go:embed'd bundle (globalThis.handle)
    Schema:  schemaDDL,           // per-tenant SQLite DDL (CREATE TABLE IF NOT EXISTS …)
    DataDir: deps.DataDir,        // files land at {DataDir}/{Name}/{tenantSlug}.db
    OnOpen:  seedRow,             // optional per-tenant seed, run once after migrate
})
```

Then, in each zip route handler, resolve the tenant from the **validated**
principal and dispatch:

```go
org, ok := principal.Tenant(c)          // gojabase does NOT authenticate; the leaf does
if !ok { return zip.ErrForbidden("X-Org-Id required") }
resp, err := host.Dispatch(c.Context(), org, gojabase.Request{
    Route:  "stakeholders.add",
    Params: map[string]string{"id": c.Param("id")},
    Body:   decodedJSONBody,             // any (map / slice / scalar), or nil for reads
})
c.SetHeader("Content-Type", "application/json")
return c.Bytes(resp.Status, resp.Body)   // resp is {Status int, Body json.RawMessage}
```

## What the bundle sees (the host contract)

gojabase injects these native globals onto the runtime **per dispatch**, bound to
the tenant's DB + a per-request transaction:

```
globalThis.__db.query(sql, args)  -> row objects           (SELECT; TEXT→string)
globalThis.__db.exec(sql, args)   -> { changes, lastId }    (INSERT/UPDATE/DELETE)
globalThis.__newId()              -> collision-resistant id (crypto/rand, 128-bit)
globalThis.__now()                -> unix milliseconds

globalThis.__blob.put(key, b64)   -> (only when Config.Blob is set)  store bytes off-DB
globalThis.__blob.get(key)  -> b64  (only when Config.Blob is set)  read them back

globalThis.handle({ route, params, query, orgId, body }) -> { status, body }
```

`__blob` is the OPTIONAL object-storage seam (`Config.Blob`, backed by the cloud
VFS/S3). Use it for large binaries that must NOT bloat the per-tenant SQLite — e.g.
sign's PDFs: the bundle stores the bytes with `__blob.put` and keeps only the
returned key in a column. Keys are tenant-scoped by the host (`{Name}/{TenantSegment}`),
so a bundle can never reach another tenant's blobs. Payloads cross as base64.

`orgId` is the tenant passed to `Dispatch` — the bundle uses it to scope rows
(defence in depth on top of the per-tenant file). `args` is a positional array
bound to `?` placeholders. The Go host owns the schema (migrations); the bundle
issues SQL against it — column names are the coupling, so keep them in sync.

## Guarantees

- **Per-tenant isolation** — one SQLite file per org
  (`{DataDir}/{Name}/{TenantSegment}.db`), opened lazily, migrated once, pooled
  (LRU-capped + idle-evicted). `TenantSegment` is an **injective**, traversal-safe
  encoding (lowercased unpadded base32 of the raw org bytes), so DISTINCT orgs —
  including case/separator variants like `Acme`/`acme` and `a b`/`a_b` — NEVER
  share a file, and the `[a-z2-7]` segment can never traverse the data tree.
- **Atomicity** — each `Dispatch` runs `handle` inside ONE transaction that
  **commits iff** the response status `< 400` and `handle` did not throw;
  otherwise it **rolls back**. Multi-statement mutations (e.g. a share transfer:
  shrink source + insert target) are all-or-nothing for free, and a validation
  400 leaves the DB untouched. `MaxOpenConns(1)` serializes writes per tenant.
- **No JS-visible transaction API** — the per-request transaction removes the
  need for one; bundles just call `query`/`exec`.

## Leaf wiring (register)

Register the leaf and blank-import it in `subsystems/subsystems.go`. It mounts
under the mount-all default (empty `CLOUD_ENABLE`) — the captable/sign/dataroom
folds are NOT staged (their standalone apps are retired/empty, so the one binary
is authoritative from first write):

```go
func init() { cloud.RegisterWithShutdown("captable", 133, cloud.Typed(Mount), shutdown) }
```

See `clients/captable` for the complete reference leaf (schema, seed, routes).
