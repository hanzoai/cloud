// Package projects is where your sites live: create one, deploy a build, roll
// back to any release.
//
// It is the ONE org-scoped store of buildable/deployable sites, shared by every
// surface that shows a user's projects.
//
// Why it exists: hanzo.app (the builder) and console.hanzo.ai (the Projects
// module) must show the SAME projects for the same org. They do, because both
// call this one /v1/project surface through the gateway, which mints the
// org (X-Org-Id) from the validated IAM JWT (HIP-0111). There is no second
// copy of project state anywhere — this SQLite-backed store is the source of
// truth; the builder keeps only per-project working state (chat, draft files)
// in Hanzo Base.
//
// Surface (all org-scoped; see CONTRACT.md — the published shape console
// consumes):
//
//	POST   /v1/project                      create
//	GET    /v1/project                      list (org)
//	GET    /v1/project/:slug                get
//	PATCH  /v1/project/:slug                update
//	DELETE /v1/project/:slug                delete (+ purge S3 site)
//	POST   /v1/project/:slug/deploy         deploy (tar body | git json)
//	POST   /v1/project/:slug/purge          purge the edge cache-tag (no redeploy)
//	GET    /v1/project/edge                 the edge: provider, reach, cache policy
//	GET    /v1/project/:slug/deployments    deploy history
//	GET    /v1/project/:slug/deployments/:id one deployment
//	POST   /v1/project/:slug/deployments/:id/complete  CI completion hook
//
// Sites (the surface-agnostic deploy_site capability, shared with agents):
//
//	POST   /v1/project/sites                generate a responsive site from a brief + deploy
//	POST   /v1/project/sites/deploy         deploy a raw file manifest (the deploy_site tool)
//	GET    /v1/project/sites                list the org's live sites
//	GET    /v1/project/sites/:slug          one live site
//
// Releases (the server-side promote — see release.go):
//
//	POST   /v1/project/:slug/publish                      promote a build output + go live
//	POST   /v1/project/:slug/releases                     promote only (no flip)
//	GET    /v1/project/:slug/releases                     rollback menu, newest first
//	POST   /v1/project/:slug/releases/:release/activate   flip the pointer (go live / roll back)
//
// The browser half — GET /v1/project/tags, the public pk-keyed pixel config —
// is served from tags.go, here because this process owns the store it reads.
//
// Deploy pipeline: a deploy uploads the built static site to OUR S3
// (CLOUD_PROJECTS_BUCKET on s3.hanzo.ai) under "<org>/<slug>/", marks the
// bucket public-read, and records a live URL. The hanzoai/static container
// (the static-app image) serves the same bucket behind the gateway for a pretty
// host; GitHub export is an optional second step that never blocks going live.
package projects
