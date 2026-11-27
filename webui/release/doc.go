// Package release is the console's bytes: the ACTIVE release of a published
// site, held in memory and kept current.
//
// The console used to be //go:embed'd into this binary, which welded the
// frontend's lifecycle to the backend's — a CSS fix cost a ~22-minute cloud build
// and a `strategy: Recreate` rollout that took api.hanzo.ai down for 2m15s. The
// console is now published like any other site (apps/projects → apps/sites): a
// publish is under a second, a rollback is faster, and neither builds or restarts
// anything.
//
// It is the SAME release, read from the SAME store, resolved through the SAME
// registry the site edge uses (sites.CurrentResolver) — not a second copy of any
// of it. What differs is only where the bytes go: the edge STREAMS them for a
// site host, and this loads them once and hands webui an fs.FS, because
// console.hanzo.ai is deliberately NOT a site host (it must keep answering /v1 on
// the same origin, or the console's first-party session cookie stops working).
//
// A leaf, like webui itself: apps/sites + sdk/object + stdlib, never package
// cloud — both composition roots import it.
package release
