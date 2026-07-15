package provision

import (
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

// Take an organization and the owning user
func Provision(org *organization.Organization, usr *user.User) {
	// Make sure org exists
	if org.CreatedAt.IsZero() {
		org.MustCreate()
	}

	// Make sure user exists
	if usr.CreatedAt.IsZero() {
		usr.MustCreate()
	}

	// The org's commerce store is NOT provisioned here: it is created lazily and
	// idempotently on the org's first authenticated GET /v1/store/current, via the
	// ONE canonical primitive store.EnsureDefault (org-scoped, no payment creds).
	// See clients/commerce/api/store/current.go.
}
