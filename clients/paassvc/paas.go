// paas.go — the cluster-facing half of the native Hanzo PaaS control plane.
//
// It mounts /v1/paas/* on the unified cloud binary and speaks to the SAME
// operator surface the standalone platform's deploy-executor drove: the
// `hanzo.ai/v1` `services` CustomResource. Two responsibilities, both a straight
// port of the Node platform:
//
//	GET  /v1/paas/apps            — the fleet drift board (inventory.ts): list every
//	                                operator Service CR across the platform
//	                                namespaces, read declared vs running tag +
//	                                health from the CR (+ its status), and attach
//	                                the drift verdict (drift.go / apps-drift.ts).
//	GET  /v1/paas/apps/:app       — one service row by CR name.
//	POST /v1/paas/apps/:app/deploy— deploy a new image tag by merge-patching the
//	                                Service CR's `.spec.image` (deploy-executor.ts).
//	                                The operator reconciles the rollout; cloud never
//	                                reimplements a deployer.
//	GET  /v1/paas/health          — real k8s reachability + Service CRD presence.
//
// SECURITY — every route is GLOBAL-ADMIN ONLY, fail-closed, gated on the SAME
// predicate the rest of cloud uses: c.IsAdmin() (true only for a JWT-validated
// principal whose org is the admin org, matching the gateway's admin-guard — see
// clients/admin). Unlike clients/ml (per-tenant namespaces), the PaaS control
// plane reads and mutates SYSTEM Service CRs across the whole fleet, so it is
// admin-only: a tenant must never patch another org's — or a platform — service.
// The user-facing PaaS view lives in console; users never call this surface.
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev — the identical
// construction clients/ml uses. When no kubeconfig is resolvable the subsystem
// mounts anyway and every endpoint fails closed (503 + the real init error; the
// health route reports "degraded"), never status-theater.
package paassvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	luxlog "github.com/luxfi/log"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// servicesGVR is the operator Service CR — the single source of truth for the
// declared image of every Hanzo service. Asserted in the tests; a typo here
// silently breaks the whole board.
var servicesGVR = schema.GroupVersionResource{Group: "hanzo.ai", Version: "v1", Resource: "services"}

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

const userAgent = "hanzo-cloud-paassvc"

type svc struct {
	dyn     dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health + ready()
	log     luxlog.Logger
}

// Mount wires the /v1/paas/* surface onto app. Every handler gates on
// c.IsAdmin() first (global-admin only), then reads/patches the operator Service
// CRs.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("paassvc.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("paassvc.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "paas")

	s := &svc{log: log}
	if dyn, err := newDynamic(); err != nil {
		s.initErr = err.Error()
		log.Warn("kubernetes client unavailable; /v1/paas endpoints will fail closed", "err", err)
	} else {
		s.dyn = dyn
	}

	app.Get("/v1/paas/apps", s.guard(s.listApps))
	app.Get("/v1/paas/apps/:app", s.guard(s.getApp))
	app.Post("/v1/paas/apps/:app/deploy", s.guard(s.deploy))
	app.Get("/v1/paas/health", s.health)

	log.Info("paas control plane mounted",
		"prefix", "/v1/paas", "k8s", s.dyn != nil, "brand", deps.Brand, "env", deps.Env)
	return nil
}

// Registered under "paassvc" (not "paas") for the same reason clients/ml uses
// "mlsvc": serve.go auto-mounts a generic GET /v1/<name>/health BEFORE MountAll
// and zip is first-match-wins, so a name of "paas" would shadow the real-probe
// /v1/paas/health. "paassvc" keeps the generic liveness at the unrouted
// /v1/paassvc/health and lets the real probe own /v1/paas/health. Order 128 binds
// the /v1/paas family before the projectsvc (125) neighbours and well before the
// AI /v1/* catch-all (150); it has no ordering dependency (self-contained k8s
// client).
func init() {
	cloud.Register("paassvc", 128, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("paassvc.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// guard wraps a handler with the global-admin gate. Fail-closed: any request whose
// validated identity is not a global admin is refused 403 before the handler — no
// cluster object is read or mutated, matching clients/admin.guard.
func (s *svc) guard(h zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !c.IsAdmin() {
			return zip.ErrForbidden("global admin required")
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
func (s *svc) listApps(c *zip.Ctx) error {
	if err := s.ready(); err != nil {
		return err
	}
	views, err := s.observeFleet(c.Context())
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
func (s *svc) getApp(c *zip.Ctx) error {
	if err := s.ready(); err != nil {
		return err
	}
	name := reqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	for _, ns := range scanOrder() {
		obj, err := s.dyn.Resource(servicesGVR).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return s.k8sErr("get", err)
		}
		repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
		return c.JSON(http.StatusOK, observeCR(obj, ns, nsEnv[ns], s.runningTagOf(c.Context(), ns, name, repository)))
	}
	return zip.ErrNotFound("service not found in the platform namespaces")
}

// observeFleet lists every Service CR across the scanned namespaces and maps each
// to an AppView. A namespace that does not exist / is empty is skipped, never
// fatal (the fleet board must still render the reachable namespaces).
func (s *svc) observeFleet(ctx context.Context) ([]AppView, error) {
	var views []AppView
	for _, ns := range scanOrder() {
		list, err := s.dyn.Resource(servicesGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, s.k8sErr("list", err)
		}
		// Running state: one Deployment list per namespace, indexed by name — the
		// running-tag source (inventory.ts). Best-effort: a Deployment RBAC/list
		// error leaves runningTag empty (an honest unknown) rather than failing the
		// whole board, so the declared/health/phase columns still render.
		running := s.runningTagsIn(ctx, ns)
		env := nsEnv[ns]
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

// ── deploy: merge-patch the Service CR image (deploy-executor.ts) ─────────────

// deploy rolls a new image tag onto a service by merge-patching ONLY the Service
// CR's `.spec.image`. The operator reconciles the rollout (Deployment update,
// rolling restart) — this is the exact contract deploy-executor.ts implemented,
// now native. Content-Type is JSON merge-patch (application/merge-patch+json),
// which the operator CRD accepts; the dynamic client's MergePatchType sets it.
func (s *svc) deploy(c *zip.Ctx) error {
	if err := s.ready(); err != nil {
		return err
	}
	name := reqApp(c)
	if !appNameRE.MatchString(name) {
		return zip.ErrBadRequest("app must be a DNS-1123 label")
	}

	var req struct {
		Tag        string `json:"tag"`        // required — the new image tag (e.g. v1.1.3)
		Repository string `json:"repository"` // optional — override image repo; else keep the CR's
		Namespace  string `json:"namespace"`  // optional — target ns; else resolve to where the CR lives
	}
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
	}
	tag := strings.TrimSpace(req.Tag)
	if tag == "" {
		return zip.ErrBadRequest("'tag' is required (the image tag to deploy)")
	}
	if strings.ContainsAny(tag, " \t\n/") || len(tag) > 128 {
		return zip.ErrBadRequest("'tag' must be a single image tag (no whitespace or '/')")
	}
	repo := strings.TrimSpace(req.Repository)
	if repo != "" && !imageRepoRE.MatchString(repo) {
		return zip.ErrBadRequest("'repository' is not a valid image repository path")
	}

	ns := strings.TrimSpace(req.Namespace)
	if ns != "" {
		if _, ok := nsEnv[ns]; !ok {
			return zip.ErrBadRequest("'namespace' must be a platform namespace (hanzo|hanzo-testnet|hanzo-devnet)")
		}
	} else {
		resolved, err := s.resolveNamespace(c.Context(), name)
		if err != nil {
			return err
		}
		ns = resolved
	}

	// Build the merge-patch. When repository is omitted we patch only the tag +
	// pullPolicy so an existing repo is preserved (JSON merge-patch merges keys,
	// so omitting `repository` leaves the CR's value intact).
	image := map[string]any{"tag": tag, "pullPolicy": "Always"}
	if repo != "" {
		image["repository"] = repo
	}
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"image": image}})
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "encode patch: %v", err)
	}

	out, err := s.dyn.Resource(servicesGVR).Namespace(ns).
		Patch(c.Context(), name, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return zip.ErrNotFound("service not found in namespace " + ns)
		case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
			return zip.Errorf(http.StatusUnprocessableEntity, "patch rejected by kubernetes: %v", err)
		default:
			return s.k8sErr("patch", err)
		}
	}

	s.log.Info("deployed via Service CR patch",
		"app", name, "namespace", ns, "tag", tag, "repository", repo,
		"actor", c.User(), "requestID", c.RequestID())

	// Read the effective repo from the patched CR (the caller may have omitted
	// repository to keep the CR's existing value) so the running-tag container
	// match uses the real declared repo.
	effRepo, _, _ := unstructured.NestedString(out.Object, "spec", "image", "repository")
	view := observeCR(out, ns, nsEnv[ns], s.runningTagOf(c.Context(), ns, name, effRepo))
	return c.JSON(http.StatusOK, map[string]any{
		"rolledOut": true,
		"target":    ns + "/" + name,
		"reason":    "patched Service/" + name + " image to " + tag,
		"app":       view,
	})
}

// resolveNamespace finds the platform namespace a Service CR lives in, scanning
// in env order (main→test→dev) so a bare deploy targets production. Returns a
// clean 404 when the CR exists in none of them.
func (s *svc) resolveNamespace(ctx context.Context, name string) (string, error) {
	for _, ns := range scanOrder() {
		if _, err := s.dyn.Resource(servicesGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ns, nil
		} else if !apierrors.IsNotFound(err) {
			return "", s.k8sErr("get", err)
		}
	}
	return "", zip.ErrNotFound("service " + name + " not found in the platform namespaces")
}

// ── health ────────────────────────────────────────────────────────────────

// health is a REAL probe: it verifies the API server is reachable and that the
// Service CRD is served, and reports the actual state. 200 only when everything is
// ok; 503 + the real reason otherwise (never status-theater). Not admin-gated —
// liveness must be probe-able by the platform/operator without a JWT.
func (s *svc) health(c *zip.Ctx) error {
	res := map[string]any{"service": "paas", "status": "ok"}
	if s.dyn == nil {
		res["status"], res["k8s"], res["error"] = "degraded", false, s.initErr
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	if _, err := s.dyn.Resource(servicesGVR).Namespace("hanzo").List(c.Context(), metav1.ListOptions{Limit: 1}); err != nil {
		res["status"], res["k8s"], res["crd"], res["error"] = "degraded", true, false, err.Error()
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["k8s"], res["crd"] = true, true
	return c.JSON(http.StatusOK, res)
}

// ── k8s plumbing ────────────────────────────────────────────────────────────

func (s *svc) ready() error {
	if s.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "paas: kubernetes client not configured: %s", s.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials name
// the missing access so the operator knows exactly what to grant the cloud service
// account (get/list/patch on services.hanzo.ai). Mirrors ml.k8sErr.
func (s *svc) k8sErr(op string, err error) error {
	s.log.Error("k8s op failed", "op", op, "resource", servicesGVR.Resource, "err", err)
	if apierrors.IsForbidden(err) {
		return zip.Errorf(http.StatusBadGateway,
			"%s services: kubernetes RBAC denied (cloud service account needs %s on services.hanzo.ai): %v",
			op, op, err)
	}
	return zip.Errorf(http.StatusBadGateway, "%s services failed: %v", op, err)
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

// observeCR maps one Service CR (+ its operator-reconciled status + the running
// tag observed from the live Deployment) into an AppView, attaching the drift
// verdict. This is inventory.ts observeService fused with apps-api.ts toAppView:
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
func (s *svc) runningTagsIn(ctx context.Context, namespace string) map[string]string {
	out := map[string]string{}
	list, err := s.dyn.Resource(deploymentsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.log.Warn("list deployments for running tag failed; running tag will be empty",
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
func (s *svc) runningTagOf(ctx context.Context, namespace, name, declaredRepository string) string {
	d, err := s.dyn.Resource(deploymentsGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
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
