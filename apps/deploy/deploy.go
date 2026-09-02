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
// and it does so at the ArgoCD dashboard's own addresses, because the console that
// consumes it is that dashboard (dashboard.go):
//
//	GET  /v1/deploy/applications                    — the fleet list: name, declared
//	                                                  version, health, sync, per app.
//	GET  /v1/deploy/applications/{name}             — one app, with its resource list.
//	GET  /v1/deploy/applications/{name}/resource-tree — the owned-resource tree
//	                                                  (ownerRef edges) with per-node
//	                                                  health + sync.
//	POST /v1/deploy/applications/{name}/sync        — request an operator reconcile now.
//	POST /v1/deploy/applications/{name}/rollback    — the same request; rollback by
//	                                                  revision is the image-pin follow-on.
//	GET  /v1/deploy/gitops                          — the CD plane's own Applications.
//	POST /v1/deploy/reconcile                       — render the configured git source
//	                                                  and apply it, once (engine_mount.go).
//
// SECURITY — the projection READS are TENANT-SCOPED and the WRITES stay SuperAdmin-
// only, all fail-closed on the SAME identity boundary the rest of cloud trusts
// (resolveScope, scope.go — validated principal + injective namespace.Sanitize
// + the c.IsAdmin() SuperAdmin predicate): a SuperAdmin sees/mutates the whole fleet,
// a validated org member sees ONLY its own org's apps (hanzo.ai/org label), and the
// reconcile writes (sync/rollback) remain SuperAdmin-only. Secret objects are never
// surfaced (no node, no manifest) so the tree can never leak materialized env. The
// user-facing per-org PaaS is /v1/platform; this is the platform-operator console the
// admin dashboard consumes, now also serving a read-only per-org reflection.
//
// GitOps note (the follow-on client): today the CR is the desired-state source and a
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

	"github.com/hanzoai/authz"
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

// state is gitops's own data; shared deps live in the embedded cloud.Base.
type state struct {
	dyn       dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	clientset kubernetes.Interface
	initErr   string
	oauth     oauth // sign-in configuration (login.go)
}

// Mount wires /v1/deploy/* onto app. Every handler gates on c.IsAdmin() first.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "deploy",
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
		"brand", b.Brand, "env", b.Env, "iam", o.issuer, "client", o.clientID, "adminOrg", authz.AdminOrg)
	return st, nil
}

// The prose for the routes on this plane that are NOT typed ops. Everything else
// is one, and zipdoc lifts its doc comment into zipdoc_gen.go; these five stay raw
// handlers for the wire fact stated beside each registration — two sign-in legs
// whose success is a bodyless 302 with a Set-Cookie, a wildcard path zip and the
// document template differently, and two unbounded SSE streams. So there is no
// comment for anything to lift and the published document would carry an
// operationId and nothing else. Declared through the same registry Register uses,
// so a description renders only while the router actually serves the route.
func init() {
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
	openapi.Describe(dashPrefix+"/account/can-i/*", http.MethodGet,
		"Compatibility answer the console UI asks before enabling its buttons",
		"Always answers `yes`, whatever resource, action or subresource the path names. It "+
			"exists for the ArgoCD-compatible console, which asks this before enabling a control, "+
			"and it is NOT the authorization decision: nothing downstream consults it, and every "+
			"route that returns fleet data or mutates a CR carries its own gate. Reaching it at "+
			"all already requires SuperAdmin, so a caller who can read the `yes` is one for whom "+
			"it is true.")
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
// The first statement is bounce, which turns a REFUSED browser navigation into the
// sign-in redirect (scope.go) — one rule for the typed ops in dashboard.go and the
// raw handlers here alike. It is deploy's own presentation rule and nothing else:
// the request every typed op reads its principal off is parked on the context by
// whoever composes the app, so this surface installs no bridge of its own.
//
// It is installed FIRST: fiber runs middleware in registration order, so one
// installed after its leaves never runs.
//
// It goes on the ROOT rather than on a Group(dashPrefix) node, and cloud.Router
// confines it by PATH — scope.Use gates every request on the prefixes deploy
// declares (scope.go, manifest/apps.go). The group form put the middleware on a
// node of its own: each Group call makes a fresh node, so it stood beside — not
// above — the node dashboard.go creates for its leaves, and zip judges the node,
// not the path, so it refuses to compose middleware no route beneath it can reach.
// Deploy declares its subtree as an explicit list of /v1/deploy/<resource> paths
// rather than /v1/deploy itself, and that list covers every route registered here:
// health, login, callback, logout, reconcile, and the dashboard's settings,
// session/userinfo, version, account/can-i, applications, stream/applications,
// clusters, projects and gitops. What the change drops is /v1/deploy paths with no
// route, which answer 404 — and bounce reshapes a 403 and nothing else, so their
// answer is what it was.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// The bridge is deploy's OWN, installed on deploy's own router ahead of every
	// leaf. A typed op receives a context and nothing else, and the request its
	// principal is read off (cloud.Request, typed.go) is parked there by this and
	// only this — so an app that leaves it to whoever composes it resolves no
	// principal wherever that composer is absent, and every typed op here 403s a
	// valid caller. Production got it from cloud.App's root install and the package
	// harness had to install its own copy to compensate, which is a property held
	// in two places and true in neither by construction.
	app.Use(cloud.Bridge())
	app.Use(zip.H(bounce))

	// Liveness — public, probe-able without a JWT, and a TYPED op.
	//
	// It answers 503 carrying the SAME domain body as its 200, which used to be the
	// whole reason it could not be one: WithStatus took a single code and refused a
	// non-2xx, so the probe's facts had nowhere to live on a failure. WithStatus is
	// variadic now and the ANSWER states which of the declared statuses it is
	// (StatusCoder), so the probe declares {200, 503} and keeps its wire.
	//
	// It is a typed op DESPITE being unauthenticated, which is the right way round:
	// the route grants nothing and reports booleans only — never the raw client
	// error, which can disclose the apiserver address or an RBAC detail — so the
	// same answer is safe on every surface a typed op reaches.
	zip.Get(cloud.ZipApp(app), dashPrefix+"/health", ops{s: s}.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))
	// Sign-in — necessarily public: these three routes ARE how a browser gets an
	// authenticated principal for this host. They grant nothing themselves; the
	// session they mint is an IAM JWT the identity boundary re-verifies on every
	// later request, and a principal outside the admin org is refused a cookie.
	// See login.go.
	//
	// login and callback stay RAW, and the reason recorded here for a long time —
	// "WithStatus refuses a non-2xx" — is FALSE at the pinned zip: WithStatus is
	// variadic and takes any code in 100..599 (typed.go:154-168), and
	// WithResponseHeader + HeaderCoder (typed.go:230-258) declare a Location and a
	// Set-Cookie. What actually blocks them is the BODY.
	//
	// A typed op writes its declared headers only for a NON-NIL Out — a nil one
	// takes the early return that stamps the status and returns
	// (typed.go:543-551) — and the REST arm then ends in c.JSON(out)
	// (typed.go:567). So declaring Location at all forces a JSON body and
	// `Content-Type: application/json; charset=utf-8` onto a 302 that carries
	// NEITHER today: fiber's Redirect.To sets Location, sets the status and writes
	// nothing (fiber v3 redirect.go:328-335), which is what these two answer,
	// measured. Typing them would move the wire on the one surface a browser
	// follows blind.
	//
	// callback is blocked a SECOND way, and it is the harder one: it writes TWO
	// Set-Cookie headers on one response — clearing the single-use flow cookie
	// (login.go) and setting the session cookie — while HeaderCoder is a
	// map[string]string written with c.Set (typed.go:558), which REPLACES rather
	// than appends. One of the two cookies would simply not be sent.
	app.Get(loginPath, cloud.Handle(s, login))
	app.Get(callbackPath, cloud.Handle(s, callback))
	// Signing out is a TYPED op: POST, not GET, because it changes state and a
	// state-changing GET is reachable by a cross-site top-level navigation that a
	// SameSite=Lax cookie still rides. The Set-Cookie that ENDS the session is
	// declared here rather than written from inside the handler, which is what puts
	// the route's whole effect in the document instead of in a side channel. See
	// EndDeploySession in login.go.
	zip.Post(cloud.ZipApp(app), logoutPath, ops{s: s}.logout, zip.WithResponseHeader("Set-Cookie"))
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
// deployHealth is what the probe answers, at either status.
//
// The two booleans are POINTERS because their ABSENCE is a fact: a probe that
// could not reach the apiserver never learned whether the CRD is served, and
// reporting `crd: false` there would state something it does not know. nil is
// omitted, &false is present-and-false, and the map this replaced had exactly
// that distinction by leaving the key unset.
type deployHealth struct {
	// Service names the subsystem answering, so a probe response is attributable
	// when several are collected together.
	Service string `json:"service"`
	// Status is `ok` when this deployment can serve the delivery plane, and
	// `degraded` otherwise. It agrees with the HTTP status by construction — see
	// StatusCode.
	Status string `json:"status"`
	// K8s reports whether the Kubernetes API is reachable. Absent when the probe
	// did not get far enough to find out.
	K8s *bool `json:"k8s,omitempty"`
	// CRD reports whether the App custom resource is served and listable. Absent
	// when the apiserver was unreachable, because then it is unknown rather than
	// false.
	CRD *bool `json:"crd,omitempty"`
}

// StatusCode makes the ANSWER say which of the declared statuses it is, so an
// orchestrator reading only the code is told the truth and one reading the body
// gets the same fact.
func (h *deployHealth) StatusCode() int {
	if h.Status != "ok" {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}

// Health reports whether this deployment can observe the delivery plane.
//
// 200 only when the Kubernetes API answers AND the App custom resource is served;
// 503 with the same shape otherwise, naming which half failed. It reports BOOLEANS
// and never the underlying error, because the route is unauthenticated — liveness
// must be probe-able without a token — and a raw client error can disclose the
// apiserver address or an RBAC detail. That detail is logged server-side instead.
func (o ops) health(ctx context.Context, _ *cloud.Unit) (*deployHealth, error) {
	yes, no := true, false
	if o.s.State.dyn == nil {
		o.s.Log.Warn("deploy health: kubernetes client unavailable", "err", o.s.State.initErr)
		return &deployHealth{Service: "deploy", Status: "degraded", K8s: &no}, nil
	}
	if _, err := o.s.State.dyn.Resource(k8s.Apps).Namespace("hanzo").List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
		o.s.Log.Warn("deploy health: App CRD list failed", "err", err)
		return &deployHealth{Service: "deploy", Status: "degraded", K8s: &yes, CRD: &no}, nil
	}
	return &deployHealth{Service: "deploy", Status: "ok", K8s: &yes, CRD: &yes}, nil
}

// ready fails closed when no cluster client resolved (503 + the real reason).
func ready(s *cloud.Service[state]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "deploy: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

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
