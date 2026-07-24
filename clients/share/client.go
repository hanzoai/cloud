// Package share fronts the zrok controller so a Hanzo org can publish a local
// service to a public https://<token>.share.hanzo.ai URL — "ngrok on our own
// stack" — folded into the ONE cloud binary. The heavy data plane (the ziti
// fabric + the public frontend proxy) stays a separate runtime by design; this
// subsystem is the thin, org-scoped CONTROL surface: it provisions a per-org
// zrok account from the caller's validated IAM identity and hands the CLI the
// credential it needs, so `hanzo share 3000` needs zero manual setup.
//
// STATELESS provisioning. There is no local store: the per-org account is keyed
// deterministically off the org slug (email share+<org>@hanzo.ai, password =
// HMAC(SHARE_ACCOUNT_SECRET, org)), so ensure-account + login reconstruct the
// same credential every time from the zrok controller — the controller IS the
// store. Idempotent: creating an existing account is ignored; login always
// returns the current account token.
//
// FAIL-CLOSED. Absent the admin credential (ZROK_ADMIN_TOKEN, KMS-injected)
// every op returns an honest 503; it never fabricates a share or token.
package share

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultController is the in-cluster zrok controller admin+account API.
// Overridable by ZROK_CONTROLLER_URL (other envs, tests → httptest URL).
const defaultController = "http://zrok-controller.hanzo-zt.svc.cluster.local:18080"

// defaultURLTemplate is the public share URL shape the frontend serves; only
// used to render the caller a friendly hint (the real host is the fabric's).
const defaultURLTemplate = "https://{token}.share.hanzo.ai"

// adminHeader is the zrok admin apiKey header (specs/zrok.yml securityDefinitions).
const adminHeader = "x-token"

// accountHeader carries an account token on account-scoped calls (overview).
const accountHeader = "x-token"

const maxBody = 4 << 20

// zrokMediaType is the ONLY content type the zrok controller's go-swagger API
// consumes/produces (specs/zrok.yml). application/json returns 415/500.
const zrokMediaType = "application/zrok.v1+json"

func envTrim(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func controllerBase() string {
	if v := envTrim("ZROK_CONTROLLER_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultController
}

// publicController is the endpoint the `hanzo share` CLI (running on a user's
// laptop) points zrok at — a public host, NOT the in-cluster admin URL the
// surface itself uses. The client enables against it with the provisioned
// account token; the ziti tunnel then dials the public edge router.
func publicController() string {
	if v := envTrim("SHARE_PUBLIC_CONTROLLER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://zrok-api.hanzo.ai"
}

func urlTemplate() string {
	if v := envTrim("SHARE_URL_TEMPLATE"); v != "" {
		return v
	}
	return defaultURLTemplate
}

// namespaceToken is the zrok namespace the public frontend is mapped into; the
// share CLI selects it. Empty is allowed (default namespace) but production
// pins it via SHARE_NAMESPACE so shares land on *.share.hanzo.ai.
func namespaceToken() string { return envTrim("SHARE_NAMESPACE") }

// controller is the slice of the zrok control API this surface needs; an
// interface so handlers unit-test against a fake with no network.
type controller interface {
	ensureAccount(ctx context.Context, email, password string) error
	login(ctx context.Context, email, password string) (string, error)
	overview(ctx context.Context, accountToken string) (overviewResp, error)
	configured() bool
}

// httpController talks to the real zrok controller.
type httpController struct {
	base       string
	adminToken string
	secret     []byte // HMAC key for the deterministic per-org password
	cc         *http.Client
}

func newController() *httpController {
	return &httpController{
		base:       controllerBase(),
		adminToken: envTrim("ZROK_ADMIN_TOKEN"),
		secret:     []byte(firstNonEmpty(envTrim("SHARE_ACCOUNT_SECRET"), envTrim("ZROK_ADMIN_TOKEN"))),
		cc:         &http.Client{Timeout: 20 * time.Second},
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// configured reports whether the admin credential is present. Absent it every
// handler returns an honest 503 (never a fabricated share/token).
func (c *httpController) configured() bool { return c.adminToken != "" }

// accountEmail / accountPassword derive the per-org credential deterministically,
// so provisioning is stateless and idempotent: the same org always resolves to
// the same zrok account without any local persistence.
func accountEmail(org string) string { return "share+" + org + "@hanzo.ai" }

func (c *httpController) accountPassword(org string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("share-account:" + org))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (c *httpController) do(ctx context.Context, method, path string, body any, hdr map[string]string, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", zrokMediaType)
	}
	req.Header.Set("Accept", zrokMediaType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.cc.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if out != nil && len(raw) > 0 && resp.StatusCode/100 == 2 {
		// login returns a bare JSON string; overview returns an object. Try the
		// typed decode; a mismatch is not fatal to the status the caller checks.
		_ = json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// ensureAccount creates the per-org account; an already-existing account (the
// controller answers 4xx/5xx on a duplicate email) is treated as success so
// provisioning is idempotent — login below is the source of the live token.
func (c *httpController) ensureAccount(ctx context.Context, email, password string) error {
	code, err := c.do(ctx, http.MethodPost, "/api/v2/account",
		map[string]string{"email": email, "password": password},
		map[string]string{adminHeader: c.adminToken}, nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return fmt.Errorf("zrok admin unauthorized (ZROK_ADMIN_TOKEN)")
	}
	// 201 created OR any duplicate-email rejection → the account now exists.
	return nil
}

// login exchanges the deterministic credential for the account token (a bare
// JSON string per specs/zrok.yml). This is the token the share CLI enables with.
func (c *httpController) login(ctx context.Context, email, password string) (string, error) {
	var tok string
	code, err := c.do(ctx, http.MethodPost, "/api/v2/login",
		map[string]string{"email": email, "password": password}, nil, &tok)
	if err != nil {
		return "", err
	}
	if code/100 != 2 || tok == "" {
		return "", fmt.Errorf("zrok login failed (HTTP %d)", code)
	}
	return tok, nil
}

// overview returns the account's shares/environments for the org's dashboard.
func (c *httpController) overview(ctx context.Context, accountToken string) (overviewResp, error) {
	var ov overviewResp
	code, err := c.do(ctx, http.MethodGet, "/api/v2/overview", nil,
		map[string]string{accountHeader: accountToken}, &ov)
	if err != nil {
		return ov, err
	}
	if code/100 != 2 {
		return ov, fmt.Errorf("zrok overview failed (HTTP %d)", code)
	}
	return ov, nil
}

// overviewResp mirrors the parts of specs/zrok.yml `overview` this surface reads.
type overviewResp struct {
	Environments []struct {
		Environment struct {
			Description string `json:"description"`
			Host        string `json:"host"`
			ZID         string `json:"zId"`
		} `json:"environment"`
		Shares []struct {
			Token       string `json:"token"`
			ShareMode   string `json:"shareMode"`
			BackendMode string `json:"backendMode"`
			FrontendEndpoint string `json:"frontendEndpoint"`
			BackendProxyEndpoint string `json:"backendProxyEndpoint"`
			CreatedAt   int64  `json:"createdAt"`
		} `json:"shares"`
	} `json:"environments"`
}
