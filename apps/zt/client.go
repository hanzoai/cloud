// client.go is the ONE HTTP path from this subsystem to Hanzo Zero Trust — the
// OpenZiti-based fabric controller (hanzoai/zt) at zt-controller.hanzo.svc. Every
// handler in zt.go routes through this client, so the wire contract (base URL, the
// Ziti Edge Management API base path, session auth, the {data,meta} / {error}
// envelope, pagination, error mapping) lives once here and can never drift between
// hand-rolled fetches.
//
// API. The controller exposes the OpenZiti Edge MANAGEMENT REST API under
// /edge/management/v1 (controller/webapis/versions.go: ManagementRestApiBaseUrlV1).
// It is the admin surface — listing services and edge-routers — as distinct from
// the per-identity Client API. We speak it as a thin net/http client rather than
// pulling the heavy generated openapi client, exactly as clients/visor fronts Visor.
//
// AUTH (one rule). The controller authenticates a management caller with the
// password method: POST /authenticate?method=password {username,password} returns a
// session token, which every subsequent request carries in the `zt-session` header
// (controller/api/cors.go: ZitiSession = "zt-session"). The username/password are
// the KMS-injected service credential ZT_CLIENT_ID / ZT_CLIENT_SECRET — never
// hard-coded, never logged. The token is cached until its expiry and transparently
// re-minted (including a single retry when the controller rejects a stale token
// with 401), so a handler never sees auth state.
//
// ENVELOPE. Ziti wraps a success as {data,meta} (data is the resource or list,
// meta.pagination drives paging) and a failure as {error:{code,message}} at a
// non-2xx status. call() maps a non-2xx to that error honestly (never masking an
// upstream failure as success) and returns the raw 2xx body for the typed decoders.
//
// TLS. The controller fronts its own CA. ZT_CA_PEM (a PEM bundle, KMS-injected)
// pins the controller's root when set; otherwise the system pool is used (works
// when the controller is fronted by a publicly-trusted cert via hanzoai/ingress).
// ZT_INSECURE_SKIP_VERIFY is an explicit, documented dev-only escape hatch; the
// secure default is full verification.
package zt

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

// Edge Management API base path (controller/webapis/versions.go). All resource
// paths (/services, /edge-routers, /authenticate) hang off this root.
const mgmtBase = "/edge/management/v1"

// sessionHeader is the Ziti management session header (controller/api/cors.go).
const sessionHeader = "zt-session"

// defaultBase is the in-cluster ZT controller. Overridable by ZT_CONTROLLER_URL
// for other environments and for tests (an httptest.Server URL).
const defaultBase = "https://zt-controller.hanzo.svc:1280"

// maxBody bounds an upstream response read — ZT inventories are small object lists,
// never blobs; the cap stops a pathological upstream from ballooning memory.
const maxBody = 8 << 20

// pagination: page the controller's list endpoints `perPage` at a time, capped at
// `maxPages` so a pathological controller can neither exhaust memory nor spin
// forever. 500*40 = 20k objects of one kind — far above any real org's footprint.
const (
	perPage  = 500
	maxPages = 40
)

// tokenSkew renews a session slightly before its stated expiry so an in-flight
// request never races the controller's clock.
const tokenSkew = 30 * time.Second

func envTrim(k string) string { return strings.TrimSpace(os.Getenv(k)) }

func controllerBase() string {
	if v := envTrim("ZT_CONTROLLER_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultBase
}

func serviceClientID() string     { return envTrim("ZT_CLIENT_ID") }
func serviceClientSecret() string { return envTrim("ZT_CLIENT_SECRET") }

// client is the session-scoped ZT management HTTP client. target has no trailing
// slash and already includes the scheme+host (NOT the mgmt base path).
type client struct {
	target string
	user   string
	secret string
	cc     *http.Client

	mu       sync.Mutex // guards token/tokenExp and serializes (re)auth
	token    string
	tokenExp time.Time
}

// newClient reads the controller URL, service credential and TLS trust from env
// (all KMS-injected in production; never hard-coded). It always returns a usable
// client object — configured() reports whether the credential is present, so the
// subsystem can mount its routes and fail closed with an honest 503 rather than
// 404-ing when ZT is not wired.
func newClient() *client {
	return &client{
		target: controllerBase(),
		user:   serviceClientID(),
		secret: serviceClientSecret(),
		cc:     &http.Client{Timeout: 30 * time.Second, Transport: newTransport()},
	}
}

// configured reports whether a ZT service credential is present. Absent it, every
// handler returns an honest 503 (never a fabricated network/service/node).
func (cl *client) configured() bool { return cl.user != "" && cl.secret != "" }

// newTransport builds the HTTP transport with the controller's TLS trust: a pinned
// CA (ZT_CA_PEM) when provided, else the system pool, with an explicit dev-only
// insecure escape hatch. The secure default is full verification.
func newTransport() *http.Transport {
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        10,
		IdleConnTimeout:     30 * time.Second,
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if pem := envTrim("ZT_CA_PEM"); pem != "" {
		pool := x509.NewCertPool()
		if pool.AppendCertsFromPEM([]byte(pem)) {
			tlsCfg.RootCAs = pool
		}
	}
	switch strings.ToLower(envTrim("ZT_INSECURE_SKIP_VERIFY")) {
	case "true", "1", "yes":
		tlsCfg.InsecureSkipVerify = true // dev-only; documented in the package header.
	}
	tr.TLSClientConfig = tlsCfg
	return tr
}

// ---- envelopes ----

// ztPage is Ziti's list envelope: a typed data array plus pagination meta that
// drives paging. Parameterized so one pager serves every resource kind.
type ztPage[T any] struct {
	Data []T `json:"data"`
	Meta struct {
		Pagination struct {
			Limit      int `json:"limit"`
			Offset     int `json:"offset"`
			TotalCount int `json:"totalCount"`
		} `json:"pagination"`
	} `json:"meta"`
}

// ztAuth is the password-auth response; the session token lives at data.token.
type ztAuth struct {
	Data struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"data"`
}

// ztError is Ziti's failure envelope (non-2xx). The message is surfaced verbatim.
type ztError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ---- auth ----

// ensureToken returns a valid session token, minting one under the lock when the
// cache is empty or expiring. force discards any cached token first (used on the
// 401 retry path when the controller has invalidated a still-"fresh" token).
func (cl *client) ensureToken(c *zip.Ctx, force bool) (string, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if force {
		cl.token = ""
	}
	if cl.token != "" && time.Now().Before(cl.tokenExp.Add(-tokenSkew)) {
		return cl.token, nil
	}
	return cl.authenticate(c)
}

// authenticate performs the password-method login. Caller holds cl.mu. The
// credential is sent in the JSON body per the Ziti Authenticate model and is never
// logged. expiresAt pins the cache lifetime; absent/unparseable it defaults to a
// conservative 20 minutes (the Ziti default session TTL).
func (cl *client) authenticate(c *zip.Ctx) (string, error) {
	body, _ := json.Marshal(map[string]string{"username": cl.user, "password": cl.secret})
	u := cl.target + mgmtBase + "/authenticate?method=password"
	req, err := http.NewRequestWithContext(c.Context(), http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "zt: build auth request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := cl.cc.Do(req)
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "zt: controller unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", zip.Errorf(http.StatusBadGateway, "zt: authenticate %d: %s", resp.StatusCode, ztErrMsg(raw))
	}

	var auth ztAuth
	if err := json.Unmarshal(raw, &auth); err != nil || auth.Data.Token == "" {
		return "", zip.Errorf(http.StatusBadGateway, "zt: authenticate returned no session token")
	}
	cl.token = auth.Data.Token
	cl.tokenExp = time.Now().Add(20 * time.Minute)
	if t, perr := time.Parse(time.RFC3339, auth.Data.ExpiresAt); perr == nil && t.After(time.Now()) {
		cl.tokenExp = t
	}
	return cl.token, nil
}

// ---- request ----

// get issues one authenticated GET to a management path (relative to mgmtBase) and
// returns the raw 2xx body. It re-authenticates and retries ONCE when the
// controller rejects the session with 401 (token expired mid-flight). query is
// appended as-is. Error mapping is honest and customer-appropriate: an unreachable
// controller → 502, a non-2xx → that status with Ziti's message.
func (cl *client) get(c *zip.Ctx, path, query string) ([]byte, error) {
	raw, status, err := cl.do(c, path, query, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if raw, status, err = cl.do(c, path, query, true); err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, zip.Errorf(status, "zt: upstream %d: %s", status, ztErrMsg(raw))
	}
	return raw, nil
}

// do performs a single GET (minting/forcing a token first) and returns the raw
// body + status. It does NOT map non-2xx to an error (get() owns the 401-retry
// decision); only transport failures surface as a 502 error here.
func (cl *client) do(c *zip.Ctx, path, query string, forceAuth bool) ([]byte, int, error) {
	token, err := cl.ensureToken(c, forceAuth)
	if err != nil {
		return nil, 0, err
	}
	u := cl.target + mgmtBase + path
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(c.Context(), http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, zip.Errorf(http.StatusInternalServerError, "zt: build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set(sessionHeader, token)

	resp, err := cl.cc.Do(req)
	if err != nil {
		return nil, 0, zip.Errorf(http.StatusBadGateway, "zt: controller unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	return raw, resp.StatusCode, nil
}

// listAll pages a management list endpoint fully and returns every decoded item.
// It walks limit/offset until a short page (or the reported totalCount) is reached,
// capped at maxPages. Free function (not a method) because Go methods cannot carry
// their own type parameter.
func listAll[T any](cl *client, c *zip.Ctx, path string) ([]T, error) {
	var out []T
	for page := 0; page < maxPages; page++ {
		offset := page * perPage
		q := url.Values{}
		q.Set("limit", strconv.Itoa(perPage))
		q.Set("offset", strconv.Itoa(offset))
		raw, err := cl.get(c, path, q.Encode())
		if err != nil {
			return nil, err
		}
		var pg ztPage[T]
		if err := json.Unmarshal(raw, &pg); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "zt: decode %s: %v", path, err)
		}
		out = append(out, pg.Data...)
		total := pg.Meta.Pagination.TotalCount
		if len(pg.Data) < perPage || (total > 0 && len(out) >= total) {
			break
		}
	}
	return out, nil
}

// ztErrMsg extracts Ziti's error message for an honest upstream error, falling
// back to a bounded snippet of the raw body.
func ztErrMsg(raw []byte) string {
	var e ztError
	if json.Unmarshal(raw, &e) == nil && strings.TrimSpace(e.Error.Message) != "" {
		return e.Error.Message
	}
	s := strings.TrimSpace(string(raw))
	if len(s) > 200 {
		return s[:200]
	}
	return s
}
