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
func compose(app *zip.App) { app.Use(cloud.Bridge()) }
