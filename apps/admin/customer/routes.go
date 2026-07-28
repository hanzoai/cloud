package customer

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/zap-proto/zip"
)

// Routes registers the customer-management surface (SuperAdmin only). List (static)
// precedes the :org param route; the write actions are POST (distinct method), so none
// collide. The grants ledger + the org-in-body issue-grant share the ONE credit path.
func Routes(z *zip.App, s *cloud.Service[core.State]) {
	o := ops{s: s}
	zip.Get(z, "/v1/admin/customers", o.Customers, zip.WithOperationID("adminCustomers"))
	zip.Get(z, "/v1/admin/customers/:org", o.CustomerDetail, zip.WithOperationID("adminCustomer"))
	zip.Post(z, "/v1/admin/customers/:org/credit", o.GrantCredit, zip.WithOperationID("adminGrantCredit"))
	zip.Get(z, "/v1/admin/grants", o.Grants, zip.WithOperationID("adminGrants"))
	zip.Post(z, "/v1/admin/grants", o.IssueGrant, zip.WithOperationID("adminIssueGrant"))
	zip.Post(z, "/v1/admin/customers/:org/suspend", o.SuspendCustomer, zip.WithOperationID("adminSuspendCustomer"))
	zip.Post(z, "/v1/admin/customers/:org/reactivate", o.ReactivateCustomer, zip.WithOperationID("adminReactivateCustomer"))
}

// ops binds the kernel to the typed handlers: a TypedHandler has no parameter for the
// service, so it arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[core.State] }
