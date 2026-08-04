package o11y

import "strings"

// productmap is the ONE server-side table that resolves a client-supplied
// `product` query param into the concrete infra identities the scoped o11y
// handlers query: the datastore log `resources_string['app']` value, the
// `service` label the fleet prober records against, and the in-cluster health
// host.
//
// SECURITY (this is a tenant-isolation + injection boundary, so it lives in ONE
// place):
//
//   - The `product` param is NEVER interpolated raw into a query or a hostname.
//     It is first shape-validated to a DNS-1123 label (validProduct) — so it can
//     never smuggle a PromQL break-out (`"} or up{`), a datastore fragment, or a
//     path/host segment into the health prober (SSRF) — then mapped through the
//     console-slug ALIAS table, and finally the RESOLVED workload is checked
//     against the KNOWN set. An unknown-but-well-formed product resolves to
//     `ok=false`, and every handler answers HONEST-EMPTY (never an error, never
//     another service's data, never a probe of an arbitrary host).
//   - The known set mirrors the VERIFIED live services that emit a per-product
//     signal (VM `up{service=…}` / log `resources_string['app']`). For a plain
//     product id, app == prom service == the id.
//   - The ADDRESS is not derived by convention here. It is looked up in the one
//     registry of fleet addresses (probes.go). This file used to synthesize
//     `<workload>.hanzo.svc.cluster.local` and let the prober try /health then
//     /healthz against it, which is a second answer to a question that registry
//     already answers — and a wrong one for nineteen of the twenty-seven
//     products below, because most of these services serve neither port 80 nor
//     either path. A workload the fleet does not watch resolves with no address
//     and is not probed, which is the honest outcome: we have measured nothing
//     about it, and dialling a name nobody verified is how a healthy service is
//     reported down.
//
// The `product` param is NOT a tenant selector — the org is ALWAYS resolved
// server-side from the validated principal and pinned into every query. So
// letting the client name WHICH product's telemetry it wants is safe: it still
// only ever sees its OWN org's rows (non-admin) for that product.

// productAlias maps a console catalog slug to its concrete k8s workload name when
// they differ. Absent an entry, the product id IS the workload (identity), so ANY
// product whose slug matches a known workload works with no per-product code — the
// map only records the exceptions. The resolved workload is still allowlisted
// (knownServices) after aliasing, so an alias can never point at an unbacked host.
var productAlias = map[string]string{
	"cloud-api": "cloud",
	"api":       "cloud",
	"llm":       "gateway",
	"router":    "gateway",
	"analytics": "insights-capture",
	"observe":   "o11y",
	"o11y":      "o11y",
}

// knownServices is the VERIFIED set of in-cluster WORKLOADS that emit a per-product
// observability signal. It is the allowlist for logs/metrics/status: a resolved
// workload not in this set has no backing service and yields honest-empty. Aliases
// above may resolve TO these names (e.g. cloud-api → cloud, o11y → o11y), so the
// alias targets are members here.
//
// hanzo-mpc is absent: there is no Deployment of that name, its Service selects
// nothing, and a name that names no workload is drift, not an allowlist entry.
var knownServices = map[string]bool{
	"agents": true, "auto": true, "billing": true, "bot-gateway": true,
	"chat": true, "cloud": true, "cloud-api": true, "commerce": true,
	"datastore": true, "flow": true, "gateway": true,
	"hanzo-playground": true, "iam": true, "insights-capture": true, "kms": true,
	"models": true, "nats": true, "o11y": true, "paas": true, "pricing": true,
	"rag-api": true, "s3": true, "search": true, "studio": true, "vector": true,
	"visor": true,
}

// service is the resolved infra identity for a product.
type service struct {
	// ID is the canonical product id (== the product param, validated).
	ID string
	// App is the datastore `resources_string['app']` value for this product's logs.
	App string
	// PromService is the `service` label the fleet prober records this product's
	// hanzo_service_up under (probes.go's target name).
	PromService string
	// URL is where this service answers, taken from the one registry of fleet
	// addresses (probes.go). Empty when the fleet does not watch this workload,
	// and the scoped status read then probes nothing.
	URL string
}

// resolveService validates + resolves a product param. ok=false means the product
// is unknown/ill-formed and the caller must answer honest-empty (never an error).
// Resolution is: shape-validate → alias to workload → allowlist the workload.
func resolveService(product string) (service, bool) {
	p := strings.TrimSpace(product)
	if !validProduct(p) {
		return service{}, false
	}
	workload := p
	if a, ok := productAlias[p]; ok {
		workload = a
	}
	if !knownServices[workload] {
		return service{}, false
	}
	// A workload the fleet does not watch has no address, and the miss is not an
	// error: it emits logs and metrics we can still query, we have just never
	// measured whether it answers.
	url, _ := address(workload)
	return service{
		ID:          p,
		App:         workload,
		PromService: workload,
		URL:         url,
	}, true
}

// validProduct enforces a strict DNS-1123 label on the product param — the ONE
// shape gate that makes the value safe to place (only after an allowlist check)
// into a PromQL label, a datastore bound parameter, and a hostname. Length is
// bounded to 63 (a DNS label), lowercase alnum plus internal hyphens only.
func validProduct(p string) bool {
	if p == "" || len(p) > 63 {
		return false
	}
	for i, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0 && i < len(p)-1:
		default:
			return false
		}
	}
	return true
}
