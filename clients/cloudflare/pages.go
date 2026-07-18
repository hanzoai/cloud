package cloudflare

// pages.go — Cloudflare Pages, WIRED. Each handler resolves the caller-org token
// (authClient) and the account id (resolveAccount), then proxies to the CF Pages API
// under /accounts/{account_id}/pages/*. Request bodies are decoded into the typed
// params ported from the platform's cloudflare.ts (PagesProjectCreateParams et al.),
// so only modeled fields reach Cloudflare; responses relay verbatim (no field loss).

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
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

func pagesList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/pages/projects", nil)
}

func pagesGet(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	proj, err := pathSeg(c, "project", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/pages/projects/"+proj, nil)
}

func pagesCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	var in PagesProjectCreate
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	if !nameRE.MatchString(strings.TrimSpace(in.Name)) {
		return zip.ErrBadRequest("project name is invalid")
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/pages/projects", in)
}

func pagesDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	proj, err := pathSeg(c, "project", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/pages/projects/"+proj, nil)
}

func pagesDeploy(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
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

func pagesDomainAdd(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	proj, err := pathSeg(c, "project", nameRE)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return zip.ErrBadRequest("domain name is required")
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/pages/projects/"+proj+"/domains", map[string]string{"name": name})
}

func pagesDomainDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, _, err := authClient(s, c)
	if err != nil {
		return err
	}
	acct, err := cl.resolveAccount(c.Context(), c)
	if err != nil {
		return err
	}
	proj, err := pathSeg(c, "project", nameRE)
	if err != nil {
		return err
	}
	dom, err := pathSeg(c, "domain", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/pages/projects/"+proj+"/domains/"+dom, nil)
}
