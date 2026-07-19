// scope.go — the TENANT SCOPE of a /v1/deploy request, and the IAM-owned project
// reflection.
//
// This plane was SuperAdmin-only: it listed App CRs across ALL platform
// namespaces, hard-coded spec.project = "default", and read no org/project
// labels. It is now TENANT-AWARE and IAM-MAPPED. Every operator App CR already
// carries the tenant + project labels (clients/platform serviceCR + the fleet
// crs/*.yaml stamp them); this plane READS them and scopes each list/detail read
// to the caller's org, while a SuperAdmin keeps the whole-fleet view.
//
// The tenant boundary is the SAME one the rest of cloud trusts (clients/platform
// .tenant / clients/s3.tenant): the gateway-minted, IAM-validated identity headers
// (c.IsAdmin/c.Org), the injective provisioning.SanitizeOrg normalizer, and the
// principal.Validated gate. There is NO third slug rule — resolveScope keys the
// SAME (org → tenant-<org>) boundary the PaaS writes into.
//
// Projects are owned by Hanzo IAM (hanzo.id), the ONE source of truth for the
// org-scoped (Owner,Name) Project resource. This plane REFLECTS them read-only via
// the in-process object store (embedded IAM, no HTTP hop) — mirroring
// clients/platform/projects.go — and never persists a CD-side project row.
package deploy

import (
	"context"
	"net/http"
	"net/url"

	iamobj "github.com/hanzoai/iam/object"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/provisioning"
)

// Operator App CRs carry two labels this plane READS (never writes): clients/platform
// serviceCR and the fleet crs/*.yaml stamp them.
//
//	hanzo.ai/org               — the TENANT: the injective provisioning.SanitizeOrg slug.
//	app.kubernetes.io/part-of  — the IAM Project NAME (tenant-provisioned apps carry it).
const (
	orgLabel     = "hanzo.ai/org"
	projectLabel = "app.kubernetes.io/part-of"
)

// scope is the resolved tenant view of a /v1/deploy request. A SuperAdmin sees the
// WHOLE fleet (org == "", superAdmin == true); a normal org sees ONLY its own rows
// (org == its injective slug, superAdmin == false).
type scope struct {
	org        string // sanitized org slug; "" == whole-fleet (SuperAdmin)
	superAdmin bool
}

// resolveScope derives the scope from the VALIDATED identity, reusing clients/platform
// .tenant's EXACT boundary primitives so this plane keys the SAME tenant boundary as the
// rest of cloud — with no third slug rule.
//
// SuperAdmin FIRST and by c.IsAdmin() ALONE: SanitizeIdentity mints X-User-IsAdmin only
// for a JWT-verified SuperAdmin and NEVER restores it from the client (unlike X-Org-Id),
// so it already implies a validated principal. This is the SAME predicate the original
// guard() gates on, which is why the SuperAdmin whole-fleet view (and the e2e 108/109
// contract) is preserved byte-for-byte.
//
// A normal org additionally REQUIRES principal.Validated (X-User-Id present): X-Org-Id IS
// restorable from the client on the bearer-less "Phase-1 data" path, so trusting it without
// a validated principal would let an off-gateway caller forge `X-Org-Id: victim` and read
// another tenant. An empty or unvalidated org fails closed.
func resolveScope(c *zip.Ctx) (scope, bool) {
	if c.IsAdmin() {
		return scope{superAdmin: true}, true
	}
	if !principal.Validated(c) {
		return scope{}, false
	}
	if org := provisioning.SanitizeOrg(c.Org()); org != "" {
		return scope{org: org}, true
	}
	return scope{}, false
}

// refuse is the fail-closed refusal shared by guard() and every scoped route: a browser
// NAVIGATION is bounced to sign-in (a 403 page with no way to sign in is a dead end), while
// every API/XHR call keeps its 403. Identical shape to the original guard(); the message is
// deliberately generic so the 403 discloses no policy (whether admin or an org would pass).
func refuse(c *zip.Ctx) error {
	if wantsDocument(c.Method(), c.Header("Sec-Fetch-Dest"), c.Header("Sec-Fetch-Mode"),
		c.Header("Accept"), c.Header("X-Requested-With")) {
		return c.Redirect(http.StatusFound, loginPath+"?returnTo="+url.QueryEscape(currentPath(c)))
	}
	return zip.ErrForbidden("not authorized for this deploy console")
}

// orgOf / projectOf read the tenant + IAM-project labels off an App CR ("" when absent).
func orgOf(cr *unstructured.Unstructured) string     { return cr.GetLabels()[orgLabel] }
func projectOf(cr *unstructured.Unstructured) string { return cr.GetLabels()[projectLabel] }

// projectName is the App CR's IAM project — the app.kubernetes.io/part-of label, falling
// back to "default" when the CR carries none (the fleet CRs predate per-app projects).
func projectName(cr *unstructured.Unstructured) string {
	if p := projectOf(cr); p != "" {
		return p
	}
	return principal.DefaultProject
}

// allows reports whether an App CR is visible to this scope. A SuperAdmin sees every CR; a
// normal org sees ONLY a CR whose hanzo.ai/org label equals its slug — the injective
// SanitizeOrg slug, so two orgs can never collide. This is the cross-tenant boundary,
// applied to EVERY projected/accessed CR (list, detail, clusters, stream).
func (sc scope) allows(cr *unstructured.Unstructured) bool {
	if sc.superAdmin {
		return true
	}
	return sc.org != "" && orgOf(cr) == sc.org
}

// namespaces is the ordered set of namespaces this scope reads App CRs from. A SuperAdmin
// scans the whole platform tier (scanOrder, unchanged — preserving the pre-tenant fleet
// view); a normal org scans ONLY its own tenant namespace, tenant-<org>.
func (sc scope) namespaces() []string {
	if sc.superAdmin {
		return scanOrder()
	}
	return []string{tenantNS(sc.org)}
}

// tenantNS is the tenant namespace for an ALREADY-sanitized org slug: "tenant-"+slug — the
// read twin of clients/platform.tenantNamespace (which is "tenant-"+SanitizeOrg(org)).
// sc.org is never empty here (resolveScope gates it to a non-empty injective slug), so no
// "unknown" fallback is needed. The "tenant-" prefix is a frozen infra convention (renaming
// it is a gated namespace migration — see clients/platform/k8s.go); sc.org is the SAME
// SanitizeOrg slug platform stamps, so the two derive the same namespace.
func tenantNS(org string) string { return "tenant-" + org }

// appCRs collects every App CR VISIBLE to this scope across its namespaces, filtered by the
// org label (a SuperAdmin's filter admits all). Used where per-namespace running-tag
// batching is unneeded (clusters + the projects fallback).
func (sc scope) appCRs(s *cloud.Service[state], ctx context.Context) ([]unstructured.Unstructured, error) {
	var out []unstructured.Unstructured
	for _, ns := range sc.namespaces() {
		crs, err := listAppCRs(s, ctx, ns)
		if err != nil {
			return nil, k8sErr(s, "list", err)
		}
		for i := range crs {
			if sc.allows(&crs[i]) {
				out = append(out, crs[i])
			}
		}
	}
	return out, nil
}

// findNamespace resolves the namespace an App CR named `name` lives in, scanning this
// scope's namespaces in order and REQUIRING the CR be visible to the scope. A cross-tenant
// name (org A's app requested by org B) is reported as a clean 404 — never confirmed to
// exist, so the detail routes leak no cross-tenant existence oracle.
func (sc scope) findNamespace(s *cloud.Service[state], c *zip.Ctx, name string) (string, error) {
	for _, ns := range sc.namespaces() {
		cr, _, err := getAppCR(s, c.Context(), ns, name)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", k8sErr(s, "get", err)
		}
		if !sc.allows(cr) {
			continue // exists but not this tenant's — treat as absent
		}
		return ns, nil
	}
	return "", zip.ErrNotFound("application " + name + " not found")
}

// watches reports whether a watched App-CR object belongs to this scope's stream: its
// namespace is one the scope watches AND (for a normal org) its org label matches. Replaces
// the platform-namespace nsEnv gate forwardWatch used, generalizing it to the scope.
func (sc scope) watches(obj *unstructured.Unstructured) bool {
	return sc.watchesNamespace(obj.GetNamespace()) && sc.allows(obj)
}

func (sc scope) watchesNamespace(ns string) bool {
	for _, n := range sc.namespaces() {
		if n == ns {
			return true
		}
	}
	return false
}

// runningByNamespace collects running image tags per namespace this scope watches
// (best-effort — a list error yields an empty inner map). Scoped twin of the stream's
// per-namespace running-tag map.
func (sc scope) runningByNamespace(s *cloud.Service[state], ctx context.Context) map[string]map[string]string {
	out := make(map[string]map[string]string, len(sc.namespaces()))
	for _, ns := range sc.namespaces() {
		out[ns] = runningVersions(s, ctx, ns)
	}
	return out
}

// ── IAM-owned project reflection ─────────────────────────────────────────────

// iamStore runs an embedded-IAM object-store call, converting a nil-store panic into a
// clean 503 rather than a nil-deref crash. The store's engine (iamobj.ormer) is a package
// global that is nil until the co-resident IAM subsystem initializes it; a project call
// against a nil engine would otherwise nil-deref. Mirrors clients/platform.iamStore — a
// deployment enabling "deploy" is meant to co-mount "iam" (single-binary co-residents).
// Never masks a real error.
func iamStore[T any](fn func() (T, error)) (out T, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = zip.Errorf(http.StatusServiceUnavailable,
				"deploy requires the co-resident IAM store, which is not initialized")
		}
	}()
	return fn()
}

// iamProjects reflects the IAM-owned projects VISIBLE to this scope into projected argo
// AppProjects. A normal org gets its own organization's projects
// (GetOrganizationProjects); a SuperAdmin gets every org's (GetProjects with an empty owner
// → all owners, the same all-orgs listing IAM's own get-projects endpoint serves). IAM is
// the ONE source; this NEVER persists a CD-side project. A nil/absent embedded IAM store
// yields nil (the caller's synthesized-default fallback keeps the projection populated).
func (sc scope) iamProjects() []argoProject {
	list, err := iamStore(func() ([]*iamobj.Project, error) {
		if sc.superAdmin {
			return iamobj.GetProjects("") // empty owner → every org's projects
		}
		return iamobj.GetOrganizationProjects(sc.org)
	})
	if err != nil || len(list) == 0 {
		return nil
	}
	out := make([]argoProject, 0, len(list))
	for _, p := range list {
		out = append(out, projectFromIAM(p))
	}
	return out
}

// projectFromIAM reflects ONE IAM Project into a projected argo AppProject: name = the
// project Name (the app-scope key AND the CR part-of label), description = DisplayName or
// Description, and an hanzo.ai/org label carrying the tenant. Project scoping on this
// platform is IAM/Org, not argocd RBAC, so the projected spec is permissive — and ONLY
// these fields are surfaced (never Tags/Metadata), so nothing unintended leaks.
func projectFromIAM(p *iamobj.Project) argoProject {
	proj := synthProject(p.Name)
	proj.Spec.Description = firstNonEmpty(p.DisplayName, p.Description)
	if org := provisioning.SanitizeOrg(firstNonEmpty(p.Organization, p.Owner)); org != "" {
		proj.Metadata.Labels = map[string]string{orgLabel: org}
	}
	return proj
}

// ensureDefault guarantees a project named "default" is present (prepended if absent by
// name), so every projected app's spec.project — which falls back to "default" when a CR
// carries no part-of label — always resolves to a listed project, and the CD SPA's project
// filter can render the default bucket. Preserves the e2e invariant (projects contains
// 'default') regardless of IAM state. Pure.
func ensureDefault(items []argoProject) []argoProject {
	for i := range items {
		if items[i].Metadata.Name == principal.DefaultProject {
			return items
		}
	}
	return append([]argoProject{synthProject(principal.DefaultProject)}, items...)
}
