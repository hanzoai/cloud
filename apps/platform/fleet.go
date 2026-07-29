// fleet.go — the platform's view of its OWN service tier.
//
// It mounts /v1/platform/fleet on the unified cloud binary and reads the operator's
// `hanzo.ai/v1` `App` CustomResource — the one workload kind the fleet runs on:
//
//	GET  /v1/platform/fleet            — the fleet drift board: list every operator
//	                                     App CR across the platform namespaces, read
//	                                     declared vs running tag + health from the CR
//	                                     (+ its status), and attach the drift verdict
//	                                     (drift.go).
//	GET  /v1/platform/fleet/:app       — one app row by CR name.
//	POST /v1/platform/fleet/:app/deploy— zero-downtime ROLLING RESTART of the app's
//	                                     Deployment (the `kubectl rollout restart`
//	                                     mechanism): re-pulls the DECLARED image,
//	                                     recreates pods gracefully. It never changes the
//	                                     declared TAG (that stays a git commit, the one
//	                                     thing Hanzo CD's selfHeal reconciles), so there
//	                                     is no drift to revert. This is `hanzo deploy`.
//
// FLEET vs PROJECTS/:project/APPS — two collections, two names. `fleet` is the
// PLATFORM's own tier (the shared services the platform itself runs on: iam, kms,
// gateway, …), observed from the operator App CRs. `projects/:project/apps` is a
// CUSTOMER's apps, owned by platform's own store. They answer different questions,
// so they carry different names under the one /v1/platform prefix. This file used to
// be a second top-level product (`/v1/paas`) — the same platform under a second
// name, which is exactly the duplicate definition the one-way rule forbids.
//
// SECURITY — every route is authorized off ONE IAM identity, exactly like the
// /v1/runner build path (runner.go): the guard admits a validated principal
// (principal.Validated) who is a SuperAdmin OR an OrgAdmin, and each handler then
// CONFINES a non-super caller to its own org's platform namespaces
// (scopedNamespaces, keyed on principal.Org — never a client header). A SuperAdmin
// observes/acts on the whole fleet; an OrgAdmin only on the namespaces its org owns;
// a plain member or an unauthenticated caller is refused 403. So a tenant admin can
// never observe — or restart — another org's, or a platform, app, and the platform
// operator drives the board off a plain `hanzo login` with no shared token.
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev — the identical
// construction clients/ml uses. When no kubeconfig is resolvable the board mounts
// anyway and every endpoint fails closed (503 + the real init error; the shared
// /v1/platform/health route reports "degraded"), never status-theater.
package platform

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// k8s.Apps is the operator App CR — the one workload kind the fleet runs on. The
// drift board reads it across the platform namespaces. Asserted in the tests; a
// typo here silently breaks the whole board.

// k8s.Deployments is the live Deployment behind each Service — the source of the
// RUNNING tag (the operator Service CR status does not surface the running image,
// so the running tag is observed from the Deployment's container, exactly as the
// platform inventory reads it in inventory.ts). Read-only for this subsystem.

// nsClass is THE namespace classifier: the one place that decides what a platform
// namespace MEANS. Total — every input yields a decision and an unrecognised
// namespace is classified OUT (ok=false) rather than guessed at, so the reader can
// never reach beyond the platform tier.
//
// It replaces three separate encodings of this single fact, which had drifted out
// of agreement: the nsEnv map (3 namespaces), nsOrg's suffix-strip (knew only
// -devnet/-testnet), and scanOrder's literal list (the same 3). Because a
// `tenant-<org>` namespace matched none of them, it classified as its own org and
// was never scanned — every tenant workload was invisible BY CONSTRUCTION. Deriving
// tenant and env from one function is what makes that class of drift impossible:
// there is no second place left to disagree with.
//
// tenant is the authorization axis (scopedNamespaces confines a non-super OrgAdmin
// to namespaces whose tenant equals their validated org); env is the lifecycle
// label. Cross-cluster federation (lux-k8s/zoo-k8s) is a follow-up phase.
func nsClass(ns string) (tenant, env string, ok bool) {
	switch ns {
	case "hanzo", "hanzo-mainnet":
		return "hanzo", "main", true
	case "hanzo-testnet":
		return "hanzo", "test", true
	case "hanzo-devnet":
		return "hanzo", "dev", true
	}
	// tenant-<org>: a customer's own namespace. The org IS the tenant key, so it
	// authorizes exactly like a first-party namespace with no special case.
	if t, found := strings.CutPrefix(ns, "tenant-"); found && t != "" {
		return t, "main", true
	}
	return "", "", false
}

// envOf is nsClass's env projection ("" when the namespace is not ours).
func envOf(ns string) string { _, env, _ := nsClass(ns); return env }

// k8s.Namespaces backs discovery. Listing NAMESPACES is the honest question — "which
// namespaces are ours?" — and asks it of the one authority that knows. Deriving the
// set from a cluster-wide CR list would answer a different question ("where are
// there CRs?") and pull every tenant's objects through this process to do it.

// nsScanTTL bounds how stale a discovered scan set may be. A new tenant namespace
// appears on the board within this window without a redeploy.
const nsScanTTL = 60 * time.Second

// discoverNamespaces returns every namespace in the cluster that nsClass recognises,
// first-party ones first (main before test/dev, so a bare app name still resolves to
// production) then tenants in stable order.
//
// This is what finally makes tenant-<org> workloads visible: they were classified
// correctly but never SCANNED, because the scan set was a literal list. Discovery
// asks the cluster instead of hardcoding an answer that goes stale the moment a
// tenant is onboarded.
//
// Fail-SAFE, never fail-open: if the list fails we fall back to the first-party set,
// which is narrower than the truth — a tenant admin can lose visibility of its own
// namespace, never gain visibility of someone else's. nsClass still filters, so a
// namespace that is not ours can never enter the set however discovery goes.
func discoverNamespaces(s *cloud.Service[fleetState], ctx context.Context) []string {
	cache := s.State.scan
	if cache != nil {
		cache.mu.Lock()
		if cache.ns != nil && time.Since(cache.at) < nsScanTTL {
			cached := append([]string(nil), cache.ns...)
			cache.mu.Unlock()
			return cached
		}
		cache.mu.Unlock()
	}

	if s.State.dyn == nil {
		return scanOrder()
	}
	list, err := s.State.dyn.Resource(k8s.Namespaces).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scanOrder()
	}
	// The first-party set is ALWAYS present: it is known-good by definition, and a
	// namespace that does not exist simply yields no CRs (observeFleet tolerates
	// NotFound). Discovery only ever ADDS tenants — so an empty or partial listing
	// degrades to today's behavior instead of blanking the board.
	firstParty := scanOrder()
	known := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		if _, _, ok := nsClass(list.Items[i].GetName()); ok {
			known[list.Items[i].GetName()] = true
		}
	}
	out := make([]string, 0, len(known)+len(firstParty))
	for _, ns := range firstParty { // stable, main-first
		out = append(out, ns)
		delete(known, ns)
	}
	rest := make([]string, 0, len(known))
	for ns := range known {
		rest = append(rest, ns)
	}
	sort.Strings(rest)
	out = append(out, rest...)

	if cache != nil {
		cache.mu.Lock()
		cache.ns, cache.at = out, time.Now()
		cache.mu.Unlock()
	}
	return append([]string(nil), out...)
}

// appNameRE constrains the :app path segment to a DNS-1123 label (every Service
// CR metadata.name satisfies this). Validated at the boundary; it is the
// injection guard for the CR name a deploy/read targets.
var appNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// imageRepoRE constrains a deploy's target image repository. A registry path of
// host/namespace/name segments (letters, digits, ., -, _, /). The tag is
// validated separately (deploy accepts any non-empty tag so a controlled hotfix
// to a floating tag is possible, but the drift board then flags it loudly).
var imageRepoRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*[a-z0-9]$`)

const fleetUserAgent = "hanzo-cloud-platform-fleet"

// fleetState is the fleet board's own data; shared deps live in the embedded cloud.Base.
type fleetState struct {
	dyn     dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health + ready()

	// the discovered scan set (discoverNamespaces), rebuilt every nsScanTTL so a
	// newly-onboarded tenant namespace appears without a redeploy. A POINTER:
	// cloud.Service stores State by value, so a mutex living directly in `state`
	// would be copied (go vet: "assignment copies lock value") and each copy would
	// guard nothing. One cache, shared by every copy of the struct.
	scan *nsCache
}

// nsCache is the discovered scan set behind its own lock.
type nsCache struct {
	mu sync.Mutex
	ns []string
	at time.Time
}

// buildFleet resolves the in-process k8s dynamic client (fail-closed: when no
// kubeconfig resolves the board still mounts and every endpoint 503s honestly).
func buildFleet(b cloud.Base) fleetState {
	st := fleetState{scan: &nsCache{}}
	if dyn, err := newFleetDynamic(); err != nil {
		st.initErr = err.Error()
		b.Log.Warn("kubernetes client unavailable; /v1/platform/fleet will fail closed", "err", err)
	} else {
		st.dyn = dyn
	}
	b.Log.Info("fleet board mounted",
		"prefix", "/v1/platform/fleet", "k8s", st.dyn != nil, "brand", b.Brand, "env", b.Env)
	return st
}

// fleetRoutes registers the /v1/platform/fleet surface. Every mutating/observing
// route is behind the IAM guard (SuperAdmin or org-confined OrgAdmin).
//
// It is a SIBLING of the /v1/platform/projects/:project/apps surface, not a second
// copy of it: `fleet` is the platform's OWN service tier (the operator App CRs the
// platform runs on), `projects/:project/apps` is a CUSTOMER's apps. Two different
// collections need two different names — calling both "apps" under one prefix would
// be the duplicate definition this fold exists to remove.
func fleetRoutes(app cloud.Router, s *cloud.Service[fleetState]) {
	// FLAT paths, not a Group: `Group("/v1/platform/fleet").Get("")` composes to the
	// literal "/v1/platform/fleet/", and that trailing slash is what the OpenAPI
	// emitter publishes — so every generated SDK would call a path the manifest
	// prefix does not name. Fiber happens to match both forms, which is exactly why
	// this hides: the router forgives it and the CONTRACT does not.
	app.Get("/v1/platform/fleet", fleetGuard(s, cloud.Handle(s, listFleet)))
	app.Get("/v1/platform/fleet/:app", fleetGuard(s, cloud.Handle(s, getFleetApp)))
	// MUTATION is superadmin-only (fleetOperatorGuard), NOT the broader read guard: the
	// only namespaces this board scans are the platform's OWN tier (hanzo{,-testnet,
	// -devnet}), so a rolling restart here recreates a SHARED platform service
	// (iam/kms/gateway/…). Per the 2026-07-08 admin-org P0 a brand-org ("hanzo")
	// admin is a CUSTOMER-org admin, not a platform operator — restarting prod iam is
	// a platform-operator action. Gating the read board (below) any wider is bounded
	// (observe, audit-logged); gating a restart wider is a live DoS lever (RED H1).
	app.Post("/v1/platform/fleet/:app/deploy", fleetOperatorGuard(s, cloud.Handle(s, deployFleet)))

	// Native release seam: install the first-party CR-rollout hook (build.go's
	// RegisterServiceReleaser inversion) so a proven, clean-semver image rolls onto
	// its Service CR here — the direct-CR replacement for the image-update.yml
	// GitOps hop (rollout.go).
	registerReleaser(s)

	// Internal plane: platform.fleet answers THIS board's observation, bound to THIS
	// service, so the admin god-view (/v1/admin/products + the overview drift KPIs)
	// gets the same scan and the same tenant confinement listFleet applies (rpc.go).
	// Registering here rather than in Mount is what lets the method hold s: an
	// exposure with no service has only the package global to read, and that global
	// is the in-process seam this whole move exists to delete.
	exposeFleet(s)
}

// guard authorizes the PaaS fleet board off ONE IAM identity, exactly like the
// /v1/runner build path (clients/platform/runner.go runnerIAMAdmin): a validated
// principal (principal.Validated — X-User-Id minted from a signature-verified JWT,
// unforgeable off-gateway) who is a SuperAdmin OR an OrgAdmin. Fail-closed: any
// other request is refused 403 before the handler — no cluster object is read or
// mutated.
//
// The ROLE only opens the door; the TENANT boundary is enforced inside each
// handler by scopedNamespaces(c): a SuperAdmin observes the whole fleet, an
// OrgAdmin is CONFINED to the platform namespaces its own validated org owns, so a
// tenant admin can never observe — or restart — another org's, or a platform, app.
// This mirrors runner.go's org attribution (default to the caller's org, refuse a
// foreign one) for a READ/RESTART surface. Broadening from SuperAdmin-only lets the
// platform operator drive the board off a plain `hanzo login` with no shared token.
func fleetGuard(s *cloud.Service[fleetState], h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		p := requestPrincipal(c)
		if !p.validated {
			return zip.ErrForbidden("authentication required (run `hanzo login`)")
		}
		if !p.mayObserve() {
			return zip.ErrForbidden("admin required")
		}
		return h(c)
	}
}

// operatorGuard restricts a route to a platform SUPERADMIN (a member of the
// reserved admin org — principal.IsSuperAdmin). It is stricter than guard on
// purpose: the mutating deploy/restart acts on the platform's OWN service tier, so
// it is a platform-operator action, NOT one a customer-org admin — even an admin of
// the brand org — may take (the 2026-07-08 admin-org P0: "a plain hanzo admin is a
// customer-org admin only"). A validated OrgAdmin who is not a SuperAdmin is refused
// 403 here, closing the fleet-restart DoS lever (RED H1) while the read board stays
// on the broader guard.
func fleetOperatorGuard(s *cloud.Service[fleetState], h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !principal.Validated(c) {
			return zip.ErrForbidden("authentication required (run `hanzo login`)")
		}
		if !principal.IsSuperAdmin(c) {
			return zip.ErrForbidden("deploy is a platform-operator action — requires a superadmin identity (a member of the admin org)")
		}
		return h(c)
	}
}

// fleetPrincipal is WHO is asking, reduced to the only facts the fleet surface
// authorizes on: whether a validated principal is present at all, platform sudo,
// admin-of-one's-own-org, and the validated tenant key. It is a VALUE rather than a
// place, because the SAME caller now arrives two ways — as an HTTP request on
// /v1/platform/fleet, and as a delegated capability on the internal plane
// (rpc.go) — and the two must reach the identical verdict. Extraction differs;
// the rule below does not.
//
// The two admin bits stay APART (apps/principal: conflating them is a privilege
// escalation). super is cross-tenant platform sudo; orgAdmin administers only its
// own org and is never platform-privileged — it opens the door, and the tenant
// boundary below is what confines it.
type fleetPrincipal struct {
	validated bool   // a credential was verified (X-User-Id was minted, not restored)
	super     bool   // platform sudo: a member of the reserved admin org
	orgAdmin  bool   // admin OF ITS OWN org — org-scoped, not platform-privileged
	org       string // the VALIDATED tenant key; "" when the caller carries none
}

// requestPrincipal reads one off an HTTP request — the authority headers
// SanitizeIdentity strips on ingress and re-mints only from validated claims.
func requestPrincipal(c *zip.Ctx) fleetPrincipal {
	org, _ := principal.Org(c)
	return fleetPrincipal{
		validated: principal.Validated(c),
		super:     principal.IsSuperAdmin(c),
		orgAdmin:  principal.IsOrgAdmin(c),
		org:       org,
	}
}

// capPrincipal reads one off the identity a plane call carries. It is the SAME
// set of headers requestPrincipal reads, forwarded by zip from the gateway's
// assertion — a peer can only pass on authority it already held — and the org
// key goes through principal.OrgOf, the same decision Org applies to a request.
// So a caller cannot widen itself by crossing the socket.
func capPrincipal(who zip.Caller) fleetPrincipal {
	org, _ := principal.OrgOf(who.User, who.Org)
	return fleetPrincipal{
		validated: strings.TrimSpace(who.User) != "",
		super:     who.Admin,
		orgAdmin:  who.OrgAdmin,
		org:       org,
	}
}

// mayObserve is the ROLE gate — the whole door, in one expression: a validated
// principal that is a SuperAdmin OR an admin of its own org. It only opens the
// door; scopeNamespaces is what confines whoever walks through.
func (p fleetPrincipal) mayObserve() bool { return p.validated && (p.super || p.orgAdmin) }

// scopeNamespaces is the TENANT boundary — the one confinement rule, applied to the
// scanned set. A SuperAdmin sees every scanned namespace (the whole fleet). A
// non-super caller sees ONLY the namespaces its own validated org owns
// (nsOrg(ns) == org), bounded to its own tenant exactly as /v1/runner bounds a build
// to the caller's org. The org comes from the validated principal — never a client
// header, never a request or payload field — so it cannot be widened by a forged
// X-Org-Id or by anything a peer puts on the wire. A caller whose org owns no
// scanned namespace gets an EMPTY set (an empty board / a clean 404), never another
// org's data.
//
// It is applied AT THE SCAN, before any CR is read, so a confined caller never even
// lists another org's apps — the boundary is not a filter over rows already fetched.
func scopeNamespaces(all []string, p fleetPrincipal) []string {
	if p.super {
		return all
	}
	if p.org == "" {
		return nil
	}
	out := make([]string, 0, len(all))
	for _, ns := range all {
		if nsOrg(ns) == p.org {
			out = append(out, ns)
		}
	}
	return out
}

// scopedNamespaces is scopeNamespaces over an HTTP request: discover the scanned
// set, then confine it to what this caller may observe.
func scopedNamespaces(s *cloud.Service[fleetState], ctx context.Context, c *zip.Ctx) []string {
	return scopeNamespaces(discoverNamespaces(s, ctx), requestPrincipal(c))
}

// targetNamespaces narrows the caller's authorized namespaces (scopedNamespaces)
// to a single lifecycle env when ?env=main|test|dev is given, else returns them all
// in prod-first scan order. It composes the AUTH confinement (scopedNamespaces) with
// the optional env SELECTION — orthogonal: env can only narrow WITHIN the caller's
// own authorized set, never reach outside it.
func targetNamespaces(s *cloud.Service[fleetState], ctx context.Context, c *zip.Ctx) []string {
	nss := scopedNamespaces(s, ctx, c)
	env := strings.TrimSpace(c.Query("env"))
	if env == "" {
		return nss
	}
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		if envOf(ns) == env {
			out = append(out, ns)
		}
	}
	return out
}

// nsOrg returns the platform org that OWNS a scanned namespace: the namespace with
// its lifecycle-env suffix removed ("hanzo"→hanzo, "hanzo-testnet"→hanzo,
// "hanzo-devnet"→hanzo). It is the tenant key a namespace belongs to — the axis
// scopedNamespaces confines a non-super OrgAdmin to. Derived from nsClass.
func nsOrg(ns string) string { tenant, _, _ := nsClass(ns); return tenant }

// nsForEnv maps a lifecycle env to its scanned namespace ("main"→hanzo,
// "test"→hanzo-testnet, "dev"→hanzo-devnet), "" for an unknown env. The inverse of
// envOf over the scanned set, used to reject an invalid ?env with a clean 400.
func nsForEnv(env string) string {
	for _, ns := range scanOrder() {
		if envOf(ns) == env {
			return ns
		}
	}
	return ""
}

// ── observe: the drift board (inventory.ts) ──────────────────────────────────

// AppView is one service row on the drift board: the observed tags + topology +
// the derived drift verdict. It is the Go analogue of the platform's `AppView`
// (apps-api.ts) so console renders the same shape the Dokploy board did.
type AppView struct {
	ID          string   `json:"id"`   // <org>/<app>/<env>, e.g. hanzoai/iam/main
	Org         string   `json:"org"`  // image namespace, e.g. hanzoai
	App         string   `json:"app"`  // service / CR name, e.g. iam
	Env         string   `json:"env"`  // main|test|dev
	Repo        string   `json:"repo"` // owner/repo, e.g. hanzoai/iam
	Registry    string   `json:"registry"`
	Role        string   `json:"role"` // operator spec.role (sql|kv|generic|ingress|…) or "" — the one declared class field
	DeclaredTag string   `json:"declaredTag"`
	RunningTag  string   `json:"runningTag"`
	LatestTag   string   `json:"latestTag"`
	Health      string   `json:"health"` // green|yellow|red|"" (unknown)
	Phase       string   `json:"phase"`  // operator status.phase (Running/…)
	Cluster     string   `json:"cluster"`
	Namespace   string   `json:"namespace"`
	Endpoints   []string `json:"endpoints"`
	Drift       Drift    `json:"drift"`
}

// listApps returns the whole fleet's drift board, ordered deterministically
// (org, app, env). Optional narrowing filters mirror the platform board:
// ?env=, ?health=, ?drift=1 (only rows that are actually drifting), ?org=.
func listFleet(s *cloud.Service[fleetState], c *zip.Ctx) error {
	if err := fleetReady(s); err != nil {
		return err
	}
	// Observe only the namespaces this caller is authorized for: the whole fleet for
	// a SuperAdmin, the caller's own org namespaces for an OrgAdmin (empty board for
	// an org that owns none). The tenant boundary is applied at the scan, before any
	// CR is read, so a non-super caller never even lists another org's apps.
	views, err := observeFleet(s, c.Context(), scopedNamespaces(s, c.Context(), c))
	if err != nil {
		return err
	}

	env := strings.TrimSpace(c.Query("env"))
	fleetHealth := strings.TrimSpace(c.Query("health"))
	org := strings.TrimSpace(c.Query("org"))
	driftOnly := c.Query("drift") == "1" || c.Query("drift") == "true"

	out := make([]AppView, 0, len(views))
	byDrift := map[DriftSeverity]int{SeverityOK: 0, SeverityYellow: 0, SeverityRed: 0}
	for _, v := range views {
		if env != "" && v.Env != env {
			continue
		}
		if fleetHealth != "" && v.Health != fleetHealth {
			continue
		}
		if org != "" && v.Org != org {
			continue
		}
		if driftOnly && v.Drift.Severity == SeverityOK {
			continue
		}
		out = append(out, v)
		byDrift[v.Drift.Severity]++
	}

	return c.JSON(http.StatusOK, map[string]any{
		"apps": out,
		"summary": map[string]any{
			"total": len(out),
			"byDrift": map[string]int{
				"ok":     byDrift[SeverityOK],
				"yellow": byDrift[SeverityYellow],
				"red":    byDrift[SeverityRed],
			},
		},
	})
}

// getApp returns one service row by CR name. Scans the platform namespaces in
// env order (main→test→dev) and returns the first match, so the bare app name
// resolves to production by default.
func getFleetApp(s *cloud.Service[fleetState], c *zip.Ctx) error {
	if err := fleetReady(s); err != nil {
		return err
	}
	name := fleetReqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	for _, ns := range targetNamespaces(s, c.Context(), c) {
		obj, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return fleetK8sErr(s, "get", err)
		}
		repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
		return c.JSON(http.StatusOK, observeCR(obj, ns, envOf(ns), runningTagOf(s, c.Context(), ns, name, repository)))
	}
	return zip.ErrNotFound("app not found in the platform namespaces")
}

// observeFleet lists every App CR across the given namespaces (the caller's
// authorized set, per scopedNamespaces) and maps each to an AppView. A namespace
// that does not exist / is empty is skipped, never fatal (the board must still
// render the reachable namespaces).
func observeFleet(s *cloud.Service[fleetState], ctx context.Context, namespaces []string) ([]AppView, error) {
	var views []AppView
	for _, ns := range namespaces {
		// Running state: one Deployment list per namespace, indexed by name — the
		// running-tag source (inventory.ts). Best-effort: a Deployment RBAC/list
		// error leaves runningTag empty (an honest unknown) rather than failing the
		// whole board, so the declared/health/phase columns still render.
		running := runningTagsIn(s, ctx, ns)
		env := envOf(ns)
		list, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fleetK8sErr(s, "list", err)
		}
		for i := range list.Items {
			cr := &list.Items[i]
			views = append(views, observeCR(cr, ns, env, running[cr.GetName()]))
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Org != views[j].Org {
			return views[i].Org < views[j].Org
		}
		if views[i].App != views[j].App {
			return views[i].App < views[j].App
		}
		return views[i].Env < views[j].Env
	})
	return views, nil
}

// ── deploy: rolling restart (zero-downtime) ──────────────────────────────────

// restartedAtAnnotation is the pod-template annotation a rolling restart stamps —
// the exact key/mechanism `kubectl rollout restart` uses, so the deploy path is a
// first-class rollout, not a bespoke trigger.
const restartedAtAnnotation = "hanzo.ai/restartedAt"

// deploy performs a zero-downtime ROLLING RESTART of the app's Deployment: it
// stamps the pod-template restartedAt annotation (the `kubectl rollout restart`
// mechanism), which the default RollingUpdate strategy turns into a graceful
// pod-by-pod recreate that re-pulls the declared image.
//
// SUPERADMIN-only (operatorGuard on the route): the board scans only the platform's
// OWN tier, so a restart here recreates a shared platform service — a platform-
// operator action, not a customer-org-admin one (RED H1). The env MUST be named
// explicitly (?env=main|test|dev) — a bare deploy never silently targets production
// (RED L1). Within that env it targets only the namespaces the caller is authorized
// for (targetNamespaces).
//
// It deliberately does NOT change the declared image TAG: that is the one property
// Hanzo CD's selfHeal reconciles from universe git, so a tag change is still a git
// commit (the one way to change WHAT runs). A restart re-runs WHAT IS DECLARED with
// no drift for CD to revert — the honest, GitOps-compatible "redeploy this app".
func deployFleet(s *cloud.Service[fleetState], c *zip.Ctx) error {
	if err := fleetReady(s); err != nil {
		return err
	}
	name := fleetReqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	// L1: never SILENTLY target production. A restart must name its lifecycle env
	// explicitly (?env=main|test|dev) — a bare deploy no longer defaults to the
	// prod (main) namespace, closing the fat-finger / confused-deputy prod hazard.
	env := strings.TrimSpace(c.Query("env"))
	if env == "" {
		return zip.ErrBadRequest("specify ?env=main|test|dev — deploy does not default to production")
	}
	if nsForEnv(env) == "" {
		return zip.ErrBadRequest("env must be one of main|test|dev")
	}
	ns, err := resolveTargetIn(s, c.Context(), name, targetNamespaces(s, c.Context(), c))
	if err != nil {
		return err
	}
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, restartedAtAnnotation, restartedAt)
	if _, err := s.State.dyn.Resource(k8s.Deployments).Namespace(ns).Patch(
		c.Context(), name, k8stypes.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return zip.ErrNotFound("app " + name + " has no Deployment to restart in " + ns)
		}
		return fleetK8sErr(s, "restart", err)
	}
	s.Log.Info("fleet rolling restart", "app", name, "namespace", ns, "restartedAt", restartedAt, "actor", principal.Owner(c))
	return c.JSON(http.StatusAccepted, map[string]any{
		"ok": true, "app": name, "namespace": ns, "env": envOf(ns), "restartedAt": restartedAt,
	})
}

// resolveTarget finds the namespace an App CR lives in, scanning ALL platform
// namespaces in env order (main→test→dev) so a bare lookup targets production. The
// release path (release.go) uses this — it is a machine rollout with no per-caller
// identity to confine — so it keeps the full scan. Returns a clean 404 when the App
// exists in none of them.
func resolveTarget(s *cloud.Service[fleetState], ctx context.Context, name string) (string, error) {
	return resolveTargetIn(s, ctx, name, scanOrder())
}

// resolveTargetIn is resolveTarget's core over an EXPLICIT namespace list — the
// identity-scoped deploy/getApp pass the caller's authorized set so an OrgAdmin can
// never resolve (and thus restart/read) an app outside its own org. An empty list
// (an OrgAdmin owning no scanned namespace) resolves to a clean 404, never a leak.
func resolveTargetIn(s *cloud.Service[fleetState], ctx context.Context, name string, namespaces []string) (string, error) {
	for _, ns := range namespaces {
		if _, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ns, nil
		} else if !apierrors.IsNotFound(err) {
			return "", fleetK8sErr(s, "get", err)
		}
	}
	return "", zip.ErrNotFound("app " + name + " not found in the platform namespaces")
}

// ── k8s plumbing ────────────────────────────────────────────────────────────

func fleetReady(s *cloud.Service[fleetState]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "platform fleet: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials name
// the missing access so the operator knows exactly what to grant the cloud service
// account (get/list on apps.hanzo.ai). Mirrors ml.k8sErr.
func fleetK8sErr(s *cloud.Service[fleetState], op string, err error) error {
	s.Log.Error("k8s op failed", "op", op, "resource", k8s.Apps.Resource, "err", err)
	if apierrors.IsForbidden(err) {
		return zip.Errorf(http.StatusBadGateway,
			"%s apps: kubernetes RBAC denied (cloud service account needs %s on apps.hanzo.ai): %v",
			op, op, err)
	}
	return zip.Errorf(http.StatusBadGateway, "%s apps failed: %v", op, err)
}

// newDynamic builds the dynamic client from the in-cluster service account,
// falling back to KUBECONFIG / ~/.kube/config for local/dev — identical to
// clients/ml.newDynamic.
func newFleetDynamic() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	}
	cfg.UserAgent = fleetUserAgent
	return dynamic.NewForConfig(cfg)
}

// ── pure mapping helpers (unit-tested without a cluster) ─────────────────────

func fleetReqApp(c *zip.Ctx) string { return strings.ToLower(strings.TrimSpace(c.Param("app"))) }

// scanOrder returns the platform namespaces in a stable env order (main first),
// so a bare app-name read/deploy resolves to production before test/dev.
// Every entry MUST classify under nsClass (asserted in fleet_test.go) — nsClass is
// the one place that decides what a namespace means, and a namespace listed here
// but unclassified would render rows with an empty tenant, i.e. rows no OrgAdmin
// could ever be confined to. hanzo-mainnet was missing until 2026-07-25, so its
// CRs were invisible on the board.
//
// First-party only: a `tenant-<org>` namespace classifies correctly (nsClass) but
// is not yet DISCOVERED, because the scan set is computed without a cluster client.
// Threading the dynamic client through so the set is listed from the cluster is the
// follow-up that makes tenant workloads appear; the authorization axis they need
// already exists.
func scanOrder() []string {
	return []string{"hanzo", "hanzo-mainnet", "hanzo-testnet", "hanzo-devnet"}
}

// orgFromRepository derives the image namespace ("org") from an image repo:
// `ghcr.io/hanzoai/chat` → `hanzoai`; `docker.io/grafana/grafana` → `grafana`.
// Falls back to the whole repo when it has no namespace segment. Ported verbatim
// from inventory.ts `orgFromRepository`.
func orgFromRepository(repository string) string {
	parts := nonEmpty(strings.Split(repository, "/"))
	if len(parts) >= 3 {
		return parts[1]
	}
	if len(parts) == 2 {
		return parts[0]
	}
	return repository
}

// repoFromRepository derives the owner/repo GitHub coordinate from an image repo:
// `ghcr.io/hanzoai/chat` → `hanzoai/chat` (the image path minus the registry
// host). Ported verbatim from inventory.ts `repoFromRepository`.
func repoFromRepository(repository string) string {
	parts := nonEmpty(strings.Split(repository, "/"))
	if len(parts) >= 3 {
		return strings.Join(parts[1:], "/")
	}
	return strings.Join(parts, "/")
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// healthFromStatus rolls the operator's reconciled Service status up to the
// apps-table health vocabulary. The operator populates status.readyReplicas /
// status.replicas (and phase); we prefer that reconciled truth over re-deriving
// from the Deployment (the operator already did that join). Mirrors
// inventory.ts healthFromDeployment semantics: desired 0 ⇒ yellow (intentionally
// scaled to zero, not unhealthy), ready>=desired ⇒ green, some ready ⇒ yellow,
// none ⇒ red. Empty when the status carries no replica counts yet.
func fleetHealthFromStatus(status map[string]any) string {
	desired, hasDesired := fleetNestedInt(status, "replicas")
	fleetReady, _ := fleetNestedInt(status, "readyReplicas")
	if !hasDesired {
		// Fall back to availableReplicas if the operator only reports that.
		if avail, ok := fleetNestedInt(status, "availableReplicas"); ok {
			if avail > 0 {
				return "green"
			}
			return "red"
		}
		return "" // no replica signal yet — unknown, never a fabricated green
	}
	if desired == 0 {
		return "yellow"
	}
	if fleetReady >= desired {
		return "green"
	}
	if fleetReady > 0 {
		return "yellow"
	}
	return "red"
}

// observeCR maps one App CR (+ its operator-reconciled status + the running tag
// observed from the live Deployment) into an AppView, attaching the drift verdict.
// This is inventory.ts observeService fused with apps-api.ts toAppView:
// declared tag from the CR spec, running tag from the Deployment (passed in),
// health + phase + endpoints from the operator-reconciled CR status.
func observeCR(obj *unstructured.Unstructured, namespace, env, runningTag string) AppView {
	name := obj.GetName()
	repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
	declaredTag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	role, _, _ := unstructured.NestedString(obj.Object, "spec", "role")

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	endpoints := nestedStringSlice(status, "endpoints")

	obs := Observed{DeclaredTag: declaredTag, RunningTag: runningTag}
	return AppView{
		ID:          orgFromRepository(repository) + "/" + name + "/" + env,
		Org:         orgFromRepository(repository),
		App:         name,
		Env:         env,
		Repo:        repoFromRepository(repository),
		Registry:    repository,
		Role:        role,
		DeclaredTag: declaredTag,
		RunningTag:  runningTag,
		LatestTag:   "", // GH-release reader is a follow-up phase (release-reader.ts)
		Health:      fleetHealthFromStatus(status),
		Phase:       phase,
		Cluster:     "hanzo-k8s",
		Namespace:   namespace,
		Endpoints:   endpoints,
		Drift:       ComputeDrift(obs),
	}
}

// runningTagsIn lists the Deployments in a namespace and returns a map of
// Deployment-name → running image tag (the container whose image repo the caller
// later matches against the CR's declared repo, in runningTagOf; here we index by
// name and keep the first container's tag as the default). Best-effort: any list
// error yields an empty map so the board still renders declared/health/phase.
func runningTagsIn(s *cloud.Service[fleetState], ctx context.Context, namespace string) map[string]string {
	out := map[string]string{}
	list, err := s.State.dyn.Resource(k8s.Deployments).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Log.Warn("list deployments for running tag failed; running tag will be empty",
			"namespace", namespace, "err", err)
		return out
	}
	for i := range list.Items {
		d := &list.Items[i]
		out[d.GetName()] = firstContainerTag(d)
	}
	return out
}

// runningTagOf reads a single Deployment's running tag, matching the container
// whose image repo equals the CR's declared repo (so a sidecar like replicate/otel
// is never mistaken for the app), falling back to the first container. Mirrors
// inventory.ts runningTagFromDeployment. Best-effort: any error → "".
func runningTagOf(s *cloud.Service[fleetState], ctx context.Context, namespace, name, declaredRepository string) string {
	d, err := s.State.dyn.Resource(k8s.Deployments).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return runningTagFromDeployment(d, declaredRepository)
}

// nestedInt reads an integer-valued key from an unstructured map, tolerating the
// int64/float64 the k8s decoder may produce.
func fleetNestedInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// nestedStringSlice reads a []string key from an unstructured map (the k8s decoder
// yields []any of string).
func nestedStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// deploymentContainers extracts the pod-template container images from an
// unstructured Deployment (spec.template.spec.containers[].image).
func deploymentContainers(dep *unstructured.Unstructured) []string {
	if dep == nil {
		return nil
	}
	raw, ok, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	if !ok {
		return nil
	}
	imgs := make([]string, 0, len(raw))
	for _, c := range raw {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if img, ok := cm["image"].(string); ok && img != "" {
			imgs = append(imgs, img)
		}
	}
	return imgs
}

// runningTagFromDeployment picks the running tag from a Deployment by matching the
// container whose image repository equals the CR's declared repository (so a
// sidecar can never be mistaken for the app), falling back to the first container.
// Mirrors inventory.ts runningTagFromDeployment.
func runningTagFromDeployment(dep *unstructured.Unstructured, declaredRepository string) string {
	imgs := deploymentContainers(dep)
	if len(imgs) == 0 {
		return ""
	}
	for _, img := range imgs {
		if repoFromImageRef(img) == declaredRepository {
			return tagFromImageRef(img)
		}
	}
	return tagFromImageRef(imgs[0])
}

// firstContainerTag is the default running tag for the namespace-indexed map: the
// first container's tag. The per-service exact match (runningTagFromDeployment)
// is used when the declared repo is known; this keeps the list pass O(deployments)
// without a Get per service.
func firstContainerTag(dep *unstructured.Unstructured) string {
	imgs := deploymentContainers(dep)
	if len(imgs) == 0 {
		return ""
	}
	return tagFromImageRef(imgs[0])
}

// repoFromImageRef splits `ghcr.io/hanzoai/iam:v1` → `ghcr.io/hanzoai/iam`.
// A digest ref (`repo@sha256:…`) keeps the repo; a bare repo returns itself.
func repoFromImageRef(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	// A ':' after the last '/' is the tag separator (a ':' in a registry host:port
	// segment lives before a '/', so guard on the last slash).
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon]
	}
	return ref
}

// tagFromImageRef splits `ghcr.io/hanzoai/iam:v1` → `v1`. A digest ref returns the
// digest; a bare repo (no tag) returns "".
func tagFromImageRef(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[at+1:]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash && colon < len(ref)-1 {
		return ref[colon+1:]
	}
	return ""
}
