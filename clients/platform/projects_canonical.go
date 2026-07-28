// projects_canonical.go — the ProjectStore over the CANONICAL IAM.
//
// Split-horizon deployments run IAM as its own service (IAM_URL, e.g.
// http://iam.hanzo.svc) — the store that mints every token and serves
// /v1/iam/projects at the edge. Platform used to read projects from cloud's
// EMBEDDED copy of the iam store instead: a second database for the same noun,
// so a project created at /v1/iam was invisible to the PaaS and vice versa.
//
// This client reads the canonical store over HTTP, authenticated AS THE ORG:
// each read runs on a client_credentials token for that org's own
// "<org>-platform-kms" identity — the same credential the KMS sync uses, minted
// on first need by kmsOrgIdentity — and IAM's authorize admits exactly that
// identity, read-only, own-org-only (iam internal/authz). One identity per
// tenant, one grant, both stated once.
//
// When IAM_URL is absent the process IS the IAM (single-binary: the embedded
// subsystem serves /v1/iam), so the in-process store remains the canonical one
// and iamProjects is used unchanged. The selector is newProjectStore.
package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	model "github.com/hanzoai/iam/pkg/model"
)

// newProjectStore selects the canonical project source: the external IAM when
// the deployment names one (IAM_URL), the in-process store when this binary IS
// the IAM. ident may be nil (no KMS plane) — the canonical client then fails
// closed per read, which iamStore's 503 convention already covers.
func newProjectStore(iamIssuer string, ident tenantKMSIdentity) ProjectStore {
	base := cloud.IAMBaseURL(iamIssuer)
	if v := getenv("IAM_URL", ""); strings.TrimSpace(v) == "" {
		// No external IAM named: the embedded subsystem is the canonical store.
		return iamProjects{}
	}
	return &canonicalProjects{
		base:  base,
		ident: ident,
		hc:    &http.Client{Timeout: 10 * time.Second},
		toks:  map[string]orgToken{},
	}
}

type orgToken struct {
	bearer string
	exp    time.Time
}

type canonicalProjects struct {
	base  string
	ident tenantKMSIdentity
	hc    *http.Client

	mu   sync.Mutex
	toks map[string]orgToken
}

// token returns a live bearer for the org's own machine identity, minting the
// identity itself on first use (EnsureOrgIdentity provisions when configured).
func (c *canonicalProjects) token(ctx context.Context, org string) (string, error) {
	c.mu.Lock()
	if t, ok := c.toks[org]; ok && time.Now().Before(t.exp) {
		c.mu.Unlock()
		return t.bearer, nil
	}
	c.mu.Unlock()

	if c.ident == nil {
		return "", fmt.Errorf("projects: no identity provider for org %q (KMS plane absent)", org)
	}
	id, secret, err := c.ident.EnsureOrgIdentity(ctx, org)
	if err != nil {
		return "", fmt.Errorf("projects: org identity: %w", err)
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {id},
		"client_secret": {secret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("projects: mint token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("projects: mint token: status %d", resp.StatusCode)
	}
	exp := time.Now().Add(time.Duration(max1(out.ExpiresIn-60)) * time.Second)
	c.mu.Lock()
	c.toks[org] = orgToken{bearer: out.AccessToken, exp: exp}
	c.mu.Unlock()
	return out.AccessToken, nil
}

func (c *canonicalProjects) do(ctx context.Context, org, method, path string, body any, out any) (int, error) {
	tok, err := c.token(ctx, org)
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("projects: decode: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (c *canonicalProjects) List(ctx context.Context, org string) ([]*model.Project, error) {
	var out struct {
		Projects []*model.Project `json:"projects"`
	}
	code, err := c.do(ctx, org, http.MethodGet, "/v1/iam/projects?owner="+url.QueryEscape(org), nil, &out)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("projects: list %s: status %d", org, code)
	}
	return out.Projects, nil
}

// Get returns nil (no error) when the project does not exist — IAM's convention,
// preserved so requireProject's 404 mapping is unchanged.
func (c *canonicalProjects) Get(ctx context.Context, org, name string) (*model.Project, error) {
	var p model.Project
	code, err := c.do(ctx, org, http.MethodPost, "/v1/iam/projects/get",
		map[string]string{"owner": org, "name": name}, &p)
	if err != nil {
		return nil, err
	}
	switch code {
	case http.StatusOK:
		return &p, nil
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("projects: get %s/%s: status %d", org, name, code)
	}
}

func (c *canonicalProjects) Exists(ctx context.Context, org, name string) (bool, error) {
	p, err := c.Get(ctx, org, name)
	if err != nil {
		return false, err
	}
	return p != nil, nil
}
