package customer

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
)

// Routes registers the customer-management surface (SuperAdmin only). List (static)
// precedes the :org param route; the write actions are POST (distinct method), so none
// collide. The grants ledger + the org-in-body issue-grant share the ONE credit path.
func Routes(app cloud.Router, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/customers", core.Guard(s, Customers))
	g.Get("/customers/:org", core.Guard(s, CustomerDetail))
	g.Post("/customers/:org/credit", core.Guard(s, GrantCredit))
	g.Get("/grants", core.Guard(s, Grants))
	g.Post("/grants", core.Guard(s, IssueGrant))
	g.Post("/customers/:org/suspend", core.Guard(s, SuspendCustomer))
	g.Post("/customers/:org/reactivate", core.Guard(s, ReactivateCustomer))
}
