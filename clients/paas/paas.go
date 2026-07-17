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
//	POST /v1/paas/apps/:app/deploy— refuse (409): an App CR is declared in universe
//	                                git (infra/k8s/operator/crs/) and reconciled by
//	                                Hanzo CD with selfHeal, so a patch here is
//	                                reverted on the next sync. The response names the
//	                                git path to commit the tag to.
//	GET  /v1/paas/health          — real k8s reachability + App CRD presence.
//
// SECURITY — every route is SUPERADMIN ONLY, fail-closed, gated on the SAME
// predicate the rest of cloud uses: c.IsAdmin() (true only for a JWT-validated
// principal whose org is the admin org, matching the gateway's admin-guard — see
// clients/admin). Unlike clients/ml (per-tenant namespaces), the PaaS control
// plane reads SYSTEM App CRs across the whole fleet, so it is admin-only: a tenant
// must never observe another org's — or a platform — app. The user-facing PaaS
// view lives in console; users never call this surface.
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

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

// Mount wires the /v1/paas/* surface onto app. Every handler gates on
// c.IsAdmin() first (SuperAdmin only), then reads/patches the operator Service
// CRs.
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
// the SuperAdmin guard; the health probe is public (real k8s reachability).
func routes(app *zip.App, s *cloud.Service[state]) {
	app.Get("/v1/paas/apps", guard(s, cloud.Handle(s, listApps)))
	app.Get("/v1/paas/apps/:app", guard(s, cloud.Handle(s, getApp)))
	app.Post("/v1/paas/apps/:app/deploy", guard(s, cloud.Handle(s, deploy)))
	app.Get("/v1/paas/health", cloud.Handle(s, health))

	// Native release seam: install the first-party CR-rollout hook (build.go's
	// RegisterServiceReleaser inversion) so a proven, clean-semver image rolls onto
	// its Service CR here — the direct-CR replacement for the image-update.yml
	// GitOps hop (release.go).
	registerReleaser(s)
}

// guard wraps a handler with the SuperAdmin gate. Fail-closed: any request whose
// validated identity is not a SuperAdmin is refused 403 before the handler — no
// cluster object is read or mutated, matching clients/admin.guard.
func guard(s *cloud.Service[state], h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("SuperAdmin required")
		}
		return h(c)
	}
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
	views, err := observeFleet(s, c.Context())
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
	for _, ns := range scanOrder() {
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

// observeFleet lists every App CR across the scanned namespaces and maps each to
// an AppView. A namespace that does not exist / is empty is skipped, never fatal
// (the fleet board must still render the reachable namespaces).
func observeFleet(s *cloud.Service[state], ctx context.Context) ([]AppView, error) {
	var views []AppView
	for _, ns := range scanOrder() {
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

// ── deploy: refuse a git-declared App ────────────────────────────────────────

// deploy refuses. Every App CR in the platform namespaces is declared in universe
// git (infra/k8s/operator/crs/) and reconciled by Hanzo CD with selfHeal, so a
// direct patch here is reverted on the next sync — the tag would appear to deploy
// and then silently roll back. The endpoint confirms the app exists (404 if not)
// and returns 409 naming the git path to commit the tag to, the one way to deploy
// it.
func deploy(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	name := reqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	if _, err := resolveTarget(s, c.Context(), name); err != nil {
		return err
	}
	return zip.Errorf(http.StatusConflict,
		"%s is declared in git (App/%s, universe infra/k8s/operator/crs/%s.yaml) and reconciled by Hanzo CD with selfHeal — a patch here would be reverted on the next sync. Deploy it by committing the tag to that file.",
		name, name, name)
}

// resolveTarget finds the namespace an App CR lives in, scanning in env order
// (main→test→dev) so a bare lookup targets production. Returns a clean 404 when
// the App exists in none of them.
func resolveTarget(s *cloud.Service[state], ctx context.Context, name string) (string, error) {
	for _, ns := range scanOrder() {
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
