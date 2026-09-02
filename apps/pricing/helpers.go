package pricing

import (
	"github.com/hanzoai/cloud/internal/environ"
	"strconv"

	hplans "github.com/hanzoai/plans"
)

// loadPlansCatalog returns the @hanzo/plans catalog the pricing bundle reads
// for its subscription/blockchain/policy/tools/gpu endpoints. Sourced from the
// plans embed module so cloud has ONE copy of the plan catalog feeding both
// /v1/plan/* (plan) and /v1/pricing/{subscriptions,blockchain,…} (here).
func loadPlansCatalog() (map[string]any, error) {
	return hplans.Data()
}

// parseFloatEnv reads a float env var with a default (markup knobs).
func parseFloatEnv(key string, dflt float64) float64 {
	if v := environ.Or(key, ""); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return dflt
}
