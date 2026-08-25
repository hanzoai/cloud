// Package cluster answers what a Kubernetes namespace MEANS: which tenant owns
// it, and which environment it runs.
//
// It is ONE function, and it lives apart from the observer that scans the
// cluster so the boards that only RENDER the answer can ask it without linking a
// Kubernetes client. Every reader shares this decision: platform confines a
// non-super OrgAdmin to the namespaces whose tenant equals their validated org,
// and admin groups a workload as first-party infra or a customer deployment on
// the same fact. A board that re-derived it once classified every hanzo-mainnet
// workload as a customer's, which looks exactly like a correct answer.
//
// Class is TOTAL: every input yields a decision, and an unrecognised namespace is
// classified OUT (ok=false) rather than guessed at, so a reader can never reach
// beyond the tenants named here.
package cluster

import "strings"

// Platform is the tenant that owns the first-party namespaces — the deployment's
// own infrastructure, as against a customer's.
const Platform = "hanzo"

// Class returns the tenant and environment a namespace belongs to. tenant is the
// authorization axis; env is the lifecycle label.
func Class(ns string) (tenant, env string, ok bool) {
	switch ns {
	case "hanzo", "hanzo-mainnet":
		return Platform, "main", true
	case "hanzo-testnet":
		return Platform, "test", true
	case "hanzo-devnet":
		return Platform, "dev", true
	}
	// tenant-<org>: a customer's own namespace. The org IS the tenant key, so it
	// authorizes exactly like a first-party namespace with no special case.
	if t, found := strings.CutPrefix(ns, "tenant-"); found && t != "" {
		return t, "main", true
	}
	return "", "", false
}

// Tenant is Class's tenant projection ("" when the namespace is not ours).
func Tenant(ns string) string { t, _, _ := Class(ns); return t }

// Env is Class's env projection ("" when the namespace is not ours).
func Env(ns string) string { _, env, _ := Class(ns); return env }
