// sites.go — projects the static-plane SITES into the fleet list alongside the
// pod-backed services. A site has no App CR and no Deployment: it is a `staticFiles`
// Middleware (its S3 origin, `spec.staticFiles.root: s3://cdn/<slug>`) plus an
// IngressRoute (its public host), served straight from S3 by the ingress. Without
// this, GET /v1/deploy/applications showed only the ~72 App CRs and every site —
// including cd.hanzo.ai itself — was invisible. Each site becomes one Application
// row with Role:"site", so the CD dashboard renders the WHOLE delivery surface.
//
// A site is desired-state-in-sync by construction: it is served from exactly the
// S3 prefix its Middleware declares, so there is no declared-vs-running image drift
// to report (Version == RunningVersion == "static" ⇒ Synced). Health is whether the
// site is actually reachable: a staticFiles Middleware with a matching IngressRoute
// host is Healthy; one defined but never routed is Missing.
package deploy

import (
	"context"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// siteVersion is the placeholder image "version" for a static site — it has no
// container tag. Equal declared/running ⇒ the site reads as Synced (it is served
// from exactly its declared S3 prefix; there is no drift axis).
const siteVersion = "static"

// hostMatchRE pulls the first host out of a Traefik-style route match rule, e.g.
// `Host(`+"`gallery.hanzo.ai`"+`) && PathPrefix(`+"`/`"+`)` → `gallery.hanzo.ai`.
var hostMatchRE = regexp.MustCompile("Host\\(`([^`]+)`\\)")

// listSiteApplications projects the static-plane sites in ns as Application rows.
// Best-effort (mirrors runningVersions): a list error — missing CRD, RBAC — logs
// and yields nothing, so the services half of the board always renders. Only
// staticFiles Middlewares (a non-empty `spec.staticFiles.root`) count as a site;
// its public host is joined from the IngressRoute that references it by name.
func listSiteApplications(s *cloud.Service[state], ctx context.Context, ns string) []Application {
	mws, err := s.State.dyn.Resource(middlewaresGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Log.Warn("list middlewares for sites failed (skipping site rows)", "namespace", ns, "err", err)
		return nil
	}
	hosts := siteHostsByMiddleware(s, ctx, ns)
	out := make([]Application, 0, len(mws.Items))
	for i := range mws.Items {
		mw := &mws.Items[i]
		root, _, _ := unstructured.NestedString(mw.Object, "spec", "staticFiles", "root")
		if strings.TrimSpace(root) == "" {
			continue // not a static site (some other middleware kind)
		}
		host := hosts[mw.GetName()]
		out = append(out, Application{
			Name:           siteSlug(root, mw.GetName()),
			Namespace:      ns,
			Env:            nsEnv[ns],
			Role:           "site",
			Repository:     root, // the S3 origin IS the site's artifact home
			Version:        siteVersion,
			RunningVersion: siteVersion,
			Health:         siteHealth(host),
			Sync:           syncStatus(siteVersion, siteVersion), // always Synced
			Endpoints:      siteEndpoints(host),
		})
	}
	return out
}

// siteHostsByMiddleware builds middleware-name → public host by scanning every
// IngressRoute in ns: for each route that references a middleware by name and
// carries a Host(`…`) match, that host is the middleware's site host. Best-effort.
func siteHostsByMiddleware(s *cloud.Service[state], ctx context.Context, ns string) map[string]string {
	out := map[string]string{}
	routes, err := s.State.dyn.Resource(ingressRoutesGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Log.Warn("list ingressroutes for sites failed (site hosts unresolved)", "namespace", ns, "err", err)
		return out
	}
	for i := range routes.Items {
		specRoutes, ok, _ := unstructured.NestedSlice(routes.Items[i].Object, "spec", "routes")
		if !ok {
			continue
		}
		for _, r := range specRoutes {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			match, _ := rm["match"].(string)
			host := parseFirstHost(match)
			if host == "" {
				continue
			}
			for _, name := range routeMiddlewareNames(rm) {
				if _, seen := out[name]; !seen { // first host wins (stable)
					out[name] = host
				}
			}
		}
	}
	return out
}

// routeMiddlewareNames returns the middleware names a single route references.
func routeMiddlewareNames(route map[string]any) []string {
	raw, ok := route["middlewares"].([]any)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(raw))
	for _, m := range raw {
		if mm, ok := m.(map[string]any); ok {
			if n, ok := mm["name"].(string); ok && n != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

// parseFirstHost extracts the first Host(`…`) literal from a route match rule.
func parseFirstHost(match string) string {
	m := hostMatchRE.FindStringSubmatch(match)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

// siteSlug is the site's identity — the last path segment of its S3 root
// (`s3://cdn/gallery` → `gallery`), the canonical prefix a bundle deploys to.
// Falls back to the middleware name (minus a `-static` suffix) if the root has no
// path segment.
func siteSlug(root, mwName string) string {
	if i := strings.LastIndex(root, "/"); i >= 0 && i+1 < len(root) {
		if slug := strings.TrimSpace(root[i+1:]); slug != "" {
			return slug
		}
	}
	return strings.TrimSuffix(mwName, "-static")
}

// siteHealth: a routed static site (reachable host) is Healthy; a staticFiles
// Middleware with no IngressRoute host is defined-but-unreachable ⇒ Missing.
func siteHealth(host string) string {
	if host == "" {
		return HealthMissing
	}
	return HealthHealthy
}

// siteEndpoints is the site's public URL(s) — its host as https, or none if unrouted.
func siteEndpoints(host string) []string {
	if host == "" {
		return []string{}
	}
	return []string{"https://" + host}
}
