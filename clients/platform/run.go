// run.go — POST /v1/run, the container-serverless one-shot.
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
// (s.tenant → provisioning.SanitizeOrg), never a request value — the same
// cross-tenant isolation boundary as the rest of platform.
package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

const (
	// runFeeEnvPrefix names the operator knob for the flat run fee (ResourceFeeCents);
	// unset ⇒ the $1.00 policy default. A create/run fee only — no GB-seconds invented.
	runFeeEnvPrefix = "CLOUD_PLATFORM_RUN_FEE_CENTS"
	runKind         = "run"
)

// runReq is the CLI contract for POST /v1/run. The org is NEVER read from here —
// it is resolved from the validated identity by s.tenant.
type runReq struct {
	Name     string       `json:"name"`
	Image    string       `json:"image"`
	Runtime  string       `json:"runtime"` // accepted for the client contract; the image is the runtime unit.
	Port     int          `json:"port"`
	Shape    string       `json:"shape"` // compute size label, echoed back; sizing is the operator's default.
	MinScale int          `json:"minScale"`
	MaxScale int          `json:"maxScale"`
	GPU      int          `json:"gpu"`
	Env      []EnvVarJSON `json:"env"`
}

// runView is the CLI response: the run's identity, live URL and status.
type runView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
	Shape  string `json:"shape"`
}

func run(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := tenant(s, c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body runReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		return zip.ErrBadRequest("name is required")
	}
	slug := normalizeSlug("", name)
	if !slugRE.MatchString(slug) {
		return zip.ErrBadRequest("name must resolve to a slug matching ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	image := strings.TrimSpace(body.Image)
	if image == "" {
		return zip.ErrBadRequest("image is required")
	}
	if body.GPU < 0 {
		return zip.ErrBadRequest("gpu must be >= 0")
	}
	// Validate env keys at the boundary (same rule as createApp) before anything is
	// sealed or persisted.
	for _, e := range body.Env {
		if !envKeyRE.MatchString(e.Key) {
			return zip.ErrBadRequest("env key must match ^[A-Za-z_][A-Za-z0-9_]*$")
		}
	}
	// Scale bounds: minScale is the replica floor (clamped to [1,maxReplicas]).
	// maxScale>0 declares an autoscaling ceiling (clamped, >=minScale); maxScale==0
	// means no HPA — a fixed run at minScale (serviceCR omits the autoscaling block).
	minScale := s.State.k8s.limits.clampReplicas(body.MinScale)
	maxScale := 0
	if body.MaxScale > 0 {
		maxScale = s.State.k8s.limits.clampReplicas(body.MaxScale)
		if maxScale < minScale {
			maxScale = minScale
		}
	}

	// Per-org prepaid gate BEFORE any cluster write: the run's OWN org pays (the org
	// resolved above is sent as both the commerce user and X-Org-Id), never a
	// default — the anti-cross-tenant billing property (resource_billing.go).
	fee := cloud.ResourceFeeCents(runFeeEnvPrefix, runKind)
	project, projectValidated := principal.ValidatedProject(c)
	if err := s.Bill.Gate(c.Context(), principal.Ledger(c), project, projectValidated, runKind, fee); err != nil {
		return cloud.DenyResource(c, err)
	}

	// Fail closed if the cluster is unreachable — a run that cannot write its CR must
	// never report a fabricated URL/status.
	if err := s.State.k8s.ready(); err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "cluster unavailable: %v", err)
	}

	// Seal secret env into KMS so plaintext is never persisted (same choke point as
	// createApp); fails closed if a secret is present without KMS.
	sealedEnv, err := sealSecretEnv(s, c.Context(), org, slug, body.Env)
	if err != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	envJSON, _ := json.Marshal(sealedEnv)

	// Always attach the canonical default host so the run gets a working HTTPS URL
	// the moment the operator reconciles its ingress.
	domains := seedDefaultDomain(s, org, slug, nil)
	domainsJSON, _ := json.Marshal(domains)

	repo, tag := splitImageRef(image)
	now := time.Now().Unix()

	project, herr := ensureRunProject(s, c.Context(), org)
	if herr != nil {
		return herr
	}

	a, err := s.State.store.GetApplication(c.Context(), org, project, slug)
	switch {
	case errors.Is(err, errNotFound):
		id, gerr := genID("app")
		if gerr != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
		}
		a = Application{
			ID: id, Org: org, ProjectID: project, Slug: slug, Name: name,
			Environment: "production", Source: "image", ImageRepo: repo, ImageTag: tag,
			BuildType: "image", Port: portOr(body.Port), Replicas: minScale,
			MinScale: minScale, MaxScale: maxScale, EnvJSON: string(envJSON),
			DomainsJSON: string(domainsJSON), Status: "deploying", Namespace: tenantNamespace(org),
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.State.store.CreateApplication(c.Context(), a); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
	case err != nil:
		return zip.Errorf(http.StatusInternalServerError, "get app: %v", err)
	default:
		// Re-run: converge the existing app to the requested spec, preserving identity.
		a.Name, a.Source, a.BuildType = name, "image", "image"
		a.ImageRepo, a.ImageTag, a.Port = repo, tag, portOr(body.Port)
		a.Replicas, a.MinScale, a.MaxScale = minScale, minScale, maxScale
		a.EnvJSON, a.DomainsJSON = string(envJSON), string(domainsJSON)
		a.Status, a.Namespace, a.UpdatedAt = "deploying", tenantNamespace(org), now
		if err := s.State.store.UpdateApplication(c.Context(), a); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}
	}

	// The ONE Service-CR writer (image + autoscaling min/max + port + ingress). The
	// operator reconciles the rollout; secret-env sync is declared best-effort.
	if err := s.State.k8s.applyService(c.Context(), org, project, a, image); err != nil {
		return zip.Errorf(deployErrStatus(err), "apply Service CR: %v", err)
	}
	ensureSecretSync(s, c.Context(), org, a)

	// Record the paid unit on the run's OWN org ledger (fire-and-forget).
	s.Bill.Meter(principal.Ledger(c), principal.Project(c), runKind, fee, c.RequestID(), cloud.ClientIP(c))

	s.Log.Info("run (container-serverless)", "org", org, "app", slug, "ns", tenantNamespace(org),
		"image", image, "min", minScale, "max", maxScale, "actor", c.User(), "requestID", c.RequestID())

	return c.JSON(http.StatusAccepted, runView{
		ID:     a.ID,
		Name:   a.Name,
		URL:    "https://" + defaultHost(s, org, slug),
		Status: a.Status,
		Shape:  firstNonEmpty(strings.TrimSpace(body.Shape), "auto"),
	})
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
		// The project store being unreachable must not take /v1/run down for the
		// implicit default — the row is owed by provisioning, not load-bearing.
		s.Log.Warn("run: project store unavailable; proceeding under the implicit default project", "org", org, "err", err)
		return name, nil
	}
	if !ok {
		s.Log.Info("run: default project row absent in IAM; proceeding (IAM provisioning owes it)", "org", org)
	}
	return name, nil
}
