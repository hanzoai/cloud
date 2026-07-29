package cloudflare

// pages.go — Cloudflare Pages, WIRED. Each handler takes the caller-org client + its
// account id through the ONE shared preamble — acctClient for reads, acctWrite (org
// admin) for mutations — then proxies to /accounts/{account_id}/pages/* via cl.pass.
// Request bodies decode into the typed params ported from the platform's cloudflare.ts
// (PagesProjectCreateParams et al.), so only modeled fields reach Cloudflare;
// responses relay verbatim (no field loss).

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// ── ported request structs (mirror platform pkg/platform/src/services/cloudflare.ts) ──

// PagesKVBinding / PagesD1Binding / PagesR2Binding are the deployment-config resource
// bindings (ported from PagesDeploymentConfig).
type PagesKVBinding struct {
	NamespaceID string `json:"namespace_id"`
}
type PagesD1Binding struct {
	ID string `json:"id"`
}
type PagesR2Binding struct {
	Name string `json:"name"`
}

// PagesEnvVar is one deployment env var (plain_text | secret_text).
type PagesEnvVar struct {
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

// PagesBuildConfig is the project build config.
type PagesBuildConfig struct {
	BuildCommand   string `json:"build_command,omitempty"`
	DestinationDir string `json:"destination_dir,omitempty"`
	RootDir        string `json:"root_dir,omitempty"`
}

// PagesDeploymentConfig is a preview/production deployment config.
type PagesDeploymentConfig struct {
	CompatibilityDate  string                    `json:"compatibility_date,omitempty"`
	CompatibilityFlags []string                  `json:"compatibility_flags,omitempty"`
	EnvVars            map[string]PagesEnvVar    `json:"env_vars,omitempty"`
	KVNamespaces       map[string]PagesKVBinding `json:"kv_namespaces,omitempty"`
	D1Databases        map[string]PagesD1Binding `json:"d1_databases,omitempty"`
	R2Buckets          map[string]PagesR2Binding `json:"r2_buckets,omitempty"`
}

// PagesDeploymentConfigs pairs the preview + production deployment configs.
type PagesDeploymentConfigs struct {
	Preview    *PagesDeploymentConfig `json:"preview,omitempty"`
	Production *PagesDeploymentConfig `json:"production,omitempty"`
}

// PagesProjectCreate is the create-project request body (ported from
// PagesProjectCreateParams). The platform sends {name, production_branch}; the full
// shape is modeled so a richer caller is forwarded faithfully.
type PagesProjectCreate struct {
	Name              string                  `json:"name"`
	ProductionBranch  string                  `json:"production_branch,omitempty"`
	BuildConfig       *PagesBuildConfig       `json:"build_config,omitempty"`
	DeploymentConfigs *PagesDeploymentConfigs `json:"deployment_configs,omitempty"`
}

// ── handlers ────────────────────────────────────────────────────────────────────

// noInput is the In of an op addressed entirely by the caller's principal: it takes
// nothing off the wire.
type noInput struct{}

// projectRef addresses one Pages project by name, from the path.
type projectRef struct {
	// Project is the Pages project name.
	Project string `json:"project"`
}

// PagesList lists the org's Cloudflare Pages projects. Any org member may read.
func (o ops) pagesList(ctx context.Context, _ *noInput) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/pages/projects", nil)
}

// PagesGet reads one Cloudflare Pages project — its build config, deployment
// configs and latest deployment. Any org member may read.
//
// Example: {"project": "marketing-site"}
func (o ops) pagesGet(ctx context.Context, in *projectRef) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := seg("project", in.Project, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/pages/projects/"+proj, nil)
}

// PagesCreate creates a Cloudflare Pages project on the org's account. Requires
// org admin. Only the modeled fields reach Cloudflare, so an unmodeled key in the
// request is dropped rather than forwarded.
//
// Example: {"name": "marketing-site", "production_branch": "main"}
func (o ops) pagesCreate(ctx context.Context, in *PagesProjectCreate) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	if !nameRE.MatchString(strings.TrimSpace(in.Name)) {
		return nil, zip.ErrBadRequest("project name is invalid")
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/pages/projects", *in)
}

// PagesDelete deletes a Cloudflare Pages project, and with it every deployment it
// has ever made. Requires org admin.
//
// Example: {"project": "marketing-site"}
func (o ops) pagesDelete(ctx context.Context, in *projectRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := seg("project", in.Project, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/pages/projects/"+proj, nil)
}

// pagesDeploy triggers a new Pages deployment. Requires org admin.
//
// NOT a typed op: a body this handler cannot parse is IGNORED — the deployment
// falls back to the project's production branch — where a typed In answers 400.
// Those are different contracts, and typing it would change what the route accepts.
func (o ops) pagesDeploy(c *zip.Ctx) error {
	ctx := c.Context()
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return err
	}
	proj, err := pathSeg(c, "project", nameRE)
	if err != nil {
		return err
	}
	// A Git-connected project builds from a branch (or its production branch when
	// none is given); an absent branch is an empty POST.
	var in struct {
		Branch string `json:"branch"`
	}
	_ = json.Unmarshal(c.Body(), &in)
	var body any
	if b := strings.TrimSpace(in.Branch); b != "" {
		body = map[string]string{"branch": b}
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/pages/projects/"+proj+"/deployments", body)
}

// domainAddIn attaches a custom domain to a Pages project.
type domainAddIn struct {
	// Project is the Pages project name, from the path.
	Project string `json:"project"`
	// Name is the custom domain to attach, e.g. "www.acme.com".
	Name string `json:"name"`
}

// PagesDomainAdd attaches a custom domain to a Cloudflare Pages project. Requires
// org admin. Cloudflare owns validation and certificate issuance from here on.
//
// Example: {"project": "marketing-site", "name": "www.acme.com"}
func (o ops) pagesDomainAdd(ctx context.Context, in *domainAddIn) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := seg("project", in.Project, nameRE)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, zip.ErrBadRequest("domain name is required")
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/pages/projects/"+proj+"/domains",
		map[string]string{"name": name})
}

// domainRef addresses one custom domain on one Pages project, both from the path.
type domainRef struct {
	// Project is the Pages project name.
	Project string `json:"project"`
	// Domain is the attached custom domain to detach.
	Domain string `json:"domain"`
}

// PagesDomainDelete detaches a custom domain from a Cloudflare Pages project.
// Requires org admin.
//
// Example: {"project": "marketing-site", "domain": "www.acme.com"}
func (o ops) pagesDomainDelete(ctx context.Context, in *domainRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	proj, err := seg("project", in.Project, nameRE)
	if err != nil {
		return nil, err
	}
	dom, err := seg("domain", in.Domain, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/pages/projects/"+proj+"/domains/"+dom, nil)
}
