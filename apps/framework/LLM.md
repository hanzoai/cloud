# apps/framework — mounting the DocType engine

This package is an **adapter**, not an engine. The engine moved out:

| Module | Role |
|--------|------|
| `github.com/hanzoai/doctype`   | the VALUE — schema, coercion, naming, permission calculus. No I/O. |
| `github.com/hanzoai/framework` | the ENGINE — store, validation, hooks, leases, operations. No transport. |
| `apps/framework` (here)     | the PLACE — mounts the engine at `/v1/framework/*` in the cloud binary. |

They were split out of this directory so the CMS/ERP/CRM/Helpdesk Go apps can
build on the same engine without importing `hanzoai/cloud`.

## What this package does — and only this

1. **Opens the engine with cloud's storage policy.** `cek.Open` is injected as
   `engine.Config.OpenDB`, so the data plane is encrypted at rest under the
   KMS-held master key. The engine does not import `cek`; a test or a
   standalone app passes nothing and gets plain SQLite.
2. **Turns a validated principal into an engine `Caller`.** `principal.Org`
   returns an org ONLY for a validated principal, so a forged `X-Org-Id` yields
   an empty `Caller` and the engine refuses 403 before any store access. This is
   the ONE tenant-derivation path.
3. **Binds each engine operation to a route** and maps `engine.Classify(err)` to
   an HTTP status in one function (`fail`), so no route can answer differently
   for the same condition.
4. **Re-exports the engine vocabulary** (`alias.go`) so the app lanes keep one
   import and compile unchanged.

There is **no authorization logic here**. The engine enforces permissions in its
own operations; a second copy would be a second answer.

## Files

| File | Responsibility |
|------|----------------|
| `framework.go` | `Mount`/`Shutdown`, the `Caller` + error-Code boundaries, the typed ops, and the in-process API the lanes call |
| `alias.go`     | type aliases + re-exports of the engine and value vocabulary |
| `zipdoc_gen.go` | GENERATED (`go generate -run zipdoc ./apps/framework/...`) — the ops' doc comments, which is the only path from source to the spec and the MCP tool list |

## Routes — 17 of 19 are TYPED OPS

A typed op is ONE registry entry with N projections: the REST route, the OpenAPI
operation, the MCP tool, the CLI command and the SDK method all come from
`zip.Get(g, …)`. An untyped route is in none of them.

```
GET    /v1/framework/summary                        op
GET    /v1/framework/doctypes            op         POST /v1/framework/doctypes            op (201)
GET    /v1/framework/doctypes/:name      op         PUT  /v1/framework/doctypes/:name      op
DELETE /v1/framework/doctypes/:name      op (204)
GET    /v1/framework/roles               op         POST /v1/framework/roles               op (201)
DELETE /v1/framework/roles/:user/:role   op (204)
GET    /v1/framework/modules             op         GET  /v1/framework/modules/:module     op
POST   /v1/framework/modules/:module/install        op
GET    /v1/framework/:doctype            op         POST /v1/framework/:doctype            RAW
GET    /v1/framework/:doctype/:name      op         PUT  /v1/framework/:doctype/:name      RAW
DELETE /v1/framework/:doctype/:name      op (204)
POST   /v1/framework/:doctype/:name/submit          op
POST   /v1/framework/:doctype/:name/cancel          op
```

The wire is unchanged: same paths, same statuses, same JSON. `ops_projection_test.go`
pins the surface, the registry and both.

**The two RAW writes, and why.** A typed op's request schema is REFLECTED off its
In type, and the body of a document write IS the document's own field data — an
open object the DocType defines at run time. No Go struct both accepts that
verbatim and describes it, so typing them would publish a request schema naming
the two path segments and nothing else: an SDK method that cannot send a
document. They convert when zip carries all THREE halves of one capability:
DECLARE an open-object input (`additionalProperties: true` — today
`map[string]any` projects `additionalProperties: {"type":"object"}`, a false
schema), BIND the URL onto one (`bindURL` returns early unless the In is a
struct), and carry URL params OUTSIDE the body namespace — off the REST path
`op.invoke` gets no path map (MCP and the call plane pass nil), and for a
prompt-named DocType the create body's `name` IS the document's name
(`stringField(in, "name")` → `doctype.ResolveName`), so folding `:name` into the
body collides with a key the document owns. Re-verified against zip v1.18.6 (the
pin) and v1.18.8 (the newest published tag): none of the three shipped.
`TestOpenObjectRefusalStillHolds` reads all three, so no leg of this refusal can
outlive its cause.

The cost, measured: the two writes reach `openapi.yaml` as route-only entries —
path parameters, no `requestBody`, no `responses`, no prose. An SDK method
generated from that cannot send a document either, so the real choice is between
a schema that lies about the body and no schema at all, and only the second goes
away by itself when the capability lands.

**Known spec defect (leg 1, live).** `docView` is `map[string]any`, and it is
already the Out of four typed ops — get/submit/cancel one document, and the items
of the list — so `openapi.yaml` currently tells every SDK and every agent that
each field of a returned document is a JSON object. It is not: a document holds
strings and numbers. The fix is one `case reflect.Interface` in zip's `schemaOf`
returning `{}` (JSON Schema "any"), which is also leg 1 of the refusal above; it
needs a zip release, so it is not made in this package.

**Identity across the typed client.** A typed op receives only a `context.Context`,
so the engine `Caller` is assembled from two carriers parked ahead of the leaves
by `g.Use(cloud.Bridge(), bridgeFacts)`: the validated org from
`principal.OrgFrom` (cloud.Bridge) and the user id + platform-admin bit from
`bridgeFacts`. It is the same decision `caller()` makes on the request — never an
`In` field, which is caller-supplied and would be a tenant key the caller
asserted for itself. Off the HTTP path (MCP `tools/call`, a CLI `LocalInvoke`)
neither bridge runs, both reads come back empty, and the engine refuses 403 —
the handler's own gate, with no second gate to keep in sync.

Mounted at subsystem order 129, binding before the AI subsystem's `/v1/*`
catch-all (150). Static routes register before the generic `/:doctype` routes so
Fiber's first-match scan resolves them unambiguously, and those names are
reserved DocType names so no document route can shadow them.

`pathParam` percent-decodes path segments at the ONE place this package reads
one: the router runs with Fiber's default `UnescapePath:false`, so a name
containing a space ("Sales Invoice") arrives as `%20` and would otherwise never
match its stored value.

## For app lanes

Nothing changed. Keep importing `apps/framework` and declaring DocTypes:

```go
func init() {
    framework.RegisterModule("cms", []framework.DocType{{
        Name:   "Article",
        Fields: []framework.DocField{{Fieldname: "title", Fieldtype: framework.FieldData}},
    }})
    framework.RegisterHook("Article", framework.ActionBeforeSave, computeSlug)
}
```

`framework.DocType` here and `doctype.DocType` in the value module are the SAME
type (an alias), so a lane can move to importing the engine directly whenever it
wants, with no conversion.

For the DocType model, fieldtypes, naming, permissions and hook semantics, read
the two modules' own `LLM.md` — this file deliberately does not restate them.
