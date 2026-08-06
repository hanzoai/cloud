package o11y

import (
	"sort"
	"strings"

	"github.com/hanzoai/cloud/manifest"
)

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

// knownServices answers "does a WORKLOAD answer at an address"; manifest.Apps
// answers "does an APP serve routes". They are different questions, and for most
// of the fleet only the second has a yes.
//
// Every routed app's requests are already on the plane: cloud's TracingMiddleware
// stamps `http.route` on every request span, and manifest.Apps is the table the
// host builds its router from — so the RED signal for all 119 apps has been in
// event.span the whole time. The only thing standing between it and a caller was
// this file's hand-maintained workload list, which had heard of 12 of them. An
// app that nobody probes still has no address and is still not probed (URL stays
// empty, status answers honest-empty); what it gains is its own logs and metrics,
// which never needed an address.
//
// This is why the fleet surface is DERIVED rather than a second list to maintain:
// a new plugin gets metrics/logs/status the moment it has a manifest row, which
// it must have to be routable at all (TestEveryPluginNameIsInTheManifest).
func fleetRoutes(name string) []string {
	ps := manifest.PrefixesFor(name)
	for _, p := range ps {
		if strings.TrimSuffix(p, "/") == apiRoot {
			// An app that declares the API ROOT is the router's FALLBACK — it is
			// handed whatever no other app claimed. That is a routing role, not a
			// product boundary, and "every request in the fleet minus everyone
			// else's" is not a RED series anyone should read as this product's
			// traffic. ai is the only such app (it serves the OpenAI-compatible
			// surface at top level so zen's c.Next() has somewhere to fall through
			// to), and it has no bounded route scope for the same reason zen has
			// none: zen routes nothing, ai routes everything.
			//
			// Measured, in case anyone is tempted to compute it anyway: expressing
			// it as "/v1 minus the 251 nested prefixes" costs 15.5s against six
			// hours of live spans, versus a ~4-5s baseline for a bounded app — three
			// times the cost, for a number that would have counted KMS's 161,705
			// requests as inference.
			return nil
		}
	}
	return ps
}

// apiRoot is the prefix every routed app is under, so declaring it claims
// everything and bounds nothing.
const apiRoot = "/v1"

// nestedPrefixes is the LONGEST-PREFIX-WINS half, and it is not optional.
//
// manifest.OwnerOf says it plainly: "Anything deciding policy from a bare
// HasPrefix scan will attribute those paths to the wrong app." A RED query is
// deciding policy. ai declares `/v1` — the catch-all it serves the
// OpenAI-compatible surface from — so a bare startsWith('/v1') scan hands ai every
// request in the fleet. Measured over six hours of live spans: /v1/kms/* alone is
// 161,705 of 434,765, and ai would have reported all of them as its own.
//
// So a product's spans are the ones under its prefixes MINUS the ones a strictly
// longer prefix belonging to a DIFFERENT app claims — the same rule the router
// used to route them in the first place. For nearly every app this list is empty
// (nothing nests inside /v1/kms) and the exclusion costs nothing.
func nestedPrefixes(own []string) []string {
	roots := make([]string, 0, len(own))
	for _, p := range own {
		if r := strings.TrimSuffix(p, "/"); r != "" {
			roots = append(roots, r)
		}
	}
	seen := map[string]bool{}
	var nested []string
	for _, a := range manifest.Apps {
		for _, q := range a.Prefixes {
			qr := strings.TrimSuffix(q, "/")
			if qr == "" || seen[qr] {
				continue
			}
			for _, pr := range roots {
				// STRICTLY longer and genuinely nested. An app's own prefixes are
				// never longer than themselves, so this cannot exclude the product
				// from its own subtree.
				if len(qr) > len(pr) && strings.HasPrefix(qr, pr+"/") {
					seen[qr] = true
					nested = append(nested, qr)
					break
				}
			}
		}
	}
	// Keep only the MINIMAL set. Excluding /v1/platform already excludes
	// /v1/platform/fleet, and carrying both puts two predicates in the query where
	// one decides. For ai this is the difference between 282 exclusions and a set
	// small enough to read.
	sort.Slice(nested, func(i, j int) bool { return len(nested[i]) < len(nested[j]) })
	var out []string
	for _, q := range nested {
		covered := false
		for _, kept := range out {
			if strings.HasPrefix(q, kept+"/") {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
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
	// Excludes is the set of longer prefixes owned by OTHER apps that nest inside
	// Routes. Without it a product that owns an ancestor path absorbs its
	// children's traffic — see nestedPrefixes.
	Excludes []string
	// Routes is the set of path prefixes whose request spans belong to this
	// product — the manifest's own answer where there is one.
	//
	// It is NOT `/v1/<id>` by convention, because for ~20 apps that convention is
	// simply wrong: plan serves /v1/plans, storage serves /v1/s3/buckets, account
	// serves /v1/orgs (and five more), knowledge serves /v1/kb/*. Reading the RED
	// series off `/v1/<name>` for those scopes the query to a subtree nobody
	// serves, which returns zero and looks exactly like a healthy idle service.
	Routes []string
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
	// Routes come from the PRODUCT id, not the aliased workload, because the alias
	// answers a different question. productAlias maps a console slug to the k8s
	// workload that ANSWERS (for probing and the prom `service` label); the routing
	// table is keyed by APP NAME. For analytics the two disagree in both
	// directions — the app is `analytics` and serves five prefixes
	// (/v1/analytics, /v1/errors, /v1/event, /v1/insights/*), while the workload is
	// `insights-capture` and is not a routed app at all. Looking the routes up by
	// workload silently lost four of those five prefixes.
	routes := fleetRoutes(p)
	if len(routes) == 0 {
		routes = fleetRoutes(workload)
	}
	// EITHER answer admits the product: a verified workload (it has an address, so
	// status can probe it) or a routed app (it has request spans, so metrics and
	// logs can read it). Requiring both would keep 107 routed apps dark for want of
	// a probe they never needed.
	if !knownServices[workload] && len(routes) == 0 {
		return service{}, false
	}
	if len(routes) == 0 {
		// A verified workload with no manifest row is not a routed app — it is a
		// bare k8s service (chat, studio, nats, vector). `/v1/<id>` is what this
		// file has always assumed for those, and it stays their answer; the
		// manifest simply has nothing better to say about them.
		routes = []string{"/v1/" + p}
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
		Routes:      routes,
		Excludes:    nestedPrefixes(routes),
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
