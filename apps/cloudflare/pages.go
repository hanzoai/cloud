package cloudflare

// pages.go — Cloudflare Pages, WIRED. Each handler takes the caller-org client + its
// account id through the ONE shared preamble — acctClient for reads, acctWrite (org
// admin) for mutations — then proxies to /accounts/{account_id}/pages/* via cl.pass.
// Request bodies decode into the typed params ported from the platform's cloudflare.ts
// (PagesProjectCreateParams et al.), so only modeled fields reach Cloudflare;
// responses relay verbatim (no field loss).

import (
	"github.com/hanzoai/cloud"
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
	// NamespaceID is the KV namespace this binding points at, by Cloudflare's id
	// rather than its title. The BINDING NAME — what the Worker code reads it as — is
	// the map key this value sits under, not a field here.
	NamespaceID string `json:"namespace_id"`
}
type PagesD1Binding struct {
	// ID is the D1 database this binding points at, by Cloudflare's uuid. The binding
	// name the Worker code reads it as is the map key, not a field here.
	ID string `json:"id"`
}
type PagesR2Binding struct {
	// Name is the R2 bucket this binding points at, by bucket name — R2 addresses
	// buckets by name where KV and D1 use ids. The binding name the Worker code reads
	// it as is the map key.
	Name string `json:"name"`
}

// PagesEnvVar is one deployment env var (plain_text | secret_text).
type PagesEnvVar struct {
	// Value is the variable's value. Under type "secret_text" Cloudflare encrypts it
	// on arrival and never reads it back, so a later read of the project shows the
	// variable without this.
	Value string `json:"value"`
	// Type is "plain_text" or "secret_text" and decides that: plain text is readable
	// afterwards, secret text is write-only. Empty is Cloudflare's default,
	// plain_text — so a secret with no type set is stored in the clear.
	Type string `json:"type,omitempty"`
}

// PagesBuildConfig is the project build config.
type PagesBuildConfig struct {
	// BuildCommand is what Cloudflare runs to build the site ("npm run build").
	// Omitted means no build step: the repository is published as it stands.
	BuildCommand string `json:"build_command,omitempty"`
	// DestinationDir is the directory the build leaves the site in ("dist"),
	// relative to RootDir. It is what gets served.
	DestinationDir string `json:"destination_dir,omitempty"`
	// RootDir is where in the repository the build runs, for a project that is not at
	// the repository root. Omitted means the root.
	RootDir string `json:"root_dir,omitempty"`
}

// PagesDeploymentConfig is a preview/production deployment config.
type PagesDeploymentConfig struct {
	// CompatibilityDate pins which Workers runtime behaviour the functions run under,
	// as a date ("2024-01-01"). It is a pin, not a version: the runtime keeps that
	// date's semantics for code deployed against it.
	CompatibilityDate string `json:"compatibility_date,omitempty"`
	// CompatibilityFlags turn individual runtime behaviours on or off ahead of, or
	// behind, the date above ("nodejs_compat").
	CompatibilityFlags []string `json:"compatibility_flags,omitempty"`
	// EnvVars are the environment variables the functions see, KEYED BY VARIABLE NAME.
	// The key is the name; the value carries the value and whether it is a secret.
	EnvVars map[string]PagesEnvVar `json:"env_vars,omitempty"`
	// KVNamespaces binds KV namespaces into the functions, KEYED BY THE BINDING NAME
	// the code reads (`env.SESSIONS`). Same shape for the two below.
	KVNamespaces map[string]PagesKVBinding `json:"kv_namespaces,omitempty"`
	// D1Databases binds D1 databases in, keyed by binding name.
	D1Databases map[string]PagesD1Binding `json:"d1_databases,omitempty"`
	// R2Buckets binds R2 buckets in, keyed by binding name.
	R2Buckets map[string]PagesR2Binding `json:"r2_buckets,omitempty"`
}

// PagesDeploymentConfigs pairs the preview + production deployment configs.
type PagesDeploymentConfigs struct {
	// Preview is the config every branch build other than the production branch runs
	// under. It is a SEPARATE set of bindings and variables, which is what lets a
	// preview point at test data.
	Preview *PagesDeploymentConfig `json:"preview,omitempty"`
	// Production is the config the production branch builds under.
	Production *PagesDeploymentConfig `json:"production,omitempty"`
}

// PagesProjectCreate is the create-project request body (ported from
// PagesProjectCreateParams). The platform sends {name, production_branch}; the full
// shape is modeled so a richer caller is forwarded faithfully.
type PagesProjectCreate struct {
	// Name is the project name, and it is also the address: the site answers at
	// <name>.pages.dev. Cloudflare will not rename a project afterwards.
	Name string `json:"name"`
	// ProductionBranch is which git branch builds to production; every other branch
	// builds a preview. Omitted leaves Cloudflare's own default.
	ProductionBranch string `json:"production_branch,omitempty"`
	// BuildConfig says how to build the site. Omitted means no build step.
	BuildConfig *PagesBuildConfig `json:"build_config,omitempty"`
	// DeploymentConfigs carries the preview and production runtime configs — the
	// bindings and variables the built site's functions run with.
	DeploymentConfigs *PagesDeploymentConfigs `json:"deployment_configs,omitempty"`
}

// ── handlers ────────────────────────────────────────────────────────────────────

// projectRef addresses one Pages project by name, from the path.
type projectRef struct {
	// Project is the Pages project name.
	Project string `json:"project"`
}

// PagesList lists the org's Cloudflare Pages projects. Any org member may read.
func (o ops) pagesList(ctx context.Context, _ *cloud.Unit) (*cfResult, error) {
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

// PagesDeploy is the deploy request body: the branch to build. It is the whole
// shape this route reads, so it is also what the document declares for it
// (openapi.Register, cloudflare.go) — one struct, bound by the handler and
// reflected by the spec, so the published contract cannot drift from the code.
type PagesDeploy struct {
	// Branch is the branch to build. Omit it to build the project's production branch.
	Branch string `json:"branch"`
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
	var in PagesDeploy
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
