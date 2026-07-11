package customer

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the customer-management surface (global-admin only). List (static)
// precedes the :org param route; the write actions are POST (distinct method), so none
// collide. The grants ledger + the org-in-body issue-grant share the ONE credit path.
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	app.Get("/v1/admin/customers", core.Guard(s, Customers))
	app.Get("/v1/admin/customers/:org", core.Guard(s, CustomerDetail))
	app.Post("/v1/admin/customers/:org/credit", core.Guard(s, GrantCredit))
	app.Get("/v1/admin/grants", core.Guard(s, Grants))
	app.Post("/v1/admin/grants", core.Guard(s, IssueGrant))
	app.Post("/v1/admin/customers/:org/suspend", core.Guard(s, SuspendCustomer))
	app.Post("/v1/admin/customers/:org/reactivate", core.Guard(s, ReactivateCustomer))
}
