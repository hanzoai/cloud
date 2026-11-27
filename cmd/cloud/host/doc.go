// Command cloud is the Hanzo Cloud router: one binary that serves the whole API
// by mounting every subsystem as its own process, started on the first request
// that reaches it.
//
// It IS the shipped binary now — the fused monolith that imported apps and linked
// all 112 subsystem graphs into one ~3105-package link is gone. This host knows
// only where each app lives and what path it answers — never what the app does —
// so it links zip, the generated manifest, and the light webui console embed, and
// nothing else. A subsystem changing rebuilds itself; the host is untouched.
//
// Lazy is what makes 112 services affordable. Mounting them eagerly costs 112
// processes, 112 resident sets and 112 startup times at boot for a set that is
// mostly idle. Mounted lazily, an app nobody calls costs a route entry and a
// struct; the cost moves to the first request that needs it. The subsystems that
// cannot wait for a request — the ones that own a listener or a background loop —
// say so with Eager on their row in apps.go and start with the host, unless the
// listener is a TCP port the host can own for them (Wire, wire.go), in which case
// the first connection is the request. The ones a
// turn waits on say Turn, and start with the host where the deployment reserves
// 6 GiB or states nothing (manifest.TurnPinned).
//
// The apps themselves are unchanged and unaware: each is the same plugin/<name>
// binary that already exists, serving the same routes through the same
// cloud.Listen middleware it would serve standalone. Identity, billing and
// telemetry run in the app's own process, where they already ran.
//
// The host is the ENTRY POINT, so it owns three things no plugin can: it serves
// the white-labelled console at "/" (webui, mounted last so every app prefix
// wins); it threads the deployment's operator flags to the children as CLOUD_*
// env (run→forward), and its own build — the tag and the commit the linker
// stamped into it — the same way (env.ExportBuild). The data-plane key is NOT
// one of those: it arrives in the environment from KMS and every child inherits
// it, which is how each one opens the encrypted files they all share.
//
// Deployment is one directory: the host plus its plugins, which is what the image
// already ships. Point CLOUD_<NAME>_ADDR at an instance running elsewhere, or
// CLOUD_<NAME>_BIN at a specific build, to override one of them.
package host
