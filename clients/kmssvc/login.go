package kmssvc

// login.go — the /v1/kms/auth/login broker.
//
// The kms-operator's universalAuth transport (github.com/luxfi/kms-operator
// packages/kmsapi) authenticates by POSTing {clientId, clientSecret} to
// {hostAPI}/v1/kms/auth/login and expecting {accessToken, expiresIn, tokenType}.
// cloud hosts the operator's KMS at cloud.hanzo.svc, so cloud must serve that
// endpoint — this is it.
//
// cloud is NOT a token issuer. It cannot mint a bearer its own SanitizeIdentity
// would validate (that requires IAM's signing key). So login BROKERS: it exchanges
// the caller's credential at IAM's client_credentials endpoint and returns IAM's
// own JWT verbatim. The returned token's `owner` claim scopes it to exactly one
// org, and cloud's org-scope guard (kmssvc.guard) re-checks that owner against the
// URL :org on every secret read. This endpoint therefore grants NOTHING beyond
// IAM's already-public client_credentials grant — it is a thin, stateless relay.
//
// This mirrors the canonical kmsd (~/work/hanzo/kms) login: one IAM identity per
// scope, short-lived JWT, owner-checked at read time. The per-tenant credential is
// a machine identity whose token carries owner=<tenant-org>, so one tenant can
// authenticate as itself and no other.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

const (
	// maxCredLen bounds each credential field so a hostile body cannot synthesise a
	// multi-megabyte upstream request. Mirrors kmsapi's MaxSlugLen-class bounds.
	maxCredLen = 512
	// loginHTTPTimeout caps the upstream IAM exchange. The operator caches the
	// token ~1h, so this path is cold and latency-tolerant.
	loginHTTPTimeout  = 15 * time.Second
	maxLoginRespBytes = 1 << 20  // 1 MiB cap on the IAM token response
	maxLoginBodyBytes = 64 << 10 // 64 KiB cap on the inbound login body

	// loginMaxConnsPerHost hard-bounds the concurrent outbound connections the public
	// /v1/kms/auth/login broker may open to the IAM token host. The broker is a
	// public, unauthenticated fan-out to IAM, so without a bound a flood of login
	// attempts could pin an unbounded number of upstream connections open against IAM
	// (a DoS amplifier / outbound-conn pinning). HTTP/2 is disabled on this client
	// (TLSNextProto below) so this is a TRUE concurrency ceiling — one in-flight
	// exchange per connection — not "N conns each multiplexing unlimited streams".
	loginMaxConnsPerHost = 32

	// loginRateLimit / loginRateWindow bound login attempts PER SOURCE IP — the
	// credential-stuffing-through-cloud dimension. The kms-operator authenticates
	// per tenant and caches each token ~1h, so its steady-state login rate from a
	// single (in-cluster) source IP is a few per minute — far under this ceiling —
	// while a brute-force source is cut to loginRateLimit/window. Per-pod, best
	// effort; loginMaxConnsPerHost is the hard backstop on the actual IAM fan-out.
	loginRateLimit  = 60
	loginRateWindow = time.Minute
)

// loginHTTPClient is the shared client for the IAM token exchange. Its transport
// caps concurrent connections to the IAM host (loginMaxConnsPerHost) and disables
// HTTP/2 so that cap is a real concurrency bound on the public login broker's
// outbound fan-out. Timeouts are set so a slow or absent IAM cannot wedge a
// connection open indefinitely.
var loginHTTPClient = &http.Client{
	Timeout: loginHTTPTimeout,
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxConnsPerHost:       loginMaxConnsPerHost,
		MaxIdleConns:          loginMaxConnsPerHost,
		MaxIdleConnsPerHost:   loginMaxConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Disable HTTP/2 so MaxConnsPerHost bounds CONCURRENCY: h2 would multiplex
		// many in-flight exchanges over a single connection, defeating the ceiling.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	},
}

// loginRequest is the operator's universalAuth credential (kmsapi.Client.Login
// posts exactly this shape).
type loginRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// loginResponse mirrors kmsapi.LoginResponse — the shape the operator decodes.
type loginResponse struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int64  `json:"expiresIn"`
	TokenType   string `json:"tokenType"`
}

// login exchanges the caller's clientId/clientSecret for an owner-scoped IAM JWT.
// Fail-closed: no IAM issuer configured → 503; malformed input → 400; a bad
// credential → 401; an IAM outage → 502. It NEVER logs or echoes the secret, and
// its error bodies carry no IAM internals (no credential-validity oracle beyond
// what IAM's own endpoint already exposes).
func (s *svc) login(ctx *zip.Ctx) error {
	if s.iamTokenURL == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "kms login unavailable: no IAM issuer configured")
	}
	body := ctx.Body()
	if len(body) > maxLoginBodyBytes {
		return zip.ErrBadRequest("login body too large")
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return zip.ErrBadRequest("invalid JSON body")
	}
	cid := strings.TrimSpace(req.ClientID)
	csec := strings.TrimSpace(req.ClientSecret)
	if cid == "" || csec == "" {
		return zip.ErrBadRequest("clientId and clientSecret are required")
	}
	if len(cid) > maxCredLen || len(csec) > maxCredLen || hasCtrlByte(cid) || hasCtrlByte(csec) {
		return zip.ErrBadRequest("credentials malformed")
	}

	tok, expiresIn, status := s.brokerIAMToken(ctx.Context(), cid, csec)
	if status != http.StatusOK {
		// One clean status, no upstream detail. 401 = auth failed; 502 = IAM down.
		return zip.Errorf(status, "kms login failed")
	}
	return ctx.JSON(http.StatusOK, loginResponse{AccessToken: tok, ExpiresIn: expiresIn, TokenType: "Bearer"})
}

// brokerIAMToken performs the client_credentials exchange at IAM and returns the
// access token + its lifetime, or an HTTP status to surface on failure. A 4xx from
// IAM (bad/unknown credential) maps to 401; a 5xx or transport failure maps to 502
// (upstream). hanzo.id (Casdoor) expects the credential in the form body, matching
// cloud's own proven client_credentials call (clients/aihttp AuthStyleInParams).
func (s *svc) brokerIAMToken(ctx context.Context, clientID, clientSecret string) (token string, expiresIn int64, status int) {
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.iamTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, http.StatusBadGateway
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := loginHTTPClient.Do(req)
	if err != nil {
		return "", 0, http.StatusBadGateway
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxLoginRespBytes))
	if resp.StatusCode/100 != 2 {
		if resp.StatusCode/100 == 5 {
			return "", 0, http.StatusBadGateway
		}
		return "", 0, http.StatusUnauthorized
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", 0, http.StatusBadGateway
	}
	if decoded.AccessToken == "" {
		// A 200 with no token (Casdoor may 200 an {"error":...}) is an auth failure.
		return "", 0, http.StatusUnauthorized
	}
	return decoded.AccessToken, decoded.ExpiresIn, http.StatusOK
}

// hasCtrlByte reports whether s carries a NUL or C0 control byte (TAB allowed). A
// credential never contains one; rejecting it keeps a hostile value out of the
// upstream form and any log line. Mirrors kmsapi.ContainsUnsafeControl intent.
func hasCtrlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b == 0x00 || (b < 0x20 && b != '\t') || b == 0x7f {
			return true
		}
	}
	return false
}
