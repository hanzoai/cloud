// Package mlsvc mounts the Hanzo Cloud /v1/ml/* and /v1/train/* surfaces: a
// thin, tenant-scoped bridge that turns three Kubeflow-family CustomResources
// into a small REST API. No ML logic is reimplemented here — the operators
// (kserve, trainer, katib) own reconciliation; this subsystem only translates
// REST <-> the Kubernetes API and enforces tenant isolation.
//
// Three resources, one CRUD shape each (kserve names are internal/opaque — the
// user-facing model catalog lives in the hub, never here, so no upstream model
// identity is ever introduced by this layer):
//
//	/v1/ml/models           InferenceService  serving.kserve.io/v1beta1
//	/v1/train/jobs          TrainJob          trainer.kubeflow.org/v1alpha1
//	/v1/train/experiments   Experiment        kubeflow.org/v1beta1   (katib)
//
// Plus two leaf surfaces: POST /v1/ml/models/{name}/predict proxies the request
// body to the model's kserve v2 data plane (/v2/models/{name}/infer at the
// InferenceService's cluster-internal address), and
// GET /v1/train/experiments/{name}/trials lists the katib Trials owned by an
// experiment.
//
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id /
// c.Org()) and lands in a PER-ORG Kubernetes namespace ("ml-"<org>). The
// namespace IS the tenant boundary — an org physically cannot name into,
// list, read, mutate or predict against another org's resources because the
// dynamic client is always pinned to the caller's namespace. The org slug is
// validated against a strict DNS-label regex (no lossy sanitize), so the
// org->namespace map is injective: two distinct orgs can never fold onto one
// namespace. Empty org is rejected 403 unless the caller is a gateway-minted
// admin (bucketed under the literal "ml-admin" namespace).
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev. It is NOT
// hung off the shared cloud.Deps: a raw Kubernetes client has none of the
// in-process/ZAP-RPC duality the Deps inter-subsystem clients model, and it is
// used by exactly this one subsystem — so it stays self-contained here, the
// same way provisioningsvc builds its own backend clients. When no kubeconfig
// is resolvable the subsystem mounts anyway and every endpoint fails closed:
// mutating routes return 503 and the health routes report status "degraded"
// with the real init error (never a fake success).
package ml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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

// GroupVersionResources for the three managed CRDs (+ trials and core
// namespaces). These are the single source of truth for the wire identity of
// each resource; a typo here silently breaks every call, so they are asserted
// in the tests.
var (
	isvcGVR       = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1beta1", Resource: "inferenceservices"}
	trainjobGVR   = schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}
	experimentGVR = schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "experiments"}
	trialGVR      = schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "trials"}
	nsGVR         = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
)

// resourceKind binds a GVR to the apiVersion/kind strings needed to build an
// unstructured object on create. One value per user-facing resource family.
type resourceKind struct {
	gvr        schema.GroupVersionResource
	apiVersion string
	kind       string
}

var (
	modelKind = resourceKind{isvcGVR, "serving.kserve.io/v1beta1", "InferenceService"}
	jobKind   = resourceKind{trainjobGVR, "trainer.kubeflow.org/v1alpha1", "TrainJob"}
	expKind   = resourceKind{experimentGVR, "kubeflow.org/v1beta1", "Experiment"}
)

const (
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "hanzo-cloud"
	orgLabel       = "hanzo.ai/org"
	katibExpLabel  = "katib.kubeflow.org/experiment"
	nsPrefix       = "ml-"
	predictBodyCap = 32 << 20 // 32 MiB ceiling on a predictor response read
	predictTimeout = 5 * time.Minute
)

// nameRE constrains a user-supplied resource name to a DNS-1123 label (the
// shape every CR metadata.name must satisfy). Validated at the boundary; it is
// the injection guard for the path/object name.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// orgRE constrains the gateway-minted org slug. Strict (no lossy folding) so
// "ml-"<org> is an injective tenant->namespace map and stays a valid DNS-1123
// label (<=63 chars: "ml-" + <=42).
var orgRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,40}[a-z0-9])?$`)

// computeFeeEnvPrefix is the operator knob for the per-create compute fee. The
// effective fee is cloud.ResourceFeeCents(computeFeeEnvPrefix, kind): a per-kind
// override (e.g. CLOUD_COMPUTE_FEE_CENTS_TRAINJOB=…) wins over the global
// CLOUD_COMPUTE_FEE_CENTS, else the $1.00 default. Set a kind to 0 to make it
// free (and therefore un-gated).
//
// This is the create/submission fee. A TrainJob's ongoing GPU-hour cost
// (hanzoai/pricing infrastructure.compute centsPerHour) is billed by REUSING
// s.bill.Meter with a runtime-derived amount from a future usage watcher — never
// fabricated here.
const computeFeeEnvPrefix = "CLOUD_COMPUTE_FEE_CENTS"

type svc struct {
	dyn     dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health
	hc      *http.Client      // predictor data-plane client (inference latency)
	log     luxlog.Logger
	// bill is the shared per-org resource gate+meter (reuses deps.Metering, the
	// one commerce client). Nil/!Enabled() makes Gate allow and Meter a no-op.
	bill *cloud.ResourceMeter
}

// Mount wires the /v1/ml/* and /v1/train/* surfaces onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ml.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("ml.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "ml")

	s := &svc{log: log, hc: &http.Client{Timeout: predictTimeout}, bill: cloud.NewResourceMeter(deps, "compute")}
	if dyn, err := newDynamic(); err != nil {
		s.initErr = err.Error()
		log.Warn("kubernetes client unavailable; ml/train endpoints will fail closed", "err", err)
	} else {
		s.dyn = dyn
	}

	// Models (kserve InferenceService).
	app.Get("/v1/ml/models", s.list(modelKind))
	app.Post("/v1/ml/models", s.create(modelKind))
	app.Get("/v1/ml/models/:name", s.get(modelKind))
	app.Patch("/v1/ml/models/:name", s.patch(modelKind))
	app.Delete("/v1/ml/models/:name", s.del(modelKind))
	app.Post("/v1/ml/models/:name/predict", s.predict)

	// Training jobs (trainer TrainJob).
	app.Get("/v1/train/jobs", s.list(jobKind))
	app.Post("/v1/train/jobs", s.create(jobKind))
	app.Get("/v1/train/jobs/:name", s.get(jobKind))
	app.Delete("/v1/train/jobs/:name", s.del(jobKind))

	// Experiments + trials (katib).
	app.Get("/v1/train/experiments", s.list(expKind))
	app.Post("/v1/train/experiments", s.create(expKind))
	app.Get("/v1/train/experiments/:name", s.get(expKind))
	app.Delete("/v1/train/experiments/:name", s.del(expKind))
	app.Get("/v1/train/experiments/:name/trials", s.trials)

	// Real-probe health (the generic auto-health from serve.go lands at the
	// harmless, unrouted /v1/mlsvc/health for the registered subsystem name;
	// these two report ACTUAL k8s reachability + CRD presence).
	app.Get("/v1/ml/health", s.health("ml", isvcGVR))
	app.Get("/v1/train/health", s.health("train", trainjobGVR, experimentGVR))

	log.Info("ml/train surface mounted", "k8s", s.dyn != nil, "brand", deps.Brand, "env", deps.Env, "billing", s.bill.Enabled())
	return nil
}

// Registered under the name "mlsvc" (not "ml"/"train") on purpose: serve.go
// auto-mounts a generic GET /v1/<name>/health BEFORE MountAll, and zip/fiber is
// first-match-wins, so a name of "ml" would shadow our real-probe /v1/ml/health.
// "mlsvc" keeps the generic liveness at the unrouted /v1/mlsvc/health and lets
// the real probes own /v1/ml/health and /v1/train/health. Order 130 binds the
// /v1/ml and /v1/train families before the AI subsystem's /v1/* catch-all (150).
func init() {
	cloud.Register("mlsvc", 130, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("ml.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// ── CRUD (generic across the three kinds) ────────────────────────────────────

func (s *svc) list(k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := s.ready(); err != nil {
			return err
		}
		ns, _, err := s.tenant(c)
		if err != nil {
			return err
		}
		ul, err := s.dyn.Resource(k.gvr).Namespace(ns).List(c.Context(), metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) { // tenant namespace not created yet
				return c.JSON(http.StatusOK, map[string]any{"items": []any{}})
			}
			return s.k8sErr(c, k, "list", err)
		}
		return c.JSON(http.StatusOK, map[string]any{"items": viewList(ul.Items)})
	}
}

func (s *svc) create(k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := s.ready(); err != nil {
			return err
		}
		ns, org, err := s.tenant(c)
		if err != nil {
			return err
		}
		var req struct {
			Name   string            `json:"name"`
			Spec   json.RawMessage   `json:"spec"`
			Labels map[string]string `json:"labels"`
		}
		if err := json.Unmarshal(c.Body(), &req); err != nil {
			return zip.Errorf(http.StatusBadRequest, "invalid JSON body: %v", err)
		}
		name := strings.ToLower(strings.TrimSpace(req.Name))
		if !nameRE.MatchString(name) {
			return zip.ErrBadRequest("'name' must be a DNS-1123 label: ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
		}
		if len(req.Spec) == 0 {
			return zip.ErrBadRequest("'spec' is required (the " + k.kind + " spec)")
		}
		var spec map[string]any
		if err := json.Unmarshal(req.Spec, &spec); err != nil {
			return zip.Errorf(http.StatusBadRequest, "invalid 'spec': %v", err)
		}

		// Pre-create balance gate (fail-closed, per-org). Refuse BEFORE the tenant
		// namespace or CR is created so an unfunded org cannot run free GPU
		// compute; an unreachable commerce refuses 503 (default fail-closed).
		// Scoped to THIS caller's org (the same slug that maps to the tenant
		// namespace, derived by #66's identity sanitizer from a validated JWT),
		// so billing can never target another tenant. fee is reused by the
		// post-success debit; fee==0 or unconfigured billing makes this a no-op.
		fee := cloud.ResourceFeeCents(computeFeeEnvPrefix, k.kind)
		if err := s.bill.Gate(c.Context(), org, k.kind, fee); err != nil {
			return cloud.DenyResource(c, err)
		}

		if err := s.ensureNamespace(c.Context(), ns, org); err != nil {
			return zip.Errorf(http.StatusBadGateway, "ensure tenant namespace: %v", err)
		}
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": k.apiVersion,
			"kind":       k.kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels":    labelsFor(org, req.Labels),
			},
			"spec": spec,
		}}
		out, err := s.dyn.Resource(k.gvr).Namespace(ns).Create(c.Context(), obj, metav1.CreateOptions{})
		if err != nil {
			switch {
			case apierrors.IsAlreadyExists(err):
				return zip.ErrConflict(k.kind + " already exists")
			case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
				return zip.Errorf(http.StatusUnprocessableEntity, "%s rejected by kubernetes: %v", k.kind, err)
			default:
				return s.k8sErr(c, k, "create", err)
			}
		}
		// Resource created — debit the caller's org ledger for the compute
		// submission (per-org, env-attributed, async best-effort). Ongoing
		// GPU-hour cost reuses s.bill.Meter from a future runtime usage watcher.
		s.bill.Meter(org, k.kind, fee, c.RequestID(), cloud.ClientIP(c))
		return c.JSON(http.StatusCreated, view(out, true))
	}
}

func (s *svc) get(k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := s.ready(); err != nil {
			return err
		}
		ns, _, err := s.tenant(c)
		if err != nil {
			return err
		}
		out, err := s.dyn.Resource(k.gvr).Namespace(ns).Get(c.Context(), reqName(c), metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return zip.ErrNotFound(k.kind + " not found")
			}
			return s.k8sErr(c, k, "get", err)
		}
		return c.JSON(http.StatusOK, view(out, true))
	}
}

func (s *svc) patch(k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := s.ready(); err != nil {
			return err
		}
		ns, _, err := s.tenant(c)
		if err != nil {
			return err
		}
		body := c.Body()
		if len(body) == 0 {
			return zip.ErrBadRequest("empty patch body (send a JSON merge patch)")
		}
		out, err := s.dyn.Resource(k.gvr).Namespace(ns).Patch(c.Context(), reqName(c), k8stypes.MergePatchType, body, metav1.PatchOptions{})
		if err != nil {
			switch {
			case apierrors.IsNotFound(err):
				return zip.ErrNotFound(k.kind + " not found")
			case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
				return zip.Errorf(http.StatusUnprocessableEntity, "patch rejected by kubernetes: %v", err)
			default:
				return s.k8sErr(c, k, "patch", err)
			}
		}
		return c.JSON(http.StatusOK, view(out, true))
	}
}

func (s *svc) del(k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := s.ready(); err != nil {
			return err
		}
		ns, _, err := s.tenant(c)
		if err != nil {
			return err
		}
		if err := s.dyn.Resource(k.gvr).Namespace(ns).Delete(c.Context(), reqName(c), metav1.DeleteOptions{}); err != nil {
			if apierrors.IsNotFound(err) {
				return zip.ErrNotFound(k.kind + " not found")
			}
			return s.k8sErr(c, k, "delete", err)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// ── leaf surfaces ────────────────────────────────────────────────────────────

// predict proxies the request body to the model's kserve v2 data plane. The v2
// model name defaults to the InferenceService name (kserve's single-model
// convention) and may be overridden with ?model=. The predictor's status + body
// are returned verbatim so a model-side error surfaces honestly.
func (s *svc) predict(c *zip.Ctx) error {
	if err := s.ready(); err != nil {
		return err
	}
	ns, _, err := s.tenant(c)
	if err != nil {
		return err
	}
	name := reqName(c)
	obj, err := s.dyn.Resource(isvcGVR).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return zip.ErrNotFound("model not found")
		}
		return s.k8sErr(c, modelKind, "get", err)
	}
	addr := internalURL(obj)
	if addr == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "model %q is not ready (no serving address yet)", name)
	}
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		model = name
	}
	target := strings.TrimRight(addr, "/") + "/v2/models/" + model + "/infer"
	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, target, bytes.NewReader(c.Body()))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "build predict request: %v", err)
	}
	ct := c.Header("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	resp, err := s.hc.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "predict: model data plane unreachable: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, predictBodyCap))
	if respCT := resp.Header.Get("Content-Type"); respCT != "" {
		c.SetHeader("Content-Type", respCT)
	}
	return c.Bytes(resp.StatusCode, rb)
}

// trials lists the katib Trials owned by an experiment in the caller's tenant
// namespace. The experiment is fetched first so a cross-tenant or missing name
// is a clean 404 rather than an empty list.
func (s *svc) trials(c *zip.Ctx) error {
	if err := s.ready(); err != nil {
		return err
	}
	ns, _, err := s.tenant(c)
	if err != nil {
		return err
	}
	name := reqName(c)
	if _, err := s.dyn.Resource(experimentGVR).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return zip.ErrNotFound("experiment not found")
		}
		return s.k8sErr(c, expKind, "get", err)
	}
	ul, err := s.dyn.Resource(trialGVR).Namespace(ns).List(c.Context(), metav1.ListOptions{
		LabelSelector: katibExpLabel + "=" + name,
	})
	if err != nil {
		return s.k8sErr(c, resourceKind{trialGVR, "kubeflow.org/v1beta1", "Trial"}, "list", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"experiment": name, "items": viewList(ul.Items)})
}

// health is a REAL probe: it verifies the API server is reachable and that the
// subsystem's CRDs are served, and reports the actual state. 200 only when
// everything is ok; 503 + the real reason otherwise (never status-theater).
func (s *svc) health(name string, gvrs ...schema.GroupVersionResource) zip.Handler {
	return func(c *zip.Ctx) error {
		res := map[string]any{"service": name, "status": "ok"}
		if s.dyn == nil {
			res["status"], res["k8s"], res["error"] = "degraded", false, s.initErr
			return c.JSON(http.StatusServiceUnavailable, res)
		}
		ctx := c.Context()
		if _, err := s.dyn.Resource(nsGVR).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			res["status"], res["k8s"], res["error"] = "degraded", false, err.Error()
			return c.JSON(http.StatusServiceUnavailable, res)
		}
		res["k8s"] = true
		crds := map[string]bool{}
		allOK := true
		for _, g := range gvrs {
			_, err := s.dyn.Resource(g).Namespace(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{Limit: 1})
			crds[g.Resource] = err == nil
			if err != nil {
				allOK = false
			}
		}
		res["crds"] = crds
		if !allOK {
			res["status"] = "degraded"
			return c.JSON(http.StatusServiceUnavailable, res)
		}
		return c.JSON(http.StatusOK, res)
	}
}

// ── tenancy + k8s plumbing ───────────────────────────────────────────────────

// tenant resolves the per-org namespace for a request from the gateway-minted
// identity. Pure mapping lives in tenantNS for testability.
func (s *svc) tenant(c *zip.Ctx) (ns, org string, err error) {
	if !principal.Validated(c) {
		// No validated principal — the restored X-Org-Id is a forge. Refuse before
		// mapping to a per-org k8s namespace (provisions/reads ML resources).
		return "", "", zip.ErrForbidden("no validated principal")
	}
	return tenantNS(c.Org(), c.IsAdmin())
}

// tenantNS maps a gateway org slug to its tenant namespace. Empty org is
// rejected unless admin (literal "admin" bucket). The org is lowercased and
// validated against orgRE with NO lossy sanitize, making org->namespace
// injective — two distinct orgs can never fold onto one namespace.
func tenantNS(rawOrg string, isAdmin bool) (ns, org string, err error) {
	org = strings.ToLower(strings.TrimSpace(rawOrg))
	if org == "" {
		if isAdmin {
			return nsPrefix + "admin", "admin", nil
		}
		return "", "", zip.ErrForbidden("X-Org-Id required")
	}
	if !orgRE.MatchString(org) {
		return "", "", zip.ErrForbidden("invalid org identifier")
	}
	return nsPrefix + org, org, nil
}

// ensureNamespace idempotently creates the tenant namespace before the first
// resource lands in it.
func (s *svc) ensureNamespace(ctx context.Context, ns, org string) error {
	if _, err := s.dyn.Resource(nsGVR).Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   ns,
			"labels": map[string]any{managedByLabel: managedByValue, orgLabel: org},
		},
	}}
	if _, err := s.dyn.Resource(nsGVR).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (s *svc) ready() error {
	if s.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "ml: kubernetes client not configured: %s", s.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials
// name the missing access so the operator knows exactly what to grant the
// cloud-api service account.
func (s *svc) k8sErr(c *zip.Ctx, k resourceKind, op string, err error) error {
	s.log.Error("k8s op failed", "op", op, "kind", k.kind, "resource", k.gvr.Resource, "err", err)
	if apierrors.IsForbidden(err) {
		return zip.Errorf(http.StatusBadGateway,
			"%s %s: kubernetes RBAC denied (cloud-api service account needs %s on %s.%s): %v",
			op, k.kind, op, k.gvr.Resource, k.gvr.Group, err)
	}
	return zip.Errorf(http.StatusBadGateway, "%s %s failed: %v", op, k.kind, err)
}

// mlTokenFileEnv names a mounted `cloud-ml` ServiceAccount token. When it is set
// (and readable) the ML control plane authenticates to the API server as the
// dedicated cloud-ml identity — decomplected from the pod's own cloud-api SA — so
// ML's KServe/Kubeflow cluster reach is never inherited by the product-API path
// (least privilege; blast-radius separation). See universe
// infra/k8s/cloud/ml-rbac.yaml (ClusterRoleBinding cloud-mlsvc -> cloud-ml).
const mlTokenFileEnv = "HANZO_ML_TOKEN_FILE"

// newDynamic builds the dynamic client. In-cluster it authenticates as the
// dedicated cloud-ml identity when HANZO_ML_TOKEN_FILE points at a mounted
// cloud-ml token, otherwise as the pod's own in-cluster SA. Falls back to
// KUBECONFIG / ~/.kube/config for local/dev.
func newDynamic() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	} else if tokenFile := strings.TrimSpace(os.Getenv(mlTokenFileEnv)); tokenFile != "" {
		// Re-scope the in-cluster client to the mounted cloud-ml token: keep the
		// API server host + CA discovered in-cluster, swap ONLY the identity.
		// Fail closed if the configured token is missing — never silently fall
		// back to the broader cloud-api SA.
		if _, statErr := os.Stat(tokenFile); statErr != nil {
			return nil, fmt.Errorf("%s=%q not readable: %w", mlTokenFileEnv, tokenFile, statErr)
		}
		cfg.BearerToken = ""
		cfg.BearerTokenFile = tokenFile
	}
	cfg.UserAgent = "hanzo-cloud-mlsvc"
	return dynamic.NewForConfig(cfg)
}

// ── pure helpers ─────────────────────────────────────────────────────────────

func reqName(c *zip.Ctx) string { return strings.ToLower(strings.TrimSpace(c.Param("name"))) }

// labelsFor stamps the managed-by + tenant-org labels onto a create. The org
// label is set LAST so a caller can never override the tenant boundary marker.
func labelsFor(org string, user map[string]string) map[string]any {
	m := map[string]any{}
	for k, v := range user {
		m[k] = v
	}
	m[managedByLabel] = managedByValue
	m[orgLabel] = org
	return m
}

// view trims a CR to an honest, non-bloated shape: name + creation time + live
// status, plus the spec on single-object reads. Namespace is intentionally
// omitted (internal tenant detail).
func view(obj *unstructured.Unstructured, withSpec bool) map[string]any {
	m := map[string]any{
		"name":      obj.GetName(),
		"createdAt": obj.GetCreationTimestamp().UTC().Format(time.RFC3339),
	}
	if st, ok, _ := unstructured.NestedMap(obj.Object, "status"); ok {
		m["status"] = st
	}
	if withSpec {
		if sp, ok, _ := unstructured.NestedMap(obj.Object, "spec"); ok {
			m["spec"] = sp
		}
	}
	return m
}

func viewList(items []unstructured.Unstructured) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, view(&items[i], false))
	}
	return out
}

// internalURL returns the cluster-internal serving address of an
// InferenceService, preferring status.address.url (the in-cluster URL) over
// status.url (the external/ingress URL). Empty until the model is ready.
func internalURL(obj *unstructured.Unstructured) string {
	if v, ok, _ := unstructured.NestedString(obj.Object, "status", "address", "url"); ok && v != "" {
		return v
	}
	if v, ok, _ := unstructured.NestedString(obj.Object, "status", "url"); ok && v != "" {
		return v
	}
	return ""
}
