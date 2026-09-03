// run.go — POST /v1/platform/run, the container-serverless one-shot.
//
// It is the single-call shortcut over the project → app → deploy flow: given an
// image (and optional port / scale bounds / env), it create-or-updates an
// image-source Application in the org's default project and writes the operator
// hanzo.ai/v1 Service CR through the ONE shared writer (k8sClient.applyService /
// serviceCR), so a run is a first-class Application — listable, stoppable and
// redeployable via the /v1/platform routes — and re-running the same name UPDATES
// it in place (idempotent). There is NO parallel Service-CR writer here: this
// handler reuses deploy.go's machinery (s.tenant, the store, sealSecretEnv,
// seedDefaultDomain, applyService) end to end.
//
// Every cluster write targets tenant-<org> derived from the VALIDATED org
// (s.tenant → namespace.Sanitize), never a request value — the same
// cross-tenant isolation boundary as the rest of platform.

package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

const (
	// runFeeEnvPrefix names the operator knob for the flat run fee (ResourceFeeCents);
	// unset ⇒ the $1.00 policy default. A create/run fee only — no GB-seconds invented.
	runFeeEnvPrefix = "CLOUD_PLATFORM_RUN_FEE_CENTS"
	runKind         = "run"
)

// runReq is the CLI contract for POST /v1/platform/run. The org is NEVER read from here —
// it is resolved from the validated identity.
//
// Every field carries `url:"-"`: zip's binder fills an In field from the query
// string as well as the body, and this route has never taken a run's fields there,
// so without the opt-out `?image=other` would silently run something the body did
// not ask for.
type runReq struct {
	// Name is the run's name, and the slug is derived from it. Required, and it
	// must resolve to `^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`. Re-running the same
	// name updates that run in place.
	Name string `json:"name" url:"-"`
	// Image is the container image to run. Required.
	Image string `json:"image" url:"-"`
	// Runtime is accepted for the client contract and echoed nowhere: the image
	// IS the runtime unit.
	Runtime string `json:"runtime" url:"-"`
	// Port is the container port the run listens on.
	Port int `json:"port" url:"-"`
	// Shape is a compute size label, echoed back; sizing is the operator's
	// default. Defaults to "auto".
	Shape string `json:"shape" url:"-"`
	// MinScale is the replica floor, clamped to the deployment's limit.
	MinScale int `json:"minScale" url:"-"`
	// MaxScale above the floor declares an autoscaling ceiling; 0 means no
	// autoscaler at all — a fixed run at the floor.
	MaxScale int `json:"maxScale" url:"-"`
	// GPU is how many GPUs the run asks for; a negative value is 400.
	GPU int `json:"gpu" url:"-"`
	// Env is the run's environment. Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`;
	// a variable marked `secret: true` is sealed into KMS.
	Env []EnvVarJSON `json:"env" url:"-"`
}

// runView is the CLI response: the run's identity, live URL and status.
type runView struct {
	// ID is the application id the run created or converged.
	ID string `json:"id"`
	// Name is the run's name, as stored.
	Name string `json:"name"`
	// URL is the run's live HTTPS address.
	URL string `json:"url"`
	// Status is the application's state — `deploying` on a fresh accept.
	Status string `json:"status"`
	// Shape is the compute size label the request asked for, or "auto".
	Shape string `json:"shape"`
}

// run runs a container image and gives back a URL.
//
// The one-call shortcut over project → app → deploy: give it a `name` and an
// `image` and it creates or updates an image-source application in your org's
// DEFAULT project, deploys it through the same operator Service-CR writer
// everything else uses, and answers its id, name, live URL, status and shape.
// Re-running the same name UPDATES it in place, so the call is idempotent by name.
//
// What it produces is a first-class application, not a special object: it is
// listable, stoppable and redeployable through the /v1/platform routes like any
// other app.
//
// `minScale` is the replica floor. `maxScale` above it declares an autoscaling
// ceiling; `maxScale: 0` means no autoscaler at all — a fixed run at the floor.
// Both are clamped to the deployment's limits. `runtime` and `shape` are accepted
// for the client contract and echoed back: the image is the runtime unit and sizing
// is the operator's default.
//
// It is BILLING-GATED before it touches the cluster: a flat per-run fee is
// authorized against the org's own prepaid balance first, so an org that cannot pay
// is refused without anything being created. An unreachable cluster is 503 — a run
// never reports a URL it did not create. Secret env is sealed into KMS and fails
// closed without it.
//
// Requires a validated principal; 403 without one. The org is resolved from that
// validated identity and is what both pays and owns the namespace — it is never
// read from the body.
func (o ops) run(ctx context.Context, body *runReq) (*runView, error) {
	s := o.s
	c, org, err := o.caller(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("name is required")
	}
	slug := normalizeSlug("", name)
	if !slugRE.MatchString(slug) {
		return nil, zip.ErrBadRequest("name must resolve to a slug matching ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		return nil, zip.ErrBadRequest("image is required")
	}
	if body.GPU < 0 {
		return nil, zip.ErrBadRequest("gpu must be >= 0")
	}
	// Validate env keys at the boundary (same rule as createApp) before anything is
	// sealed or persisted.
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return nil, zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	// Scale bounds: minScale is the replica floor (clamped to [1,maxReplicas]).
	// maxScale>0 declares an autoscaling ceiling (clamped, >=minScale); maxScale==0
	// means no HPA — a fixed run at minScale (serviceCR omits the autoscaling block).
	minScale := s.State.k8s.limits.clampReplicas(body.MinScale)
	maxScale := 0
	if body.MaxScale > 0 {
		maxScale = max(s.State.k8s.limits.clampReplicas(body.MaxScale), minScale)
	}

	// Per-org prepaid gate BEFORE any cluster write: the run's OWN org pays (the org
	// resolved above is sent as both the commerce user and X-Org-Id), never a
	// default — the anti-cross-tenant billing property (resource_billing.go).
	fee := cloud.ResourceFeeCents(runFeeEnvPrefix, runKind)
	gateProject, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Authorize(ctx, principal.Payer(c), gateProject, projectValidated, runKind, fee); err != nil {
		return nil, cloud.DenyResource(c, err)
	}

	// Fail closed if the cluster is unreachable — a run that cannot write its CR must
	// never report a fabricated URL/status.
	if err := s.State.k8s.ready(); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}

	// Seal secret env into KMS so plaintext is never persisted (same choke point as
	// createApp); fails closed if a secret is present without KMS.
	sealedEnv, err := sealSecretEnv(s, ctx, org, slug, body.Env)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealedEnv)

	// Always attach the canonical default host so the run gets a working HTTPS URL
	// the moment the operator reconciles its ingress.
	domains := seedDefaultDomain(s, org, slug, nil)
	domainsJSON, _ := json.Marshal(domains)

	repo, tag := splitImageRef(image)
	now := time.Now().Unix()

	project, err := ensureRunProject(s, ctx, org)
	if err != nil {
		return nil, err
	}

	a, err := s.State.store.GetApplication(ctx, org, project, slug)
	switch {
	case errors.Is(err, errNotFound):
		id := genID("app")
		a = Application{
			ID: id, Org: org, ProjectID: project, Slug: slug, Name: name,
			Environment: "production", Source: "image", ImageRepo: repo, ImageTag: tag,
			BuildType: "image", Port: portOr(body.Port), Replicas: minScale,
			MinScale: minScale, MaxScale: maxScale, EnvJSON: string(envJSON),
			DomainsJSON: string(domainsJSON), Status: "deploying", Namespace: tenantNamespace(org),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.State.store.CreateApplication(ctx, a); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
	case err != nil:
		return nil, zip.Errorf(http.StatusInternalServerError, "get app: %v", err)
	default:
		// Re-run: converge the existing app to the requested spec, preserving identity.
		a.Name, a.Source, a.BuildType = name, "image", "image"
		a.ImageRepo, a.ImageTag, a.Port = repo, tag, portOr(body.Port)
		a.Replicas, a.MinScale, a.MaxScale = minScale, minScale, maxScale
		a.EnvJSON, a.DomainsJSON = string(envJSON), string(domainsJSON)
		a.Status, a.Namespace, a.UpdatedAt = "deploying", tenantNamespace(org), now
		if err := s.State.store.UpdateApplication(ctx, a); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
	}

	// The ONE Service-CR writer (image + autoscaling min/max + port + ingress). The
	// operator reconciles the rollout; secret-env sync is declared best-effort.
	if err := s.State.k8s.applyService(ctx, org, project, a, image); err != nil {
		return nil, zip.Errorf(deployErrStatus(err), "apply Service CR: %v", err)
	}
	ensureSecretSync(s, ctx, org, a)

	// Record the paid unit on the run's OWN org ledger (fire-and-forget).
	s.Bill.Record(principal.Payer(c), runKind, metering.Usage{
		Model:       runKind,
		AmountCents: fee,
		Project:     principal.Project(c),
		RequestID:   c.RequestID(),
		ClientIP:    cloud.ClientIP(c),
	})

	s.Log.Info("run (container-serverless)", "org", org, "app", slug, "ns", tenantNamespace(org),
		"image", image, "min", minScale, "max", maxScale, "actor", c.User(), "requestID", c.RequestID())

	return &runView{
		ID:     a.ID,
		Name:   a.Name,
		URL:    "https://" + defaultHost(s, org, slug),
		Status: a.Status,
		Shape:  cmp.Or(strings.TrimSpace(body.Shape), "auto"),
	}, nil
}

// ensureRunProject resolves the project a run lands in. The DEFAULT project is
// IMPLICIT: it is part of what an org IS, so a run under it proceeds whether or
// not IAM has materialized the row yet — platform still never CREATES a project
// (that is IAM's, at /v1/iam/projects), it just declines to fail an org for a
// row IAM owes it. An explicit, non-default project must exist, exactly like
// the apps routes require.
func ensureRunProject(s *cloud.Service[state], ctx context.Context, org string) (string, error) {
	name := principal.DefaultProject
	ok, err := s.State.projects.Exists(ctx, org, name)
	if err != nil {
		// The project store being unreachable must not take /v1/platform/run down for the
		// implicit default — the row is owed by provisioning, not load-bearing.
		s.Log.Warn("run: project store unavailable; proceeding under the implicit default project", "org", org, "err", err)
		return name, nil
	}
	if !ok {
		s.Log.Info("run: default project row absent in IAM; proceeding (IAM provisioning owes it)", "org", org)
	}
	return name, nil
}
