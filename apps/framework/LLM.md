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
| `framework.go` | `Mount`/`Shutdown`, the `Caller` + error-Code boundaries, HTTP handlers, and the in-process API the lanes call |
| `alias.go`     | type aliases + re-exports of the engine and value vocabulary |

## Routes (unchanged)

```
GET    /v1/framework/summary
GET    /v1/framework/doctypes            POST /v1/framework/doctypes
GET    /v1/framework/doctypes/:name      PUT|DELETE /v1/framework/doctypes/:name
GET    /v1/framework/roles               POST /v1/framework/roles
DELETE /v1/framework/roles/:user/:role
GET    /v1/framework/modules             GET /v1/framework/modules/:module
POST   /v1/framework/modules/:module/install
GET    /v1/framework/:doctype            POST /v1/framework/:doctype
GET    /v1/framework/:doctype/:name      PUT|DELETE /v1/framework/:doctype/:name
POST   /v1/framework/:doctype/:name/submit
POST   /v1/framework/:doctype/:name/cancel
```

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
