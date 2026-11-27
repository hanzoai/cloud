package billing

import (
	"net/http"

	"github.com/hanzoai/cloud/sdk/spec"
)

// The routes a caller reaches with no credential: the public plan catalog, and a hosted invoice, which the unguessable link in its path authorizes.
func init() {
	spec.Open("/v1/billing/plans", http.MethodGet)
	spec.Open("/v1/billing/invoices/hosted/:link", http.MethodGet)
	spec.Open("/v1/billing/invoices/hosted/:link/pdf", http.MethodGet)
}
