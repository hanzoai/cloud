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
	"math/big"
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

// capAuthorizeTimeout HARD-bounds the per-scope spend-cap check. The cap is a POLICY
// overlay, NEVER a gate on availability: a slow, broken, or hot-looping commerce
// authorize must fail-open (allow) FAST, never hang the completion path (the SEV1 that
// a legacy-org GetById hot-loop caused). Short enough that a healthy in-proc call
// (sub-50ms) is unaffected, while a stuck one is abandoned and the request proceeds.
const capAuthorizeTimeout = 1500 * time.Millisecond

// OnCapError, when set, is called (best-effort) whenever the cap check FAILS OPEN — a
// timeout or any error on the authorize call. It lets the host log/alert on a degraded
// cap without this leaf package taking a logger dependency. nil = no-op.
var OnCapError func(error)

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
	// No LEDGER -> cannot bill either, and the client default is NOT a substitute.
	// orgFor falls back to c.org (the deployment's BRAND org, "hanzo"), which is the
	// right default for a CONFIG read — spend-alert rules, plan tier, cap rows are
	// scoped by the X-Org-Id header and the brand owns the platform's own. It is the
	// WRONG default for money: an org-less principal gated against the brand's balance
	// reads a wallet it has no claim on, and every unattributable request in the fleet
	// would be authorized by whatever Hanzo happens to be holding. An unresolvable org
	// refuses; it never charges — or clears — someone else.
	org := strings.TrimSpace(in.Org)
	if org == "" {
		if c.failOpen {
			return Verdict{Allow: true}, nil
		}
		return Verdict{}, fmt.Errorf("metering: empty org")
	}

	available, err := c.fetchAvailable(ctx, user, org, currencyOr(in.Currency))
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
		if OnCapError != nil {
			OnCapError(serr) // observe the fail-open (timeout / broken commerce); never block.
		}
		return Verdict{Allow: true}, nil // fail-open: a cap-check failure NEVER blocks a completion.
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

	// HARD BOUND (SEV1 safety): the cap check must NEVER hang the completion path. Run
	// the authorize with a strict deadline AND a select-based hard timeout that returns
	// to the caller even if the in-proc handler goroutine is STUCK (an unresponsive
	// handler — e.g. a hot-loop — cannot be interrupted, so ctx cancellation alone would
	// not unblock c.get). On timeout OR any error the caller (AuthorizeVerdict) fails
	// open and ALLOWS the request; a stuck goroutine is abandoned (a leak bounded by the
	// upstream hot-loop fix), never a wait.
	tctx, cancel := context.WithTimeout(ctx, capAuthorizeTimeout)
	defer cancel()
	type getResult struct {
		body []byte
		err  error
	}
	done := make(chan getResult, 1)
	go func() {
		b, e := c.get(tctx, pathLimitsAuthorize, q, c.orgFor(in.Org))
		done <- getResult{b, e}
	}()

	var body []byte
	select {
	case r := <-done:
		if r.err != nil {
			return scopeVerdict{}, r.err
		}
		body = r.body
	case <-tctx.Done():
		return scopeVerdict{}, fmt.Errorf("metering: cap authorize exceeded %s — failing open: %w", capAuthorizeTimeout, tctx.Err())
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

// Tier resolves the subject's commerce subscription-plan NAME
// (free | starter | pro | enterprise) via GET /v1/billing/tier?user=<subject>,
// scoped to org (X-Org-Id). This is the in-process (co-resident) — or S2S HTTP —
// read the embedded ai module's per-tier SKU gate consumes (via
// aiobject.SetTierReader) INSTEAD of an authed self-call to the cloud edge: the edge
// 401/403s a service call to /v1/billing/*, so the ai module's own HTTP path always
// returned "" in-cluster and the gate failed OPEN. This rides the SAME transport and
// service token the metering gate already bills over, so it reaches commerce's OWN
// service-token middleware (which reads the tenant from X-Org-Id), never the cloud edge.
//
// Empty subject or a not-configured client returns ("", nil): the gate treats an
// unknown tier as ALLOW (fail-safe), so a commerce hiccup never locks out a paying
// caller. Unlike fetchAvailable this does NOT short-circuit to the finance ledger —
// the plan tier is a commerce subscription fact, not a wallet balance.
func (c *Client) Tier(ctx context.Context, subject, org string) (string, error) {
	if !c.Enabled() || strings.TrimSpace(subject) == "" {
		return "", nil
	}
	body, err := c.get(ctx, pathTier, url.Values{"user": {subject}}, c.orgFor(org))
	if err != nil {
		return "", err
	}
	var tr struct {
		Tier struct {
			Name string `json:"name"`
		} `json:"tier"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("metering: decode tier name: %w", err)
	}
	return strings.TrimSpace(tr.Tier.Name), nil
}

// Usage is one usage event to record. The amount (the cost to debit) is the
// essential beside the billing key (User); the rest is descriptive metadata
// commerce stores on the transaction.
//
// Amount is the debit as an exact money.Amount — the canonical, typed value,
// native 18-decimal USD (the co-resident finance ledger's precision). One typed
// value, no precedence rules: a caller that has the exact cost (zen, which
// prices per token at 18-dp) sets Amount directly and the co-resident path
// debits it with NO rounding. The legacy int64 wire fields (AmountCents,
// AmountMicros) remain only for the HTTP path to commerce and for older callers
// that build a Usage without a money.Amount; from them Record reconstructs the
// same money.Amount. Amount, when non-zero, always wins.
type Usage struct {
	User     string `json:"user"`            // per-org billing key (org slug) — the debit destination.
	Actor    string `json:"actor,omitempty"` // org/sub identity for the audit trail (commerce ignores unknown fields today; forward-compatible).
	Org      string `json:"-"`               // routed via X-Org-Id, not the body.
	Currency string `json:"currency,omitempty"`

	// Amount is the exact debit, typed. Not serialized: the co-resident finance
	// path reads it directly; the HTTP path derives the wire fields below from it.
	Amount money.Amount `json:"-"`

	// AmountCents is the debit in whole cents (legacy wire field). Set by older
	// callers and by Record when serializing a typed Amount for commerce. When
	// Amount is set, this is ignored on the co-resident path.
	AmountCents int64 `json:"amount"`
	// AmountMicros is the debit in micro-USD (1e6 = $1), sub-cent precision for the
	// HTTP path so a tiny per-call cost is not lost to cent rounding. Commerce
	// prefers it over AmountCents (usage.go: effMicros); when set, AmountCents may
	// be 0. Zero/absent → commerce falls back to AmountCents*10000. Ignored on the
	// co-resident path when Amount is set.
	AmountMicros int64 `json:"amountMicros,omitempty"`

	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
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

// amountMoney returns the canonical typed debit. Amount wins; otherwise the
// int64 wire fields are reconstructed (micros preferred, then cents) so a
// legacy Usage without a typed Amount still debits. The result is zero when no
// amount is set, which Record treats as "skip".
func (u Usage) amountMoney() money.Amount {
	if !u.Amount.IsZero() {
		return u.Amount
	}
	if u.AmountMicros > 0 {
		// micro-USD (1e6 = $1) → 18-dp USD: scale the integer micros by 1e12.
		return money.FromAtto(new(big.Int).Mul(big.NewInt(u.AmountMicros), big.NewInt(1_000_000_000_000)))
	}
	if u.AmountCents > 0 {
		return money.FromCents(u.AmountCents)
	}
	return money.Zero()
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
	amt := u.amountMoney()
	if !c.Enabled() || amt.IsZero() || amt.IsNeg() {
		return nil, nil
	}
	if strings.TrimSpace(u.User) == "" {
		return nil, fmt.Errorf("metering: Record requires a user")
	}
	// The DEBIT names its ledger or it does not happen. Same rule as the gate above,
	// and the same reason: c.org would silently make the brand org pay for work it
	// never asked for. The native path already refuses an empty org inside finance,
	// but the HTTP path would post it to commerce under the brand header — so state it
	// once, here, where both paths pass.
	if strings.TrimSpace(u.Org) == "" {
		return nil, fmt.Errorf("metering: Record requires an org")
	}
	if u.Currency == "" {
		u.Currency = "usd"
	}

	// Co-resident native ledger: post the usage debit DIRECTLY (no HTTP), the ONE
	// money seam. finance is an exact 18-decimal USD ledger, so the typed Amount
	// debits with NO rounding — a per-token cost priced at 18-dp is never floored to
	// cents or micros. The debit is idempotent on RequestID inside finance, and a
	// test-mode client hits the sandbox books.
	if fin := finance.Current(); fin != nil {
		if err := fin.RecordUsage(ctx, types.UsageInput{
			Org: u.Org, Subject: u.User, Amount: amt, Currency: u.Currency,
			Model: u.Model, Provider: u.Provider, Project: u.Project, Service: u.Service,
			RequestID: u.RequestID, Test: c.test,
		}); err != nil {
			return nil, err
		}
		return &RecordResult{User: u.User, Amount: amt.Cents(), Currency: u.Currency, Type: "withdraw"}, nil
	}

	// HTTP path to commerce: serialize the wire fields. A typed Amount is folded to
	// micros (sub-cent) when it carries sub-cent precision, else cents — commerce
	// preserves the exact micros in metadata (usage.go: effMicros). When commerce
	// grows an 18-dp wire field, the typed Amount flows there unrounded too.
	if u.AmountMicros == 0 && u.AmountCents == 0 {
		u.AmountCents = amt.Cents()
	}

	payload, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("metering: encode usage: %w", err)
	}

	body, err := c.post(ctx, pathUsage, payload, u.Org)
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

// orgFor picks the X-Org-Id a CONFIG read is scoped by: the per-call org, else the
// deployment's own (brand) org. It is for reads whose absence of an org means "the
// platform's own settings" — spend-alert rules, cap rows, plan tier — and it is
// deliberately NOT reachable from the gate or the debit. Money has no default payer:
// AuthorizeVerdict and Record refuse an empty org before they ever get here.
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
