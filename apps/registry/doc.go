// Package registry is your container and package registry: push images, pull them
// back, see what you store.
//
// It is Hanzo Registry: the management plane over the platform's artifact
// registries — list projects, container images, tags and npm packages, and mint
// scoped pull tokens, on the unified /v1 plane.
//
// PRODUCT-REPO MODEL. The registries themselves are running products: the OCI
// registry at oci.hanzo.ai (github.com/hanzoai/registry — CNCF distribution,
// S3-backed, Hanzo IAM token auth) and the npm registry at pkg.hanzo.ai
// (hanzoai/pkg — verdaccio on S3, with hanzoai/forge's /v1/packages
// ecosystems beside it on the same host). This subsystem reimplements NONE of
// them: every op is a typed read of what those services genuinely answer today,
// plus one token mint through the SAME IAM realm the docker CLI uses. cloud
// adds IAM auth, the tenant boundary, and the unified surface (OpenAPI/MCP/
// CLI/SDK projection).
//
// CONTROL PLANE ONLY. The OCI wire — manifests, blobs, push, pull — stays on
// oci.hanzo.ai and is deliberately NOT proxied here: a registry data path
// through the API host would double-move every image byte and break the
// content-addressed client protocol. /v1/registry answers the questions AROUND
// the wire (what exists, who may pull it) and hands out the address of the wire
// itself.
//
// TENANT ISOLATION. The org is the VALIDATED principal's org (principal.Org —
// minted by the identity boundary from a verified credential), NEVER an In
// field. The registries are shared platform deployments, so the boundary is
// enforced HERE on the registries' own namespace conventions: an org's images
// are the catalog entries under `<org>/…` (the fleet's `<host>/<org>/<app>`
// push convention) and its packages are `<org>` and `@<org>/…` on the npm
// host. Reads outside the namespace are filtered out before the response
// exists; a minted token can only ever name `<org>/<image>` with the `pull`
// action.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. The
// catalog credential (REGISTRY_CLIENT_ID/REGISTRY_CLIENT_SECRET, an IAM
// application's service credentials, KMS-synced env) rides only Basic auth to
// the token realm; an upstream that refuses it surfaces 503 — a deployment
// fault, never a caller-auth bug. An unreachable upstream is 503.
package registry
