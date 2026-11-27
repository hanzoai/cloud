// client.go is the ONE HTTP path from this subsystem to Hanzo Zero Trust — the
// ZT fabric controller (hanzozt/zt) at zt-api.hanzo.ai. Every
// handler in zt.go routes through this client, so the wire contract (base URL, the
// ZT management API base path, session auth, the {data,meta} / {error}
// envelope, pagination, error mapping) lives once here and can never drift between
// hand-rolled fetches.
//
// API. The controller exposes the controller management REST API under
// /edge/management/v1 (controller/webapis/versions.go: ManagementRestApiBaseUrlV1).
// It is the admin surface — listing services and edge-routers, ensuring identities
// and publishing services — as distinct from the per-identity Client API. We speak
// it as a thin net/http client rather than pulling the heavy generated openapi
// client, exactly as clients/visor fronts Visor.
//
// AUTH (one rule). The controller trusts Hanzo IAM as its only signer, and the
// cloud logs in as ITSELF: POST /authenticate?method=ext-jwt with the cloud's own
// IAM access token (iamurl.Machine) as `Authorization: Bearer`. The controller
// matches the token's `sub` to an admin identity's externalId and returns a
// session token, which every subsequent request carries in the `zt-session`
// header (controller/api/cors.go: "zt-session"). There is no ZT credential of
// its own. The session is cached until its expiry and transparently re-minted
// (including a single retry when the controller rejects a stale session with
// 401), so a handler never sees auth state.
//
// ENVELOPE. The controller wraps a success as {data,meta} (data is the resource or list,
// meta.pagination drives paging) and a failure as {error:{code,message}} at a
// non-2xx status. call() maps a non-2xx to that error honestly (never masking an
// upstream failure as success) and returns the raw 2xx body for the typed decoders.
//
// TLS. The management API is served behind Cloudflare under a publicly trusted
// certificate, so the system pool verifies it and nothing is pinned.

package network

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/text"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"golang.org/x/oauth2"
)

// Edge Management API base path (controller/webapis/versions.go). All resource
// paths (/services, /edge-routers, /authenticate) hang off this root.
const mgmtBase = "/edge/management/v1"

// sessionHeader is the ZT management session header (controller/api/cors.go).
const sessionHeader = "zt-session"

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

// client is the session-scoped ZT management HTTP client. target has no trailing
// slash and already includes the scheme+host (NOT the mgmt base path).
type client struct {
	target string
	iam    oauth2.TokenSource // the cloud's own IAM identity; nil when it holds none
	cc     *http.Client
	log    luxlog.Logger

	// owned is set while the cloud's own identity is being given, or has been
	// given, the role the dial policies admit it by (publish.go own).
	owned atomic.Bool

	mu       sync.Mutex // guards token/tokenExp and serializes (re)auth
	token    string
	tokenExp time.Time
}

// newClient reads the controller URL from env and takes the cloud's own IAM
// identity. It always returns a usable client object — configured() reports
// whether that identity is present, so the subsystem can mount its routes and
// fail closed with an honest 503 rather than 404-ing when ZT is not wired.
func newClient(log luxlog.Logger) *client {
	return &client{
		target: cloud.ZTBase(),
		iam:    iam.Machine(),
		cc:     &http.Client{Timeout: 30 * time.Second},
		log:    log,
	}
}

// configured reports whether the cloud holds an IAM identity to log in with.
// Absent it, every handler returns an honest 503 (never a fabricated
// network/service/node).
func (cl *client) configured() bool { return cl.iam != nil }

// ---- envelopes ----

// ztPage is the controller's list envelope: a typed data array plus pagination meta that
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

// ztAuth is the authenticate response; the session token lives at data.token.
type ztAuth struct {
	Data struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	} `json:"data"`
}

// ztError is the controller's failure envelope (non-2xx). The message is surfaced verbatim.
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
func (cl *client) ensureToken(ctx context.Context, force bool) (string, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if force {
		cl.token = ""
	}
	if cl.token != "" && time.Now().Before(cl.tokenExp.Add(-tokenSkew)) {
		return cl.token, nil
	}
	return cl.authenticate(ctx)
}

// authenticate performs the ext-jwt login with the cloud's own IAM token. Caller
// holds cl.mu. The bearer rides only the Authorization header and is never
// logged. expiresAt pins the cache lifetime; absent/unparseable it defaults to a
// conservative 20 minutes (the controller default session TTL).
func (cl *client) authenticate(ctx context.Context) (string, error) {
	bearer, err := cl.iam.Token()
	if err != nil {
		return "", zip.Errorf(http.StatusBadGateway, "zt: mint IAM token: %v", err)
	}
	u := cl.target + mgmtBase + "/authenticate?method=ext-jwt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader("{}"))
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "zt: build auth request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer.AccessToken)

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

// call issues one authenticated request to a management path (relative to
// mgmtBase) and returns the raw 2xx body. body, when non-nil, is marshalled once
// and sent as JSON — once, so the 401 retry re-sends the same bytes. It
// re-authenticates and retries ONCE when the controller rejects the session with
// 401 (token expired mid-flight). query is appended as-is. Error mapping is
// honest and customer-appropriate: an unreachable controller → 502, a non-2xx →
// that status with the controller's message.
func (cl *client) call(ctx context.Context, method, path, query string, body any) ([]byte, error) {
	cl.own(ctx)
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "zt: encode %s body: %v", path, err)
		}
	}
	raw, status, err := cl.do(ctx, method, path, query, payload, false)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized {
		if raw, status, err = cl.do(ctx, method, path, query, payload, true); err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, zip.Errorf(status, "zt: upstream %d: %s", status, ztErrMsg(raw))
	}
	return raw, nil
}

// get is call for the reads, which every list in this subsystem goes through.
func (cl *client) get(ctx context.Context, path, query string) ([]byte, error) {
	return cl.call(ctx, http.MethodGet, path, query, nil)
}

// do performs a single request (minting/forcing a token first) and returns the
// raw body + status. It does NOT map non-2xx to an error (call() owns the
// 401-retry decision); only transport failures surface as a 502 error here.
func (cl *client) do(ctx context.Context, method, path, query string, payload []byte, forceAuth bool) ([]byte, int, error) {
	token, err := cl.ensureToken(ctx, forceAuth)
	if err != nil {
		return nil, 0, err
	}
	u := cl.target + mgmtBase + path
	if query != "" {
		u += "?" + query
	}
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, 0, zip.Errorf(http.StatusInternalServerError, "zt: build request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
func listAll[T any](cl *client, ctx context.Context, path string) ([]T, error) {
	var out []T
	for page := range maxPages {
		offset := page * perPage
		q := url.Values{}
		q.Set("limit", strconv.Itoa(perPage))
		q.Set("offset", strconv.Itoa(offset))
		raw, err := cl.get(ctx, path, q.Encode())
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

// ztErrMsg extracts the controller's error message for an honest upstream error, falling
// back to a bounded snippet of the raw body.
func ztErrMsg(raw []byte) string {
	var e ztError
	if json.Unmarshal(raw, &e) == nil && strings.TrimSpace(e.Error.Message) != "" {
		return e.Error.Message
	}
	return text.Cut(strings.TrimSpace(string(raw)), 200)
}
