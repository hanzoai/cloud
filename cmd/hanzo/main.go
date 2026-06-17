// Command hanzo is the unified Hanzo Go binary, dispatched by subcommand.
//
//	hanzo                 list subcommands
//	hanzo --help          list subcommands
//	hanzo <svc> [flags]   serve exactly one subsystem (iam, kms, commerce,
//	                      gateway, ai, base, vfs, o11y, …)
//	hanzo cloud [flags]   serve the full unified surface (all enabled
//	                      subsystems mounted into one zip.App / one listener)
//
// One binary. Many subsystems. The subcommand selects WHICH subsystem(s)
// serve this process; the same artifact is every standalone service AND
// the fused cloud control plane.
//
// Design — one mechanism, not many. Every Hanzo subsystem registers a
// cloud.MountSpec{Name, Order, Mount} into cloud.Registry via init() at
// package load (kms order 10, iam 50, gateway 80, commerce 100, …). A
// subcommand is therefore just a *selection* over that registry:
//
//   - `hanzo <svc>`  ⇒ cloud.Serve([]string{svc}); MountAll mounts only it.
//   - `hanzo cloud`  ⇒ cloud.Serve(nil); cfg.Enable per --enable (empty = all).
//
// Both paths run the identical compose root (BuildDeps → zip.App → health
// contract → MountAll → graceful Listen) — that body lives once in cloud.Serve
// and is shared with cmd/cloud. No subcommand duplicates boot logic.
//
// The single exception is `hanzo iam`. The registry's iam Mount (pkg/iam,
// order 50) wraps the Beego handler under /v1/iam/* for the fused surface;
// the FULL standalone IAM — login UI at /, all ~150 routes at root, LDAP +
// RADIUS listeners — is iamserver.Run(), the body of the legacy iamd
// main(). `hanzo iam` runs that, so the standalone identity provider is
// byte-for-byte what iamd shipped. See the iam case in dispatch().
//
// THE BEEGO CRUX (and why this binary does not init-panic). iam imports
// github.com/hanzoai/beego/v2; that fork carries process-global state
// (web.BeeApp singleton, ORM model registry, logger registration). The
// fear is that importing iam alongside the other subsystems collides at
// package load regardless of subcommand. It does not, for two reasons
// this codebase already established:
//
//  1. iam's ~150 route registrations and ORM table creation are NOT at
//     package init() — they live inside iamserver.Init() / routers.InitAPI()
//     / object.CreateTables(), which run only when iam actually serves.
//     Blank-importing the package is inert: no router, no ORM, no listener.
//  2. There is exactly ONE Beego v2 import path in the graph
//     (hanzoai/beego/v2), so there is exactly one Beego global to
//     initialize, and it is initialized lazily by whichever path serves
//     iam. (visor, the other Beego service, pins the *v1* fork
//     github.com/beego/beego and is intentionally NOT linked here — two
//     Beego majors in one binary is the collision to avoid, so we don't.)
//
// The proof is mechanical: the existing cmd/cloud binary already links
// this same graph (iam Beego v2 + kms + commerce + gateway + …) and builds
// + boots. cmd/hanzo links the same set plus iamserver for the standalone
// path; nothing new collides.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/hanzoai/cloud"

	// iamserver is the body of the standalone iamd main() — full Beego
	// server (login UI, all routes, LDAP/RADIUS). `hanzo iam` calls Run().
	"github.com/hanzoai/iam/iamserver"

	// Every subsystem registers into cloud.Registry via init(); the set is
	// defined ONCE in the subsystems bundle (shared with cmd/cloud), so the
	// dispatcher and the full-surface binary mount an identical set. Inert at
	// load — see THE BEEGO CRUX.
	_ "github.com/hanzoai/cloud/subsystems"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// nonRegistrySubcommands are the dispatch targets that do NOT correspond to
// a single cloud.Registry entry: the full fused surface, the standalone IAM
// boot, and the datastore (a ClickHouse C++ fork with no Go serve target —
// see the datastore case in dispatch()). Listed in --help alongside the
// registry-backed subcommands.
var nonRegistrySubcommands = map[string]string{
	"cloud":     "serve the full unified surface (all enabled subsystems, one listener)",
	"iam":       "serve standalone Hanzo IAM (full Beego server: login UI, OAuth2/OIDC, LDAP/RADIUS)",
	"datastore": "ClickHouse-fork analytics DB — not a Go serve target (see help text)",
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		return
	}
	sub := os.Args[1]
	switch sub {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	case "version", "--version", "-v":
		fmt.Printf("hanzo %s\n", version)
		return
	}

	// Reset os.Args so the delegated service / cloud.LoadConfig sees its own
	// flags at argv[1:], not the subcommand token. e.g. `hanzo kms --listen=:9000`
	// → the kms serve path parses `--listen=:9000`.
	os.Args = append(os.Args[:1], os.Args[2:]...)

	if err := dispatch(sub); err != nil {
		fmt.Fprintf(os.Stderr, "hanzo %s: %v\n", sub, err)
		os.Exit(1)
	}
}

// dispatch routes a subcommand to its serve entrypoint.
func dispatch(sub string) error {
	switch sub {
	case "cloud":
		// Full fused surface: --enable governs the set (empty = all).
		return cloud.Serve(nil)

	case "iam":
		// Standalone IAM = the body of iamd's main(). Full Beego server:
		// login UI at /, ~150 routes at root, LDAP + RADIUS listeners,
		// background sync loops. iamserver.Run() blocks until the process
		// is signalled. This is intentionally NOT the /v1/iam/*-wrapped
		// registry Mount — `hanzo iam` IS the identity provider, not a
		// route-prefixed subsystem inside the cloud surface.
		iamserver.Run()
		return nil

	case "datastore":
		// Hanzo Datastore is a ClickHouse C++ fork. It has no Go
		// Serve()/Run() to dispatch to: the server is the ClickHouse
		// engine (built via CMake), and the only Go in the repo is
		// cmd/zap-bridge — a SEPARATE per-package Go module
		// (github.com/hanzoai/datastore/cmd/zap-bridge) built solely by
		// the datastore Dockerfile's zap-builder stage, not part of this
		// module graph. Folding it into `hanzo` would mean either cgo-
		// linking ClickHouse into every Hanzo binary (a non-starter) or
		// vendoring a second main module (violates one-binary). So
		// datastore stays its own artifact; `hanzo datastore` documents
		// that boundary instead of pretending to serve it.
		return fmt.Errorf(
			"datastore is a ClickHouse-fork analytics DB, not a Go serve target.\n" +
				"  - server:    the ClickHouse engine (CMake build) — run its own image ghcr.io/hanzoai/datastore\n" +
				"  - zap-bridge: github.com/hanzoai/datastore/cmd/zap-bridge is a separate Go module,\n" +
				"               built only by the datastore Dockerfile; it is not linked into hanzo.\n" +
				"  use the standalone datastore deployment; `hanzo` composes the request-tier Go services")

	default:
		// Registry-backed single-service mode: serve exactly `sub`.
		// Validate it is a known subsystem before booting anything.
		if !registryHas(sub) {
			usage(os.Stderr)
			return fmt.Errorf("unknown subcommand %q", sub)
		}
		return cloud.Serve([]string{sub})
	}
}

// registryHas reports whether name is a registered subsystem.
func registryHas(name string) bool {
	for _, spec := range cloud.Registry {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// usage prints the subcommand list: the non-registry targets (cloud, iam,
// datastore) plus every subsystem registered into cloud.Registry, sorted.
func usage(w *os.File) {
	fmt.Fprintf(w, "hanzo %s — the unified Hanzo Go binary\n\n", version)
	fmt.Fprintf(w, "Usage:\n  hanzo <subcommand> [flags]\n\n")
	fmt.Fprintf(w, "Service subcommands:\n")

	// Collect: registry names ∪ non-registry names, dedup, sort.
	seen := map[string]string{}
	for name, desc := range nonRegistrySubcommands {
		seen[name] = desc
	}
	for _, spec := range cloud.Registry {
		if _, ok := seen[spec.Name]; !ok {
			seen[spec.Name] = fmt.Sprintf("serve the %s subsystem standalone", spec.Name)
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-12s %s\n", name, seen[name])
	}

	fmt.Fprintf(w, "\nMeta:\n")
	fmt.Fprintf(w, "  %-12s %s\n", "help", "show this message")
	fmt.Fprintf(w, "  %-12s %s\n", "version", "print version and exit")
	fmt.Fprintf(w, "\nFlags are per-subcommand (e.g. `hanzo cloud --enable=iam,kms --brand=hanzo`,\n")
	fmt.Fprintf(w, "`hanzo kms --listen=:8443`). Run a subcommand to see its config via env/flags.\n")
}
