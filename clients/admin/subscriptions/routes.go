package subscriptions

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the fleet subscription view (SuperAdmin only, cross-tenant).
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/subscriptions", core.Guard(s, Subscriptions))
}
