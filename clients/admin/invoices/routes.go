package invoices

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
)

// Routes registers the fleet invoice view (SuperAdmin only, cross-tenant).
func Routes(app cloud.Router, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/invoices", core.Guard(s, Invoices))
}
