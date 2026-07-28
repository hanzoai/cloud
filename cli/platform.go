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

// Platform is a thin client over the LIVE Hanzo Cloud control plane
// (platform.hanzo.ai / api.hanzo.ai → svc `cloud`, the Go binary). Every route it
// calls is served by that one binary and authorized off ONE IAM identity: after
// `hanzo login` the CLI sends the IAM access token as the bearer, and the cloud's
// identity middleware (SanitizeIdentity) validates the JWT and org-scopes the
// caller — no separate platform/service token. A purpose-minted machine token
// still works (flag > env > credential store > IAM login) for automation.
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
	if e.status == http.StatusUnauthorized || e.status == http.StatusForbidden {
		hint = " (run `hanzo login` — your IAM identity authorizes the platform; admin ops need an org-admin or superadmin identity)"
	}
	return fmt.Sprintf("platform %s: HTTP %d: %s%s", e.path, e.status, msg, hint)
}

// do performs one JSON request with the given bearer token, decoding a 2xx body
// into out (when non-nil) and mapping a non-2xx into an *apiError.
func (p *Platform) do(ctx context.Context, method, path, token string, body, out any) error {
	if token == "" {
		return fmt.Errorf("not authenticated: run `hanzo login` (an IAM login now authorizes the platform; a --platform-token / HANZO_PLATFORM_TOKEN still works for machine automation)")
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

// serverMessage pulls the `{ "message": … }` / `{ "error": … }` field the cloud's
// errors use, falling back to the raw (truncated) body.
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
// Apps board — GET /v1/paas/apps, GET /v1/paas/apps/{app}. The live Go cloud's
// fleet drift board (clients/paas): the operator App CRs across the platform
// namespaces, declared/running/latest tags + health + the drift verdict. It is
// org-confined server-side (a SuperAdmin sees the fleet; an OrgAdmin only its own
// org), so the CLI sends NO org filter — identity scopes the view.
// ---------------------------------------------------------------------------

// AppView mirrors clients/paas.AppView (the LIVE board DTO). Tags/health are plain
// strings ("" == unknown, rendered "-"); Drift is kept raw so --json is
// byte-faithful and the drift schema can evolve without a client bump.
type AppView struct {
	ID          string          `json:"id"`  // <org>/<app>/<env>, e.g. hanzoai/iam/main
	Org         string          `json:"org"` // image namespace, e.g. hanzoai
	App         string          `json:"app"`
	Env         string          `json:"env"`
	Repo        string          `json:"repo"`
	Registry    string          `json:"registry"`
	DeclaredTag string          `json:"declaredTag"`
	RunningTag  string          `json:"runningTag"`
	LatestTag   string          `json:"latestTag"`
	Health      string          `json:"health"`
	Phase       string          `json:"phase"`
	Cluster     string          `json:"cluster"`
	Namespace   string          `json:"namespace"`
	Endpoints   []string        `json:"endpoints"`
	Drift       json.RawMessage `json:"drift"`
}

// AppsList is the /v1/paas/apps envelope: ordered rows + a drift summary.
type AppsList struct {
	Apps    []AppView `json:"apps"`
	Summary struct {
		Total   int            `json:"total"`
		ByDrift map[string]int `json:"byDrift"`
	} `json:"summary"`
}

// AppsQuery are the optional /v1/paas/apps filters (server-honored). Env/Health/
// Drift narrow the board; there is deliberately no org filter — the board is
// confined to the caller's org by the validated identity, never a client value.
type AppsQuery struct {
	Env    string
	Health string
	Drift  bool
}

func (p *Platform) Apps(ctx context.Context, q AppsQuery) (*AppsList, error) {
	v := url.Values{}
	if q.Env != "" {
		v.Set("env", q.Env)
	}
	if q.Health != "" {
		v.Set("health", q.Health)
	}
	if q.Drift {
		v.Set("drift", "1")
	}
	path := "/v1/paas/apps"
	if len(v) > 0 {
		path += "?" + v.Encode()
	}
	out := &AppsList{}
	return out, p.do(ctx, http.MethodGet, path, p.token, nil, out)
}

// App gets one app row by its <app> CR name (production by default; the server
// scans the caller's authorized namespaces main→test→dev).
func (p *Platform) App(ctx context.Context, app string) (*AppView, error) {
	out := &AppView{}
	return out, p.do(ctx, http.MethodGet, "/v1/paas/apps/"+url.PathEscape(app), p.token, nil, out)
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
// Clusters — GET /v1/clusters. The live Go cloud's compute fleet (clients/visor):
// Visor-managed node pools + the org's BYO clusters, tenant-scoped server-side by
// the validated org (?owner is the caller's IAM org). No org in the path.
// ---------------------------------------------------------------------------

// NodePool mirrors clients/visor.nodePoolView.
type NodePool struct {
	PoolID    string `json:"poolId"`
	Name      string `json:"name"`
	Size      string `json:"size"`
	Count     int    `json:"count"`
	MinNodes  int    `json:"minNodes"`
	MaxNodes  int    `json:"maxNodes"`
	AutoScale bool   `json:"autoScale"`
}

// Cluster mirrors clients/visor.clusterView — the LIVE cluster DTO. `kind` is
// "managed" (Visor-provisioned) or "byo" (attached kubeconfig).
type Cluster struct {
	DoksClusterID string     `json:"doksClusterId"`
	DoClusterID   string     `json:"doClusterId"`
	Name          string     `json:"name"`
	Region        string     `json:"region"`
	Status        string     `json:"status"`
	NodePools     []NodePool `json:"nodePools"`
	NodeSize      string     `json:"nodeSize"`
	NodeCount     int        `json:"nodeCount"`
	CreatedAt     string     `json:"createdAt"`
	Kind          string     `json:"kind"`
	NvidiaGPU     int        `json:"nvidiaGpu"`
	AmdGPU        int        `json:"amdGpu"`
}

// ID is the stable cluster identifier for display/lookup: the DOKS id when managed,
// else the name (a BYO cluster keys on its attached name).
func (c Cluster) ID() string {
	if c.DoksClusterID != "" {
		return c.DoksClusterID
	}
	return c.Name
}

func (p *Platform) Clusters(ctx context.Context) ([]Cluster, error) {
	var out struct {
		Clusters []Cluster `json:"clusters"`
	}
	err := p.do(ctx, http.MethodGet, "/v1/clusters", p.token, nil, &out)
	return out.Clusters, err
}

// ---------------------------------------------------------------------------
// Deploy — POST /v1/paas/apps/{app}/deploy: a zero-downtime ROLLING RESTART of the
// app's Deployment (re-pulls the declared image, recreates pods). Org-confined
// server-side; an optional env selects the lifecycle namespace (main|test|dev).
// ---------------------------------------------------------------------------

// Redeploy triggers a rolling restart of the named app. env is optional
// (main|test|dev); empty targets production (the first match, main→test→dev).
func (p *Platform) Redeploy(ctx context.Context, app, env string) (*DeployResult, error) {
	path := "/v1/paas/apps/" + url.PathEscape(app) + "/deploy"
	if env != "" {
		path += "?env=" + url.QueryEscape(env)
	}
	out := &DeployResult{}
	if err := p.do(ctx, http.MethodPost, path, p.token, nil, out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("redeploy did not report ok")
	}
	return out, nil
}

// DeployResult is the /deploy acceptance (202): the restarted app + its namespace.
type DeployResult struct {
	OK          bool   `json:"ok"`
	App         string `json:"app"`
	Namespace   string `json:"namespace"`
	Env         string `json:"env"`
	RestartedAt string `json:"restartedAt"`
}

// ---------------------------------------------------------------------------
// Build — POST /v1/runner (platform-native CI, no GitHub builders). Authorized off
// the IAM login exactly like the surfaces above (or a dedicated build token for
// machine automation). Unchanged wire contract.
// ---------------------------------------------------------------------------

// BuildBinary is ONE hanzo.yml `binaries:` entry, sent verbatim. The recipe a
// repo declares IS the request body — there is no CLI-side recipe format.
type BuildBinary struct {
	Name      string   `json:"name" yaml:"name"`
	Main      string   `json:"main,omitempty" yaml:"main"`
	Run       string   `json:"run,omitempty" yaml:"run"`
	Out       string   `json:"out,omitempty" yaml:"out"`
	Ldflags   string   `json:"ldflags,omitempty" yaml:"ldflags"`
	Platforms []string `json:"platforms,omitempty" yaml:"platforms"`
	Image     string   `json:"image,omitempty" yaml:"image"`
}

// BuildReq is the direct-enqueue body. Repo/SHA are required, plus EITHER Image
// (build a container image) or Binaries (build the artifacts hanzo.yml declares).
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

	// The artifact lane: `binaries:` + `bucket:` from the repo's hanzo.yml.
	Binaries []BuildBinary `json:"binaries,omitempty"`
	Bucket   string        `json:"bucket,omitempty"`
	Tag      string        `json:"tag,omitempty"`
}

// BuildJob is the enqueue acceptance (HTTP 202). Image is the image lane's
// output; Index is the artifact lane's binaries.json — a build has one or other.
type BuildJob struct {
	BuildJobID string `json:"buildJobId"`
	Status     string `json:"status"`
	RunnerPool string `json:"runnerPool"`
	Image      string `json:"image"`
	Target     string `json:"target"`
	Index      string `json:"index,omitempty"`
}

// EnqueueBuild enqueues a native build. buildToken is resolved by the caller (IAM
// login is the final fallback; a dedicated build token wins when present).
func (p *Platform) EnqueueBuild(ctx context.Context, req BuildReq, buildToken string) (*BuildJob, error) {
	if buildToken == "" {
		return nil, fmt.Errorf("not authenticated: run `hanzo login` (an IAM login now authorizes builds; HANZO_BUILD_TOKEN / --build-token still works for machine automation)")
	}
	out := &BuildJob{}
	return out, p.do(ctx, http.MethodPost, "/v1/runner", buildToken, req, out)
}
