package openapi

import (
	"maps"
	"slices"
)

// THE OPERATOR'S OPERATIONS AT A CAPABILITY'S OWN ADDRESS.
//
// HIP-0139 §3.2 gives the operator one address family, /v1/admin/<name>, and
// [audience] keeps it out of the public document by address. A few operations
// that admit only a SuperAdmin still answer at the address of the capability they
// operate — the rate table, the CD console, the platform blueprint — so their
// address alone cannot say who they are for. This set says it, and [Compose]
// marks each one as the operator's the way it marks a stage: the fleet declares
// it, an app describing itself never sets it, and the one rule reads it.
//
// An entry is "METHOD /path" at the address the document publishes. Every entry
// names an operation whose handler refuses anyone but a SuperAdmin, and
// TestOperatorOperationsReachOnlyThePrivateDocument holds each one to an
// operation the internal document carries and the public one does not, so an
// entry cannot outlive its route. An operation that moves under /v1/admin/<name>
// leaves this set in the same change.
var operated = map[string]bool{
	"POST /v1/benchmark/attempts":                         true,
	"POST /v1/benchmark/claims/promote":                   true,
	"GET /v1/commerce/admin/catalog":                      true,
	"GET /v1/commerce/catalog/entries":                    true,
	"POST /v1/commerce/catalog/entries":                   true,
	"POST /v1/commerce/catalog/seed":                      true,
	"GET /v1/commerce/deposits":                           true,
	"GET /v1/commerce/plans/entries":                      true,
	"GET /v1/commerce/rates/entries":                      true,
	"POST /v1/commerce/rates/entries":                     true,
	"DELETE /v1/commerce/rates/entries/{product}/{meter}": true,
	"PUT /v1/commerce/rates/entries/{product}/{meter}":    true,
	"POST /v1/commerce/rates/import":                      true,
	"POST /v1/company/kyc/decision":                       true,
	"POST /v1/crawl/sign":                                 true, // narrower still: only the platform's own agent apps (apps/crawl sign.go)
	"POST /v1/deploy/applications/{name}/sync":            true,
	"GET /v1/deploy/callback":                             true,
	"GET /v1/deploy/gitops":                               true,
	"POST /v1/deploy/reconcile":                           true,
	"GET /v1/deploy/settings":                             true,
	"GET /v1/deploy/version":                              true,
	"GET /v1/engine/replicas":                             true,
	"PUT /v1/engine/replicas":                             true,
	"GET /v1/guide/blueprint":                             true,
	"PUT /v1/guide/blueprint":                             true,
	"GET /v1/guide/blueprint/versions":                    true,
	"PATCH /v1/guide/blueprint/{collection}/{id}":         true,
	"POST /v1/iam/assume":                                 true,
	"POST /v1/iam/impersonate":                            true,
	"POST /v1/iam/release":                                true,
	"POST /v1/marketplace/jobs/{id}/resolve":              true,
	"GET /v1/o11y/availability":                           true,
	"GET /v1/platform/apps/{app}/diff":                    true,
	"POST /v1/platform/apps/{app}/sync":                   true,
	"POST /v1/platform/fleet/{app}/deploy":                true,
	"POST /v1/principal/keys":                             true,
	"GET /v1/principal/matches":                           true,
	"POST /v1/principal/matches/{id}":                     true,
	"POST /v1/provider/github/claim":                      true,
	"POST /v1/reference/refresh":                          true,
	"POST /v1/sbom":                                       true,
	"POST /v1/tool/catalog/sync":                          true,
	"PATCH /v1/tool/catalog/{id}":                         true,
	"GET /v1/train/policies/{name}":                       true,
	"POST /v1/train/policies/{name}":                      true,
	"GET /v1/train/policies/{name}/diff":                  true,
	"POST /v1/train/policies/{name}/promote":              true,
	"POST /v1/train/policies/{name}/rollback":             true,
	"GET /v1/train/policies/{name}/versions/{version}":    true,
	"GET /v1/train/published":                             true,
}

// Operated reports the operator's operations outside /v1/admin, as "METHOD /path".
func Operated() []string { return slices.Sorted(maps.Keys(operated)) }

// RegisterOperated adds operator-only "METHOD /path" entries for private plugins
// layered onto the OSS surface.
func RegisterOperated(ops ...string) {
	for _, op := range ops {
		if op != "" {
			operated[op] = true
		}
	}
}
