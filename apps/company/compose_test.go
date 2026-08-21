package company

import (
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// compose stands in for the composer. Production programs are built by
// cloud.App, which installs the principal enrichment once at the root before
// any route; a test that mounts this subsystem on a bare app owns that duty
// itself, exactly once, here. A test that sends no identity is unaffected —
// with nothing validated there is nothing to park — so anonymous cases still
// refuse, and principal-carrying cases reach the handler as they do in
// production.
// compose is what Serve does for the whole binary, narrowed to what this
// package's ops need: the validated tenant on the context, and the money wire's
// own envelope for a refusal a typed op returns (serve.go installs both, and no
// package's own harness runs Serve).
func compose(app *zip.App) {
	app.Use(cloud.Bridge())
	app.Use(cloud.DenyEnvelope())
}
