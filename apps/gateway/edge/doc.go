// Package edge is the runtime-mutable store for the cloud edge ("gateway
// role") policy: the CORS allowlist, the pre-auth per-client-IP flood cap, and the
// authenticated per-org rate ceiling. It is a LEAF package (stdlib + the Hanzo
// SQLite driver only, no import of the root cloud package) so BOTH consumers can
// use it without an import cycle:
//
//   - the edge middleware (package cloud, middleware_edge.go / middleware_ratelimit.go)
//     reads the effective policy live, per request, so an operator's change takes
//     effect without a redeploy;
//   - the /v1/gateway HTTP subsystem (clients/gateway) serves GET/PUT over the
//     SAME store, IAM-scoped.
//
// SCOPES. There are two, keyed by org in one database: an encrypted SQLite file, or
// on hanzo-sql a schema of its own (its SQL is the dialect both engines share,
// sdk/dialect):
//
//   - PLATFORM policy — the row stored under the admin org (cfg.AdminOrg). It holds
//     the pre-auth edge knobs (CORS origins, per-IP cap + window) that have no
//     tenant at evaluation time (CORS preflight + the anonymous-flood cap run
//     BEFORE identity). Only a SuperAdmin may write it. It is layered over the
//     static boot defaults (env/flags), so an un-provisioned deployment behaves
//     exactly as the static config until an operator PUTs an override.
//   - PER-ORG policy — a tenant's own row, holding its self-service edge config:
//     OrgRPM (authenticated rate ceiling), CacheTTLSec + CachePaths (edge-cache
//     TTL, default and per-path), and Methods (accepted-method allowlist). An org
//     admin writes its own; a SuperAdmin may write any. An unset field inherits the
//     platform default, then the static default.
//
// Fail-soft: every resolver (Platform/OrgRPM/CacheTTL/Methods) returns the static/platform default
// on any store error, so a policy-store outage never takes the edge down. Writes
// fail loud (an unavailable store returns an error to the PUT handler).
package edge
