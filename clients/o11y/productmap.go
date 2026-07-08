package o11y

import "strings"

// productmap is the ONE server-side table that resolves a client-supplied
// `product` query param into the concrete infra identities the scoped o11y
// handlers query: the ClickHouse log `resources_string['app']` value, the
// VictoriaMetrics `up{service=…}` selector, and the in-cluster health host.
//
// SECURITY (this is a tenant-isolation + injection boundary, so it lives in ONE
// place):
//
//   - The `product` param is NEVER interpolated raw into a query or a hostname.
//     It is first shape-validated to a DNS-1123 label (validProduct) — so it can
//     never smuggle a PromQL break-out (`"} or up{`), a ClickHouse fragment, or a
//     path/host segment into the health prober (SSRF) — and then checked against
//     the KNOWN set. An unknown-but-well-formed product resolves to `ok=false`,
//     and every handler answers HONEST-EMPTY (never an error, never another
//     service's data, never a probe of an arbitrary host).
//   - The known set mirrors the VERIFIED live services that emit a per-product
//     signal (VM `up{service=…}` / log `resources_string['app']`). app == prom
//     service == the id, and the health host is derived once (`<id>.hanzo.svc…`),
//     so the mapping is DRY: one set, three derivations.
//
// The `product` param is NOT a tenant selector — the org is ALWAYS resolved
// server-side from the validated principal and pinned into every query. So
// letting the client name WHICH product's telemetry it wants is safe: it still
// only ever sees its OWN org's rows (non-admin) for that product.

// knownServices is the VERIFIED set of in-cluster services that emit a
// per-product observability signal. It is the allowlist for logs/metrics/status:
// a product not in this set has no backing service and resolves to honest-empty.
var knownServices = map[string]bool{
	"agents": true, "auto": true, "billing": true, "bot-gateway": true,
	"chat": true, "cloud": true, "cloud-api": true, "commerce": true,
	"datastore": true, "flow": true, "gateway": true, "hanzo-mpc": true,
	"hanzo-playground": true, "iam": true, "insights-capture": true, "kms": true,
	"models": true, "nats": true, "paas": true, "pricing": true, "rag-api": true,
	"s3": true, "search": true, "studio": true, "vector": true, "visor": true,
}

// clusterDNSSuffix is the in-cluster DNS suffix for the health prober. The
// services all run in the same `hanzo` namespace as this pod, so a short
// `<svc>.hanzo.svc` resolves; the full suffix is explicit for clarity and is
// overridable per deployment.
const clusterDNSSuffix = ".hanzo.svc.cluster.local"

// service is the resolved infra identity for a product.
type service struct {
	// ID is the canonical service id (== the product param, validated).
	ID string
	// App is the ClickHouse `resources_string['app']` value for this product's logs.
	App string
	// PromService is the VictoriaMetrics `service` label value (up{service=…}).
	PromService string
	// HealthHost is the in-cluster host the status prober hits (<id>.hanzo.svc…).
	HealthHost string
}

// resolveService validates + resolves a product param. ok=false means the product
// is unknown/ill-formed and the caller must answer honest-empty (never an error).
func resolveService(product string) (service, bool) {
	p := strings.TrimSpace(product)
	if !validProduct(p) || !knownServices[p] {
		return service{}, false
	}
	return service{
		ID:          p,
		App:         p,
		PromService: p,
		HealthHost:  p + clusterDNSSuffix,
	}, true
}

// validProduct enforces a strict DNS-1123 label on the product param — the ONE
// shape gate that makes the value safe to place (only after an allowlist check)
// into a PromQL label, a ClickHouse bound parameter, and a hostname. Length is
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
