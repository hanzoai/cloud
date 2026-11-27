// dashboard.go — the ArgoCD-UI-compatible projection API at /v1/deploy/*,
// fed the App-CR projection (projection.go). NO argocd api-server, NO
// repo-server, NO redis, NO stored Application/AppProject CRD — every response
// is synthesized from our operator App CRs. The FRONTEND is NOT here: the
// monochrome dashboard ships as the `hanzoai/spa`-based `cd-ui` App CR served at
// cd.hanzo.ai/ (base-href /); this plane is only the same-origin API it calls (no /api/, no inner /v1):
//
//	GET  /v1/deploy/settings          → AuthSettings (auth disabled; IAM gates at the edge)
//	GET  /v1/deploy/session/userinfo  → {loggedIn:true,...}
//	GET  /v1/deploy/version              → VersionMessage
//	GET  /v1/deploy/account/can-i/*   → {"value":"yes"}
//	GET  /v1/deploy/applications                          → ApplicationList (projected)
//	GET  /v1/deploy/applications/{name}                   → Application (projected)
//	GET  /v1/deploy/applications/{name}/resource-tree     → ApplicationTree
//	POST /v1/deploy/applications/{name}/sync              → sync the application, or roll it back
//
// SECURITY — TENANT-SCOPED reads, SuperAdmin-only writes, fail-closed (scope.go):
// the READ projections (applications list/detail/resource-tree, clusters, projects,
// stream) resolve the request's scope (resolveScope) — a SuperAdmin sees the whole
// fleet, a validated org member sees ONLY its own org's apps (hanzo.ai/org label,
// tenant-<org> namespace) and sites, anyone else 403s. The WRITE action (sync) and
// the argocd bootstrap (settings/version/can-i) stay SuperAdmin-only, save one
// write: a member may roll back its own org's site, which the projects app already
// lets it publish (sites.go, sync below) — the typed
// ops ask superAdminOf INSIDE the op, so the gate holds on every projection and
// not only on the REST route; can-i, the one raw handler left here, keeps the
// guard() middleware, which is all a raw handler can have. The
// argocd UI's own auth is disabled because IAM owns identity at the edge (the SPA is
// public static assets, the data is scoped). AppProject → IAM/Org (no argocd RBAC):
// projects are REFLECTED read-only from the IAM-owned (org,name) Project resource.

package deploy

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// dashPrefix is the API base the monochrome dashboard SPA calls. The FE itself
// is NOT served here — it ships as the `hanzoai/spa`-based `cd-ui` App CR served
// at cd.hanzo.ai/ (base-href /); this cloud plane is ONLY the IAM-gated
// projection API at cd.hanzo.ai/v1/deploy/*, same-origin with the SPA (no CORS).
const dashPrefix = "/v1/deploy"

// registerDashboardRoutes wires the ArgoCD-UI-compatible API surface (no FE —
// the SPA is a separate hanzoai/spa App). Called from routes() (deploy.go), which
// installs the sign-in bounce so it covers this prefix.
//
// Every READ here is a TYPED op declared on that group, so the op's path is the
// prefix composed with the leaf — the identity every projection keys on — and the
// doc comment on each handler reaches the OpenAPI description, the MCP tool and
// the generated SDK method from that one registration. The authorization runs
// INSIDE the op (scopeOf / superAdminOf, typed.go) rather than in middleware
// wrapped around the route, because middleware runs on the REST projection alone.
func registerDashboardRoutes(app cloud.Router, s *cloud.Service[state]) {
	// The group is assigned HERE, in the file that DECLARES the typed ops, because
	// that is what cmd/zipdoc reads: it resolves an op's prefix from a
	// `g := <router>.Group("/prefix")` in the SAME file, and a prefix it cannot
	// resolve is prose filed under the wrong path and silently dropped from both the
	// document and the MCP tool — so it refuses, naming the file, the line and this
	// fix. The group is a bare path prefix carrying NO middleware, which is what
	// keeps it composable: the leaves below register through THIS node, and
	// middleware belongs on the root gated by path (routes(), deploy.go), because a
	// second Group at the same prefix would be a fresh node — middleware there
	// stands beside these routes, not above them, and zip judges the node.
	g := app.Group(dashPrefix)
	o := ops{s: s}

	// Bootstrap (the SPA awaits settings + userinfo before first render).
	g.Get("/settings", o.settings)
	// userinfo is the ONE deliberately PUBLIC bootstrap route: it is how the SPA
	// asks "am I signed in?", and a 403 to that question is unanswerable — the SPA
	// is an XHR client, so the document bounce never fires for it and it dead-ends
	// with no way to reach sign-in. Anonymous callers get {loggedIn:false} and the
	// sign-in URL; nothing else. It discloses no identity, no cluster state, and no
	// configuration, and it is NOT a gate: every route that returns fleet data or
	// mutates a CR keeps its own.
	g.Get("/session/userinfo", o.userinfo)
	g.Get("/version", o.version)
	// can-i stays a RAW handler, and the reason is zip's path templating, not this
	// plane's: its route is a fiber WILDCARD (argocd asks about a
	// resource/action/subresource triple, which is several segments), and zip's
	// Template returns a pattern holding no ':' UNCHANGED (address.go:61-64), so a
	// typed op would publish "/v1/deploy/account/can-i/*" while cloud's translate
	// renders the LIVE route as "{wildcard1}" (openapi/openapi.go:807-823). The two
	// readings then name different paths and openapi.Fold refuses the whole
	// document (openapi/openapi.go:699-705, "has no live route") — so this app
	// would publish NOTHING, not merely lose one operation. Typable the day zip
	// templates a wildcard the way the document does, which is one function: ID()
	// already spells '*' as "wildcardN" (zip openapi.go:328-330) and Template does
	// not. Naming three params instead is not the remedy — the subresource argocd
	// asks about is routinely two segments.
	g.Raw(http.MethodGet, "/account/can-i/*", guard(s, cloud.Handle(s, dashCanI)))

	// Applications projection (read). TENANT-SCOPED: each op resolves the caller's
	// scope (scopeOf → resolveScope) and fails closed — a SuperAdmin sees the whole
	// fleet, a validated org member sees ONLY its own org's apps, anyone else 403s.
	g.Get("/applications", o.listApplications)
	g.Get("/applications/:name", o.getApplication)
	g.Get("/applications/:name/resource-tree", o.resourceTree)
	// Per-app detail projections the SPA's application view calls (detail.go). Same
	// tenant scope as getApplication: scopeOf + findNamespace, a cross-tenant name 404s.
	g.Get("/applications/:name/syncwindows", o.syncWindows)
	g.Get("/applications/:name/revisions/:revision/metadata", o.revisionMetadata)

	// The two SSE streams stay RAW handlers. Their response is an unbounded
	// text/event-stream written frame by frame for the life of the connection
	// (SendStreamWriter, stream.go/detail.go); a typed op answers with ONE marshalled
	// Out and zip has no vocabulary for a stream, so typing either would replace the
	// live watch with a single JSON object.
	g.Raw(http.MethodGet, "/stream/applications", cloud.Handle(s, dashStreamApps))
	g.Raw(http.MethodGet, "/stream/applications/:name/resource-tree", cloud.Handle(s, dashStreamResourceTree))

	// Destination clusters + AppProjects — the two lists the applications view
	// resolves alongside the fleet (Destination column + project filter). Tenant-scoped:
	// clusters count only the caller's apps; projects reflect the caller's IAM projects.
	g.Get("/clusters", o.clusters)
	g.Get("/projects", o.projects)

	// The CD plane's own state (gitops.go) — the git source it polls, the commit it
	// last applied, and its deploy history. Fleet infrastructure with no tenant
	// dimension, so SuperAdmin-only rather than scope-resolved.
	g.Get("/gitops", o.gitops)
	// What each service runs (live.go): its images, the commit each was built
	// from, the universe commit CD applied, and CD's verdicts. Scoped as gitops is.
	g.Get("/live", o.live)
	// Why a service is or is not live (release.go): its newest claimed versions,
	// what the pipeline's runs said of each, what was smoked, pinned and is live.
	g.Get("/release", o.release)

	// The one action: sync, and a sync to an earlier revision is the rollback —
	// there is no second address for it. The gate is INSIDE the typed op rather than
	// in route middleware: a typed op is also reached by POST /mcp and by the
	// by-name call plane, where no route middleware runs, and scopeOf additionally
	// refuses off the HTTP path, so a CLI LocalInvoke with no attested caller cannot
	// reach it either. Its body is optional and names only the revision.
	g.Post("/applications/:name/sync", o.sync)
}

// ── clusters + projects projection ───────────────────────────────────────────

// ListDeployClusters returns the argocd ClusterList of the destinations the
// caller's applications reconcile into: one entry per distinct destination
// server, carrying the count of applications reconciling into it. The in-cluster
// destination is always present, so an empty fleet still answers one cluster, and
// no cluster credential can appear — the projected type physically has no config
// field.
//
// It is TENANT-SCOPED and reads the SAME App CRs the applications list reads: a
// platform SuperAdmin counts the whole fleet, a validated org member counts only
// its own org's applications, anyone else is refused.
func (o ops) clusters(ctx context.Context, _ *cloud.Unit) (*argoClusterList, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	// Only the caller's own apps are counted (SuperAdmin: the whole fleet). The in-cluster
	// destination is always present (projectClusters), and no cluster credential can leak
	// (argoCluster has no config field) — so a tenant view is still credential-free.
	crs, err := sc.appCRs(o.s, ctx)
	if err != nil {
		return nil, err
	}
	list := projectClusters(crs)
	return &list, nil
}

// ListDeployProjects returns the argocd AppProjectList this console groups and
// filters applications by. Projects are owned by Hanzo IAM rather than by argocd,
// so they are REFLECTED read-only from the IAM project store and nothing is
// persisted here: a validated org member gets its own organization's projects and
// a platform SuperAdmin gets every organization's.
//
// A SuperAdmin whose IAM store is not reachable falls back to the real
// argoproj.io AppProject CRs when that CRD is served, and otherwise to one
// permissive synthesized project per distinct project name the App CRs declare.
// A project named "default" is always present, because that is what an App CR
// carrying no project label projects to.
func (o ops) projects(ctx context.Context, _ *cloud.Unit) (*argoProjectList, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	// IAM is the ONE source of truth for the (org,name) Project resource. Reflect it: a
	// normal org sees ONLY its own organization's projects, a SuperAdmin sees every org's.
	items := sc.iamProjects()
	if sc.superAdmin && len(items) == 0 {
		// Whole-fleet fallback when the embedded IAM store is unavailable/empty: preserve the
		// pre-tenant projection — real argocd AppProject CRs if that CRD is served (that list is
		// cluster-wide/unscoped, so it is a SuperAdmin-only path, NEVER a tenant's), else
		// synthesize from the fleet's distinct App-CR project names. Keeps the SuperAdmin view
		// populated even before IAM is reachable (e.g. in a unit test with no embedded store).
		if real, served := listAppProjects(o.s, ctx); served {
			return &argoProjectList{Metadata: argoListMeta{}, Items: real}, nil
		}
		crs, err := sc.appCRs(o.s, ctx)
		if err != nil {
			return nil, err
		}
		for _, name := range projectedProjectNames(crs) {
			items = append(items, synthProject(name))
		}
	}
	// "default" always resolves (every projected app's spec.project falls back to it), which
	// also holds the e2e invariant that the projects list contains 'default'.
	return &argoProjectList{Metadata: argoListMeta{}, Items: ensureDefault(items)}, nil
}

// listAppProjects lists real argoproj.io/v1alpha1 AppProject CRs cluster-wide. It
// returns (projected, true) ONLY when the CRD is served AND at least one project
// exists; any error (CRD absent — the norm here, or RBAC) or an empty set yields
// (nil, false) so the caller synthesizes. It never fails the request.
func listAppProjects(s *cloud.Service[state], ctx context.Context) ([]argoProject, bool) {
	list, err := s.State.dyn.Resource(k8s.CDAppProjects).List(ctx, metav1.ListOptions{})
	if err != nil || list == nil || len(list.Items) == 0 {
		return nil, false
	}
	out := make([]argoProject, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, projectAppProject(&list.Items[i]))
	}
	return out, true
}

// ── bootstrap ────────────────────────────────────────────────────────────────

// deployEmpty is the element of a list, and the referent of a pointer, this
// projection NEVER populates: argocd's Dex connectors, its UI plugins, its OIDC
// configuration. Naming them as an object with no properties says the true thing
// — the list is always [] and the pointer always null — instead of inventing a
// shape for entries that are never produced.
type deployEmpty struct{}

// consoleSettings is the argocd AuthSettings object the dashboard SPA reads at
// bootstrap. Every field is a CONSTANT of this projection, not configuration read
// from anywhere, and the fields are declared in the order encoding/json sorted
// the map this replaced — so the bytes on the wire are unchanged.
type consoleSettings struct {
	// AppsInAnyNamespaceEnabled is false: applications are projected from operator
	// App CRs in the platform namespaces, never declared in an arbitrary one.
	AppsInAnyNamespaceEnabled bool `json:"appsInAnyNamespaceEnabled"`
	// DexConfig carries no connectors. This console does not run Dex; its sign-in is
	// GET /v1/deploy/login, which is an IAM authorization-code round trip.
	DexConfig struct {
		// Connectors is always empty.
		Connectors []deployEmpty `json:"connectors"`
	} `json:"dexConfig"`
	// ExecEnabled is false: this plane serves no container terminal.
	ExecEnabled bool `json:"execEnabled"`
	// GoogleAnalytics carries no tracking id and anonymizes users — this console
	// reports no analytics.
	GoogleAnalytics struct {
		// AnonymizeUsers is true.
		AnonymizeUsers bool `json:"anonymizeUsers"`
		// TrackingID is always empty.
		TrackingID string `json:"trackingID"`
	} `json:"googleAnalytics"`
	// Help carries no chat link and no binary download URLs.
	Help struct {
		// BinaryUrls is always empty.
		BinaryUrls map[string]string `json:"binaryUrls"`
		// ChatText is always empty.
		ChatText string `json:"chatText"`
		// ChatUrl is always empty.
		ChatUrl string `json:"chatUrl"`
	} `json:"help"`
	// HydratorEnabled is false: there is no manifest hydrator on this plane.
	HydratorEnabled bool `json:"hydratorEnabled"`
	// KustomizeVersions is always empty: an App CR is an image pin, not a kustomize
	// build.
	KustomizeVersions []string `json:"kustomizeVersions"`
	// OidcConfig is always null. The SPA's own OIDC flow is deliberately not
	// configured — identity is owned by Hanzo IAM at the edge and minted for this
	// host by GET /v1/deploy/login.
	OidcConfig *deployEmpty `json:"oidcConfig"`
	// Plugins is always empty: this plane loads no argocd config-management plugins.
	Plugins []deployEmpty `json:"plugins"`
	// StatusBadgeEnabled is false: no badge endpoint is served.
	StatusBadgeEnabled bool `json:"statusBadgeEnabled"`
	// StatusBadgeRootUrl is always empty, for the same reason.
	StatusBadgeRootUrl string `json:"statusBadgeRootUrl"`
	// SyncWithReplaceAllowed is false: a sync here asks the operator to reconcile an
	// App CR, and never replaces an object.
	SyncWithReplaceAllowed bool `json:"syncWithReplaceAllowed"`
	// UiBannerContent is always empty: this console shows no banner.
	UiBannerContent string `json:"uiBannerContent"`
	// UiCssURL is always empty: no stylesheet is injected.
	UiCssURL string `json:"uiCssURL"`
	// Url is the console's public origin, https://cd.hanzo.ai.
	Url string `json:"url"`
	// UserLoginsDisabled is true: the SPA must not render its own username/password
	// form. Signing in goes through IAM, at GET /v1/deploy/login.
	UserLoginsDisabled bool `json:"userLoginsDisabled"`
}

// GetDeploySettings returns the argocd AuthSettings object the dashboard SPA
// awaits before its first render.
//
// Every value is a CONSTANT of this projection rather than configuration read
// from anywhere: the SPA's own login form is reported disabled and its OIDC
// config null because Hanzo IAM owns identity at the edge and this console's
// sign-in is GET /v1/deploy/login, and every argocd feature the projection does
// not implement — status badges, Dex connectors, config-management plugins,
// kustomize versions, the exec terminal, apps-in-any-namespace, the hydrator,
// sync-with-replace — is reported off. Platform SuperAdmin only.
func (o ops) settings(ctx context.Context, _ *cloud.Unit) (*consoleSettings, error) {
	if _, err := superAdminOf(ctx); err != nil {
		return nil, err
	}
	out := &consoleSettings{
		KustomizeVersions:  []string{},
		Plugins:            []deployEmpty{},
		Url:                "https://cd.hanzo.ai",
		UserLoginsDisabled: true,
	}
	out.DexConfig.Connectors = []deployEmpty{}
	out.GoogleAnalytics.AnonymizeUsers = true
	out.Help.BinaryUrls = map[string]string{}
	return out, nil
}

// sessionUser is the argocd GetUserInfoResponse this console answers with. It has
// two shapes and they do not overlap: an anonymous caller gets loggedIn plus
// loginUrl and nothing else, a signed-in one gets everything except loginUrl.
// Groups is a POINTER to a slice for exactly that reason — an empty slice must
// still be written as [] for a signed-in caller, and must be ABSENT for an
// anonymous one, which `omitempty` on a plain slice cannot express. Fields are
// declared in the order encoding/json sorted the maps this replaced, so the bytes
// on the wire are unchanged.
type sessionUser struct {
	// Groups is the caller's group list, always empty here: this console
	// authorizes on the platform SuperAdmin fact alone, not on argocd RBAC groups.
	// Absent for an anonymous caller.
	Groups *[]string `json:"groups,omitempty"`
	// Iss is the token issuer as the SPA expects to see it — the literal "argocd",
	// so the UI never triggers an SSO redirect of its own. Absent for an anonymous
	// caller.
	Iss string `json:"iss,omitempty"`
	// LoggedIn reports whether this browser holds a session this console accepts.
	LoggedIn bool `json:"loggedIn"`
	// LoginURL is where an anonymous caller signs in. Absent once signed in.
	LoginURL string `json:"loginUrl,omitempty"`
	// LogoutURL is where a signed-in caller ends the session. Absent when anonymous.
	LogoutURL string `json:"logoutUrl,omitempty"`
	// Username is the validated principal's user ID — the opaque gateway id, which
	// is what argocd's UI renders as the signed-in user here — or "admin" when the
	// principal carries none. Absent when anonymous.
	Username string `json:"username,omitempty"`
}

// GetDeploySession answers "is this browser signed in, and if not where does it
// sign in?" — the dashboard SPA's bootstrap question, and the only route on this
// plane that answers for an anonymous caller.
//
// The anonymous answer carries loggedIn:false and a URL and NOTHING else: no
// username, no org, no groups, no issuer, no hint about who the caller might be or
// what exists in the cluster. Answering it costs nothing (the caller already knows
// whether it holds a cookie) and withholding it costs the whole sign-in journey.
//
// The predicate is the platform SuperAdmin fact — the SAME one every other route
// here gates on, minted from a validated principal whose org is the reserved admin
// org — so a validated-but-not-SuperAdmin caller is reported as NOT signed in,
// which is the truth as this console defines it: they cannot use it.
func (o ops) userinfo(ctx context.Context, _ *cloud.Unit) (*sessionUser, error) {
	user, superAdmin := consoleUser(ctx)
	if !superAdmin {
		return &sessionUser{LoggedIn: false, LoginURL: loginPath}, nil
	}
	if user == "" {
		user = "admin"
	}
	groups := []string{}
	return &sessionUser{
		Groups:    &groups,
		Iss:       "argocd", // keep == argocd so the UI never triggers an SSO redirect
		LoggedIn:  true,
		LogoutURL: logoutPath,
		Username:  user,
	}, nil
}

// versionMessage is the argocd VersionMessage. Its keys are PascalCase because
// that is the wire shape the SPA reads, and its fields are declared in the order
// encoding/json sorted the map this replaced.
type versionMessage struct {
	// BuildDate is the time THIS RESPONSE was generated, in RFC 3339 — not a build
	// timestamp. There is no argocd build here to report one for.
	BuildDate string `json:"BuildDate"`
	// Compiler is the constant "gc" the SPA expects; it is not read from this
	// process.
	Compiler string `json:"Compiler"`
	// GoVersion is always empty.
	GoVersion string `json:"GoVersion"`
	// Platform is the constant "linux/amd64" the SPA expects; it is not this
	// process's own GOOS/GOARCH.
	Platform string `json:"Platform"`
	// Version names the projection, "hanzo-cd (projection)".
	Version string `json:"Version"`
}

// GetDeployVersion returns the argocd VersionMessage the dashboard SPA reads at
// bootstrap. There is no argocd binary behind this plane — it is a projection
// over operator App CRs — so the fields say so rather than describing a build:
// Version names the projection, BuildDate is the moment this response was
// generated, and Compiler/Platform/GoVersion are the constants the SPA tolerates
// rather than facts about this process. Platform SuperAdmin only.
func (o ops) version(ctx context.Context, _ *cloud.Unit) (*versionMessage, error) {
	if _, err := superAdminOf(ctx); err != nil {
		return nil, err
	}
	return &versionMessage{
		BuildDate: time.Now().UTC().Format(time.RFC3339),
		Compiler:  "gc",
		Platform:  "linux/amd64",
		Version:   "hanzo-cd (projection)",
	}, nil
}

func dashCanI(s *cloud.Service[state], c *zip.Ctx) error {
	// Every route is already SuperAdmin-gated; report yes so buttons enable.
	return c.JSON(http.StatusOK, map[string]any{"value": "yes"})
}

// ── applications projection ──────────────────────────────────────────────────

// ListDeployApplications returns the fleet as an argocd ApplicationList: one
// projected Application per operator App CR, carrying the image tag the CR
// DECLARES, the tag actually RUNNING in the cluster's Deployment, the reconciled
// health, and the sync verdict those two produce (declared == running ⇒ Synced,
// both known and different ⇒ OutOfSync, either unknown ⇒ Unknown).
//
// It is TENANT-SCOPED: a platform SuperAdmin reads every platform namespace, a
// validated org member reads only its own org's tenant namespace and only the App
// CRs labelled with its org, and anyone else is refused. A cross-tenant CR is
// never projected into an answer. The CD Applications the caller may see follow,
// then the sites of the caller's org (or the org a SuperAdmin acts in), each
// named site-<slug>; sites the projects app cannot answer for are left out.
func (o ops) listApplications(ctx context.Context, _ *cloud.Unit) (*argoAppList, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	list := argoAppList{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationList", Metadata: argoListMeta{}, Items: []argoApp{}}
	for _, ns := range sc.namespaces() {
		crs, err := listAppCRs(o.s, ctx, ns)
		if err != nil {
			return nil, k8sErr(o.s, "list", err)
		}
		running := runningVersions(o.s, ctx, ns)
		for i := range crs {
			if !sc.allows(&crs[i]) {
				continue // cross-tenant CR — never projected to this scope
			}
			list.Items = append(list.Items, projectApp(&crs[i], ns, running[crs[i].GetName()]))
		}
	}
	// The CD plane, filtered by the caller's scope (sc.allowsCD): a SuperAdmin sees
	// the whole fleet, and a validated org caller sees only CD Applications that
	// belong to its org.
	cd, err := o.cdApplications(ctx, sc)
	if err != nil {
		return nil, k8sErr(o.s, "list", err)
	}
	list.Items = append(list.Items, cd...)
	// The caller's org's sites, read from the projects app for the caller (sites.go).
	list.Items = append(list.Items, o.siteApps(ctx, sc)...)
	return &list, nil
}

// GetDeployApplication returns ONE projected argocd Application by name, with
// status.resources filled in from its reconciled resource tree — which is what
// makes it the detail view rather than a row of the list.
//
// A CD Application and a site (site-<slug>) also carry status.history: the
// revisions they were deployed at, which a sync names to roll back to.
//
// It is TENANT-SCOPED, and a name that belongs to another org is reported NOT
// FOUND rather than refused: a 403 would confirm the application exists, so the
// route would become a cross-tenant existence oracle. A name that is not a
// DNS-1123 label is a 400 before any cluster read.
func (o ops) getApplication(ctx context.Context, in *appRef) (*argoApp, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	t, err := o.resolve(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	if t.site != nil {
		app := projectSite(t.org, t.site)
		return &app, nil
	}
	if t.cd != nil {
		g := observeGitOpsApp(t.cd)
		app := projectCDApp(g, t.cd)
		app.Status.History = argoHistoryOf(g)
		return &app, nil
	}
	cr, ns := t.cr, t.ns
	running := runningVersions(o.s, ctx, ns)
	app := projectApp(cr, ns, running[name])
	// Detail view: populate status.resources from the reconciled tree.
	tree := projectTree(buildTree(o.s, ctx, ns, name, cr))
	for _, n := range tree.Nodes {
		app.Status.Resources = append(app.Status.Resources, argoResourceStatus{
			Group: n.Group, Version: n.Version, Kind: n.Kind, Namespace: n.Namespace,
			Name: n.Name, Status: app.Status.Sync.Status, Health: n.Health,
		})
	}
	return &app, nil
}

// GetDeployResourceTree returns one application's argocd ApplicationTree: the
// objects the operator reconciled from its App CR, reached by ownerRef — the
// Deployment and, under it, the ReplicaSet and Pods, plus the Service, Ingress,
// HorizontalPodAutoscaler, PodDisruptionBudget and ConfigMaps it owns — each node
// carrying its parent edges and its health.
//
// Secrets are DELIBERATELY not walked, so no materialized environment can ever
// appear in the tree. Tenant-scoped exactly like the application read: another
// org's name is not found, a malformed name is a 400.
func (o ops) resourceTree(ctx context.Context, in *appRef) (*argoTree, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	t, err := o.resolve(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	if t.site != nil {
		tree := siteTree()
		return &tree, nil
	}
	if t.cd != nil {
		tree := cdTree(t.cd)
		return &tree, nil
	}
	tree := projectTree(buildTree(o.s, ctx, t.ns, name, t.cr))
	return &tree, nil
}

// syncAnnotation is the App CR annotation a sync request stamps. The operator's
// watch observes the change and reconciles; the VALUE is only a timestamp, so two
// requests a second apart are two reconciles and two in the same second are one.
const syncAnnotation = "gitops.hanzo.ai/sync-requested-at"

// SyncDeployApplication syncs ONE application to what its source declares now
// or, given a revision from its own history, back to that revision: the rollback.
//
// A Hanzo CD Application (what the estate runs) gets the sync operation
// cd.hanzo.ai's Sync button requests: CD applies a commit to that one
// application, with its own sync options and no pruning. With no revision that is
// the commit CD last compared with git. A revision must be one CD deployed the
// application at before (status.history), not the one it runs, and the
// application must not sync itself, or CD would put git's head back on its next
// poll. A tenant's operator App CR gets a sync-requested timestamp the operator
// reconciles on its own clock; it keeps no history, so it has no revision to go
// back to. A site (site-<slug>) is synced only to a revision: one of the releases
// its live deployments were kept as (its status.history), which it then serves
// again — the projects app moves its serving pointer and purges the edge. Its next
// deployment serves that deployment, as always.
//
// WHO MAY. A platform SuperAdmin, any application. Below SuperAdmin, only a
// rollback of a site of the caller's own org, by a validated member, because the
// projects app already lets that member publish the site and activate its
// releases. A credential whose limit names the deploy kind must also name the
// write: deploy:rollback for a revision, deploy:sync for none (sdk/grant.go). A key
// limited to product:deploy and deploy:rollback therefore reads this console and
// rolls back its org's sites, and reaches nothing else. The gate is INSIDE the op
// rather than in middleware around the route: this op is also reached by POST
// /mcp and by the by-name call plane, neither of which runs route middleware.
//
// It answers the application re-projected; a CD operation is QUEUED, so poll the
// application for how it ends. An unknown name is a 404 (never a 403, which would
// confirm the application exists), a name that is not a DNS-1123 label is a 400,
// a sync or rollback refused for the application's state (one already queued or
// running, a revision not in its history or the one it runs, an application that
// syncs itself) is a 409, a site release retention has reclaimed is a 410, and no
// cluster client is a 503.
func (o ops) sync(ctx context.Context, in *syncRef) (*argoApp, error) {
	sc, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(o.s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	revision := strings.TrimSpace(in.Revision)
	if len(revision) > maxRevisionLen {
		return nil, zip.ErrBadRequest("revision is longer than any revision this console holds")
	}
	write := "sync"
	if revision != "" {
		write = "rollback"
	}
	// Below SuperAdmin the one write is a site's rollback, so any other name is
	// refused before it is looked up, exactly as it always was.
	if !sc.superAdmin && !strings.HasPrefix(name, siteName) {
		return nil, forbidden()
	}
	t, err := o.resolve(ctx, sc, name)
	if err != nil {
		return nil, err
	}
	if (!sc.superAdmin && t.site == nil) || !grants(ctx, write) {
		return nil, forbidden()
	}
	actor, _ := consoleUser(ctx)
	switch {
	case t.site != nil:
		if revision == "" {
			return nil, zip.ErrBadRequest(name + " is a site, which is synced only to one of the releases it was kept as; name a revision from its status.history")
		}
		return o.rollbackSite(ctx, t, revision)
	case t.cd != nil:
		return o.syncCD(ctx, t.cd, actor, revision)
	case revision != "":
		return nil, zip.ErrConflict(name + " is an App CR, which keeps no history to roll back to")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{syncAnnotation: now}}})
	if _, err := o.s.State.dyn.Resource(k8s.Apps).Namespace(t.ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return nil, k8sErr(o.s, "patch", err)
	}
	o.s.Log.Info("dashboard reconcile requested", "app", name, "namespace", t.ns, "actor", actor)
	app := projectApp(t.cr, t.ns, runningVersions(o.s, ctx, t.ns)[name])
	return &app, nil
}

// syncCD requests the sync of one CD Application, to revision or (empty) to the
// commit CD compared, through k8s.RequestSyncTo, the one implementation of the
// request the platform surface shares.
func (o ops) syncCD(ctx context.Context, cd *unstructured.Unstructured, actor, revision string) (*argoApp, error) {
	actor, org := consoleActor(ctx, actor)
	reason := "synced from cd.hanzo.ai"
	if revision != "" {
		reason = "rolled back from cd.hanzo.ai"
	}
	got, err := k8s.RequestSyncTo(ctx, o.s.State.dyn, cd, revision, actor, reason)
	switch {
	case errors.Is(err, k8s.ErrSyncRefused):
		return nil, zip.ErrConflict(err.Error())
	case apierrors.IsConflict(err):
		return nil, zip.ErrConflict(cd.GetName() + " changed while this sync was requested; read it again and sync")
	case err != nil:
		return nil, k8sErr(o.s, "patch", err)
	}
	o.s.Log.Info("cd sync requested", "application", cd.GetName(), "to", cmp.Or(revision, "compared"),
		"rollback", revision != "", "actor", actor, "org", org)
	g := observeGitOpsApp(got)
	app := projectCDApp(g, got)
	app.Status.History = argoHistoryOf(g)
	return &app, nil
}
