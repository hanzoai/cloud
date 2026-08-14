package taxonomy

import (
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// compose stands in for the composer. Production programs are built by cloud.App,
// which installs the request bridge once at the root before any route; a test that
// mounts this subsystem on a bare app owns that duty itself, exactly once, here.
// A test that sends no identity is unaffected — with nothing validated there is
// nothing to park — so anonymous cases still read the published catalogue and are
// still refused every write, as they are in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }
