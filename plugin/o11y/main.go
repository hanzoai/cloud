package main

import (
	"context"
	"fmt"
	"os"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/o11y"
	_ "github.com/hanzoai/cloud/internal/driver"
	"github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
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
	// `o11y describe` is the exception, and for the same reason cloud.Listen makes
	// it one: the artifacts are a projection of routes, so describing must be a
	// function of the code alone and must not open the deployment's real stores
	// (the default data dir is /var/lib/cloud, which a describe run cannot write).
	specDir, describing := host.DescribeRequested()
	cfg := cloud.LoadConfig()
	if describing {
		spec, done, err := host.SpecConfig()
		if err != nil {
			return err
		}
		defer done()
		cfg = spec
	}
	deps := cloud.BuildDeps(cfg)
	if describing {
		deps = host.SpecDeps(cfg)
	}

	// A plugin is a host for its own requests, so it owns its own providers —
	// the SAME bootstrap cloud.Listen runs, not a second one. Before the mount, so
	// the trace sink this process registers below is already the destination its
	// own spans route to. Without this the child served /v1/o11y/* with the global
	// no-op provider and emitted nothing.
	defer host.InstallTelemetry(context.Background(), luxlog.Default(), "hanzo-o11y")(context.Background())

	// The audit trail Listen opens for a process that does not hold it: every
	// record appended to admin's over the plane (audit_serve.go).
	rec, err := host.Recorder(cfg, luxlog.Default(), "o11y", false)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	deps.Audit = rec

	app := newApp(cfg, deps)

	if err := o11y.Use(app, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	// The same self-description every generated app binary gets from cloud.Listen.
	// This main is hand-written (it is a plugin, not a Wire stub), so it asks for
	// the mode itself — through the same one producer.
	if describing {
		return host.Describe(specDir, app)
	}

	// Teardown belongs to the process that owns the resources. The OTLP
	// collector, the trace sink and the event-ingest Datastore all live HERE
	// now, so their flush-and-close runs here on our own shutdown rather than
	// in the host's UseAll teardown.
	app.OnShutdown(o11y.ShutdownO11y)

	// Bind the CANONICAL plane socket before serving the edge.
	//
	// cloud.Listen does this for every generated app main; this one is
	// hand-written, so nothing else in the process will — and without it the app
	// is unreachable over the plane while looking perfectly healthy: zip binds
	// its own listener at a temp path, /var/lib/cloud/run/o11y.sock never
	// exists, and zip.DialApp("o11y") finds nothing. Peers then fail with no
	// error anyone can see. That is exactly how POST /v1/event/{project}/envelope
	// answered 503 to every Sentry SDK while o11y served its own reads fine, and how
	// the LLM-obs claim silently declined so those batches walked the product wire.
	//
	// Fail-SOFT: a plane that will not bind must not take the HTTP surface down
	// with it — this process is what answers /v1/o11y.
	//
	// The AGENT MCP SERVER goes on that plane first, for the third time in this file and
	// for the third identical reason: cloud.Listen registers it for every generated
	// app main and this one is hand-written. Without it this app's tools are the
	// only ones in the fleet a caller from INSIDE reaches through the edge, so the
	// identity boundary deletes that caller's principal and all 365 of them refuse
	// it — see cloud.UseMCP.
	cloud.UseMCP(app)
	if stop, err := cloud.ServePlane("o11y", luxlog.Default()); err != nil {
		luxlog.Default().Warn("plane: socket not served", "app", "o11y", "err", err)
	} else {
		defer func() { _ = stop() }()
	}

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
// this one. Every scaffolded app gets it from cloud.Listen; this main is
// hand-written (see the package doc), and that is exactly why it has to install
// it explicitly — nothing else in this process will.
//
// EdgeCORS in particular, because these prefixes include GET /v1/o11y/summary: the
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
//
// THE IDENTITY BOUNDARY, for the same reason and with more force. o11y scopes
// every read by the validated tenant (apps/o11y/scope.go reads c.Org(), and its
// admin surfaces read X-User-IsAdmin) — headers that are only trustworthy because
// SOMETHING strips the client's copy and re-mints them from a verified token.
// cloud.Serve installs that boundary for every scaffolded app; this main is
// hand-written, so it has to install it itself, and until it did, this process
// took those headers from the wire.
//
// It is also what the abuse gate below needs: the gate classes a caller from the
// boundary's own attestation (principal.Minted), so without a boundary every
// caller here is anonymous — safe, but blind to the customer automation it exists
// to tell apart from a scraper.
//
// AbuseGate then, by the same argument as EdgeCORS: this process answers public
// requests, so it carries the lifecycle defense itself, and an app that opted out
// of the gate by being hand-written would be the one prefix family a stolen
// credential could work against unwatched.
//
// Shadow per org by default, exactly as in the fused binary, so this is a sensor
// here until an operator arms the org — not a second policy.
func newApp(cfg *cloud.Config, deps cloud.Deps) *zip.App {
	// cloud.App, not zip.New. Every other app binary already ran this exact chain,
	// because every other one reaches it through cloud.Listen; this program was the
	// single exception, and the six members it silently lacked are what the long
	// note above is about. Assembling one by hand is no longer possible, so the
	// exception cannot come back.
	//
	// nil tools: the MCP surface is projected from a subsystem list, and this
	// program mounts o11y directly rather than holding one.
	app := cloud.App("o11y", cfg, deps, nil)

	// The audit trail with the support-session containment inside it, exactly as
	// cloud.Listen installs them. Without the trail this program recorded no write
	// of anyone's and no request of a support session (impersonation.go).
	cloud.Witness(app, deps.Audit)

	// AbuseGate is NOT part of the constructor: in cloud.Listen it sits after the
	// audit trail and the per-org ceiling, so that a refused request is still
	// recorded. Installing it here keeps it after everything the constructor
	// carries, which is the same position relative to identity that it has there.
	app.Use(host.AbuseGate(deps, host.EdgePolicy(cfg), host.Traffic()))
	return app
}
