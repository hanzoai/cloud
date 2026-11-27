// o11y is the observability subsystem built as its OWN binary.
//
// It is an ordinary zip app. There is no SDK, no schema and nothing
// plugin-specific in here except zip.Addr — which is the whole plugin contract:
// serve on the socket a host handed us, or on our own port when run directly.
// The same binary therefore covers both deployments without a second code path.
//
// It mounts EXACTLY what the fused binary used to mount in-process, by calling
// the same o11y.Use. The subsystem's code did not move and did not fork;
// only the process it runs in changed, which is the point — where a subsystem
// runs is a deployment decision, not a property of the source.
//
// This is hand-written rather than scaffolded because o11y is its own composition
// root: it builds its Deps, installs telemetry, and owns the OTLP collector /
// trace sink / event Datastore lifetime — none of which the lean cloud.Listen stub
// expresses. cmd/gen-app-cmds leaves an existing main untouched, so this one
// stays; it links only o11y's own graph, never the fleet.
package main
