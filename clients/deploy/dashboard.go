// dashboard.go — the ArgoCD-UI-compatible projection API at /v1/deploy/api/*,
// fed the App-CR projection (projection.go). NO argocd api-server, NO
// repo-server, NO redis, NO stored Application/AppProject CRD — every response
// is synthesized from our operator App CRs. The FRONTEND is NOT here: the
// monochrome dashboard ships as the `hanzoai/spa`-based `cd-ui` App CR served at
// cd.hanzo.ai/ (base-href /); this plane is only the same-origin API it calls:
//
//	GET  /v1/deploy/api/v1/settings          → AuthSettings (auth disabled; IAM gates at the edge)
//	GET  /v1/deploy/api/v1/session/userinfo  → {loggedIn:true,...}
//	GET  /v1/deploy/api/version              → VersionMessage
//	GET  /v1/deploy/api/v1/account/can-i/*   → {"value":"yes"}
//	GET  /v1/deploy/api/v1/applications                          → ApplicationList (projected)
//	GET  /v1/deploy/api/v1/applications/{name}                   → Application (projected)
//	GET  /v1/deploy/api/v1/applications/{name}/resource-tree     → ApplicationTree
//	POST /v1/deploy/api/v1/applications/{name}/{sync,rollback}   → request App-CR reconcile
//
// Every route is SuperAdmin-gated (c.IsAdmin), fail-closed; the argocd UI's own
// auth is disabled because IAM owns identity at the edge (the SPA is public
// static assets, the data is gated). AppProject → IAM/Org (no argocd RBAC).
package deploy

import (
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
func registerDashboardRoutes(app *zip.App, s *cloud.Service[state]) {
	// Bootstrap (the SPA awaits settings + userinfo before first render).
	app.Get(dashPrefix+"/api/v1/settings", guard(s, cloud.Handle(s, dashSettings)))
	app.Get(dashPrefix+"/api/v1/session/userinfo", guard(s, cloud.Handle(s, dashUserInfo)))
	app.Get(dashPrefix+"/api/version", guard(s, cloud.Handle(s, dashVersion)))
	app.Get(dashPrefix+"/api/v1/account/can-i/*", guard(s, cloud.Handle(s, dashCanI)))

	// Applications projection (read).
	app.Get(dashPrefix+"/api/v1/applications", guard(s, cloud.Handle(s, dashAppList)))
	app.Get(dashPrefix+"/api/v1/applications/:name", guard(s, cloud.Handle(s, dashApp)))
	app.Get(dashPrefix+"/api/v1/applications/:name/resource-tree", guard(s, cloud.Handle(s, dashResourceTree)))

	// Actions → App-CR reconcile ops.
	app.Post(dashPrefix+"/api/v1/applications/:name/sync", guard(s, cloud.Handle(s, dashSync)))
	app.Post(dashPrefix+"/api/v1/applications/:name/rollback", guard(s, cloud.Handle(s, dashSync)))
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

func dashUserInfo(s *cloud.Service[state], c *zip.Ctx) error {
	// IAM authenticated the request at the edge; report the principal.
	user := c.User()
	if user == "" {
		user = "admin"
	}
	return c.JSON(http.StatusOK, map[string]any{
		"loggedIn": true,
		"username": user,
		"iss":      "argocd", // keep == argocd so the UI never triggers an SSO redirect
		"groups":   []string{},
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
