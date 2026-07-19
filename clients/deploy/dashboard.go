// dashboard.go — the ArgoCD-UI-compatible projection API at /v1/deploy/api/*,
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
// Every route is SuperAdmin-gated (c.IsAdmin), fail-closed; the argocd UI's own
// auth is disabled because IAM owns identity at the edge (the SPA is public
// static assets, the data is gated). AppProject → IAM/Org (no argocd RBAC).
package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

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
// the SPA is a separate hanzoai/spa App). Called from routes() (deploy.go).
func registerDashboardRoutes(app *zip.App, s *cloud.Service[state]) {
	// Bootstrap (the SPA awaits settings + userinfo before first render).
	app.Get(dashPrefix+"/settings", guard(s, cloud.Handle(s, dashSettings)))
	// userinfo is the ONE deliberately PUBLIC bootstrap route: it is how the SPA
	// asks "am I signed in?", and a 403 to that question is unanswerable — the SPA
	// is an XHR client, so the document bounce in guard() never fires for it and it
	// dead-ends with no way to reach sign-in. Anonymous callers get
	// {loggedIn:false} and the sign-in URL; nothing else. It discloses no identity,
	// no cluster state, and no configuration, and it is NOT a gate: every route
	// that returns fleet data or mutates a CR stays guard()ed.
	app.Get(dashPrefix+"/session/userinfo", cloud.Handle(s, dashUserInfo))
	app.Get(dashPrefix+"/version", guard(s, cloud.Handle(s, dashVersion)))
	app.Get(dashPrefix+"/account/can-i/*", guard(s, cloud.Handle(s, dashCanI)))

	// Applications projection (read).
	app.Get(dashPrefix+"/applications", guard(s, cloud.Handle(s, dashAppList)))
	app.Get(dashPrefix+"/applications/:name", guard(s, cloud.Handle(s, dashApp)))
	app.Get(dashPrefix+"/applications/:name/resource-tree", guard(s, cloud.Handle(s, dashResourceTree)))
	// Applications watch (Server-Sent Events) — the live stream the applications
	// view opens; see stream.go.
	app.Get(dashPrefix+"/stream/applications", guard(s, cloud.Handle(s, dashStreamApps)))

	// Destination clusters + AppProjects — the two lists the applications view
	// resolves alongside the fleet (Destination column + project filter).
	app.Get(dashPrefix+"/clusters", guard(s, cloud.Handle(s, dashClusters)))
	app.Get(dashPrefix+"/projects", guard(s, cloud.Handle(s, dashProjects)))

	// Actions → App-CR reconcile ops.
	app.Post(dashPrefix+"/applications/:name/sync", guard(s, cloud.Handle(s, dashSync)))
	app.Post(dashPrefix+"/applications/:name/rollback", guard(s, cloud.Handle(s, dashSync)))
}

// ── clusters + projects projection ───────────────────────────────────────────

// dashClusters is GET /v1/deploy/clusters — the ArgoCD ClusterList of the
// destinations the fleet reconciles into (always ≥ the in-cluster destination),
// read from the SAME App-CR source dashAppList uses. It NEVER surfaces a cluster
// credential (argoCluster has no config field — see projection.go).
func dashClusters(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	crs, err := allAppCRs(s, c.Context())
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, projectClusters(crs))
}

// dashProjects is GET /v1/deploy/projects — the ArgoCD AppProjectList. It PREFERS
// real argoproj.io/v1alpha1 AppProject CRs when that CRD is served; otherwise it
// synthesizes one permissive project per distinct App-CR project name (default
// always present). Read-only, from the same App-CR source.
func dashProjects(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	if items, ok := listAppProjects(s, c.Context()); ok {
		return c.JSON(http.StatusOK, argoProjectList{Metadata: argoListMeta{}, Items: items})
	}
	crs, err := allAppCRs(s, c.Context())
	if err != nil {
		return err
	}
	names := projectedProjectNames(crs)
	items := make([]argoProject, 0, len(names))
	for _, name := range names {
		items = append(items, synthProject(name))
	}
	return c.JSON(http.StatusOK, argoProjectList{Metadata: argoListMeta{}, Items: items})
}

// allAppCRs collects every App CR across the platform namespaces — the exact
// listAppCRs source dashAppList reads, flattened (order does not matter for the
// cluster/project sets, which dedupe).
func allAppCRs(s *cloud.Service[state], ctx context.Context) ([]unstructured.Unstructured, error) {
	var out []unstructured.Unstructured
	for _, ns := range scanOrder() {
		crs, err := listAppCRs(s, ctx, ns)
		if err != nil {
			return nil, k8sErr(s, "list", err)
		}
		out = append(out, crs...)
	}
	return out, nil
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

func dashSettings(s *cloud.Service[state], c *zip.Ctx) error {
	return c.JSON(http.StatusOK, map[string]any{
		"url":                       "https://cd.hanzo.ai",
		"statusBadgeEnabled":        false,
		"statusBadgeRootUrl":        "",
		"oidcConfig":                nil,
		"dexConfig":                 map[string]any{"connectors": []any{}},
		"googleAnalytics":           map[string]any{"trackingID": "", "anonymizeUsers": true},
		"help":                      map[string]any{"chatUrl": "", "chatText": "", "binaryUrls": map[string]any{}},
		"plugins":                   []any{},
		"userLoginsDisabled":        true,
		"kustomizeVersions":         []any{},
		"uiCssURL":                  "",
		"uiBannerContent":           "",
		"execEnabled":               false,
		"appsInAnyNamespaceEnabled": false,
		"hydratorEnabled":           false,
		"syncWithReplaceAllowed":    false,
	})
}

// dashUserInfo answers "is this browser signed in, and if not where does it sign
// in?" — the SPA's bootstrap question, and the only route on this plane that
// answers for an anonymous caller.
//
// The anonymous branch carries loggedIn:false and a URL, and NOTHING else: no
// username, no org, no groups, no issuer, no hint about who the caller might be or
// what exists in the cluster. Answering it costs nothing (the caller already knows
// whether it holds a cookie) and withholding it costs the whole sign-in journey.
//
// The predicate is c.IsAdmin() — the SAME SuperAdmin fact guard() gates on, minted
// by SanitizeIdentity from a validated principal whose org is the reserved admin
// org. So a validated-but-not-SuperAdmin caller is reported as NOT logged in here,
// which is the truth as this console defines it: they cannot use it.
func dashUserInfo(s *cloud.Service[state], c *zip.Ctx) error {
	if !c.IsAdmin() {
		return c.JSON(http.StatusOK, map[string]any{
			"loggedIn": false,
			"loginUrl": loginPath,
		})
	}
	user := c.User()
	if user == "" {
		user = "admin"
	}
	return c.JSON(http.StatusOK, map[string]any{
		"loggedIn":  true,
		"username":  user,
		"iss":       "argocd", // keep == argocd so the UI never triggers an SSO redirect
		"groups":    []string{},
		"logoutUrl": logoutPath,
	})
}

func dashVersion(s *cloud.Service[state], c *zip.Ctx) error {
	// PascalCase keys (VersionMessage wire shape).
	return c.JSON(http.StatusOK, map[string]any{
		"Version":   "hanzo-cd (projection)",
		"BuildDate": time.Now().UTC().Format(time.RFC3339),
		"GoVersion": "", "Compiler": "gc", "Platform": "linux/amd64",
	})
}

func dashCanI(s *cloud.Service[state], c *zip.Ctx) error {
	// Every route is already SuperAdmin-gated; report yes so buttons enable.
	return c.JSON(http.StatusOK, map[string]any{"value": "yes"})
}

// ── applications projection ──────────────────────────────────────────────────

func dashAppList(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	list := argoAppList{APIVersion: "argoproj.io/v1alpha1", Kind: "ApplicationList", Metadata: argoListMeta{}, Items: []argoApp{}}
	for _, ns := range scanOrder() {
		crs, err := listAppCRs(s, c.Context(), ns)
		if err != nil {
			return k8sErr(s, "list", err)
		}
		running := runningVersions(s, c.Context(), ns)
		for i := range crs {
			list.Items = append(list.Items, projectApp(&crs[i], ns, running[crs[i].GetName()]))
		}
	}
	return c.JSON(http.StatusOK, list)
}

func dashApp(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqName(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	ns, err := resolveNamespace(s, c, name)
	if err != nil {
		return err
	}
	cr, _, err := getAppCR(s, c.Context(), ns, name)
	if err != nil {
		return k8sErr(s, "get", err)
	}
	running := runningVersions(s, c.Context(), ns)
	app := projectApp(cr, ns, running[name])
	// Detail view: populate status.resources from the reconciled tree.
	tree := projectTree(buildTree(s, c.Context(), ns, name, cr))
	for _, n := range tree.Nodes {
		app.Status.Resources = append(app.Status.Resources, argoResourceStatus{
			Group: n.Group, Version: n.Version, Kind: n.Kind, Namespace: n.Namespace,
			Name: n.Name, Status: app.Status.Sync.Status, Health: n.Health,
		})
	}
	return c.JSON(http.StatusOK, app)
}

func dashResourceTree(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqName(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	ns, err := resolveNamespace(s, c, name)
	if err != nil {
		return err
	}
	cr, _, err := getAppCR(s, c.Context(), ns, name)
	if err != nil {
		return k8sErr(s, "get", err)
	}
	return c.JSON(http.StatusOK, projectTree(buildTree(s, c.Context(), ns, name, cr)))
}

// dashSync requests an operator reconcile of the App CR (the sync + rollback UI
// actions both map to "reconcile this App now" — the App CR is the source of
// truth; rollback-by-revision is the image-pin follow-on). Returns the projected
// Application (the UI only checks for a non-error response).
func dashSync(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqName(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("name must be a DNS-1123 label")
	}
	ns, err := resolveNamespace(s, c, name)
	if err != nil {
		return err
	}
	cr, gvr, err := getAppCR(s, c.Context(), ns, name)
	if err != nil {
		return k8sErr(s, "get", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	patch, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]any{syncAnnotation: now}}})
	if _, err := s.State.dyn.Resource(gvr).Namespace(ns).Patch(c.Context(), name, k8stypes.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return k8sErr(s, "patch", err)
	}
	s.Log.Info("dashboard sync requested", "app", name, "namespace", ns, "actor", c.User())
	return c.JSON(http.StatusOK, projectApp(cr, ns, runningVersions(s, c.Context(), ns)[name]))
}
