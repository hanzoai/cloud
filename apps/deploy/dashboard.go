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
//	POST /v1/deploy/applications/{name}/{sync,rollback}   → request App-CR reconcile
//
// SECURITY — TENANT-SCOPED reads, SuperAdmin-only writes, fail-closed (scope.go):
// the READ projections (applications list/detail/resource-tree, clusters, projects,
// stream) resolve the request's scope (resolveScope) — a SuperAdmin sees the whole
// fleet, a validated org member sees ONLY its own org's apps (hanzo.ai/org label,
// tenant-<org> namespace), anyone else 403s. The WRITE actions (sync/rollback) and
// the argocd bootstrap (settings/version/can-i) stay SuperAdmin-only (guard). The
// argocd UI's own auth is disabled because IAM owns identity at the edge (the SPA is
// public static assets, the data is scoped). AppProject → IAM/Org (no argocd RBAC):
// projects are REFLECTED read-only from the IAM-owned (org,name) Project resource.
package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

// dashPrefix is the API base the monochrome dashboard SPA calls. The FE itself
// is NOT served here — it ships as the `hanzoai/spa`-based `cd-ui` App CR served
// at cd.hanzo.ai/ (base-href /); this cloud plane is ONLY the IAM-gated
// projection API at cd.hanzo.ai/v1/deploy/*, same-origin with the SPA (no CORS).
const dashPrefix = "/v1/deploy"

// registerDashboardRoutes wires the ArgoCD-UI-compatible API surface (no FE —
// the SPA is a separate hanzoai/spa App). Called from routes() (deploy.go).
func registerDashboardRoutes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	// The two gates, as the middleware a typed op is declared through: `su` is the
	// SuperAdmin gate guard() applies, `tenant` the scope gate every read resolves.
	// Both refuse BEFORE the op runs, which is what lets a refusal still be a 302
	// to sign-in (see scope.go).
	su, tenant := z.With(admitted), z.With(scoped)
	// Bootstrap (the SPA awaits settings + userinfo before first render).
	zip.Get(su, dashPrefix+"/settings", o.settings)
	// userinfo is the ONE deliberately PUBLIC bootstrap route: it is how the SPA
	// asks "am I signed in?", and a 403 to that question is unanswerable — the SPA
	// is an XHR client, so the document bounce in guard() never fires for it and it
	// dead-ends with no way to reach sign-in. Anonymous callers get
	// {loggedIn:false} and the sign-in URL; nothing else. It discloses no identity,
	// no cluster state, and no configuration, and it is NOT a gate: every route
	// that returns fleet data or mutates a CR stays guard()ed.
	zip.Get(z, dashPrefix+"/session/userinfo", o.userInfo)
	zip.Get(su, dashPrefix+"/version", o.version)
	// UNTYPED: the path ends in a WILDCARD (/account/can-i/<resource>/<action>/<sub>).
	// A typed op's path may carry no wildcard — the two path translations disagree on
	// it and the whole app's document would fail to project.
	app.Get(dashPrefix+"/account/can-i/*", guard(s, cloud.Handle(s, dashCanI)))

	// Applications projection (read). TENANT-SCOPED, not blanket-guard()ed: each handler
	// resolves the request's scope (resolveScope) and fails closed — a SuperAdmin sees the
	// whole fleet, a validated org member sees ONLY its own org's apps, anyone else 403s.
	zip.Get(tenant, dashPrefix+"/applications", o.applications)
	zip.Get(tenant, dashPrefix+"/applications/:name", o.application)
	zip.Get(tenant, dashPrefix+"/applications/:name/resource-tree", o.resourceTree)
	// Per-app detail projections the SPA's application view calls (detail.go). Same tenant
	// scope as dashApp: resolveScope + findNamespace, a cross-tenant name 404s.
	zip.Get(tenant, dashPrefix+"/applications/:name/syncwindows", o.syncWindows)
	zip.Get(tenant, dashPrefix+"/applications/:name/revisions/:revision/metadata", o.revisionMetadata)
	// Applications watch (Server-Sent Events) — the live stream the applications
	// view opens; see stream.go. Same tenant scope as the list.
	// UNTYPED (both streams): the response is an open Server-Sent-Events body, not a
	// JSON value — a typed op answers exactly one document and cannot express a stream.
	app.Get(dashPrefix+"/stream/applications", cloud.Handle(s, dashStreamApps))
	// Per-app live resource-tree stream (detail.go) — the detail view's tree watch, same
	// tenant scope: the scope gate runs before any SSE frame is emitted.
	app.Get(dashPrefix+"/stream/applications/:name/resource-tree", cloud.Handle(s, dashStreamResourceTree))

	// Destination clusters + AppProjects — the two lists the applications view
	// resolves alongside the fleet (Destination column + project filter). Tenant-scoped:
	// clusters count only the caller's apps; projects reflect the caller's IAM projects.
	zip.Get(tenant, dashPrefix+"/clusters", o.clusters)
	zip.Get(tenant, dashPrefix+"/projects", o.projects)

	// The CD plane's own state (gitops.go) — the git source it polls, the commit it
	// last applied, and its deploy history. Fleet infrastructure with no tenant
	// dimension, so SuperAdmin-only rather than scope-resolved.
	zip.Get(su, dashPrefix+"/gitops", o.gitops)

	// Actions → App-CR reconcile ops. STILL SuperAdmin-only (guard): write-back to the
	// fleet is a follow-on; this plane's tenant surface is read-only reflection for now.
	zip.Post(su, dashPrefix+"/applications/:name/sync", o.sync)
	zip.Post(su, dashPrefix+"/applications/:name/rollback", o.rollback)
}

// ops binds the service to deploy's typed handlers: a typed handler takes only a
// context and its decoded In, so the service arrives as a RECEIVER.
type ops struct{ s *cloud.Service[state] }

// appRef addresses one projected Application.
type appRef struct {
	// Name is the application name from the path; a DNS-1123 label.
	Name string `json:"name"`
}

// revisionRef addresses one revision of one Application.
type revisionRef struct {
	// Name is the application name from the path; a DNS-1123 label.
	Name string `json:"name"`
	// Revision is the revision from the path; "" or "HEAD" resolves to the CR's tag.
	Revision string `json:"revision"`
}

// ── clusters + projects projection ───────────────────────────────────────────

// clusters lists the destination clusters the caller's applications reconcile into.
// The in-cluster destination is always present, only the caller's own apps are
// counted (a SuperAdmin counts the fleet), and no cluster credential can leak —
// the projected cluster carries no config field at all.
func (o ops) clusters(ctx context.Context, _ *struct{}) (*argoClusterList, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	// Only the caller's own apps are counted (SuperAdmin: the whole fleet). The in-cluster
	// destination is always present (projectClusters), and no cluster credential can leak
	// (argoCluster has no config field) — so a tenant view is still credential-free.
	crs, err := sc.appCRs(s, ctx)
	if err != nil {
		return nil, err
	}
	out := projectClusters(crs)
	return &out, nil
}

// projects lists the caller's IAM projects, reflected read-only as AppProjects.
// IAM is the one source of truth for the (org, name) Project resource: a normal
// org sees only its own organization's projects, a SuperAdmin every org's, and
// "default" always resolves. When the embedded IAM store is unreachable a
// SuperAdmin still sees real argoproj.io/v1alpha1 AppProject CRs if that CRD is
// served, else one permissive project per distinct App-CR project name.
func (o ops) projects(ctx context.Context, _ *struct{}) (*argoProjectList, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
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
		if real, served := listAppProjects(s, ctx); served {
			return &argoProjectList{Metadata: argoListMeta{}, Items: real}, nil
		}
		crs, err := sc.appCRs(s, ctx)
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
	list, err := s.State.dyn.Resource(appProjectGVR).List(ctx, metav1.ListOptions{})
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

// authSettings is the bootstrap configuration the dashboard SPA reads before its
// first render. Every value is FIXED: this plane runs no OIDC/Dex of its own —
// IAM owns identity at the edge — and exposes no exec, no plugins, no badges.
type authSettings struct {
	// URL is the console's own base URL.
	URL string `json:"url"`
	// StatusBadgeEnabled is always false: this plane serves no badges.
	StatusBadgeEnabled bool `json:"statusBadgeEnabled"`
	// StatusBadgeRootURL is always empty, for the same reason.
	StatusBadgeRootURL string `json:"statusBadgeRootUrl"`
	// OIDCConfig is always null: IAM gates identity at the edge, not here.
	OIDCConfig any `json:"oidcConfig"`
	// DexConfig carries an empty connector list, for the same reason.
	DexConfig settingsDex `json:"dexConfig"`
	// GoogleAnalytics is off, with user anonymization on.
	GoogleAnalytics settingsAnalytics `json:"googleAnalytics"`
	// Help carries no chat or binary download links.
	Help settingsHelp `json:"help"`
	// Plugins is always empty: no plugin runs on this plane.
	Plugins []any `json:"plugins"`
	// UserLoginsDisabled is always true: the SPA never renders its own login form.
	UserLoginsDisabled bool `json:"userLoginsDisabled"`
	// KustomizeVersions is always empty.
	KustomizeVersions []any `json:"kustomizeVersions"`
	// UICSSURL is always empty (no injected stylesheet).
	UICSSURL string `json:"uiCssURL"`
	// UIBannerContent is always empty (no banner).
	UIBannerContent string `json:"uiBannerContent"`
	// ExecEnabled is always false: no pod exec from this console.
	ExecEnabled bool `json:"execEnabled"`
	// AppsInAnyNamespaceEnabled is always false.
	AppsInAnyNamespaceEnabled bool `json:"appsInAnyNamespaceEnabled"`
	// HydratorEnabled is always false.
	HydratorEnabled bool `json:"hydratorEnabled"`
	// SyncWithReplaceAllowed is always false: replace-on-sync is never offered.
	SyncWithReplaceAllowed bool `json:"syncWithReplaceAllowed"`
}

// settingsDex is the (always empty) connector list of authSettings.dexConfig.
type settingsDex struct {
	// Connectors is always empty.
	Connectors []any `json:"connectors"`
}

// settingsAnalytics is authSettings.googleAnalytics.
type settingsAnalytics struct {
	// TrackingID is always empty: no analytics is wired.
	TrackingID string `json:"trackingID"`
	// AnonymizeUsers is always true.
	AnonymizeUsers bool `json:"anonymizeUsers"`
}

// settingsHelp is authSettings.help.
type settingsHelp struct {
	// ChatURL is always empty.
	ChatURL string `json:"chatUrl"`
	// ChatText is always empty.
	ChatText string `json:"chatText"`
	// BinaryURLs is always empty.
	BinaryURLs map[string]any `json:"binaryUrls"`
}

// settings returns the fixed bootstrap configuration the dashboard SPA reads first.
// Nothing here is per-request or per-tenant: this plane runs no OIDC/Dex of its
// own (IAM gates identity at the edge) and offers no exec, plugins or badges.
func (o ops) settings(ctx context.Context, _ *struct{}) (*authSettings, error) {
	return &authSettings{
		URL:                       "https://cd.hanzo.ai",
		StatusBadgeEnabled:        false,
		StatusBadgeRootURL:        "",
		OIDCConfig:                nil,
		DexConfig:                 settingsDex{Connectors: []any{}},
		GoogleAnalytics:           settingsAnalytics{TrackingID: "", AnonymizeUsers: true},
		Help:                      settingsHelp{ChatURL: "", ChatText: "", BinaryURLs: map[string]any{}},
		Plugins:                   []any{},
		UserLoginsDisabled:        true,
		KustomizeVersions:         []any{},
		UICSSURL:                  "",
		UIBannerContent:           "",
		ExecEnabled:               false,
		AppsInAnyNamespaceEnabled: false,
		HydratorEnabled:           false,
		SyncWithReplaceAllowed:    false,
	}, nil
}

// userInfoView answers "is this browser signed in, and if not where does it sign
// in?". The anonymous form carries loggedIn:false and loginUrl and NOTHING else:
// no username, no org, no issuer, no hint about who the caller might be.
type userInfoView struct {
	// LoggedIn reports whether the caller may use this console at all.
	LoggedIn bool `json:"loggedIn"`
	// LoginURL is where an anonymous caller signs in; absent once signed in.
	LoginURL string `json:"loginUrl,omitempty"`
	// Username is the signed-in operator; absent for an anonymous caller.
	Username string `json:"username,omitempty"`
	// Iss is the issuer the SPA checks before offering an SSO redirect; absent
	// for an anonymous caller.
	Iss string `json:"iss,omitempty"`
	// Groups is the signed-in operator's group list, always empty here; absent
	// for an anonymous caller.
	Groups *[]string `json:"groups,omitempty"`
	// LogoutURL is where the signed-in operator signs out; absent when anonymous.
	LogoutURL string `json:"logoutUrl,omitempty"`
}

// userInfo reports whether this browser is signed in, and where to sign in if not.
// It is the ONE route here that answers an anonymous caller, because a 403 to
// that question is unanswerable for an XHR client. The predicate is the same
// SuperAdmin fact every other route gates on, so a validated-but-not-SuperAdmin
// caller is reported as not logged in — the truth as this console defines it.
//
// Response: {"loggedIn": true, "username": "z", "iss": "argocd", "groups": [], "logoutUrl": "/v1/deploy/logout"}
func (o ops) userInfo(ctx context.Context, _ *struct{}) (*userInfoView, error) {
	c, ok := cloud.Request(ctx)
	if !ok || !c.IsAdmin() {
		return &userInfoView{LoggedIn: false, LoginURL: loginPath}, nil
	}
	user := c.User()
	if user == "" {
		user = "admin"
	}
	groups := []string{}
	return &userInfoView{
		LoggedIn:  true,
		Username:  user,
		Iss:       "argocd", // keep == argocd so the UI never triggers an SSO redirect
		Groups:    &groups,
		LogoutURL: logoutPath,
	}, nil
}

// versionMessage is the VersionMessage wire shape, PascalCase keys and all.
type versionMessage struct {
	// Version names this plane as a projection, not a real argocd server.
	Version string `json:"Version"`
	// BuildDate is the moment this answer was produced, RFC3339 UTC.
	BuildDate string `json:"BuildDate"`
	// GoVersion is always empty: this plane discloses no toolchain.
	GoVersion string `json:"GoVersion"`
	// Compiler is always "gc".
	Compiler string `json:"Compiler"`
	// Platform is always "linux/amd64".
	Platform string `json:"Platform"`
}

// version identifies this plane to the dashboard SPA as a projection.
// It is not a real argocd server and says so; the toolchain is never disclosed.
func (o ops) version(ctx context.Context, _ *struct{}) (*versionMessage, error) {
	return &versionMessage{
		Version:   "hanzo-cd (projection)",
		BuildDate: time.Now().UTC().Format(time.RFC3339),
		GoVersion: "", Compiler: "gc", Platform: "linux/amd64",
	}, nil
}

func dashCanI(s *cloud.Service[state], c *zip.Ctx) error {
	// Every route is already SuperAdmin-gated; report yes so buttons enable.
	return c.JSON(http.StatusOK, map[string]any{"value": "yes"})
}

// ── applications projection ──────────────────────────────────────────────────

// applications lists the caller's applications, projected from the operator App CRs.
// A SuperAdmin sees the whole fleet; a validated org member sees only its own
// org's apps. Nothing is read from a stored argocd Application — every item is
// synthesized from the App CR that actually runs.
func (o ops) applications(ctx context.Context, _ *struct{}) (*argoAppList, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	list := argoAppList{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationList", Metadata: argoListMeta{}, Items: []argoApp{}}
	for _, ns := range sc.namespaces() {
		crs, err := listAppCRs(s, ctx, ns)
		if err != nil {
			return nil, k8sErr(s, "list", err)
		}
		running := runningVersions(s, ctx, ns)
		for i := range crs {
			if !sc.allows(&crs[i]) {
				continue // cross-tenant CR — never projected to this scope
			}
			list.Items = append(list.Items, projectApp(&crs[i], ns, running[crs[i].GetName()]))
		}
	}
	return &list, nil
}

// application returns one projected application, with its reconciled resources.
// A name another org owns is reported as not found, never as forbidden, so the
// detail route leaks no cross-tenant existence oracle.
//
// Example: {"name": "cloud"}
func (o ops) application(ctx context.Context, in *appRef) (*argoApp, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	// findNamespace 404s a cross-tenant name (org A's app requested by org B) — no oracle.
	ns, err := sc.findNamespace(s, ctx, name)
	if err != nil {
		return nil, err
	}
	cr, _, err := getAppCR(s, ctx, ns, name)
	if err != nil {
		return nil, k8sErr(s, "get", err)
	}
	running := runningVersions(s, ctx, ns)
	app := projectApp(cr, ns, running[name])
	// Detail view: populate status.resources from the reconciled tree.
	tree := projectTree(buildTree(s, ctx, ns, name, cr))
	for _, n := range tree.Nodes {
		app.Status.Resources = append(app.Status.Resources, argoResourceStatus{
			Group: n.Group, Version: n.Version, Kind: n.Kind, Namespace: n.Namespace,
			Name: n.Name, Status: app.Status.Sync.Status, Health: n.Health,
		})
	}
	return &app, nil
}

// appName normalizes and bounds the {name} path segment. A value the router
// carried that is not a DNS-1123 label is refused before any cluster read.
func appName(raw string) (string, error) {
	name := regexpLower(raw)
	if !appNameRE.MatchString(name) {
		return "", zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	return name, nil
}

// resourceTree returns one application's live resource tree, node by node.
// The tree is built from what the cluster actually runs; a name another org owns
// is reported as not found.
//
// Example: {"name": "cloud"}
func (o ops) resourceTree(ctx context.Context, in *appRef) (*argoTree, error) {
	s := o.s
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	name, err := appName(in.Name)
	if err != nil {
		return nil, err
	}
	ns, err := sc.findNamespace(s, ctx, name)
	if err != nil {
		return nil, err
	}
	cr, _, err := getAppCR(s, ctx, ns, name)
	if err != nil {
		return nil, k8sErr(s, "get", err)
	}
	tree := projectTree(buildTree(s, ctx, ns, name, cr))
	return &tree, nil
}

// sync requests an operator reconcile of one application, and returns it projected.
// The App CR is the source of truth, so a sync is an annotation bump the operator
// acts on — nothing is applied from this plane. SuperAdmin only.
//
// Example: {"name": "cloud"}
func (o ops) sync(ctx context.Context, in *appRef) (*argoApp, error) {
	return o.reconcileApp(ctx, in.Name)
}

// rollback requests the same operator reconcile sync does, for the UI's Rollback action.
// Rollback-by-revision is the image-pin follow-on; today both actions mean
// "reconcile this App now". SuperAdmin only.
//
// Example: {"name": "cloud"}
func (o ops) rollback(ctx context.Context, in *appRef) (*argoApp, error) {
	return o.reconcileApp(ctx, in.Name)
}

// reconcileApp is the ONE sync/rollback path: resolve, annotate, project.
func (o ops) reconcileApp(ctx context.Context, raw string) (*argoApp, error) {
	s := o.s
	// Reached only through the SuperAdmin gate, so the scope is always whole-fleet;
	// resolving it keeps ONE namespace-resolution path (findNamespace) across the plane.
	sc, err := scopeFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := ready(s); err != nil {
		return nil, err
	}
	name, err := appName(raw)
	if err != nil {
		return nil, err
	}
	ns, err := sc.findNamespace(s, ctx, name)
	if err != nil {
		return nil, err
	}
	cr, gvr, err := getAppCR(s, ctx, ns, name)
	if err != nil {
		return nil, k8sErr(s, "get", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{syncAnnotation: now}}})
	if _, err := s.State.dyn.Resource(gvr).Namespace(ns).Patch(ctx, name, k8stypes.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return nil, k8sErr(s, "patch", err)
	}
	actor := ""
	if c, ok := cloud.Request(ctx); ok {
		actor = c.User()
	}
	s.Log.Info("dashboard sync requested", "app", name, "namespace", ns, "actor", actor)
	app := projectApp(cr, ns, runningVersions(s, ctx, ns)[name])
	return &app, nil
}
