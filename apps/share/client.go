package share

import (
	"bytes"
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/env"
)

// defaultController is the in-cluster zrok controller admin+account API.
// Overridable by ZROK_CONTROLLER_URL (other envs, tests → httptest URL).
const defaultController = "http://zrok-controller.hanzo-zt.svc.cluster.local:18080"

// defaultURLTemplate is the public share URL shape the frontend serves; only
// used to render the caller a friendly hint (the real host is the fabric's).
const defaultURLTemplate = "https://share-{token}.hanzo.app"

// adminHeader is the zrok admin apiKey header (specs/zrok.yml securityDefinitions).
const adminHeader = "x-token"

// apiBase is the zrok controller API base path: /v1, overridable by ZROK_API_BASE.
func apiBase() string {
	if v := envTrim("ZROK_API_BASE"); v != "" {
		return "/" + strings.Trim(v, "/")
	}
	return "/v1"
}

// accountHeader carries an account token on account-scoped calls (overview).
const accountHeader = "x-token"

const maxBody = 4 << 20

// zrokMediaType is the ONLY content type the zrok controller's go-swagger API
// consumes/produces (specs/zrok.yml). application/json returns 415/500.
const zrokMediaType = "application/zrok.v1+json"

func envTrim(k string) string { return env.Or(k, "") }

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
// pins it via SHARE_NAMESPACE so shares land on share-<token>.hanzo.app.
func namespaceToken() string { return envTrim("SHARE_NAMESPACE") }

// controller is the slice of the zrok control API this surface needs; an
// interface so handlers unit-test against a fake with no network.
type controller interface {
	// token resolves the account's token, login-first; create mints it when
	// absent (enable) vs errNoAccount when not (listShares).
	token(ctx context.Context, h holding, create bool) (string, error)
	// rotate replaces the account's token and ends every environment enabled on
	// it, answering how many; errNoAccount when no account holds its address.
	rotate(ctx context.Context, h holding) (int, error)
	overview(ctx context.Context, accountToken string) (overviewResp, error)
	configured() bool
}

// maxAccountGen bounds how many deterministic fallback generations a holding
// walks when an earlier email row in zrok is burned (soft-deleted — zrok keeps
// deleted emails in its unique index forever — or left under a prior secret).
const maxAccountGen = 2

// httpController talks to the real zrok controller.
type httpController struct {
	base       string
	fallback   string // public controller URL when default in-cluster DNS/dial is unreachable
	adminToken string
	secret     []byte // HMAC key for the deterministic per-org password
	cc         *http.Client
}

func newController() *httpController {
	base := controllerBase()
	var fallback string
	if envTrim("ZROK_CONTROLLER_URL") == "" {
		if pub := publicController(); pub != "" && pub != base {
			fallback = pub
		}
	}
	return &httpController{
		base:       base,
		fallback:   fallback,
		adminToken: envTrim("ZROK_ADMIN_TOKEN"),
		secret:     []byte(cmp.Or(envTrim("SHARE_ACCOUNT_SECRET"), envTrim("ZROK_ADMIN_TOKEN"))),
		cc: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				DisableKeepAlives:     true,
				TLSHandshakeTimeout:   5 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
	}
}

// configured reports whether the admin credential is present. Absent it every
// handler returns an honest 503 (never a fabricated share/token).
func (c *httpController) configured() bool { return c.adminToken != "" }

// accountEmail / accountPassword derive the per-org credential deterministically,
// so provisioning is stateless and idempotent: the same org always resolves to
// the same zrok account without any local persistence.
// accountEmail derives the per-org account email. A hyphen (not "+") — a
// plus-addressed local part trips some validators, and this is a stable
// account identity, not a routing alias.
// accountEmail is a per-org, globally-FRESH, deterministic address: the HMAC
// suffix makes it unguessable and, crucially, distinct from any email a human
// or a prior scheme ever created — zrok SOFT-deletes accounts (a deleted email
// stays in the unique index and can never be recreated), so a plain
// share-<org>@ that was ever touched is burned forever. This escapes that.
func (c *httpController) accountEmail(org string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("share-email:" + org))
	return "share-" + org + "-" + hex.EncodeToString(m.Sum(nil))[:8] + "@hanzo.ai"
}

func (c *httpController) accountPassword(org string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte("share-account:" + org))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// credential is the login of one account. An org's is accountEmail and
// accountPassword of its name, as it always was. A person's is keyed on
// "person:<org>:<full subject hash>" — a key no org name is, since an org is
// never a person — and derived under labels of its own, at an address that
// begins "person-" where every org's begins "share-". So no org, whatever it is
// named, derives a person's address or password, and creating that org can
// neither log in as the person nor take their address first.
func (c *httpController) credential(h holding) (email, password string) {
	return c.credentialGen(h, 0)
}

// credentialGen derives generation gen of h's login. Generation 0 is the
// canonical address; generations 1..maxAccountGen escape an email row that zrok
// burned in its unique index (soft-deleted or left under a retired secret) while
// preserving the disjoint "person-" vs "share-" address spaces across every gen.
func (c *httpController) credentialGen(h holding, gen int) (email, password string) {
	if gen == 0 {
		if h.person == "" {
			return c.accountEmail(h.org), c.accountPassword(h.org)
		}
		key := "person:" + h.org + ":" + h.person
		return "person-" + c.mac("share-person-email:" + key)[:32] + "@hanzo.ai",
			c.mac("share-person-account:" + key)[:32]
	}
	tag := fmt.Sprintf("g%d:", gen)
	if h.person == "" {
		return fmt.Sprintf("share-%s-g%d-%s@hanzo.ai", h.org, gen, c.mac("share-email:" + tag + h.org)[:8]),
			c.mac("share-account:" + tag + h.org)[:32]
	}
	key := "person:" + h.org + ":" + h.person
	return "person-" + c.mac("share-person-email:" + tag + key)[:32] + "@hanzo.ai",
		c.mac("share-person-account:" + tag + key)[:32]
}

// mac is the hex HMAC of msg under the account secret.
func (c *httpController) mac(msg string) string {
	m := hmac.New(sha256.New, c.secret)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func (c *httpController) do(ctx context.Context, method, path string, body any, hdr map[string]string, out any) (int, error) {
	code, raw, err := c.send(ctx, method, path, body, hdr)
	if err != nil {
		return 0, err
	}
	if out != nil && len(raw) > 0 && code/100 == 2 {
		// login returns a bare JSON string; overview returns an object. Try the
		// typed decode; a mismatch is not fatal to the status the caller checks.
		_ = json.Unmarshal(raw, out)
	}
	return code, nil
}

// send is one call to the controller, answering its status and its body.
//
// Read/idempotent calls retry once on a transient socket reset or upstream
// 502/503/504, and fall back to the public controller URL when the default
// in-cluster address fails at the dial/DNS layer.
func (c *httpController) send(ctx context.Context, method, path string, body any, hdr map[string]string) (int, []byte, error) {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	code, raw, err := c.sendOnce(ctx, c.base, method, path, payload, body != nil, hdr)
	if canRetryControllerCall(method, path) && shouldRetryControllerResult(ctx, code, err) {
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
		code, raw, err = c.sendOnce(ctx, c.base, method, path, payload, body != nil, hdr)
	}
	if err != nil && ctx.Err() == nil && c.fallback != "" && c.fallback != c.base {
		code, raw, err = c.sendOnce(ctx, c.fallback, method, path, payload, body != nil, hdr)
	}
	return code, raw, err
}

func (c *httpController) sendOnce(ctx context.Context, base, method, path string, payload []byte, hasBody bool, hdr map[string]string) (int, []byte, error) {
	var rdr io.Reader
	if hasBody {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if hasBody {
		req.Header.Set("Content-Type", zrokMediaType)
	}
	req.Header.Set("Accept", zrokMediaType)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	client := c.cc
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

func canRetryControllerCall(method, path string) bool {
	return method == http.MethodGet ||
		strings.HasSuffix(path, "/login") ||
		strings.HasSuffix(path, "/grants")
}

func shouldRetryControllerResult(ctx context.Context, code int, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		return true
	}
	return code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout
}

// errNoAccount marks a login-only lookup for an org that has not provisioned.
var errNoAccount = errors.New("no share account for org")

// createStatusErr records an HTTP rejection from zrok's createAccount endpoint
// so token can tell a burned email row (duplicate unique key → 500/409/400) from
// an unreachable controller or bad ZROK_ADMIN_TOKEN (401).
type createStatusErr struct {
	code int
}

func (e createStatusErr) Error() string {
	return fmt.Sprintf("zrok create account failed (HTTP %d)", e.code)
}

func isBurnedEmailCreateErr(err error) bool {
	var ce createStatusErr
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.code {
	case http.StatusInternalServerError, http.StatusConflict, http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// token resolves the org's zrok account token, LOGIN-FIRST: an existing account
// (the common case) costs one login and never touches the admin API. Only when
// the controller refuses the login does it create the account (once) and log in
// again. create=false is the read path (listShares) — it returns errNoAccount
// instead of minting an account just to list, so a GET never provisions.
//
// errNoAccount is the controller's own answer, never a guess: a controller that
// cannot be reached, or that fails, has said nothing about the account, so its
// error is returned as it is, and enable answers it as unavailable before any
// provision is priced or reserved.
func (c *httpController) token(ctx context.Context, h holding, create bool) (string, error) {
	for gen := 0; gen <= maxAccountGen; gen++ {
		email, password := c.credentialGen(h, gen)
		tok, err := c.login(ctx, email, password)
		if err == nil {
			return tok, nil
		}
		if !errors.Is(err, errNoAccount) {
			return "", err
		}
	}
	if !create {
		return "", errNoAccount
	}
	var firstErr error
	for gen := 0; gen <= maxAccountGen; gen++ {
		email, password := c.credentialGen(h, gen)
		err := c.createAccount(ctx, email, password)
		if err == nil {
			return c.login(ctx, email, password)
		}
		if firstErr == nil {
			firstErr = err
		}
		if !isBurnedEmailCreateErr(err) {
			return "", err
		}
	}
	return "", firstErr
}

// createAccount mints the per-org account (admin). A duplicate-email rejection is
// NOT success here (login-first already ruled out a usable existing account, so a
// duplicate means a soft-deleted/mispassworded row) — it surfaces honestly so
// token can advance to the next deterministic generation.
func (c *httpController) createAccount(ctx context.Context, email, password string) error {
	code, err := c.do(ctx, http.MethodPost, apiBase()+"/account",
		map[string]string{"email": email, "password": password},
		map[string]string{adminHeader: c.adminToken}, nil)
	if err != nil {
		return err
	}
	if code == http.StatusUnauthorized {
		return fmt.Errorf("zrok admin unauthorized (ZROK_ADMIN_TOKEN)")
	}
	if code/100 != 2 {
		return createStatusErr{code: code}
	}
	return nil
}

// login exchanges the deterministic credential for the account token (a bare
// JSON string per specs/zrok.yml). This is the token the share CLI enables with.
// A refused login (401, "invalid login") is errNoAccount; anything else that is
// not a token is the controller failing. The controller refuses a login alike
// for an address no account holds, for a password that is not the account's and
// for a store it could not read, so a caller that must tell those apart asks
// (exists).
func (c *httpController) login(ctx context.Context, email, password string) (string, error) {
	var tok string
	code, err := c.do(ctx, http.MethodPost, apiBase()+"/login",
		map[string]string{"email": email, "password": password}, nil, &tok)
	if err != nil {
		return "", fmt.Errorf("zrok login: %w", err)
	}
	if code == http.StatusUnauthorized {
		return "", errNoAccount
	}
	if code/100 != 2 || tok == "" {
		return "", fmt.Errorf("zrok login failed (HTTP %d)", code)
	}
	return tok, nil
}

// rotate replaces the account's token and ends every environment it enabled.
//
// The token goes first (regenerateAccountToken, authenticated by the old one),
// so from that moment the old token opens nothing at the controller — no
// overview, no new share, no access to a private one. What the old token already
// enabled outlives it, though: an environment is a fabric identity of its own,
// and its shares and accesses keep serving. So each is ended (disable), under
// the new token, which only this call holds. A retry after a partial sweep logs
// in with the deterministic credential, rotates again and ends what is left.
//
// A refused login is "nothing to rotate" only when no account holds the address
// (exists). An account that is there and refuses the derived credential — its
// password changed at the controller — keeps every token it ever handed out
// live, and a rotation that cannot reach it is an error, never a success that
// ended nothing.
func (c *httpController) rotate(ctx context.Context, h holding) (int, error) {
	ended := 0
	rotatedAny := false
	gen0Refused := false

	for gen := 0; gen <= maxAccountGen; gen++ {
		email, password := c.credentialGen(h, gen)
		old, err := c.login(ctx, email, password)
		if errors.Is(err, errNoAccount) {
			if gen == 0 {
				gen0Refused = true
			}
			continue
		}
		if err != nil {
			return ended, err
		}
		n, err := c.rotateOne(ctx, email, old)
		ended += n
		if err != nil {
			return ended, err
		}
		rotatedAny = true
	}

	if gen0Refused {
		email0, _ := c.credentialGen(h, 0)
		there, perr := c.exists(ctx, email0)
		switch {
		case perr != nil:
			return ended, perr
		case there:
			return ended, errForeign
		}
	}
	if !rotatedAny {
		return 0, errNoAccount
	}
	return ended, nil
}

func (c *httpController) rotateOne(ctx context.Context, email, old string) (int, error) {
	var out struct {
		AccountToken string `json:"accountToken"`
	}
	code, err := c.do(ctx, http.MethodPost, apiBase()+"/regenerateAccountToken",
		map[string]string{"emailAddress": email}, map[string]string{accountHeader: old}, &out)
	if err != nil {
		return 0, err
	}
	if code/100 != 2 || out.AccountToken == "" {
		return 0, fmt.Errorf("zrok regenerate account token failed (HTTP %d)", code)
	}
	ov, err := c.overview(ctx, out.AccountToken)
	if err != nil {
		return 0, err
	}
	ended := 0
	for _, env := range ov.Environments {
		zid := env.Environment.ZID
		if zid == "" {
			continue
		}
		code, err := c.do(ctx, http.MethodPost, apiBase()+"/disable",
			map[string]string{"identity": zid}, map[string]string{accountHeader: out.AccountToken}, nil)
		if err != nil {
			return ended, err
		}
		if code/100 != 2 {
			return ended, fmt.Errorf("zrok disable failed (HTTP %d)", code)
		}
		ended++
	}
	return ended, nil
}

// errForeign is an account the controller holds under a credential this
// deployment does not derive: there, and out of a rotation's reach.
var errForeign = errors.New("the share controller holds this account under a credential this deployment does not derive")

// exists asks the controller, as its admin, whether an account holds email.
//
// grants is the one admin call keyed on an address that tells a held address
// from a free one, and the controller answers it 404 WITH NO BODY when no
// account holds the address (admin.GrantsNotFound writes a bare status). Any
// other 404 — a route the controller does not serve, which its router answers
// with a JSON body, or a proxy's page in front of it — says nothing about the
// account and is an error, as is every other status but 2xx. The controller has
// no read-only way to ask: for an address that is held, grants re-applies the
// account's interstitial setting to its public shares, and forces the
// interstitial on when it cannot read the setting. So it is asked only after the
// account's own login was refused — never of an account a rotation can reach.
func (c *httpController) exists(ctx context.Context, email string) (bool, error) {
	code, raw, err := c.send(ctx, http.MethodPost, apiBase()+"/grants",
		map[string]string{"email": email}, map[string]string{adminHeader: c.adminToken})
	switch {
	case err != nil:
		return false, err
	case code == http.StatusNotFound && len(bytes.TrimSpace(raw)) == 0:
		return false, nil
	case code/100 == 2:
		return true, nil
	}
	return false, fmt.Errorf("zrok account lookup failed (HTTP %d)", code)
}

// overview returns the account's shares/environments for the org's dashboard.
func (c *httpController) overview(ctx context.Context, accountToken string) (overviewResp, error) {
	var ov overviewResp
	code, err := c.do(ctx, http.MethodGet, apiBase()+"/overview", nil,
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
			Token                string `json:"token"`
			ShareMode            string `json:"shareMode"`
			BackendMode          string `json:"backendMode"`
			FrontendEndpoint     string `json:"frontendEndpoint"`
			BackendProxyEndpoint string `json:"backendProxyEndpoint"`
			CreatedAt            int64  `json:"createdAt"`
		} `json:"shares"`
	} `json:"environments"`
}
