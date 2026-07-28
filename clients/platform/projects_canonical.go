// projects_canonical.go — the ProjectStore over the CANONICAL IAM.
//
// Split-horizon deployments run IAM as its own service (IAM_URL, e.g.
// http://iam.hanzo.svc) — the store that mints every token and serves
// /v1/iam/projects at the edge. Platform used to read projects from cloud's
// EMBEDDED copy of the iam store instead: a second database for the same noun,
// so a project created at /v1/iam was invisible to the PaaS and vice versa.
//
// This client reads the canonical store over HTTP, authenticated AS THE ORG:
// each read presents client_secret_basic for that org's own
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
		creds: map[string]orgCred{},
	}
}

type orgCred struct {
	id, secret string
	exp        time.Time
}

type canonicalProjects struct {
	base  string
	ident tenantKMSIdentity
	hc    *http.Client

	mu    sync.Mutex
	creds map[string]orgCred
}

// cred returns the org's machine credential, minting the identity itself on
// first use (EnsureOrgIdentity provisions when configured) and caching briefly
// so a burst of reads is one KMS read, not many.
func (c *canonicalProjects) cred(ctx context.Context, org string) (string, string, error) {
	c.mu.Lock()
	if cr, ok := c.creds[org]; ok && time.Now().Before(cr.exp) {
		c.mu.Unlock()
		return cr.id, cr.secret, nil
	}
	c.mu.Unlock()
	if c.ident == nil {
		return "", "", fmt.Errorf("projects: no identity provider for org %q (KMS plane absent)", org)
	}
	id, secret, err := c.ident.EnsureOrgIdentity(ctx, org)
	if err != nil {
		return "", "", fmt.Errorf("projects: org identity: %w", err)
	}
	c.mu.Lock()
	c.creds[org] = orgCred{id: id, secret: secret, exp: time.Now().Add(time.Minute)}
	c.mu.Unlock()
	return id, secret, nil
}

func (c *canonicalProjects) do(ctx context.Context, org, method, path string, body any, out any) (int, error) {
	id, secret, err := c.cred(ctx, org)
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
	// client_secret_basic, not a minted bearer: IAM's app() path resolves this
	// to Principal{App: name, Org: served-org} — exactly the shape the
	// projects-read grant admits. A client_credentials BEARER resolves its org
	// from the SUBJECT's owner half (the app row's owner, "admin"), which the
	// grant rightly refuses — measured live before this client switched.
	req.SetBasicAuth(id, secret)
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
