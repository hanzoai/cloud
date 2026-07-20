package metrics

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the SaaS-metrics god-view (SuperAdmin only, cross-tenant business
// aggregate).
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/metrics", core.Guard(s, Metrics))
}
