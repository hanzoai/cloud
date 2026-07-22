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
// Design — one mechanism, not many. The subsystem set is the explicit list
// apps.Wire() returns — []cloud.MountSpec in mount order (kms first-tier,
// iam 50, commerce 100, …, ai last), no init()-registry. A subcommand is just a
// *selection* over that slice:
//
//   - `hanzo <svc>`  ⇒ cloud.Serve(specs, []string{svc}); MountAll mounts only it.
//   - `hanzo cloud`  ⇒ cloud.Serve(specs, nil); cfg.Enable per --enable (empty = all).
//
// Both paths run the identical compose root (BuildDeps → zip.App → health
// contract → MountAll → graceful Listen) — that body lives once in cloud.Serve
// and is shared with cmd/cloud. No subcommand duplicates boot logic.
//
// Identity is served on the CLEAN github.com/hanzoai/iam (the Casdoor/Beego fork
// github.com/hanzoai/iam-v1 is RETIRED, GONE from this binary's graph): the
// in-process fold clients/iam (order 50) embeds the clean iam-v2 zip-natively under
// /v1/iam/* (+ /login/oauth/*) via iamserver.Mount(app, db) for the fused surface;
// the FULL standalone identity provider is the clean iam's own binary. The legacy
// `hanzo iam` subcommand (which launched the Casdoor daemon via iamserver.Run) is
// gone — nothing here links the dead Beego module, so there is no beego process-global
// to collide at package load.
package main

import (
	"fmt"
	"os"
	"sort"

	// cli is the cloud-control CLI (client mode). `hanzo <verb>` — login, apps,
	// deploy, clusters, build, k8s, config — is a thin client over IAM +
	// platform + cloud; `hanzo <subsystem>` (below) is server mode. The two
	// worlds share one binary and are selected by the first token. Imported
	// FIRST so its init() captures the real stdout before the server-graph
	// dependencies' init() functions emit startup chatter to it.
	"github.com/hanzoai/cloud/cli"

	"github.com/hanzoai/cloud"

	// The subsystem set is defined ONCE in the subsystems bundle (shared with
	// cmd/cloud). apps.Wire() returns it in mount order; main threads that
	// slice through dispatch/usage/Serve. Inert at load.
	"github.com/hanzoai/cloud/apps"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// nonRegistrySubcommands are the dispatch targets that do NOT correspond to
// a single Wire() subsystem entry: the full fused surface and the datastore (a
// datastore C++ fork with no Go serve target — see the datastore case in
// dispatch()). Listed in --help alongside the registry-backed subcommands.
var nonRegistrySubcommands = map[string]string{
	"cloud":     "serve the full unified surface (all enabled subsystems, one listener)",
	"datastore": "datastore-fork analytics DB — not a Go serve target (see help text)",
}

func main() {
	// Restore the real stdout (cli.init redirected it to stderr so dependency
	// startup chatter cannot corrupt machine-readable output).
	cli.RestoreStdout()

	// Share the build version with the CLI (User-Agent, `hanzo version`).
	cli.Version = version

	// The composition root's subsystem list, threaded through usage + dispatch +
	// Serve. Defined ONCE (apps.Wire()); cloud never imports it (cycle).
	specs := apps.Wire()

	if len(os.Args) < 2 {
		usage(os.Stdout, specs)
		return
	}
	sub := os.Args[1]
	switch sub {
	case "-h", "--help", "help":
		usage(os.Stdout, specs)
		return
	case "version", "--version", "-v":
		fmt.Printf("hanzo %s\n", version)
		return
	}

	// CLIENT MODE. A control-plane verb (login, apps, deploy, clusters, build,
	// k8s, config) routes to the cobra cloud-control CLI with the full args
	// (including the verb) so cobra can parse subcommands + flags. cobra prints
	// its own errors, so just translate to a non-zero exit.
	//
	// A control verb emits ZERO server-init chatter — its stdout/stderr stay
	// clean and pipeable. The two historical noise sources were dependency
	// package init() side effects that fire on IMPORT, i.e. before this branch
	// runs, so they cannot be gated here; they are fixed at their source to be
	// lazy / server-only instead. A control verb links that graph but triggers
	// neither:
	//   - the IAM registry-token signing key is resolved on first mint (a server
	//     path), not in controllers' package init (iam.EnsureRegistrySigningKey);
	//   - beego's conf/app.conf global-config probe is silent when the default
	//     file is absent (the CLI case), loud only on a present-but-broken file.
	if cli.IsControlVerb(sub) {
		if err := cli.Execute(os.Args[1:]); err != nil {
			os.Exit(1)
		}
		return
	}

	// SERVER MODE. Reset os.Args so the delegated service / cloud.LoadConfig
	// sees its own flags at argv[1:], not the subcommand token. e.g.
	// `hanzo kms --listen=:9000` → the kms serve path parses `--listen=:9000`.
	os.Args = append(os.Args[:1], os.Args[2:]...)

	if err := dispatch(sub, specs); err != nil {
		fmt.Fprintf(os.Stderr, "hanzo %s: %v\n", sub, err)
		os.Exit(1)
	}
}

// dispatch routes a subcommand to its serve entrypoint.
func dispatch(sub string, specs []cloud.MountSpec) error {
	switch sub {
	case "cloud":
		// Full fused surface: --enable governs the set (empty = all).
		return cloud.Serve(specs, nil)

	// NOTE: the legacy `hanzo iam` subcommand (the standalone Casdoor/Beego
	// identity daemon, github.com/hanzoai/iam-v1) is RETIRED. iam-v1 is dead; the
	// standalone identity provider is now the clean github.com/hanzoai/iam binary
	// (zip-native + hanzoai/orm), and the in-process fold is clients/iam (which
	// embeds the clean iam). Nothing here links the dead Casdoor module anymore.

	case "datastore":
		// Hanzo Datastore is a datastore C++ fork. It has no Go
		// Serve()/Run() to dispatch to: the server is the datastore
		// engine (built via CMake), and the only Go in the repo is
		// cmd/zap-bridge — a SEPARATE per-package Go module
		// (github.com/hanzoai/datastore/cmd/zap-bridge) built solely by
		// the datastore Dockerfile's zap-builder stage, not part of this
		// module graph. Folding it into `hanzo` would mean either cgo-
		// linking datastore into every Hanzo binary (a non-starter) or
		// vendoring a second main module (violates one-binary). So
		// datastore stays its own artifact; `hanzo datastore` documents
		// that boundary instead of pretending to serve it.
		return fmt.Errorf(
			"datastore is a datastore-fork analytics DB, not a Go serve target.\n" +
				"  - server:    the datastore engine (CMake build) — run its own image ghcr.io/hanzoai/datastore\n" +
				"  - zap-bridge: github.com/hanzoai/datastore/cmd/zap-bridge is a separate Go module,\n" +
				"               built only by the datastore Dockerfile; it is not linked into hanzo.\n" +
				"  use the standalone datastore deployment; `hanzo` composes the request-tier Go services")

	default:
		// Registry-backed single-service mode: serve exactly `sub`.
		// Validate it is a known subsystem before booting anything.
		if !registryHas(specs, sub) {
			usage(os.Stderr, specs)
			return fmt.Errorf("unknown subcommand %q", sub)
		}
		return cloud.Serve(specs, []string{sub})
	}
}

// registryHas reports whether name is a registered subsystem.
func registryHas(specs []cloud.MountSpec, name string) bool {
	for _, spec := range specs {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// usage prints the subcommand list: the non-registry targets (cloud, iam,
// datastore) plus every subsystem in the composition root (Wire()), sorted.
func usage(w *os.File, specs []cloud.MountSpec) {
	fmt.Fprintf(w, "hanzo %s — the unified Hanzo Go binary\n\n", version)
	fmt.Fprintf(w, "Usage:\n  hanzo <command> [flags]\n\n")

	// Control commands (client mode) — manage the live estate. Defined once in
	// the cli package so this list cannot drift from the router.
	fmt.Fprintf(w, "Control commands (gcloud/doctl-style):\n")
	ctrl := cli.ControlCommands()
	ctrlNames := make([]string, 0, len(ctrl))
	for name := range ctrl {
		ctrlNames = append(ctrlNames, name)
	}
	sort.Strings(ctrlNames)
	for _, name := range ctrlNames {
		fmt.Fprintf(w, "  %-12s %s\n", name, ctrl[name])
	}
	fmt.Fprintf(w, "\nService subcommands (server mode):\n")

	// Collect: registry names ∪ non-registry names, dedup, sort.
	seen := map[string]string{}
	for name, desc := range nonRegistrySubcommands {
		seen[name] = desc
	}
	for _, spec := range specs {
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
