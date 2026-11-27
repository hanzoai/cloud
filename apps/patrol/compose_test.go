package patrol

import (
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// compose stands in for the composer. A production binary is built by cloud.App,
// which installs the principal enrichment once at the root before any route; a
// test that mounts this subsystem on a bare app owns that duty itself, exactly
// once, here. A request carrying no identity is unaffected — with nothing
// validated there is nothing to park — so anonymous cases still refuse.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }
