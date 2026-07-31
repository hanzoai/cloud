// Package ml is model serving and training: deploy a model behind an endpoint,
// run a training job, tune it with experiments.
//
// The /v1/ml/* and /v1/train/* routes are a thin, tenant-scoped bridge that
// turns three Kubeflow-family CustomResources into a small REST API. No ML
// logic is reimplemented here — the operators
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
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id / c.Org())
// narrowed by the org SUB-SCOPE (X-Project-Id / principal.Project), and lands in a
// PER-ORG(+PROJECT) Kubernetes namespace: "ml-"<org> for the default project (the
// backward-compatible single-project shape) and "ml-"<org>"-"<project> for a
// non-default one. The namespace IS the tenant boundary — a tenant physically cannot
// name into, list, read, mutate or predict against another org's (or project's)
// resources because the dynamic client is always pinned to the caller's namespace.
// Both org and project are validated against strict DNS-label regexes (no lossy
// sanitize), so the (org, project)->namespace map is injective: two distinct scopes
// can never fold onto one namespace. Empty org is rejected 403 unless the caller is a
// gateway-minted admin (bucketed under the literal "ml-admin" namespace).
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev. It is NOT
// hung off the shared cloud.Deps: a raw Kubernetes client has none of the
// in-process/ZAP-RPC duality the Deps inter-subsystem clients model, and it is
// used by exactly this one subsystem — so it stays self-contained here, the
// same way provisioning builds its own backend clients. When no kubeconfig
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

	"github.com/hanzoai/cloud/apps/k8s"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/fleet"
	"github.com/hanzoai/cloud/apps/principal"
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

// GroupVersionResources for the three managed CRDs (+ trials and core
// namespaces). These are the single source of truth for the wire identity of
// each resource; a typo here silently breaks every call, so they are asserted
// in the tests.
var (
	isvcGVR       = schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1beta1", Resource: "inferenceservices"}
	trainjobGVR   = schema.GroupVersionResource{Group: "trainer.kubeflow.org", Version: "v1alpha1", Resource: "trainjobs"}
	experimentGVR = schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "experiments"}
	trialGVR      = schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1beta1", Resource: "trials"}
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
	projectLabel   = "hanzo.ai/project" // org SUB-SCOPE; stamped only for a non-default project
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

// projectRE constrains the org SUB-SCOPE (X-Project-Id) that suffixes the tenant
// namespace to a DNS-1123 label — the SAME slug shape projects enforces — so the
// (org, project) -> namespace map stays injective (no lossy fold) and the suffix is
// always a valid label segment. A non-conforming project (e.g. an opaque id) is
// REFUSED, never folded; the default project adds no suffix, so this gates only the
// non-default case. The composed "ml-"<org>"-"<project> is still length-checked
// against the 63-char label ceiling in tenantNS.
var projectRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// computeFeeEnvPrefix is the operator knob for the per-create compute fee. The
// effective fee is cloud.ResourceFeeCents(computeFeeEnvPrefix, kind): a per-kind
// override (e.g. CLOUD_COMPUTE_FEE_CENTS_TRAINJOB=…) wins over the global
// CLOUD_COMPUTE_FEE_CENTS, else the $1.00 default. Set a kind to 0 to make it
// free (and therefore un-gated).
//
// This is the create/submission fee. A TrainJob's ongoing GPU-hour cost
// (hanzoai/pricing infrastructure.compute centsPerHour) is billed by REUSING
// s.State.bill.Meter with a runtime-derived amount from a future usage watcher — never
// fabricated here.
const computeFeeEnvPrefix = "CLOUD_COMPUTE_FEE_CENTS"

// state is ml's own data; shared deps live in the embedded cloud.Base. The billing
// meter is kept here (not in Base.Bill) because its commerce product label is
// "compute", NOT the subsystem name "ml".
type state struct {
	dyn     dynamic.Interface // in-cluster (home) client; nil when unresolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health
	hc      *http.Client      // predictor data-plane client (inference latency)
	// bill is the shared per-org resource gate+meter (reuses deps.Metering, the
	// one commerce client). Nil/!Enabled() makes Gate allow and Meter a no-op.
	bill *cloud.ResourceMeter
	// fleet is the shared per-org BYO-cluster registry (owned by the visor fleet
	// surface). dynForOrg federates ML serving onto the org's registered cluster
	// via it; a nil/empty registry => serving stays on the home in-cluster client.
	fleet *fleet.Registry
}

// Mount wires the /v1/ml/* and /v1/train/* surfaces onto app per HIP-0106. The
// "compute"-product meter, the k8s client bring-up and the shared fleet registry
// make this a direct construction (cloud.NewBase), not cloud.Mount.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("ml.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("ml.Mount: nil deps.Logger")
	}

	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "ml"),
		State: state{hc: &http.Client{Timeout: predictTimeout}, bill: cloud.NewResourceMeter(deps, "compute")},
	}
	if dyn, err := newDynamic(); err != nil {
		s.State.initErr = err.Error()
		s.Log.Warn("kubernetes client unavailable; ml/train endpoints will fail closed", "err", err)
	} else {
		s.State.dyn = dyn
	}
	// Shared BYO-cluster registry (registration surface is the visor fleet at
	// /v1/clusters; ml only READS it to federate serving onto the org's cluster).
	s.State.fleet = fleet.New(deps.Brand, s.Log)

	mount(s, app)
	s.Log.Info("ml/train surface mounted", "k8s", s.State.dyn != nil, "brand", deps.Brand, "env", deps.Env, "billing", s.State.bill.Enabled())
	return nil
}

// mount registers the surface on app. It is separate from Mount because Mount
// BUILDS the state — a Kubernetes client from whatever config the process can
// resolve — and the routes do not care where the state came from. Splitting the
// two lets a test pin the state (a fake client, or a deliberately nil one) and
// exercise the ROUTES, instead of asserting a wire that depends on whether the
// box running the suite happens to have a kubeconfig.
func mount(s *cloud.Service[state], app cloud.Router) {
	// TWO groups, one per top-level noun this subsystem owns, so each op's path is
	// its group's prefix composed with its leaf — the identity every projection
	// keys on, and the composition cmd/zipdoc resolves the same way (zip v1.18.3+),
	// which is what carries the prose in typed.go to the document and the MCP tool
	// list.
	//
	// cloud.Bridge FIRST on each group: a typed op receives only a context, so the
	// request its tenant seam reads (tenantFrom — the org SUB-SCOPE and
	// platform-admin-ness live in headers principal.OrgFrom does not carry) has to
	// be parked there. fiber runs middleware in registration order, so this must
	// precede the leaves below; nesting under Serve's own app-wide Bridge is
	// harmless — the inner one is what the handler sees.
	// One `g := <router>.Group("/prefix")` per line: cmd/zipdoc resolves a group's
	// prefix by reading that exact assignment form, and it FAILS the generate rather
	// than filing prose under a path that does not exist — so a tuple assignment
	// costs the doc comments below, silently, in both the document and the MCP tool
	// list.
	gml := app.Group("/v1/ml")
	gtrain := app.Group("/v1/train")
	gml.Use(cloud.Bridge())
	gtrain.Use(cloud.Bridge())
	o := ops{s: s}

	// Models (kserve InferenceService).
	zip.Get(gml, "/models", o.listModels)
	// UNTYPED BY DESIGN — the pre-create billing gate. cloud.DenyResource writes
	// the fleet's nested {"error":{"code","message"}} 402/503 contract IN BAND on
	// the response; a typed op's only refusal channel is a returned error, which
	// zip renders as the flat {"status","code","error"} HTTPError. Typing it would
	// change the 402 body every funded-balance client already parses. Same for the
	// two below. See typed_wire_test.go.
	gml.Post("/models", create(s, modelKind))
	zip.Get(gml, "/models/:name", o.getModel)
	// UNTYPED BY DESIGN — an opaque RFC 7386 merge patch, relayed VERBATIM to the
	// Kubernetes API. A typed In would re-encode it, and re-encoding a merge patch
	// changes what it means (an integer round-trips through float64).
	gml.Patch("/models/:name", patch(s, modelKind))
	zip.Delete(gml, "/models/:name", o.deleteModel)
	// UNTYPED BY DESIGN — a verbatim proxy: the predictor's own status code, body
	// bytes and Content-Type are returned unchanged, which no typed Out can carry.
	gml.Post("/models/:name/predict", cloud.Handle(s, predict))

	// Training jobs (trainer TrainJob).
	zip.Get(gtrain, "/jobs", o.listJobs)
	gtrain.Post("/jobs", create(s, jobKind)) // untyped by design — see /v1/ml/models
	zip.Get(gtrain, "/jobs/:name", o.getJob)
	zip.Delete(gtrain, "/jobs/:name", o.deleteJob)

	// Experiments + trials (katib).
	zip.Get(gtrain, "/experiments", o.listExperiments)
	gtrain.Post("/experiments", create(s, expKind)) // untyped by design — see /v1/ml/models
	zip.Get(gtrain, "/experiments/:name", o.getExperiment)
	zip.Delete(gtrain, "/experiments/:name", o.deleteExperiment)
	zip.Get(gtrain, "/experiments/:name/trials", o.listTrials)

	// Real-probe health. The subsystem registers with cloud.HealthOwner, so
	// serve.go skips its generic auto-health and these two own the probes,
	// reporting ACTUAL k8s reachability + CRD presence.
	//
	// UNTYPED BY DESIGN — both answer 503 carrying the degraded REPORT as their
	// body (status/k8s/error/crds), which is the point of a real probe. A typed op
	// can only reach a non-2xx by returning an error, and zip renders that as its
	// own envelope, dropping the report.
	gml.Get("/health", health(s, "ml", isvcGVR))
	gtrain.Get("/health", health(s, "train", trainjobGVR, experimentGVR))
}

// ── CRUD ─────────────────────────────────────────────────────────────────────
//
// list, get, delete and the trials leaf are TYPED ops — see typed.go. What
// remains here is the three routes whose wire a typed op cannot state: create
// (the in-band billing denial) and patch (the verbatim merge patch), plus the
// predict proxy and the two real health probes below.

func create(s *cloud.Service[state], k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := ready(s); err != nil {
			return err
		}
		ns, org, project, err := tenant(c)
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
		_, projectValidated := principal.ValidatedProject(c)
		if err := s.State.bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, k.kind, fee); err != nil {
			return cloud.DenyResource(c, err)
		}

		if err := ensureNamespace(s, c.Context(), ns, org, project); err != nil {
			return zip.Errorf(http.StatusBadGateway, "ensure tenant namespace: %v", err)
		}
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": k.apiVersion,
			"kind":       k.kind,
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels":    labelsFor(org, project, req.Labels),
			},
			"spec": spec,
		}}
		out, err := s.State.dyn.Resource(k.gvr).Namespace(ns).Create(c.Context(), obj, metav1.CreateOptions{})
		if err != nil {
			switch {
			case apierrors.IsAlreadyExists(err):
				return zip.ErrConflict(k.kind + " already exists")
			case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
				return zip.Errorf(http.StatusUnprocessableEntity, "%s rejected by kubernetes: %v", k.kind, err)
			default:
				return k8sErr(s, k, "create", err)
			}
		}
		// Resource created — debit the caller's org ledger for the compute
		// submission (per-org, env-attributed, async best-effort). Ongoing
		// GPU-hour cost reuses s.State.bill.Meter from a future runtime usage watcher.
		s.State.bill.Meter(principal.Ledger(c), project, k.kind, fee, c.RequestID(), cloud.ClientIP(c))
		return c.JSON(http.StatusCreated, view(out, true))
	}
}

func patch(s *cloud.Service[state], k resourceKind) zip.Handler {
	return func(c *zip.Ctx) error {
		if err := ready(s); err != nil {
			return err
		}
		ns, _, _, err := tenant(c)
		if err != nil {
			return err
		}
		body := c.Body()
		if len(body) == 0 {
			return zip.ErrBadRequest("empty patch body (send a JSON merge patch)")
		}
		out, err := s.State.dyn.Resource(k.gvr).Namespace(ns).Patch(c.Context(), reqName(c), k8stypes.MergePatchType, body, metav1.PatchOptions{})
		if err != nil {
			switch {
			case apierrors.IsNotFound(err):
				return zip.ErrNotFound(k.kind + " not found")
			case apierrors.IsInvalid(err), apierrors.IsBadRequest(err):
				return zip.Errorf(http.StatusUnprocessableEntity, "patch rejected by kubernetes: %v", err)
			default:
				return k8sErr(s, k, "patch", err)
			}
		}
		return c.JSON(http.StatusOK, view(out, true))
	}
}

// ── leaf surfaces ────────────────────────────────────────────────────────────

// predict proxies the request body to the model's kserve v2 data plane. The v2
// model name defaults to the InferenceService name (kserve's single-model
// convention) and may be overridden with ?model=. The predictor's status + body
// are returned verbatim so a model-side error surfaces honestly.
func predict(s *cloud.Service[state], c *zip.Ctx) error {
	if err := ready(s); err != nil {
		return err
	}
	ns, _, _, err := tenant(c)
	if err != nil {
		return err
	}
	name := reqName(c)
	obj, err := s.State.dyn.Resource(isvcGVR).Namespace(ns).Get(c.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return zip.ErrNotFound("model not found")
		}
		return k8sErr(s, modelKind, "get", err)
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
	resp, err := s.State.hc.Do(req)
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

// health is a REAL probe: it verifies the API server is reachable and that the
// subsystem's CRDs are served, and reports the actual state. 200 only when
// everything is ok; 503 + the real reason otherwise (never status-theater).
func health(s *cloud.Service[state], name string, gvrs ...schema.GroupVersionResource) zip.Handler {
	return func(c *zip.Ctx) error {
		res := map[string]any{"service": name, "status": "ok"}
		if s.State.dyn == nil {
			res["status"], res["k8s"], res["error"] = "degraded", false, s.State.initErr
			return c.JSON(http.StatusServiceUnavailable, res)
		}
		ctx := c.Context()
		if _, err := s.State.dyn.Resource(k8s.Namespaces).List(ctx, metav1.ListOptions{Limit: 1}); err != nil {
			res["status"], res["k8s"], res["error"] = "degraded", false, err.Error()
			return c.JSON(http.StatusServiceUnavailable, res)
		}
		res["k8s"] = true
		crds := map[string]bool{}
		allOK := true
		for _, g := range gvrs {
			_, err := s.State.dyn.Resource(g).Namespace(metav1.NamespaceDefault).List(ctx, metav1.ListOptions{Limit: 1})
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

// tenant resolves the per-org(+project) namespace for a request from the
// gateway-minted identity. Pure mapping lives in tenantNS for testability. The
// returned project is the validated sub-scope (DefaultProject for a single-project
// caller) — used for resource labels and the BYO-cluster federation shard.
//
// It takes the REQUEST and nothing else: the tenant is a fact about the caller,
// not about the service. tenantFrom (typed.go) is the same answer for a typed op,
// which reaches the request through cloud.Bridge.
func tenant(c *zip.Ctx) (ns, org, project string, err error) {
	if !principal.Validated(c) {
		// No validated principal — the restored X-Org-Id is a forge. Refuse before
		// mapping to a per-org k8s namespace (provisions/reads ML resources).
		return "", "", "", zip.ErrForbidden("no validated principal")
	}
	return tenantNS(c.Org(), principal.Project(c), c.IsAdmin())
}

// tenantNS maps a gateway org slug + project sub-scope to its tenant namespace.
// Empty org is rejected unless admin (literal "admin" bucket). The org is lowercased
// and validated against orgRE with NO lossy sanitize, making org->namespace
// injective. The DEFAULT project keeps the legacy per-org namespace ("ml-"<org>) so
// existing single-project tenants are byte-identical; a non-default project appends
// "-"<project> (validated against projectRE, same no-fold discipline) and the whole
// label is checked against the 63-char DNS-1123 ceiling. The (org, project) ->
// namespace map is therefore injective across both dimensions.
func tenantNS(rawOrg, rawProject string, isAdmin bool) (ns, org, project string, err error) {
	org = strings.ToLower(strings.TrimSpace(rawOrg))
	switch {
	case org == "" && isAdmin:
		org = "admin" // literal admin bucket
	case org == "":
		return "", "", "", zip.ErrForbidden("X-Org-Id required")
	case !orgRE.MatchString(org):
		return "", "", "", zip.ErrForbidden("invalid org identifier")
	}
	// Default project → legacy per-org namespace (backward-compatible).
	if principal.IsDefaultProject(rawProject) {
		return nsPrefix + org, org, principal.DefaultProject, nil
	}
	project = strings.ToLower(strings.TrimSpace(rawProject))
	if !projectRE.MatchString(project) {
		return "", "", "", zip.ErrForbidden("invalid project identifier")
	}
	ns = nsPrefix + org + "-" + project
	if len(ns) > 63 {
		return "", "", "", zip.ErrForbidden("org and project together exceed the 63-char kubernetes namespace limit")
	}
	return ns, org, project, nil
}

// ensureNamespace idempotently creates the tenant namespace before the first
// resource lands in it.
func ensureNamespace(s *cloud.Service[state], ctx context.Context, ns, org, project string) error {
	if _, err := s.State.dyn.Resource(k8s.Namespaces).Get(ctx, ns, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	labels := map[string]any{managedByLabel: managedByValue, orgLabel: org}
	if !principal.IsDefaultProject(project) {
		labels[projectLabel] = project
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   ns,
			"labels": labels,
		},
	}}
	if _, err := s.State.dyn.Resource(k8s.Namespaces).Create(ctx, obj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ready(s *cloud.Service[state]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "ml: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials
// name the missing access so the operator knows exactly what to grant the
// cloud-api service account.
func k8sErr(s *cloud.Service[state], k resourceKind, op string, err error) error {
	s.Log.Error("k8s op failed", "op", op, "kind", k.kind, "resource", k.gvr.Resource, "err", err)
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
// infra/k8s/cloud/ml-rbac.yaml (the cloud-ml ClusterRoleBinding grants the
// cloud-ml ServiceAccount its ML cluster role).
const mlTokenFileEnv = "HANZO_ML_TOKEN_FILE"

// dynForOrg returns the client ML operations should target for an org+project: its
// registered BYO cluster (federated via the shared fleet registry, read-only from
// ml's side) or the home in-cluster client when the shard has no attached cluster.
//
// DEFECT, NOT A DESIGN: this is documented as "the ONE federation seam — handlers
// resolve their client through here" and NO handler calls it. Every route above
// reaches s.State.dyn directly, so an org that registers a BYO cluster at
// /v1/clusters gets its models and jobs on the HOME cluster and is told nothing.
// Go does not complain about an unused function, which is why it went unnoticed.
//
// Left UNWIRED on purpose here: routing an op through this changes WHICH CLUSTER a
// resource lands on, which is a behaviour change and not the description task the
// typed migration is. Whoever fixes it owns the migration question too — resources
// already created on the home cluster do not move — so it needs its own change with
// its own tests, not a line slipped into a typing commit.
func dynForOrg(s *cloud.Service[state], org, project string) dynamic.Interface {
	if d := s.State.fleet.DynForOrg(org, project); d != nil {
		return d
	}
	return s.State.dyn
}

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
	cfg.UserAgent = "hanzo-cloud-ml"
	return dynamic.NewForConfig(cfg)
}

// ── pure helpers ─────────────────────────────────────────────────────────────

// normName is the ONE normalisation of a caller-supplied resource name: lowered
// and trimmed to the DNS-1123 label a CustomResource's metadata.name must be. The
// untyped handlers read it off the path param, a typed op off its In field, and
// both land on this function so the two cannot drift.
func normName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

func reqName(c *zip.Ctx) string { return normName(c.Param("name")) }

// labelsFor stamps the managed-by + tenant-org(+project) labels onto a create. The
// org label is set LAST (after any user labels) so a caller can never override the
// tenant boundary marker; the project label is stamped only for a non-default
// project (attribution within the org — absent for single-project tenants).
func labelsFor(org, project string, user map[string]string) map[string]any {
	m := map[string]any{}
	for k, v := range user {
		m[k] = v
	}
	m[managedByLabel] = managedByValue
	m[orgLabel] = org
	if !principal.IsDefaultProject(project) {
		m[projectLabel] = project
	}
	return m
}

// view trims a CR to an honest, non-bloated shape: name + creation time + live
// status, plus the spec on single-object reads. Namespace is intentionally
// omitted (internal tenant detail).
//
// It returns the mlResource the typed ops publish (typed.go) and the untyped
// create/patch writes with c.JSON — ONE projection, so the shape a caller reads
// cannot depend on which route it came through, and the document describes both.
func view(obj *unstructured.Unstructured, withSpec bool) mlResource {
	v := mlResource{
		Name:      obj.GetName(),
		CreatedAt: obj.GetCreationTimestamp().UTC().Format(time.RFC3339),
	}
	if st, ok, _ := unstructured.NestedMap(obj.Object, "status"); ok {
		v.Status = &st
	}
	if withSpec {
		if sp, ok, _ := unstructured.NestedMap(obj.Object, "spec"); ok {
			v.Spec = &sp
		}
	}
	return v
}

func viewList(items []unstructured.Unstructured) []mlResource {
	out := make([]mlResource, 0, len(items))
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
