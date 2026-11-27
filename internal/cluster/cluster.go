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
