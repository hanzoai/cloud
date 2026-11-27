// Package ingress is your edge: automatic TLS certificates and hostname
// routing to any backend, changed live.
//
// It is cloud's embedded, runtime-configurable edge, controlled at /v1/ingress.
// It makes the ONE hanzoai/cloud binary able to BE the
// fleet edge: terminate TLS, run ACME (Let's Encrypt), and reverse-proxy by Host
// to upstreams — all configured LIVE over an API, with NO static config file
// (routes.yaml) and NO restart to change a route.
//
// # Role model — one binary, role = runtime config
//
// The same artifact runs in either role; the emphasis is a single env flag:
//
//   - app role (default, CLOUD_INGRESS_EDGE_ENABLED unset): the /v1/ingress
//     CONTROL plane is mounted (so config can be authored/inspected) but the edge
//     DATA plane never binds a listener. cloud is a pure application.
//
//   - edge role (CLOUD_INGRESS_EDGE_ENABLED=true): additionally the edge data
//     plane binds :80 (ACME HTTP-01 + HTTP router) and :443 (SNI TLS termination
//
//   - router). This instance is now a fleet edge — it routes to other cloud
//     instances / services / itself.
//
// The two planes are orthogonal:
//
//   - CONTROL plane = this file's /v1/ingress/* zip handlers (routes, services,
//     middlewares, tls, status). SuperAdmin-gated; per-tenant persistence in
//     SQLite; every mutation hot-reloads the engine.
//   - DATA plane = edge.go's net/http listeners + engine.go's atomic host table.
//     Built on github.com/vulcand/oxy/v2 (Traefik's proxy lineage) and
//     golang.org/x/crypto/acme/autocert.
//
// This is NOT "TLS in every app binary": TLS/ACME live behind the edge role,
// which a deployment selects for the ONE instance that is the edge. Every other
// instance runs app role with the listeners off.
//
// # Replacing standalone hanzoai/ingress
//
// A single-tenant / serve deploy that today fronts cloud with a standalone
// hanzoai/ingress can instead run its cloud in edge role: point DNS at that
// instance, POST its own host→backend routes and a TLS route, and the standalone
// ingress pod is gone — one binary is app + edge. The shared multi-tenant fleet
// edge can migrate the same way (a cloud-in-edge-role instance whose route table
// is the union of every tenant's routes, host-unique) — that migration is a PLAN,
// delivered but NOT applied here; the live routes.yaml is untouched.
//
// # Composition with /v1/gateway
//
// Ingress and gateway are orthogonal edge subsystems: ingress owns routing + TLS;
// gateway owns auth + rate-limit. They compose (ingress in front, gateway as a
// backend/middleware layer) without colliding — different concerns, different
// surfaces.
package ingress
