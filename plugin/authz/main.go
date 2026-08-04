package main

import (
	"fmt"
	"net/http"
	"os"

	"github.com/hanzoai/authz/serve"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The PROSE for authz's three routes, declared HERE for the same reason the Mount
// adapter below is here: authz is a leaf that must never import cloud, so it cannot
// reach openapi.Describe, and its handlers are untyped closures inside another
// module, so zipdoc has nothing to lift either. Both seams a subsystem normally uses
// are closed to it — and the alternative is what the document said until now, three
// operations published as an operationId and nothing else, which every generated SDK
// offers as a method it cannot explain and a spec-derived CLI as a command with no
// help text.
//
// Describe is additive metadata on routes that EXIST: a description whose route the
// router does not serve never renders, so this cannot invent an operation. If authz
// ever grows its own prose seam upstream, delete this init — do not keep both, or
// the two will drift and only one will be true.
func init() {
	openapi.Describe("/v1/authz/check", http.MethodPost,
		"Ask whether a subject may act on an object",
		"Answers one policy question — may this subject take this action on this object — "+
			"against the CALLER'S OWN org policy set, and answers it with a bare allow/deny.\n\n"+
			"The org comes from the gateway-minted X-Org-Id and picks the per-org enforcer, so a "+
			"decision is always rendered by that tenant's policies and never by another's. A "+
			"request carrying no org is refused rather than answered from a shared or default set: "+
			"collapsing tenants together is the one failure a policy engine must not have.\n\n"+
			"Body: {sub, obj, act}, all three required. The reply echoes them beside `allow` so a "+
			"cached or logged decision carries the question it answered.")
	openapi.Describe("/v1/authz/health", http.MethodGet,
		"Liveness of the policy engine",
		"Reports that the authz process is up. Unauthenticated by design and never org-scoped: "+
			"it answers while every tenant's enforcer is still cold, because a probe that needed a "+
			"tenant would fail for reasons that have nothing to do with the process being alive.")
	openapi.Describe("/v1/authz/readyz", http.MethodGet,
		"Readiness of the policy engine",
		"Reports that the authz process is ready to serve decisions. Unauthenticated and not "+
			"org-scoped, for the same reason health is: readiness is a property of this process, "+
			"not of any one tenant's policy set.")
}

// Standalone entry for the authz app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `authz openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "authz",
		OwnsHealth: true,
		Price: cloud.Free,
		// The adapter lives HERE, on cloud's side: authz is a leaf and must never
		// import cloud, so cloud's plugin contract bends to the leaf rather than the
		// leaf learning about Deps.
		App: func(app *zip.App, deps cloud.Deps) error { return serve.Mount(app, deps.Logger) },
	}}, []string{"authz"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
