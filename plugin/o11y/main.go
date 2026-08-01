// o11y is the observability subsystem built as its OWN binary.
//
// It is an ordinary zip app. There is no SDK, no schema and nothing
// plugin-specific in here except zip.Addr — which is the whole plugin contract:
// serve on the socket a host handed us, or on our own port when run directly.
// The same binary therefore covers both deployments without a second code path.
//
// It mounts EXACTLY what the fused binary used to mount in-process, by calling
// the same o11y.MountO11y. The subsystem's code did not move and did not fork;
// only the process it runs in changed, which is the point — where a subsystem
// runs is a deployment decision, not a property of the source.
//
// This is hand-written rather than scaffolded because o11y is its own composition
// root: it builds its Deps, installs telemetry, and owns the OTLP collector /
// trace sink / event Datastore lifetime — none of which the lean cloud.Serve stub
// expresses. cmd/gen-app-cmds leaves an existing main untouched, so this one
// stays; it links only o11y's own graph, never the fleet.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/o11y"
	"github.com/zap-proto/zip"
)

// listenEnv names the address to serve on when this binary is run DIRECTLY
// rather than by a host. Under a host, zip.Addr ignores it and uses the private
// unix socket the host created. The default deliberately is not cloud's own
// :9653, so running both on one box does not collide.
const (
	listenEnv     = "O11Y_LISTEN"
	defaultListen = ":9654"
)

// Standalone entry for the o11y app — hand-written (see the package doc). The
// host loads it as a plugin; run directly it serves on O11Y_LISTEN.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "o11y: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// The same config and the same Deps the unified binary builds — o11y reads
	// only Logger and DataDir out of it, but building it the one canonical way
	// keeps this entrypoint honest about what a subsystem may reach for.
	//
	// `o11y describe` is the exception, and for the same reason cloud.Serve makes
	// it one: the artifacts are a projection of routes, so describing must be a
	// function of the code alone and must not open the deployment's real stores
	// (the default data dir is /var/lib/cloud, which a describe run cannot write).
	specDir, describing := cloud.DescribeRequested()
	cfg := cloud.LoadConfig()
	if describing {
		spec, done, err := cloud.SpecConfig()
		if err != nil {
			return err
		}
		defer done()
		cfg = spec
	}
	deps := cloud.BuildDeps(cfg)

	// A plugin is a host for its own requests, so it owns its own providers —
	// the SAME bootstrap cloud.Serve runs, not a second one. Before the mount, so
	// the trace sink this process registers below is already the destination its
	// own spans route to. Without this the child served /v1/o11y/* with the global
	// no-op provider and emitted nothing.
	defer cloud.InstallTelemetry(context.Background(), deps.Logger, "hanzo-o11y")(context.Background())

	app := newApp(deps)

	if err := o11y.MountO11y(app, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	// The same self-description every generated app binary gets from cloud.Serve.
	// This main is hand-written (it is a plugin, not a Wire stub), so it asks for
	// the mode itself — through the same one producer.
	if describing {
		return cloud.Describe(specDir, app)
	}

	// Teardown belongs to the process that owns the resources. The OTLP
	// collector, the trace sink and the event-ingest Datastore all live HERE
	// now, so their flush-and-close runs here on our own shutdown rather than
	// in the host's MountAll teardown.
	app.OnShutdown(o11y.ShutdownO11y)

	addr := os.Getenv(listenEnv)
	if addr == "" {
		addr = defaultListen
	}
	return app.Listen(zip.Addr(addr))
}

// newApp builds this app's router with the edge policy a PUBLIC-facing process
// has to carry.
//
// The host in front of every plugin is a pure router: it claims prefixes and
// proxies them, and installs no middleware of its own (cmd/cloud/main.go). So the
// browser-facing policy belongs to the process that answers the request, which is
// this one. Every scaffolded app gets it from cloud.Serve; this main is
// hand-written (see the package doc), and that is exactly why it has to install
// it explicitly — nothing else in this process will.
//
// EdgeCORS in particular, because these prefixes include GET /v1/summary: the
// PUBLIC status document, read cross-origin by a browser on a brand host that the
// CLOUD_CORS_ORIGINS allowlist already admits. Without this the o11y plugin was
// the only public surface answering 200 with no Access-Control-Allow-Origin, and
// the browser discarded a body it had already received. It is the SAME middleware
// over the SAME live allowlist every other app answers with — one CORS policy,
// one definition of it, never a second mechanism for the endpoint that happens to
// be unauthenticated.
//
// Installed BEFORE the mount because fiber runs middleware in registration order:
// one added after the routes never runs.
func newApp(deps cloud.Deps) *zip.App {
	app := zip.New(zip.Config{AppName: "o11y", Logger: deps.Logger})
	app.Use(cloud.EdgeCORS(deps.GatewayPolicy))
	return app
}
