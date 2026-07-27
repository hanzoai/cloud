// deploy is the GitOps deploy control plane built as its OWN binary.
//
// It is an ordinary zip app. There is no SDK, no schema and nothing
// plugin-specific in here except zip.Addr — which is the whole plugin contract:
// serve on the socket a host handed us, or on our own port when run directly.
// The same binary therefore covers both deployments without a second code path.
//
// It mounts EXACTLY what apps.Wire() used to mount in-process, by calling the
// same deploy.Mount. The subsystem's code did not move and did not fork; only
// the process it runs in changed.
//
// Why this one: after o11y, deploy is the largest EXCLUSIVE contributor to the
// unified binary's package graph — 253 packages that NOTHING else in cloud
// pulls, because it is the only importer of k8s.io/kubernetes/pkg (100) and
// k8s.io/kubectl/pkg (38). Every other candidate shares its heavy deps with
// hanzoai/ai or hanzoai/commerce, so unlinking it frees almost nothing.
// clients/deploy is imported by apps/apps.go and by NOTHING else, so unlinking
// it is a pure subtraction.
//
// paas is mounted HERE TOO, and that is not incidental. deploy's rollback
// delegates the CR patch to the process-global release seam
// (cloud.OnServiceRelease), and clients/paas is what installs it — a func
// pointer, which does not cross a process boundary. Without this line the
// rollback route stops reaching releaseService and degrades to
// "release plane not available (paas subsystem not co-resident)". paas cannot
// simply move out with deploy: the HOST needs it too (clients/platform's
// release path reads the same seam, and clients/admin's god-view reads the
// fleet observer paas publishes), so it stays linked in both. This is the seam
// that has to become an HTTP call before the coupling is actually gone.
package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/deploy"
	"github.com/hanzoai/cloud/clients/paas"
	"github.com/zap-proto/zip"
)

// listenEnv names the address to serve on when this binary is run DIRECTLY
// rather than by a host. Under a host, zip.Addr ignores it and uses the private
// unix socket the host created. The default deliberately is not cloud's own
// :9653, so running both on one box does not collide.
const (
	listenEnv     = "DEPLOY_LISTEN"
	defaultListen = ":9655"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "deploy: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// The same config and the same Deps the unified binary builds — building it
	// the one canonical way keeps this entrypoint honest about what a subsystem
	// may reach for.
	deps := cloud.BuildDeps(cloud.LoadConfig())

	app := zip.New(zip.Config{AppName: "deploy", Logger: deps.Logger})

	// The release seam first: paas.Mount is what calls RegisterServiceReleaser,
	// and deploy's rollback resolves it per request. Its /v1/paas routes are
	// registered on THIS app, which the host does not forward to — the host
	// serves /v1/paas from its own linked-in paas. Running this binary directly
	// therefore yields the same board from the same code, never a second one.
	if err := paas.Mount(app, deps); err != nil {
		return fmt.Errorf("mount paas (release seam): %w", err)
	}
	if err := deploy.Mount(app, deps); err != nil {
		return fmt.Errorf("mount: %w", err)
	}

	addr := os.Getenv(listenEnv)
	if addr == "" {
		addr = defaultListen
	}
	return app.Listen(zip.Addr(addr))
}
