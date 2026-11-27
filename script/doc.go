// Package script executes a provider's action NATIVELY when that action is
// JavaScript rather than Go
// in-process — no Node. A provider authored against the ActivePieces
// framework (@activepieces/providers-framework + providers-common) is compiled once
// to a single CommonJS program (see Bundle) and run inside goja, with the
// framework supplied by an in-process shim (shim.js) whose only impure
// primitive is a Go HTTP doer. Because that doer resolves synchronously, an
// action's `async run(ctx)` settles inside goja's own microtask drain, so a
// provider executes as ordinary in-process work — a goroutine, not a service.
//
// This is the substrate that retires the standalone ActivePieces Node engine
// (the `auto` pod). It sits ALONGSIDE apps/automations' Tier-A native Go
// providers: those are hand-written Go; this runs the long tail of JS
// providers unchanged. Both are org-scoped by the caller — the runtime never
// resolves a credential itself; it receives the already-resolved `auth`.
package script
