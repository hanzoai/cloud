// Package goja is the in-process JavaScript host: it runs a service's goja
// bundle (a self-contained, ESM-free JS file exposing globalThis.handle(req))
// inside the cloud binary, with an optional tenant-bound Base/SQLite binding.
//
// It is a LIBRARY, not a subsystem — it registers no route and has no manifest
// row.
//
// It is the SHARED glue used by apps/plan and apps/pricing to host
// @hanzo/plans and @hanzo/pricing in-process via dop251/goja — the same engine
// base/plugins/gojavm uses. We do not import base's gojavm Runtime directly
// because that loader is manifest-driven (extension.json + a single exported
// `fn` over JSON-over-the-wire payloads); our services instead inject a catalog
// of JSON globals at VM init and call a richer handle({route,params,...}) entry.
// The VM-pool + compile-once + per-runtime ensureLoaded discipline here mirrors
// gojavm/runtime.go exactly so behavior and the pool semantics are identical.
//
// Module boundary: the JS bundle + catalog data live in the service repos
// (hanzoai/plans, hanzoai/pricing) and are passed in by the caller. This
// package carries zero service logic — only the engine plumbing.
//
// READ-WRITE variant: this package hosts bundles with a read-only catalog
// injected once at New (plans/pricing). Subsystems that need PERSISTENCE — a
// bundle that reads AND writes per-tenant Base/SQLite (captable #97, esign #100,
// dataroom #101) — use NewBase (base.go / basestore.go), the Base binding folded
// into THIS package: it builds on this engine (via DispatchWith) and injects a
// tenant-bound __db bridge per request. Reach for NewBase when your bundle stores
// data; reach for New directly only for a read-only bundle.
package goja
