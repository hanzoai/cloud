// paas.go — the cluster-facing half of the native Hanzo PaaS control plane.
//
// It mounts /v1/paas/* on the unified cloud binary and reads the operator's
// `hanzo.ai/v1` `App` CustomResource — the one workload kind the fleet runs on:
//
//	GET  /v1/paas/apps            — the fleet drift board (inventory.ts): list every
//	                                operator App CR across the platform namespaces,
//	                                read declared vs running tag + health from the CR
//	                                (+ its status), and attach the drift verdict
//	                                (drift.go / apps-drift.ts).
//	GET  /v1/paas/apps/:app       — one app row by CR name.
//	POST /v1/paas/apps/:app/deploy— zero-downtime ROLLING RESTART of the app's
//	                                Deployment (the `kubectl rollout restart`
//	                                mechanism): re-pulls the DECLARED image, recreates
//	                                pods gracefully. It never changes the declared
//	                                TAG (that stays a git commit, the one thing Hanzo
//	                                CD's selfHeal reconciles), so there is no drift to
//	                                revert. This is `hanzo deploy`.
//	GET  /v1/paas/health          — real k8s reachability + App CRD presence.
//
// SECURITY — every route is authorized off ONE IAM identity, exactly like the
// /v1/runner build path (clients/platform/runner.go): the guard admits a validated
// principal (principal.Validated) who is a SuperAdmin OR an OrgAdmin, and each
// handler then CONFINES a non-super caller to its own org's platform namespaces
// (scopedNamespaces, keyed on principal.Org — never a client header). A SuperAdmin
// observes/acts on the whole fleet; an OrgAdmin only on the namespaces its org owns;
// a plain member or an unauthenticated caller is refused 403. So a tenant admin can
// never observe — or restart — another org's, or a platform, app, and the platform
// operator drives the board off a plain `hanzo login` with no shared token. The
// user-facing per-app PaaS view still lives in console; this is the CLI/operator
// surface.
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev — the identical
// construction clients/ml uses. When no kubeconfig is resolvable the subsystem
// mounts anyway and every endpoint fails closed (503 + the real init error; the
// health route reports "degraded"), never status-theater.
package paas

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// appsGVR is the operator App CR — the one workload kind the fleet runs on. The
// drift board reads it across the platform namespaces. Asserted in the tests; a
// typo here silently breaks the whole board.
var appsGVR = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "apps"}

// deploymentsGVR is the live Deployment behind each Service — the source of the
// RUNNING tag (the operator Service CR status does not surface the running image,
// so the running tag is observed from the Deployment's container, exactly as the
// platform inventory reads it in inventory.ts). Read-only for this subsystem.
var deploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

// nsEnv maps each scanned namespace to the lifecycle env it represents, mirroring
// the platform inventory's DEFAULT_TARGETS (inventory.ts): the `hanzo` namespace
// is production (main); the env-split namespaces map to test/dev. Only listed
// namespaces are scanned — the reader never reaches beyond the platform tier.
// Cross-cluster federation (lux-k8s/zoo-k8s) is a follow-up phase (a per-cluster
// client from a KMS-loaded kubeconfig), exactly as the Node inventory federates.
var nsEnv = map[string]string{
	"hanzo":         "main",
	"hanzo-testnet": "test",
	"hanzo-devnet":  "dev",
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

const userAgent = "hanzo-cloud-paas"

// state is paas's own data; shared deps live in the embedded cloud.Base.
type state struct {
	dyn     dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health + ready()
}

// Mount wires the /v1/paas/* surface onto app. Every handler is behind the IAM
// guard (SuperAdmin or OrgAdmin, org-confined), then reads/patches the operator
// App CRs + their Deployments.
func Mount(app *zip.App, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "paas", build, routes)
}

// build resolves the in-process k8s dynamic client (fail-closed: when no kubeconfig
// resolves the subsystem still mounts and every endpoint 503s honestly).
func build(b cloud.Base) (state, error) {
	var st state
	if dyn, err := newDynamic(); err != nil {
		st.initErr = err.Error()
		b.Log.Warn("kubernetes client unavailable; /v1/paas endpoints will fail closed", "err", err)
	} else {
		st.dyn = dyn
	}
	b.Log.Info("paas control plane mounted",
		"prefix", "/v1/paas", "k8s", st.dyn != nil, "brand", b.Brand, "env", b.Env)
	return st, nil
}

// routes registers the /v1/paas/* surface. Every mutating/observing route is behind
// the IAM guard (SuperAdmin or org-confined OrgAdmin); the health probe is public
// (real k8s reachability).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/paas/apps", guard(s, cloud.Handle(s, listApps)))
	app.Get("/v1/paas/apps/:app", guard(s, cloud.Handle(s, getApp)))
	// MUTATION is superadmin-only (operatorGuard), NOT the broader read guard: the
	// only namespaces this board scans are the platform's OWN tier (hanzo{,-testnet,
	// -devnet}), so a rolling restart here recreates a SHARED platform service
	// (iam/kms/gateway/…). Per the 2026-07-08 admin-org P0 a brand-org ("hanzo")
	// admin is a CUSTOMER-org admin, not a platform operator — restarting prod iam is
	// a platform-operator action. Gating the read board (below) any wider is bounded
	// (observe, audit-logged); gating a restart wider is a live DoS lever (RED H1).
	app.Post("/v1/paas/apps/:app/deploy", operatorGuard(s, cloud.Handle(s, deploy)))
	app.Get("/v1/paas/health", cloud.Handle(s, health))

	// Native release seam: install the first-party CR-rollout hook (build.go's
	// RegisterServiceReleaser inversion) so a proven, clean-semver image rolls onto
	// its Service CR here — the direct-CR replacement for the image-update.yml
	// GitOps hop (release.go).
	registerReleaser(s)
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
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !principal.Validated(c) {
			return zip.ErrForbidden("authentication required (run `hanzo login`)")
		}
		if principal.IsSuperAdmin(c) || principal.IsOrgAdmin(c) {
			return h(c)
		}
		return zip.ErrForbidden("admin required")
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
func operatorGuard(s *cloud.Service[state], h zip.Handler) zip.Handler {
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

// scopedNamespaces returns the platform namespaces the caller may observe/act on —
// the TENANT confinement for the fleet board. A SuperAdmin sees every scanned
// namespace (the whole fleet). A non-super OrgAdmin sees ONLY the namespaces its
// own validated org owns (nsOrg(ns) == principal.Org), so it is bounded to its own
// tenant exactly as /v1/runner bounds a build to the caller's org. The org is taken
// from the validated principal — never a client header — so it cannot be widened by
// a forged X-Org-Id. An OrgAdmin whose org owns no scanned namespace gets an empty
// set (an empty board / a clean 404), never another org's data.
func scopedNamespaces(c *zip.Ctx) []string {
	all := scanOrder()
	if principal.IsSuperAdmin(c) {
		return all
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(all))
	for _, ns := range all {
		if nsOrg(ns) == org {
			out = append(out, ns)
		}
	}
	return out
}

// targetNamespaces narrows the caller's authorized namespaces (scopedNamespaces)
// to a single lifecycle env when ?env=main|test|dev is given, else returns them all
// in prod-first scan order. It composes the AUTH confinement (scopedNamespaces) with
// the optional env SELECTION — orthogonal: env can only narrow WITHIN the caller's
// own authorized set, never reach outside it.
func targetNamespaces(c *zip.Ctx) []string {
	nss := scopedNamespaces(c)
	env := strings.TrimSpace(c.Query("env"))
	if env == "" {
		return nss
	}
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		if nsEnv[ns] == env {
			out = append(out, ns)
		}
	}
	return out
}

// nsOrg returns the platform org that OWNS a scanned namespace: the namespace with
// its lifecycle-env suffix removed ("hanzo"→hanzo, "hanzo-testnet"→hanzo,
// "hanzo-devnet"→hanzo). It is the tenant key a namespace belongs to — the axis
// scopedNamespaces confines a non-super OrgAdmin to. Kept in lockstep with nsEnv.
func nsOrg(ns string) string {
	return strings.TrimSuffix(strings.TrimSuffix(ns, "-devnet"), "-testnet")
}

// nsForEnv maps a lifecycle env to its scanned namespace ("main"→hanzo,
// "test"→hanzo-testnet, "dev"→hanzo-devnet), "" for an unknown env. The inverse of
// nsEnv, used to reject an invalid ?env with a clean 400 rather than a confusing 404.
func nsForEnv(env string) string {
	for ns, e := range nsEnv {
		if e == env {
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
func listApps(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	// Observe only the namespaces this caller is authorized for: the whole fleet for
	// a SuperAdmin, the caller's own org namespaces for an OrgAdmin (empty board for
	// an org that owns none). The tenant boundary is applied at the scan, before any
	// CR is read, so a non-super caller never even lists another org's apps.
	views, err := observeFleet(s, c.Context(), scopedNamespaces(c))
	if err != nil {
		return err
	}

	env := strings.TrimSpace(c.Query("env"))
	health := strings.TrimSpace(c.Query("health"))
	org := strings.TrimSpace(c.Query("org"))
	driftOnly := c.Query("drift") == "1" || c.Query("drift") == "true"

	out := make([]AppView, 0, len(views))
	byDrift := map[DriftSeverity]int{SeverityOK: 0, SeverityYellow: 0, SeverityRed: 0}
	for _, v := range views {
		if env != "" && v.Env != env {
			continue
		}
		if health != "" && v.Health != health {
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
func getApp(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	for _, ns := range targetNamespaces(c) {
		obj, err := s.State.dyn.Resource(appsGVR).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return k8sErr(s, "get", err)
		}
		repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
		return c.JSON(http.StatusOK, observeCR(obj, ns, nsEnv[ns], runningTagOf(s, c.Context(), ns, name, repository)))
	}
	return zip.ErrNotFound("app not found in the platform namespaces")
}

// observeFleet lists every App CR across the given namespaces (the caller's
// authorized set, per scopedNamespaces) and maps each to an AppView. A namespace
// that does not exist / is empty is skipped, never fatal (the board must still
// render the reachable namespaces).
func observeFleet(s *cloud.Service[state], ctx context.Context, namespaces []string) ([]AppView, error) {
	var views []AppView
	for _, ns := range namespaces {
		// Running state: one Deployment list per namespace, indexed by name — the
		// running-tag source (inventory.ts). Best-effort: a Deployment RBAC/list
		// error leaves runningTag empty (an honest unknown) rather than failing the
		// whole board, so the declared/health/phase columns still render.
		running := runningTagsIn(s, ctx, ns)
		env := nsEnv[ns]
		list, err := s.State.dyn.Resource(appsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, k8sErr(s, "list", err)
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
func deploy(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqApp(c)
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
	ns, err := resolveTargetIn(s, c.Context(), name, targetNamespaces(c))
	if err != nil {
		return err
	}
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, restartedAtAnnotation, restartedAt)
	if _, err := s.State.dyn.Resource(deploymentsGVR).Namespace(ns).Patch(
		c.Context(), name, k8stypes.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return zip.ErrNotFound("app " + name + " has no Deployment to restart in " + ns)
		}
		return k8sErr(s, "restart", err)
	}
	s.Log.Info("paas rolling restart", "app", name, "namespace", ns, "restartedAt", restartedAt, "actor", principal.Owner(c))
	return c.JSON(http.StatusAccepted, map[string]any{
		"ok": true, "app": name, "namespace": ns, "env": nsEnv[ns], "restartedAt": restartedAt,
	})
}

// resolveTarget finds the namespace an App CR lives in, scanning ALL platform
// namespaces in env order (main→test→dev) so a bare lookup targets production. The
// release path (release.go) uses this — it is a machine rollout with no per-caller
// identity to confine — so it keeps the full scan. Returns a clean 404 when the App
// exists in none of them.
func resolveTarget(s *cloud.Service[state], ctx context.Context, name string) (string, error) {
	return resolveTargetIn(s, ctx, name, scanOrder())
}

// resolveTargetIn is resolveTarget's core over an EXPLICIT namespace list — the
// identity-scoped deploy/getApp pass the caller's authorized set so an OrgAdmin can
// never resolve (and thus restart/read) an app outside its own org. An empty list
// (an OrgAdmin owning no scanned namespace) resolves to a clean 404, never a leak.
func resolveTargetIn(s *cloud.Service[state], ctx context.Context, name string, namespaces []string) (string, error) {
	for _, ns := range namespaces {
		if _, err := s.State.dyn.Resource(appsGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ns, nil
		} else if !apierrors.IsNotFound(err) {
			return "", k8sErr(s, "get", err)
		}
	}
	return "", zip.ErrNotFound("app " + name + " not found in the platform namespaces")
}

// ── health ────────────────────────────────────────────────────────────────

// health is a REAL probe: it verifies the API server is reachable and that the
// App CRD — the kind the fleet runs on, and the kind the board is useless without
// — is served, and reports the actual state. 200 only when everything is ok; 503
// + the real reason otherwise (never status-theater). Not admin-gated — liveness
// must be probe-able by the platform/operator without a JWT.
func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "paas", "status": "ok"}
	if s.State.dyn == nil {
		res["status"], res["k8s"], res["error"] = "degraded", false, s.State.initErr
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	if _, err := s.State.dyn.Resource(appsGVR).Namespace("hanzo").List(c.Context(), metav1.ListOptions{Limit: 1}); err != nil {
		res["status"], res["k8s"], res["crd"], res["error"] = "degraded", true, false, err.Error()
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["k8s"], res["crd"] = true, true
	return c.JSON(http.StatusOK, res)
}

// ── k8s plumbing ────────────────────────────────────────────────────────────

func ready(s *cloud.Service[state]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "paas: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials name
// the missing access so the operator knows exactly what to grant the cloud service
// account (get/list on apps.hanzo.ai). Mirrors ml.k8sErr.
func k8sErr(s *cloud.Service[state], op string, err error) error {
	s.Log.Error("k8s op failed", "op", op, "resource", appsGVR.Resource, "err", err)
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
func newDynamic() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	}
	cfg.UserAgent = userAgent
	return dynamic.NewForConfig(cfg)
}

// ── pure mapping helpers (unit-tested without a cluster) ─────────────────────

func reqApp(c *zip.Ctx) string { return strings.ToLower(strings.TrimSpace(c.Param("app"))) }

// scanOrder returns the platform namespaces in a stable env order (main first),
// so a bare app-name read/deploy resolves to production before test/dev.
func scanOrder() []string { return []string{"hanzo", "hanzo-testnet", "hanzo-devnet"} }

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
func healthFromStatus(status map[string]any) string {
	desired, hasDesired := nestedInt(status, "replicas")
	ready, _ := nestedInt(status, "readyReplicas")
	if !hasDesired {
		// Fall back to availableReplicas if the operator only reports that.
		if avail, ok := nestedInt(status, "availableReplicas"); ok {
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
	if ready >= desired {
		return "green"
	}
	if ready > 0 {
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
		DeclaredTag: declaredTag,
		RunningTag:  runningTag,
		LatestTag:   "", // GH-release reader is a follow-up phase (release-reader.ts)
		Health:      healthFromStatus(status),
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
func runningTagsIn(s *cloud.Service[state], ctx context.Context, namespace string) map[string]string {
	out := map[string]string{}
	list, err := s.State.dyn.Resource(deploymentsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
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
func runningTagOf(s *cloud.Service[state], ctx context.Context, namespace, name, declaredRepository string) string {
	d, err := s.State.dyn.Resource(deploymentsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return runningTagFromDeployment(d, declaredRepository)
}

// nestedInt reads an integer-valued key from an unstructured map, tolerating the
// int64/float64 the k8s decoder may produce.
func nestedInt(m map[string]any, key string) (int, bool) {
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
