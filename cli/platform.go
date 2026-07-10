package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Platform is a thin client over the platform.hanzo.ai /v1 control plane. That
// surface is machine-to-machine (service-token, "No OIDC" — it cannot validate
// IAM user tokens), so the token here is the platform service token, resolved
// from flag/env/credential store by the caller; the build endpoint takes its
// own token per call.
type Platform struct {
	baseURL string
	token   string
	http    *http.Client
}

func newPlatform(baseURL, token string) *Platform {
	return &Platform{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// apiError carries the HTTP status + server message for a failed call so
// commands can give precise diagnostics (e.g. 401 → token problem).
type apiError struct {
	status  int
	message string
	path    string
}

func (e *apiError) Error() string {
	msg := e.message
	if msg == "" {
		msg = http.StatusText(e.status)
	}
	hint := ""
	if e.status == http.StatusUnauthorized {
		hint = " (set the platform service token: --platform-token, HANZO_PLATFORM_TOKEN, or `hanzo login --platform-token`)"
	}
	return fmt.Sprintf("platform %s: HTTP %d: %s%s", e.path, e.status, msg, hint)
}

// do performs one JSON request with the given bearer token, decoding a 2xx body
// into out (when non-nil) and mapping a non-2xx into an *apiError.
func (p *Platform) do(ctx context.Context, method, path, token string, body, out any) error {
	if token == "" {
		return fmt.Errorf("no platform token: pass --platform-token, set HANZO_PLATFORM_TOKEN, or run `hanzo login --platform-token <tok>`")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	if resp.StatusCode/100 != 2 {
		return &apiError{status: resp.StatusCode, message: serverMessage(raw), path: path}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("platform %s: decode response: %w", path, err)
		}
	}
	return nil
}

// serverMessage pulls the `{ "message": … }` field platform errors use, falling
// back to the raw (truncated) body.
func serverMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		if e.Message != "" {
			return e.Message
		}
		if e.Error != "" {
			return e.Error
		}
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// Apps board — GET /v1/apps, GET /v1/apps/{id}, POST /v1/apps/sync.
// ---------------------------------------------------------------------------

// AppView mirrors the platform apps-lifecycle DTO. Nullable columns are *string
// so JSON null round-trips; Drift is kept raw so --json is byte-faithful and
// the drift schema can evolve without a client bump.
type AppView struct {
	ID            string          `json:"id"`
	Org           string          `json:"org"`
	App           string          `json:"app"`
	Env           string          `json:"env"`
	Repo          string          `json:"repo"`
	Registry      string          `json:"registry"`
	DeclaredTag   *string         `json:"declaredTag"`
	RunningTag    *string         `json:"runningTag"`
	LatestTag     *string         `json:"latestTag"`
	ReleaseURL    *string         `json:"releaseUrl"`
	ReleaseAssets int             `json:"releaseAssets"`
	Health        *string         `json:"health"`
	Cluster       *string         `json:"cluster"`
	Namespace     *string         `json:"namespace"`
	LastObserved  *string         `json:"lastObserved"`
	UpdatedAt     string          `json:"updatedAt"`
	Drift         json.RawMessage `json:"drift"`
}

// AppsList is the /v1/apps envelope: ordered rows + a drift summary.
type AppsList struct {
	Apps    []AppView `json:"apps"`
	Summary struct {
		Total   int            `json:"total"`
		ByDrift map[string]int `json:"byDrift"`
	} `json:"summary"`
}

// AppsQuery are the optional /v1/apps filters.
type AppsQuery struct {
	Org    string
	Env    string
	Health string
	Drift  bool
}

func (p *Platform) Apps(ctx context.Context, q AppsQuery) (*AppsList, error) {
	v := url.Values{}
	if q.Org != "" {
		v.Set("org", q.Org)
	}
	if q.Env != "" {
		v.Set("env", q.Env)
	}
	if q.Health != "" {
		v.Set("health", q.Health)
	}
	if q.Drift {
		v.Set("drift", "1")
	}
	path := "/v1/apps"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	out := &AppsList{}
	return out, p.do(ctx, http.MethodGet, path, p.token, nil, out)
}

func (p *Platform) App(ctx context.Context, id, org string) (*AppView, error) {
	path := "/v1/apps/" + id
	if org != "" {
		path += "?org=" + url.QueryEscape(org)
	}
	out := &AppView{}
	return out, p.do(ctx, http.MethodGet, path, p.token, nil, out)
}

func (p *Platform) SyncApps(ctx context.Context) error {
	return p.do(ctx, http.MethodPost, "/v1/apps/sync", p.token, nil, nil)
}

// driftSeverity extracts the severity string from the raw drift object.
func driftSeverity(raw json.RawMessage) string {
	var d struct {
		Severity string `json:"severity"`
	}
	if json.Unmarshal(raw, &d) == nil && d.Severity != "" {
		return d.Severity
	}
	return "-"
}

// ---------------------------------------------------------------------------
// Dedicated clusters — /v1/org/{org}/cluster[ /select | /{id}/install-baseline ].
// ---------------------------------------------------------------------------

// Cluster mirrors a doks_cluster record. `status` is DigitalOcean state; `phase`
// is the platform provisioning lifecycle — orthogonal (a DO-running cluster is
// not a usable target until phase=ready).
type Cluster struct {
	DoksClusterID     string          `json:"doksClusterId"`
	Name              string          `json:"name"`
	DoClusterID       *string         `json:"doClusterId"`
	Region            string          `json:"region"`
	Status            string          `json:"status"`
	Endpoint          *string         `json:"endpoint"`
	K8sVersion        *string         `json:"k8sVersion"`
	HA                bool            `json:"ha"`
	Phase             string          `json:"phase"`
	OperatorInstalled bool            `json:"operatorInstalled"`
	BaselineInstalled bool            `json:"baselineInstalled"`
	Active            bool            `json:"active"`
	BaselineError     *string         `json:"baselineError"`
	OrganizationID    string          `json:"organizationId"`
	CreatedAt         string          `json:"createdAt"`
	Tags              []string        `json:"tags"`
	MaintenancePolicy json.RawMessage `json:"maintenancePolicy,omitempty"`
}

// ProvisionReq is the dedicated-cluster provisioning body (org forced by path).
type ProvisionReq struct {
	Region    string `json:"region,omitempty"`
	HA        bool   `json:"ha,omitempty"`
	NodeSize  string `json:"nodeSize,omitempty"`
	NodeCount int    `json:"nodeCount,omitempty"`
}

// Target is the redacted ClusterTargetView — the kubeconfig is never present.
type Target struct {
	Cluster    string            `json:"cluster"`
	Namespaces map[string]string `json:"namespaces"`
	Dedicated  bool              `json:"dedicated"`
}

func (p *Platform) Clusters(ctx context.Context, org string) ([]Cluster, error) {
	var out struct {
		Clusters []Cluster `json:"clusters"`
	}
	err := p.do(ctx, http.MethodGet, "/v1/org/"+url.PathEscape(org)+"/cluster", p.token, nil, &out)
	return out.Clusters, err
}

func (p *Platform) ProvisionCluster(ctx context.Context, org string, req ProvisionReq) (*Cluster, error) {
	var out struct {
		Cluster Cluster `json:"cluster"`
	}
	err := p.do(ctx, http.MethodPost, "/v1/org/"+url.PathEscape(org)+"/cluster", p.token, req, &out)
	return &out.Cluster, err
}

func (p *Platform) Target(ctx context.Context, org string) (*Target, error) {
	var out struct {
		Target Target `json:"target"`
	}
	err := p.do(ctx, http.MethodGet, "/v1/org/"+url.PathEscape(org)+"/cluster/select", p.token, nil, &out)
	return &out.Target, err
}

// SelectTarget activates a dedicated cluster as the org's deploy target, or
// reverts to the shared cluster when clusterID is nil.
func (p *Platform) SelectTarget(ctx context.Context, org string, clusterID *string) (*Target, error) {
	var out struct {
		Target Target `json:"target"`
	}
	body := map[string]any{"doksClusterId": clusterID}
	err := p.do(ctx, http.MethodPost, "/v1/org/"+url.PathEscape(org)+"/cluster/select", p.token, body, &out)
	return &out.Target, err
}

func (p *Platform) InstallBaseline(ctx context.Context, org, clusterID string) error {
	path := "/v1/org/" + url.PathEscape(org) + "/cluster/" + url.PathEscape(clusterID) + "/install-baseline"
	return p.do(ctx, http.MethodPost, path, p.token, nil, nil)
}

// ---------------------------------------------------------------------------
// Deploy — POST …/container/{id}/redeploy (rolling restart, zero-downtime).
// ---------------------------------------------------------------------------

// Redeploy triggers a rolling restart of the container's k8s Deployment. The
// coordinates are exact (the platform validates org+project+env+container scope).
func (p *Platform) Redeploy(ctx context.Context, org, project, env, container string) error {
	path := fmt.Sprintf("/v1/org/%s/project/%s/env/%s/container/%s/redeploy",
		url.PathEscape(org), url.PathEscape(project), url.PathEscape(env), url.PathEscape(container))
	var out struct {
		OK bool `json:"ok"`
	}
	if err := p.do(ctx, http.MethodPost, path, p.token, nil, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("redeploy did not report ok")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Build — POST /v1/runner (platform-native CI, no GitHub builders).
// ---------------------------------------------------------------------------

// BuildReq is the direct-enqueue body. Repo/SHA/Image are required.
type BuildReq struct {
	Repo           string `json:"repo"`
	SHA            string `json:"sha"`
	Image          string `json:"image"`
	Branch         string `json:"branch,omitempty"`
	Ref            string `json:"ref,omitempty"`
	Dockerfile     string `json:"dockerfile,omitempty"`
	Context        string `json:"context,omitempty"`
	DockerTarget   string `json:"dockerTarget,omitempty"`
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	OrganizationID string `json:"organizationId,omitempty"`
}

// BuildJob is the enqueue acceptance (HTTP 202).
type BuildJob struct {
	BuildJobID string `json:"buildJobId"`
	Status     string `json:"status"`
	RunnerPool string `json:"runnerPool"`
	Image      string `json:"image"`
	Target     string `json:"target"`
}

// EnqueueBuild enqueues a native build. It authenticates with the dedicated
// build-callback token, not the service token.
func (p *Platform) EnqueueBuild(ctx context.Context, req BuildReq, buildToken string) (*BuildJob, error) {
	if buildToken == "" {
		return nil, fmt.Errorf("no build token: set HANZO_BUILD_TOKEN / PLATFORM_BUILD_CALLBACK_TOKEN or `hanzo login --build-token <tok>`")
	}
	out := &BuildJob{}
	return out, p.do(ctx, http.MethodPost, "/v1/runner", buildToken, req, out)
}
