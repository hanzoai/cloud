// Package sites is your published site, live on the public web at
// <slug>.hanzo.app.
//
// It is the host-routed edge that turns `<slug>.hanzo.app` into the static site
// a user deployed to OUR S3.
//
// It is NOT a /v1 API. It owns the ROOT path space for requests whose Host is a
// site host (`<slug>.<apex>`, apex default hanzo.app). It is installed as the
// FIRST middleware in the compose root (serve.go), before identity/billing, so a
// public site GET never enters the authenticated API pipeline — a site is a
// public artifact, not a tenant API call.
//
// Tenant isolation is the whole point (this is RED-reviewed). The org and the S3
// prefix a request may read come ONLY from the store lookup keyed by the
// validated subdomain slug — NEVER from the request path, a client header, or the
// Host beyond the one validated label. One slug ⇒ exactly one `<org>/<slug>/` S3
// prefix, hard-bounded, and the object key is rooted-clean so no `..`/encoded
// traversal can escape that prefix into another project or org. See resolveKey +
// its exhaustive test.
//
// The resolver (slug → {org,bucket,prefix,status}) is the projects store,
// injected via SetResolver at mount. sites does NOT import projects (projects
// imports cloud, cloud imports sites — importing projects here would form a
// cycle); the store implements the tiny Resolver interface and registers itself.
package sites
