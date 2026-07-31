// Package deploy is Hanzo CD: see what each app is running, sync it, and roll
// back a bad release.
//
// It is the GitOps plane at /v1/deploy — observe the operator-managed fleet
// (applications, resource tree, per-object health, live diff, logs), and
// reconcile it (sync, rollback, git → cluster with prune-safe self-heal).
//
// Each operator hanzo.ai/v1 App CR IS a GitOps Application: the desired state
// declared for one workload, which the Hanzo operator reconciles into a
// Deployment + Service + Ingress (+ HPA/PDB/Pods). This plane OBSERVES that
// reconciliation the way a CD controller observes a synced Application —
//
//	GET  /v1/deploy/applications        — the fleet list: name, declared version,
//	                                      health, sync, per app.
//	GET  /v1/deploy/{name}/tree         — the owned-resource tree (ownerRef edges)
//	                                      with per-node health + sync.
//	GET  /v1/deploy/{name}/resource/{ref} — one node's live manifest + a
//	                                      desired-vs-live diff.
//	GET  /v1/deploy/{name}/logs         — the app's current pod logs.
//	POST /v1/deploy/{name}/rollback     — pin the CR image tag to a prior semver
//	                                      (the operator reconciles the rollout).
//	POST /v1/deploy/{name}/sync         — request an operator reconcile now.
//
// SECURITY — the projection READS are TENANT-SCOPED and the WRITES stay SuperAdmin-
// only, all fail-closed on the SAME identity boundary the rest of cloud trusts
// (resolveScope, scope.go — validated principal + injective provisioning.SanitizeOrg
// + the c.IsAdmin() SuperAdmin predicate): a SuperAdmin sees/mutates the whole fleet,
// a validated org member sees ONLY its own org's apps (hanzo.ai/org label), and the
// reconcile writes (sync/rollback) remain SuperAdmin-only. Secret objects are never
// surfaced (no node, no manifest) so the tree can never leak materialized env. The
// user-facing per-org PaaS is /v1/platform; this is the platform-operator console the
// admin dashboard consumes, now also serving a read-only per-org reflection.
//
// GitOps note (the follow-on seam): today the CR is the desired-state source and a
// rollback/rollout PATCHES it directly (P1's RegisterServiceReleaser), so deploys
// work now. The end-state is true GitOps on OUR native git — RegisterPushBuilder
// commits the CR image-tag change to the manifest repo on git.hanzo.ai
// (github.com/hanzoai/git) and this engine syncs that repo → cluster with
// self-heal. The desired-vs-live diff below is already structured for that: it
// reads a desired source that is "cluster last-applied" now and becomes the
// git.hanzo.ai manifest later, with no shape change. See deployDesiredTODO.
package deploy

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/hanzoai/cloud/apps/k8s"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// k8s.Apps is the operator App CR (apps.hanzo.ai) — the one workload kind this
// plane reads. Each App IS a GitOps Application: the desired state for one
// workload, which the operator reconciles into a Deployment + Service + Ingress
// (+ HPA/PDB/Pods). Group hanzo.ai disambiguates it from the core/v1 Service (a
// CHILD it reconciles), which is why a resource ref always carries its group.

// Static-plane sites are NOT App CRs — a site is a `staticFiles` Middleware (its
// S3 origin) + an IngressRoute (its host), served straight from S3 with zero pods.
// These two GVRs let the fleet list ALSO project each site as an Application row
// (role:"site"), so cd.hanzo.ai shows every service AND every site — the whole
// delivery surface, not just the pod-backed half. Group hanzo.ai/v1alpha1 is the
// operator's ingress CRD group (distinct from the upstream traefik.io mirror).
var (
	middlewaresGVR   = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1alpha1", Resource: "middlewares"}
	ingressRoutesGVR = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1alpha1", Resource: "ingressroutes"}
)

// childGVRs are the operator-owned workload objects the tree walks at depth 1
// (owned by the Service CR) and their descendants (ReplicaSet → Pod). Secrets are
// DELIBERATELY absent: the tree never surfaces materialized env. Order is the
// display order.
var (
	replicaSetsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}
	podsGVR        = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	coreSvcGVR     = schema.GroupVersionResource{Version: "v1", Resource: "services"}
	ingressGVR     = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}
	hpaGVR         = schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}
	pdbGVR         = schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}
	configMapsGVR  = schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
)

// kindGVR resolves a resource ref's (group, kind) to its GVR — the fixed, closed
// registry of kinds this plane reads. A ref for any kind NOT here is refused, so
// the resource endpoint can never be steered at an arbitrary cluster object.
// Keyed by "group/Kind" (group "" for the core API group).
var kindGVR = map[string]schema.GroupVersionResource{
	"hanzo.ai/App":                        k8s.Apps,
	"apps/Deployment":                     k8s.Deployments,
	"apps/ReplicaSet":                     replicaSetsGVR,
	"/Pod":                                podsGVR,
	"/Service":                            coreSvcGVR,
	"/ConfigMap":                          configMapsGVR,
	"networking.k8s.io/Ingress":           ingressGVR,
	"autoscaling/HorizontalPodAutoscaler": hpaGVR,
	"policy/PodDisruptionBudget":          pdbGVR,
}

// nsEnv maps each scanned platform namespace to its lifecycle env, mirroring
// clients/paas (main first). Only these namespaces are read — the plane never
// reaches beyond the platform tier.
var nsEnv = map[string]string{"hanzo": "main", "hanzo-testnet": "test", "hanzo-devnet": "dev"}

// scanOrder is the platform namespaces in stable env order (production first), so
// a bare app name resolves to main before test/dev.
func scanOrder() []string { return []string{"hanzo", "hanzo-testnet", "hanzo-devnet"} }

// appNameRE constrains the {name} path segment to a DNS-1123 label (every Service
// CR metadata.name satisfies this) — the injection guard for the CR a route reads.
var appNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

const userAgent = "hanzo-cloud-deploy"

// deployDesiredTODO documents the desired-state source seam: "last-applied" (the
// kubectl last-applied-configuration annotation on the live object) today; the
// git.hanzo.ai manifest repo once RegisterPushBuilder commits CR changes there.
// The diff shape does not change when the source flips.
const deployDesiredTODO = "last-applied"

// state is gitops's own data; shared deps live in the embedded cloud.Base.
type state struct {
	dyn       dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	clientset kubernetes.Interface
	initErr   string
	oauth     oauth // sign-in configuration (login.go)
}

// Mount wires /v1/deploy/* onto app. Every handler gates on c.IsAdmin() first.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "deploy",
		func(b cloud.Base) (state, error) { return build(b, newOAuth(deps)) }, routes)
}

// build resolves the in-process k8s clients (fail-closed: when no kubeconfig
// resolves the subsystem still mounts and every endpoint 503s honestly).
func build(b cloud.Base, o oauth) (state, error) {
	st := state{oauth: o}
	dyn, cs, err := newClients()
	if err != nil {
		st.initErr = err.Error()
		b.Log.Warn("kubernetes client unavailable; /v1/deploy endpoints will fail closed", "err", err)
	} else {
		st.dyn, st.clientset = dyn, cs
	}
	b.Log.Info("deploy control plane mounted", "prefix", "/v1/deploy", "k8s", st.dyn != nil,
		"brand", b.Brand, "env", b.Env, "iam", o.issuer, "client", o.clientID, "adminOrg", o.adminOrg)
	return st, nil
}

// The prose for the routes on this plane that cannot be typed ops. The read
// projections are typed and zipdoc lifts their doc comments into zipdoc_gen.go;
// these ten stay raw handlers for the reasons stated beside each registration — a
// probe whose 503 carries the same domain body as its 200, a sign-in whose success
// IS a 302 with a Set-Cookie, POSTs that read no body at all, a wildcard path zip
// and the document template differently, and two unbounded SSE streams. So there is
// no comment for anything to lift and the published document would carry an
// operationId and nothing else. Half of them mutate a live cluster or mint a
// session, so a caller reading only the document has to be told which gate stands
// in front. Declared through the same registry Register uses, so a description
// renders only while the router actually serves the route.
func init() {
	openapi.Describe(dashPrefix+"/health", http.MethodGet,
		"Whether this control plane can actually reach the cluster it deploys to",
		"Reports the plane's real reachability: 200 only when the Kubernetes API server "+
			"answers AND the App CRD is served, 503 with the same body shape otherwise, so a "+
			"caller reads the same `k8s` and `crd` booleans either way rather than parsing an "+
			"error envelope. It is a genuine dependency probe, not a process liveness ping — a "+
			"running plane with no cluster behind it reports degraded.\n\n"+
			"This is the ONE unauthenticated route that reports state, because liveness must be "+
			"probe-able without a JWT. It therefore discloses booleans only: the underlying "+
			"failure — the API server address, an RBAC refusal — is logged server-side and never "+
			"put on the wire.")
	openapi.Describe(loginPath, http.MethodGet,
		"Start the sign-in round trip for this console",
		"Redirects the browser to IAM's authorize endpoint, having minted a nonce and a PKCE "+
			"verifier into a short-lived, single-use flow cookie. The nonce comes back as `state` "+
			"and is what proves the code belongs to the round trip THIS browser started; the "+
			"verifier never appears in the address bar.\n\n"+
			"Necessarily public — this is how a browser gets a principal for this host in the "+
			"first place — and it grants nothing by itself. An optional `returnTo` names where to "+
			"land afterwards and is run through the open-redirect guard, so only a same-host path "+
			"survives. A deployment with no sign-in configured answers 503 rather than "+
			"redirecting nowhere.")
	openapi.Describe(callbackPath, http.MethodGet,
		"Finish the sign-in round trip and mint the console session",
		"Completes the redirect from IAM: it validates `state` against the single-use flow "+
			"cookie in constant time, redeems the authorization code with the PKCE verifier, and "+
			"then VERIFIES the resulting token exactly as this deployment's identity boundary "+
			"will on every later request — so a token that would be refused next request fails "+
			"here with the real reason instead of producing a sign-in loop. On success it sets "+
			"the session cookie, bounded by the token's own expiry, and redirects to the "+
			"validated return path.\n\n"+
			"It fails closed, and closes on the ADMIN ORG: a principal whose verified owner claim "+
			"is not the reserved admin org is told plainly that it lacks the role (403) and no "+
			"cookie is minted for it. That check is not the authorization decision — every gated "+
			"route re-derives SuperAdmin from the verified JWT — it exists so nobody is handed a "+
			"session that silently 403s everything. No flow in progress, or a mismatched `state`, "+
			"is a 400; a refused or unexchangeable code is a 401.")
	openapi.Describe(logoutPath, http.MethodPost,
		"End the console session on this host",
		"Clears this console's session cookie and answers the signed-out state with the sign-in "+
			"URL to start again. IAM's own session is untouched — this ends the console session "+
			"only, so signing back in may not prompt for credentials.\n\n"+
			"It is a POST because it changes state. As a GET it was reachable by a cross-site "+
			"top-level navigation, which a SameSite=Lax cookie still rides, so any page could "+
			"sign a SuperAdmin out; a POST is not carried cross-site by that cookie.")
	openapi.Describe(dashPrefix+"/reconcile", http.MethodPost,
		"Render the configured git source and apply it to the cluster, once",
		"Runs one full GitOps sync through the embedded engine — render the configured repo, "+
			"ref and path, then three-way server-side apply with scoped prune — and answers the "+
			"revision it applied, the source it came from, the declared/synced/pruned/failed "+
			"counts and a per-resource result. This is the WRITE half of the plane: it mutates "+
			"live cluster objects and, with prune enabled, deletes objects the source no longer "+
			"declares.\n\n"+
			"SuperAdmin-only and fail-closed — a non-SuperAdmin is refused before any cluster "+
			"object is read or touched. The git source is read AS THE CALLER, so the source plane "+
			"scopes the answer itself rather than trusting this one to have scoped it. It reads "+
			"no request body; the source is configuration, not a parameter. A deployment with the "+
			"engine switched off, or with no usable cluster config, answers 503; a failure to "+
			"start, render or sync is a 502.")
	openapi.Describe(dashPrefix+"/account/can-i/*", http.MethodGet,
		"Compatibility answer the console UI asks before enabling its buttons",
		"Always answers `yes`, whatever resource, action or subresource the path names. It "+
			"exists for the ArgoCD-compatible console, which asks this before enabling a control, "+
			"and it is NOT the authorization decision: nothing downstream consults it, and every "+
			"route that returns fleet data or mutates a CR carries its own gate. Reaching it at "+
			"all already requires SuperAdmin, so a caller who can read the `yes` is one for whom "+
			"it is true.")
	openapi.Describe(dashPrefix+"/applications/:name/sync", http.MethodPost,
		"Ask the operator to reconcile one application now",
		"Requests an immediate reconcile of one application by stamping a sync-requested "+
			"timestamp onto its App CR, which the operator's watch observes, and answers the "+
			"application re-projected. It ASKS, it does not apply: the operator performs the "+
			"reconcile on its own clock, so a 200 means the request landed, not that the rollout "+
			"finished — the returned row's running version still lags until it does. The CR is "+
			"the desired source today, so this is a nudge; when git becomes the source the same "+
			"address becomes apply-from-git.\n\n"+
			"SuperAdmin-only and fail-closed — a non-SuperAdmin is refused before any cluster "+
			"object is read or patched, and the write surface stays admin-only while the tenant "+
			"surface is read-only reflection. It reads no request body. An unknown application "+
			"name is a 404; no cluster client configured is a 503.")
	openapi.Describe(dashPrefix+"/applications/:name/rollback", http.MethodPost,
		"The console's rollback control — today it requests a reconcile, nothing more",
		"Performs exactly what the sync action performs: it stamps the sync-requested "+
			"timestamp onto the application's App CR and answers the application re-projected. "+
			"It does NOT select, pin or revert to a prior image tag, and that is the one thing to "+
			"know before wiring anything to it — the name is the console's, the behaviour is the "+
			"sync. Pinning a previous release rides the release seam, which this address does not "+
			"call yet.\n\n"+
			"SuperAdmin-only and fail-closed, reading no request body, with an unknown application "+
			"name a 404 and no cluster client a 503 — the same gate and the same failures as the "+
			"sync it shares a handler with.")
	openapi.Describe(dashPrefix+"/stream/applications", http.MethodGet,
		"Live application fleet updates as Server-Sent Events",
		"Holds the connection open as text/event-stream and pushes one watch event per "+
			"application change. It opens with an `ADDED` frame for every application currently "+
			"present — the same projection the applications list serves, so a client renders a "+
			"complete fleet from the stream alone — and then forwards `ADDED`, `MODIFIED` and "+
			"`DELETED` as they happen, with a keep-alive every 25 seconds that is also how a "+
			"vanished client is noticed and its watch torn down.\n\n"+
			"Read-only and TENANT-SCOPED, fail-closed: a platform SuperAdmin streams the whole "+
			"fleet, a validated org member streams only its own org's applications, anyone else "+
			"gets 403 and no stream. No cluster client configured is 503. If the deployment is "+
			"not granted the watch verb the stream degrades to keep-alives only — the initial "+
			"state still renders, it simply stops updating — rather than failing the connection.")
	openapi.Describe(dashPrefix+"/stream/applications/:name/resource-tree", http.MethodGet,
		"Live resource tree for one application, as Server-Sent Events",
		"Holds the connection open as text/event-stream and pushes the application's whole "+
			"resource tree — its live child objects and each one's derived health — once "+
			"immediately and again on every keep-alive tick, so a client always has a current "+
			"picture without polling. The refresh IS the keep-alive: it is a cheap rebuild rather "+
			"than a watch, so there is no multi-resource watch to leak.\n\n"+
			"TENANT-SCOPED and fail-closed BEFORE the stream opens, which is the rule that "+
			"matters: the caller's scope and the application's namespace are resolved first, so "+
			"an unvalidated caller gets a plain 403 and an application belonging to another "+
			"tenant gets a plain 404 — never an opened stream that emits nothing. A SuperAdmin "+
			"reaches the whole fleet, an org member only its own org's applications. No cluster "+
			"client configured is 503.")
}

// routes registers the /v1/deploy/* surface. Every observing/mutating route is
// SuperAdmin-gated; the health probe is public (real k8s reachability).
//
// The first statement is the plane's MIDDLEWARE, hung on the /v1/deploy prefix so
// it reaches every route below — the typed ops in dashboard.go included, which
// register their own leaves on the same prefix. It carries the two facts every
// route beneath it needs:
//
//   - cloud.Bridge, which parks the request on the context so a TYPED op — which
//     receives only a context — can still reach the validated principal its
//     scope is derived from (typed.go). Serve installs the same bridge for the
//     whole binary; nesting is harmless (the inner one is what the handler sees)
//     and this keeps the ops scoped wherever they are mounted, including a test
//     app that never calls Serve.
//   - bounce, which turns a REFUSED browser navigation into the sign-in redirect
//     (scope.go). It is one rule for the typed ops and the raw handlers alike.
//
// It is installed FIRST: fiber runs middleware in registration order, so one
// installed after its leaves never runs.
func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Group(dashPrefix).Use(cloud.Bridge(), bounce)

	// Liveness — public (probe-able without a JWT). It stays a RAW handler because
	// it answers 503 carrying the SAME domain body as its 200 (status + the k8s and
	// crd booleans), and a typed op's only non-2xx is a returned error, which zip
	// renders as the flat HTTPError {status,code,error} — there is nowhere in that
	// shape for the probe's facts. zip.WithStatus refuses a non-2xx by design
	// (typed.go:112), so this is the multi-status gap, not an oversight.
	app.Get(dashPrefix+"/health", cloud.Handle(s, health))
	// Sign-in — necessarily public: these three routes ARE how a browser gets an
	// authenticated principal for this host. They grant nothing themselves; the
	// session they mint is an IAM JWT the identity boundary re-verifies on every
	// later request, and a principal outside the admin org is refused a cookie.
	// See login.go.
	//
	// login and callback stay RAW because their success IS a 302 with a Set-Cookie:
	// a typed op answers 200/204 or a 2xx it DECLARED, and redirecting from inside
	// one does not escape that — zip stamps cmp.Or(op.Status, 204) over the 302 for
	// a nil Out (typed.go:305-309). logout stays raw for the body-decode reason the
	// other POSTs do (registerDashboardRoutes).
	app.Get(loginPath, cloud.Handle(s, login))
	app.Get(callbackPath, cloud.Handle(s, callback))
	// POST, not GET: signing out changes state, and a state-changing GET is
	// reachable by a cross-site top-level navigation that a SameSite=Lax cookie
	// still rides. See logout in login.go.
	app.Post(logoutPath, cloud.Handle(s, logout))
	// Engine (write) reconcile — the embedded gitops-engine that replaces
	// universe-crs. Gated by DEPLOY_ENGINE_ENABLED; see engine_mount.go.
	registerEngineRoutes(app, s)
	// The deploy API at /v1/deploy/<resource> (no /api/ prefix, no inner /v1) —
	// the App-CR projection the monochrome dashboard SPA (cd-ui) consumes. This
	// IS the deploy API; the FE is the separate hanzoai/spa cd-ui App.
	registerDashboardRoutes(app, s)
}

// guard wraps a handler with the SuperAdmin gate (fail-closed: a non-SuperAdmin is
// refused 403 before any cluster object is read or mutated), matching clients/paas.
//
// The gate is principal.IsSuperAdmin and nothing else, on the SanitizeIdentity
// -minted header no client can forge. It does NOT take the platform's
// cloud.Guard(cloud.Super), which additionally requires principal.Validated: this
// console gates SuperAdmin on that ONE fact in lockstep with resolveScope
// (scope.go), which the tenant-scoped routes that bypass this guard resolve
// through — X-User-IsAdmin is minted only for a JWT-verified SuperAdmin and never
// restored from client input, so it already implies a validated principal, and
// adding the conjunct here alone would give the console two admin rules.
//
// The SHAPE of the refusal is not decided here: the gate states the DECISION
// (forbidden), and the one middleware every /v1/deploy route passes through
// (bounce, scope.go) is what sends a browser NAVIGATION to the sign-in page
// instead of handing it a 403 it cannot act on. Splitting the two is what let the
// typed ops beside these raw handlers keep the identical wire — see scope.go.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !principal.IsSuperAdmin(c) {
			return forbidden() // the ONE fail-closed refusal
		}
		return h(c)
	}
}

// currentPath is the path+query to return to after signing in. It is run through
// the same open-redirect guard as a caller-supplied returnTo — the value is
// server-derived, but there is exactly ONE rule for what a return path may be.
func currentPath(c *zip.Ctx) string {
	p := c.Path()
	if q := string(c.Fiber().Request().URI().QueryString()); q != "" {
		p += "?" + q
	}
	return safeReturn(p)
}

// health is a REAL probe: the API server is reachable AND the App CRD is served.
// 200 only when both hold; 503 + the real reason otherwise. Not admin-gated —
// liveness must be probe-able without a JWT.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	// This route is UNAUTHENTICATED (liveness must be probe-able without a JWT),
	// so it reports booleans only — never the raw k8s error string, which can
	// disclose the apiserver address / RBAC detail. The detail is logged
	// server-side (RED INFO-1).
	res := map[string]any{"service": "deploy", "status": "ok"}
	if s.State.dyn == nil {
		s.Log.Warn("deploy health: kubernetes client unavailable", "err", s.State.initErr)
		res["status"], res["k8s"] = "degraded", false
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	if _, err := s.State.dyn.Resource(k8s.Apps).Namespace("hanzo").List(c.Context(), metav1.ListOptions{Limit: 1}); err != nil {
		s.Log.Warn("deploy health: App CRD list failed", "err", err)
		res["status"], res["k8s"], res["crd"] = "degraded", true, false
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["k8s"], res["crd"] = true, true
	return c.JSON(http.StatusOK, res)
}

// ready fails closed when no cluster client resolved (503 + the real reason).
func ready(s *cloud.Service[state]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "deploy: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

// reqName reads and normalizes the {name} path segment.
func reqName(c *zip.Ctx) string { return regexpLower(c.Param("name")) }

func regexpLower(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			continue
		}
		out = append(out, b)
	}
	return string(out)
}

// resolveNamespace finds the platform namespace an App CR lives in, scanning in
// env order (main first). Returns a clean 404 when found in none.
func resolveNamespace(s *cloud.Service[state], c *zip.Ctx, name string) (string, error) {
	for _, ns := range scanOrder() {
		if _, _, err := getAppCR(s, c.Context(), ns, name); err == nil {
			return ns, nil
		} else if !apierrors.IsNotFound(err) {
			return "", k8sErr(s, "get", err)
		}
	}
	return "", zip.ErrNotFound("application " + name + " not found in the platform namespaces")
}

// getAppCR gets an App CR by name from ns. Returns the object and its GVR so a
// mutation (sync/rollback) patches the App CR it read. A miss is an IsNotFound
// error.
func getAppCR(s *cloud.Service[state], ctx context.Context, ns, name string) (*unstructured.Unstructured, schema.GroupVersionResource, error) {
	obj, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, schema.GroupVersionResource{}, err
	}
	return obj, k8s.Apps, nil
}

// listAppCRs lists every App CR in ns.
func listAppCRs(s *cloud.Service[state], ctx context.Context, ns string) ([]unstructured.Unstructured, error) {
	list, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return list.Items, nil
}

// k8sErr maps a raw API error to an honest gateway error, naming the missing RBAC
// so an operator knows exactly what to grant the cloud service account.
func k8sErr(s *cloud.Service[state], op string, err error) error {
	s.Log.Error("k8s op failed", "op", op, "err", err)
	if apierrors.IsForbidden(err) {
		return zip.Errorf(http.StatusBadGateway,
			"%s: kubernetes RBAC denied (cloud service account needs %s across the platform namespaces): %v", op, op, err)
	}
	return zip.Errorf(http.StatusBadGateway, "%s failed: %v", op, err)
}

// newClients builds the dynamic + typed clients from the in-cluster service
// account, falling back to KUBECONFIG for local/dev — identical construction to
// clients/paas + clients/platform.
func newClients() (dynamic.Interface, kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	}
	cfg.UserAgent = userAgent
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("dynamic client: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("clientset: %w", err)
	}
	return dyn, cs, nil
}
