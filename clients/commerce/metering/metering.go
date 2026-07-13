// Package metering is the ONE way every Hanzo product meters usage to
// commerce — the single billing source of truth — so that every product
// (not only the LLM/cloud path) can be paid for.
//
// It provides two operations, matching the proven cloud/gateway path:
//
//   - Authorize: a pre-request balance gate. Fail-closed by default — if the
//     balance cannot be determined the request is denied, exactly like the
//     gateway's prepaid-balance gate (gateway/auth_middleware.go). With
//     TierAware enabled it consults the tier-aware effective balance, which
//     folds in the tenant's included plan allotment (e.g. the free-tier
//     daily credit) so included usage is honored before prepaid funds.
//
//   - Record: a post-request usage write. Records a usage event (cost in
//     cents) against commerce, which debits the user's balance ledger.
//
// The HTTP contract is commerce's canonical billing API, mounted under /v1
// (commerce/api/billing/handlers.go):
//
//	GET  {BaseURL}/v1/billing/balance?user={user}&currency={cur}
//	GET  {BaseURL}/v1/billing/tier?user={user}            (tier-aware)
//	POST {BaseURL}/v1/billing/usage
//
// Auth is the commerce service token (admin-scoped S2S), sent as
//
//	Authorization: Bearer {Token}
//
// plus the tenant org as the X-Org-Id header. The token is a secret and
// MUST be sourced from KMS (never plaintext); this package never reads it from
// disk — the caller supplies it (typically from an env var the operator wires
// from a KMS-backed secret, e.g. COMMERCE_SERVICE_TOKEN).
//
// Its only intra-repo dependency is the in-process finance seam (clients/finance): when a
// co-resident finance ledger is published, Authorize's balance read and Record's usage
// debit resolve it DIRECTLY (a typed in-proc call, no HTTP); otherwise both fall back to
// the commerce billing HTTP contract above. It pulls in NO commerce server internals, so
// any product — Go service, CLI, or job — can meter through it. It lives in the commerce
// repo because commerce is the billing source of truth; the package is the canonical
// client for its billing API.
package metering

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// Canonical commerce billing paths (mounted under /v1). Keep in lockstep with
// commerce/api/billing/handlers.go — a wrong prefix 404s and, fail-closed,
// denies every request.
const (
	pathBalance         = "/v1/billing/balance"
	pathTier            = "/v1/billing/tier"
	pathUsage           = "/v1/billing/usage"
	pathSpendAlerts     = "/v1/billing/spend-alerts"
	pathLimitsAuthorize = "/v1/billing/spend-alerts/authorize"
)

// Header carrying the tenant org slug for commerce namespace resolution.
// Commerce's service-token auth reads the org from X-Org-Id
// (commerce/middleware/accesstoken.go TokenRequired: c.GetHeader("X-Org-Id"));
// without it commerce falls back to COMMERCE_SERVICE_ORG, then "hanzo" — which
// would silently debit the wrong tenant.
const headerOrg = "X-Org-Id"

// headerTest opts a service-token call into commerce's TEST ledger
// (org.Live=false): balances and debits hit the sandbox books, not real money.
// See commerce/middleware/accesstoken.go (c.GetHeader("X-Hanzo-Test")). Sent
// only when Config.Test is true — production metering omits it and stays live.
const headerTest = "X-Hanzo-Test"

// ErrInsufficientBalance is returned by Authorize when commerce confirms the
// user's available balance is non-positive. It is distinct from a connectivity
// failure so callers can map it to HTTP 402 (vs 503 for "unknown").
var ErrInsufficientBalance = errors.New("metering: insufficient balance")

// ErrSpendCapExceeded is returned by Authorize when the caller is FUNDED but a
// configured per-scope spend cap (issue #70) would be exceeded by this request.
// It is DISTINCT from ErrInsufficientBalance: the balance is fine, the tenant's
// own policy ceiling is not — callers map it to a 402 spend_cap_exceeded, not the
// out-of-funds insufficient_balance.
var ErrSpendCapExceeded = errors.New("metering: spend cap exceeded")

// HTTPDoer is the minimal HTTP surface the client needs. *http.Client
// satisfies it; tests and instrumented transports can substitute their own.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config configures a Client. Only BaseURL is conceptually required; an empty
// BaseURL puts the client in "not configured" mode where Authorize allows and
// Record is a no-op — matching the gateway's behavior when no billing URL is
// set, so a product can adopt metering before its tenant billing is wired.
type Config struct {
	// BaseURL is the commerce service base, e.g.
	// "http://commerce.hanzo.svc.cluster.local:8001". No trailing /v1 — the
	// client appends the canonical billing paths itself.
	BaseURL string

	// Token is the commerce service token (admin-scoped). MUST come from KMS;
	// never hard-code or read from a file. Sent as "Authorization: Bearer".
	Token string

	// Org is the tenant org slug (e.g. "hanzo") sent as X-Org-Id so
	// commerce resolves the right tenant namespace. Per-request Org on the
	// Usage/AuthInput overrides this default.
	Org string

	// TierAware, when true, makes Authorize consult GET /v1/billing/tier and
	// gate on the effective balance (prepaid + included plan allotment such as
	// the free-tier daily credit) instead of the bare prepaid balance. This is
	// the same effectiveAvailable commerce computes in GetTier.
	TierAware bool

	// FailOpen inverts the default fail-closed posture: when commerce cannot be
	// reached, Authorize allows the request instead of denying it. Leave false
	// for paid products; set true only where availability outranks billing
	// (and accept the revenue leak). Mirrors the gateway, which is fail-closed.
	FailOpen bool

	// Test routes every call to commerce's TEST ledger (X-Hanzo-Test: true) so
	// balances and debits hit the sandbox books, not real money. Production
	// metering leaves this false. Used for end-to-end proofs and staging.
	Test bool

	// Timeout bounds each commerce HTTP call. Default 5s (the gateway's value).
	Timeout time.Duration

	// HTTPClient overrides the underlying HTTP client. When nil a client with
	// Timeout is created.
	HTTPClient HTTPDoer
}

// Client meters usage to commerce. It is safe for concurrent use.
type Client struct {
	baseURL   string
	token     string
	org       string
	tierAware bool
	failOpen  bool
	test      bool
	http      HTTPDoer
}

// New builds a metering Client from cfg. It returns an error only for an
// unparseable BaseURL; an empty BaseURL is valid ("not configured" mode).
func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base != "" {
		if _, err := url.Parse(base); err != nil {
			return nil, fmt.Errorf("metering: invalid BaseURL %q: %w", cfg.BaseURL, err)
		}
	}

	doer := cfg.HTTPClient
	if doer == nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		doer = &http.Client{Timeout: timeout}
	}

	return &Client{
		baseURL:   base,
		token:     strings.TrimSpace(cfg.Token),
		org:       strings.TrimSpace(cfg.Org),
		tierAware: cfg.TierAware,
		failOpen:  cfg.FailOpen,
		test:      cfg.Test,
		http:      doer,
	}, nil
}

// Enabled reports whether a commerce BaseURL is configured. When false,
// Authorize always allows and Record is a no-op.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// AuthInput identifies who to authorize.
//
// User is the PREPAID BILLING KEY — the org slug (e.g. "hanzo"), NOT
// "org/sub". Prepaid balance is per-org: one credit covers the whole org,
// exactly as the proven LLM gate does (ai/routers/filter_balance.go
// resolveBillingKey returns user.Owner — the org slug — and queries
// GET /v1/billing/balance?user=<org>). Keying per-user instead would check an
// empty per-user ledger and deny a funded org. IdentityFromGatewayHeaders sets
// this correctly from the gateway's X-Org-Id.
//
// Actor is the full "org/sub" identity (e.g. "hanzo/alice") recorded on the
// usage transaction for the audit trail. It never affects which balance is
// gated or debited — that is always the org (User).
//
// Org overrides the client default org (the X-Org-Id namespace) for this
// call; it normally equals User. Currency defaults to "usd".
type AuthInput struct {
	User     string
	Actor    string
	Org      string
	Currency string
	// AmountCents, when > 0, gates on available >= AmountCents instead of the
	// bare available > 0. Use it to authorize a known up-front charge (e.g. the
	// first hour of a machine) so a 1-cent balance cannot green-light an
	// arbitrarily expensive request. Zero preserves the legacy "any positive
	// balance" gate.
	AmountCents int64

	// Project and Service scope the per-scope spend cap + rate limit (issue #70).
	// Service is server-derived (route/provider). Empty = the org-wide default
	// scope. Forwarded to commerce so the right scope cap is resolved; they never
	// change which BALANCE is gated (always the org via User).
	Project string
	Service string

	// ProjectValidated reports whether Project is bound to a VALIDATED identity
	// claim. When false, commerce DEGRADES a project-scoped hard cap to a soft warn
	// (records + warns, never 402) so a forgeable X-Project-Id can neither hard-stop
	// nor be evaded. The org and service axes are always validated. Today IAM mints
	// no project claim, so cloud sends false; when it does, cloud sends true and
	// project caps auto-harden.
	ProjectValidated bool
}

// Verdict is the full gate outcome AuthorizeVerdict returns, so a gate can render
// the distinct denial shapes AND emit the soft-warn header from ONE round trip.
//
//	Allow=true,  Reason="",                     WarnPct=p  -> allow; if p>0 emit X-Spend-Warn.
//	Allow=false, Reason="insufficient_balance"             -> 402 out of funds.
//	Allow=false, Reason="spend_cap", Cap/Spent            -> 402 spend cap exceeded.
type Verdict struct {
	Allow      bool
	Reason     string // "", "insufficient_balance", "spend_cap"
	WarnPct    int
	CapCents   int64
	SpentCents int64
}

// scopeVerdict mirrors commerce GET /v1/limits/authorize
// ({allow,reason,capCents,spentCents,warnPct}); reason is "" or "spend_cap".
type scopeVerdict struct {
	Allow      bool   `json:"allow"`
	Reason     string `json:"reason"`
	CapCents   int64  `json:"capCents"`
	SpentCents int64  `json:"spentCents"`
	WarnPct    int    `json:"warnPct"`
}

// Authorize is the pre-request gate. It is the thin error-mapping wrapper over
// AuthorizeVerdict, preserving the proven three-outcome contract:
//
//	(nil)                       -> allow.
//	(ErrInsufficientBalance)    -> deny: out of funds          (map to HTTP 402).
//	(ErrSpendCapExceeded)       -> deny: funded but over a per-scope cap (HTTP 402
//	                               spend_cap_exceeded — distinct from out-of-funds).
//	(other error)               -> balance unknown; with the default fail-closed
//	                               posture this denies          (map to HTTP 503).
//	                               With FailOpen it returns nil (allow).
//
// When the client is not configured (no BaseURL) it always allows.
func (c *Client) Authorize(ctx context.Context, in AuthInput) error {
	v, err := c.AuthorizeVerdict(ctx, in)
	if err != nil {
		return err // connectivity/unknown; fail posture already applied inside.
	}
	if v.Allow {
		return nil
	}
	if v.Reason == "spend_cap" {
		return ErrSpendCapExceeded
	}
	return ErrInsufficientBalance
}

// AuthorizeVerdict is the full pre-request gate: it checks FUNDS first (the
// money-safety backstop, honoring the fail-open/closed posture on a connectivity
// error) and, only when funded, layers the per-scope SPEND CAP verdict.
//
// Spend caps are a POLICY OVERLAY, not a funds check: the balance gate already
// prevents overspending real money, so a cap-endpoint failure FAILS OPEN
// (degrades to funds-only gating) regardless of the funds fail posture — a
// commerce limits blip must never take down all paid traffic. An older commerce
// without the endpoint (404) is likewise treated as "no cap configured".
//
// The returned WarnPct (>0 when at/over a covering cap's soft threshold) lets the
// caller emit X-Spend-Warn from this one round trip.
func (c *Client) AuthorizeVerdict(ctx context.Context, in AuthInput) (Verdict, error) {
	if !c.Enabled() {
		return Verdict{Allow: true}, nil
	}
	user := strings.TrimSpace(in.User)
	if user == "" {
		// No identity -> cannot bill. Fail-closed denies (anonymous traffic
		// must be handled by a public-path bypass before reaching here).
		if c.failOpen {
			return Verdict{Allow: true}, nil
		}
		return Verdict{}, fmt.Errorf("metering: empty user")
	}

	available, err := c.fetchAvailable(ctx, user, c.orgFor(in.Org), currencyOr(in.Currency))
	if err != nil {
		if c.failOpen {
			return Verdict{Allow: true}, nil
		}
		return Verdict{}, err // unknown -> deny (fail-closed).
	}
	funded := available > 0
	if in.AmountCents > 0 {
		funded = available >= in.AmountCents
	}
	if !funded {
		return Verdict{Allow: false, Reason: "insufficient_balance"}, nil
	}

	// Funded — layer the per-scope spend cap. Fail-open on any cap error.
	sv, serr := c.scopeAuthorize(ctx, in)
	if serr != nil {
		return Verdict{Allow: true}, nil
	}
	if !sv.Allow && sv.Reason == "spend_cap" {
		return Verdict{Allow: false, Reason: "spend_cap", CapCents: sv.CapCents, SpentCents: sv.SpentCents}, nil
	}
	return Verdict{Allow: true, WarnPct: sv.WarnPct}, nil
}

// ScopeRule is one scope's rate-limit config, consumed by the cloud
// ScopeRateLimit middleware. Only rows with a positive RateLimitRpm are returned.
type ScopeRule struct {
	Project      string
	Service      string
	RateLimitRpm int
}

// ScopeRules lists the org's per-scope rate-limit rules (the rate-limited subset
// of its spend-alert rows). It is the config source for the cloud ScopeRateLimit
// middleware, which caches it with a short TTL and fails open on error. Org is
// sent as X-Org-Id so the rules are the caller org's own — never another tenant's.
func (c *Client) ScopeRules(ctx context.Context, org string) ([]ScopeRule, error) {
	if !c.Enabled() {
		return nil, nil
	}
	body, err := c.get(ctx, pathSpendAlerts, nil, c.orgFor(org))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Project      string `json:"project"`
		Service      string `json:"service"`
		RateLimitRpm int    `json:"rateLimitRpm"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]ScopeRule, 0, len(rows))
	for _, r := range rows {
		if r.RateLimitRpm > 0 {
			out = append(out, ScopeRule{Project: r.Project, Service: r.Service, RateLimitRpm: r.RateLimitRpm})
		}
	}
	return out, nil
}

// scopeAuthorize consults commerce's per-scope cap verdict for this request. The
// org (X-Org-Id) is the caller's own, so commerce resolves the cap in the
// caller's namespace — a scope on org X can never gate org Y.
func (c *Client) scopeAuthorize(ctx context.Context, in AuthInput) (scopeVerdict, error) {
	q := url.Values{"user": {strings.TrimSpace(in.User)}}
	if p := strings.TrimSpace(in.Project); p != "" {
		q.Set("project", p)
	}
	if s := strings.TrimSpace(in.Service); s != "" {
		q.Set("service", s)
	}
	if in.AmountCents > 0 {
		q.Set("amount", strconv.FormatInt(in.AmountCents, 10))
	}
	// pv=1 only when the project axis is bound to a validated claim; otherwise
	// commerce degrades a project-scoped hard cap to soft (anti project-spoof).
	if in.ProjectValidated {
		q.Set("pv", "1")
	}
	q.Set("currency", currencyOr(in.Currency))

	body, err := c.get(ctx, pathLimitsAuthorize, q, c.orgFor(in.Org))
	if err != nil {
		return scopeVerdict{}, err
	}
	var v scopeVerdict
	if err := json.Unmarshal(body, &v); err != nil {
		return scopeVerdict{}, err
	}
	return v, nil
}

// fetchAvailable returns the spendable balance in cents. With TierAware it uses
// the tier endpoint's effectiveAvailable (prepaid + included allotment);
// otherwise the bare prepaid available from the balance endpoint.
func (c *Client) fetchAvailable(ctx context.Context, user, org, cur string) (int64, error) {
	// Co-resident native wallet: the balance is a DIRECT ledger read (no HTTP). user is the
	// billing subject, org the wallet namespace; finance derives the account. A test-mode
	// client reads the sandbox books so test and live money never mix.
	if fin := finance.Current(); fin != nil {
		bal, err := fin.Balance(ctx, org, user, cur, c.test)
		if err != nil {
			return 0, err
		}
		return bal.Cents(), nil
	}
	if c.tierAware {
		q := url.Values{"user": {user}}
		body, err := c.get(ctx, pathTier, q, org)
		if err != nil {
			return 0, err
		}
		var tr tierResponse
		if err := json.Unmarshal(body, &tr); err != nil {
			return 0, fmt.Errorf("metering: decode tier: %w", err)
		}
		return tr.Balance.EffectiveAvailable, nil
	}

	q := url.Values{"user": {user}, "currency": {cur}}
	body, err := c.get(ctx, pathBalance, q, org)
	if err != nil {
		return 0, err
	}
	var br balanceResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return 0, fmt.Errorf("metering: decode balance: %w", err)
	}
	return br.Available, nil
}

// Usage is one usage event to record. User (IAM "org/sub") and AmountCents
// (the cost to debit) are the essentials; the rest is descriptive metadata
// commerce stores on the transaction. Fields mirror commerce's usageRequest
// (commerce/api/billing/usage.go) one-for-one.
type Usage struct {
	User        string `json:"user"`            // per-org billing key (org slug) — the debit destination.
	Actor       string `json:"actor,omitempty"` // org/sub identity for the audit trail (commerce ignores unknown fields today; forward-compatible).
	Org         string `json:"-"`               // routed via X-Org-Id, not the body.
	Currency    string `json:"currency,omitempty"`
	AmountCents int64  `json:"amount"`
	// AmountMicros is the debit in micro-USD (1e6 = $1), carrying sub-cent
	// precision so a tiny per-call inference cost meters EXACTLY instead of
	// rounding to zero (and slipping through unbilled). Commerce prefers it over
	// AmountCents (usage.go: effMicros); when set, AmountCents may be 0. Zero/absent
	// → commerce falls back to AmountCents*10000.
	AmountMicros int64  `json:"amountMicros,omitempty"`
	Model        string `json:"model,omitempty"`
	Provider     string `json:"provider,omitempty"`
	// Project and Service attribute this debit to a scope so commerce records the
	// dimensions the per-scope spend cap sums over (issue #70). Empty = the
	// org-wide default scope.
	Project          string `json:"project,omitempty"`
	Service          string `json:"service,omitempty"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
	RequestID        string `json:"requestId,omitempty"`
	Premium          bool   `json:"premium,omitempty"`
	Stream           bool   `json:"stream,omitempty"`
	Status           string `json:"status,omitempty"`
	ClientIP         string `json:"clientIp,omitempty"`
}

// RecordResult is the commerce response to a usage write.
type RecordResult struct {
	TransactionID string `json:"transactionId"`
	User          string `json:"user"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Type          string `json:"type"`
}

// Record writes a usage event to commerce, debiting the user's balance.
//
// It is a no-op (nil, nil) when the client is not configured or when
// AmountCents <= 0 (commerce treats zero-cost usage as "skipped"). Usage
// recording is deliberately decoupled from gating: the work already happened
// and must be recorded, so balance is NOT re-checked here — exactly as
// commerce's RecordUsage documents.
//
// Provider is the service name doing the metering when no model/provider is
// natural (e.g. "search", "functions"); set it on Usage.Provider.
func (c *Client) Record(ctx context.Context, u Usage) (*RecordResult, error) {
	if !c.Enabled() || (u.AmountCents <= 0 && u.AmountMicros <= 0) {
		return nil, nil
	}
	if strings.TrimSpace(u.User) == "" {
		return nil, fmt.Errorf("metering: Record requires a user")
	}
	if u.Currency == "" {
		u.Currency = "usd"
	}

	// Co-resident native ledger: post the usage debit DIRECTLY (no HTTP), the ONE money
	// seam. finance is a whole-cent ledger, so a micros-only caller (the AI meter prices
	// sub-cent per call) is folded to whole cents by CEIL — never dropped to zero, which
	// would leak exactly the sub-cent revenue AmountMicros exists to capture. The debit is
	// idempotent on RequestID inside finance, and a test-mode client hits the sandbox books.
	if fin := finance.Current(); fin != nil {
		cents := usageCents(u)
		if err := fin.RecordUsage(ctx, types.UsageInput{
			Org: u.Org, Subject: u.User, Amount: money.FromCents(cents), Currency: u.Currency,
			Model: u.Model, Provider: u.Provider, Project: u.Project, Service: u.Service,
			RequestID: u.RequestID, Test: c.test,
		}); err != nil {
			return nil, err
		}
		return &RecordResult{User: u.User, Amount: cents, Currency: u.Currency, Type: "withdraw"}, nil
	}

	payload, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("metering: encode usage: %w", err)
	}

	body, err := c.post(ctx, pathUsage, payload, c.orgFor(u.Org))
	if err != nil {
		return nil, err
	}

	var res RecordResult
	if err := json.Unmarshal(body, &res); err != nil {
		// Commerce returned 2xx but an unexpected shape; the debit still
		// happened, so don't treat decode failure as a hard error.
		return nil, nil
	}
	return &res, nil
}

// usageCents is the whole-cent debit for the native finance ledger (a cents-only ledger).
// It prefers AmountCents; when a caller priced sub-cent (AmountMicros only — e.g. the AI
// meter, which debits micro-USD per call) it CEILs micro-USD → cents (10_000 micro-USD =
// 1¢), so a tiny per-call cost bills at least 1¢ instead of rounding to zero and slipping
// through unbilled. Record's early guard guarantees at least one of the two is positive, so
// the result here is always >= 1.
func usageCents(u Usage) int64 {
	if u.AmountCents > 0 {
		return u.AmountCents
	}
	return (u.AmountMicros + 9999) / 10000 // ceil micro-USD to whole cents
}

// ---- HTTP plumbing -------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, q url.Values, org string) ([]byte, error) {
	u := c.baseURL + path
	if enc := q.Encode(); enc != "" {
		u += "?" + enc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req, org)
}

func (c *Client) post(ctx context.Context, path string, body []byte, org string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, org)
}

func (c *Client) do(req *http.Request, org string) ([]byte, error) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if org != "" {
		req.Header.Set(headerOrg, org)
	}
	if c.test {
		req.Header.Set(headerTest, "true")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metering: commerce unreachable: %w", err)
	}
	defer resp.Body.Close()

	// Read a bounded body — these are tiny JSON objects.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("metering: read commerce response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metering: commerce status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func (c *Client) orgFor(perCall string) string {
	if perCall = strings.TrimSpace(perCall); perCall != "" {
		return perCall
	}
	return c.org
}

func currencyOr(cur string) string {
	if cur = strings.TrimSpace(cur); cur != "" {
		return cur
	}
	return "usd"
}

// balanceResponse mirrors commerce GET /v1/billing/balance. Amounts are cents;
// available = balance - holds.
type balanceResponse struct {
	User      string `json:"user"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
}

// tierResponse mirrors commerce GET /v1/billing/tier. effectiveAvailable folds
// the tenant's included plan allotment (e.g. free-tier daily credit) into the
// prepaid available balance.
type tierResponse struct {
	User    string `json:"user"`
	Balance struct {
		Currency           string `json:"currency"`
		PrepaidAvailable   int64  `json:"prepaidAvailable"`
		DailyRemaining     int64  `json:"dailyRemaining"`
		EffectiveAvailable int64  `json:"effectiveAvailable"`
	} `json:"balance"`
}
