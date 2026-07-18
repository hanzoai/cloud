package subscriptions

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the fleet subscription view (SuperAdmin only, cross-tenant).
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	app.Get("/v1/admin/subscriptions", core.Guard(s, Subscriptions))
}
