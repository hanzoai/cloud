package cloud

// Resource billing — the ONE per-org gate+meter every non-LLM "create a
// resource" handler uses, so provisioned infrastructure (databases, buckets,
// caches, vector collections, ML models, training jobs) is paid for, in EVERY
// environment, by the resource's OWN org.
//
// It is the in-handler analogue of BillingGate (the request-edge LLM gate): both
// wrap the SAME canonical metering client (github.com/hanzoai/commerce/metering,
// Deps.Metering) and the SAME commerce ledger. The edge gate prices by PATH and
// runs as middleware; resource billing runs INSIDE a create handler, where the
// caller's org has already been resolved to the exact slug that namespaces the
// backend resource — so the balance check and the debit are guaranteed to target
// that one tenant, never a default, never another tenant.
//
// Multitenancy contract (the whole point):
//   - org is the caller's resolved org slug (the value that namespaces the
//     resource). It is sent to commerce as BOTH the user identity AND the
//     X-IAM-Org-Id tenant header, OVERRIDING the metering client's default org —
//     so an unfunded tenant can never provision on another tenant's balance, and
//     a funded tenant's spend lands only on its own ledger.
//   - Fail-closed: a non-positive balance is 402; a commerce that cannot be
//     reached is 503 (unless the client was built fail-open). No free
//     provisioning on a billing outage for a priced resource.
//
// Env awareness: the real-money-vs-sandbox decision is structural — each
// deployment (mainnet/testnet/devnet, the 3-env split) points at its OWN
// commerce/Square, so test and dev STILL meter and bill, just against that env's
// sandbox ledger. This gate fires identically in every env; it never branches on
// env to bypass billing. Env is recorded for attribution only.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// DefaultResourceFeeCents is the fallback flat provision/create fee (in cents)
// when no operator override is set — $1.00, so "every service costs money" holds
// out of the box. It is a configurable POLICY default, not a fabricated market
// price: ops sets the real number per deployment/kind via ResourceFeeCents's env
// knobs. Set a kind to 0 to make it free (and therefore un-gated, mirroring the
// edge gate's price==0 pass-through).
const DefaultResourceFeeCents int64 = 100

// ResourceMeter gates and meters per-org spend for non-LLM resource creation,
// reusing Deps.Metering (the single commerce billing client). Build it with
// NewResourceMeter. A nil meter, or one whose commerce URL is unset, makes Gate
// allow and Meter a no-op — so an unconfigured deployment is never blocked,
// exactly like BillingGate.
type ResourceMeter struct {
	m        *metering.Client
	provider string // commerce "provider" label for attribution (e.g. "provisioning", "compute").
	env      string // deployment env (mainnet|testnet|devnet); attribution only — never a billing bypass.
	log      luxlog.Logger
}

// NewResourceMeter builds a ResourceMeter from the shared deps. provider labels
// the recorded usage so spend is attributable to the surface that metered it.
func NewResourceMeter(deps Deps, provider string) *ResourceMeter {
	return &ResourceMeter{
		m:        deps.Metering,
		provider: provider,
		env:      deps.Env,
		log:      deps.Logger,
	}
}

// Enabled reports whether billing will actually enforce (a commerce URL is
// configured). When false, Gate allows and Meter is a no-op.
func (rm *ResourceMeter) Enabled() bool { return rm != nil && rm.m != nil && rm.m.Enabled() }

// Gate is the pre-create balance gate. It returns:
//
//	nil                              -> allow (balance positive, OR not priced,
//	                                    OR billing not configured).
//	metering.ErrInsufficientBalance  -> deny, out of funds (render 402).
//	other error                      -> balance unknown; fail-closed denies
//	                                    (render 503). Fail-open returns nil.
//
// costCents<=0 means the kind is free → no gate (mirrors BillingGate's price==0
// short-circuit). org MUST be the caller's resolved slug; it is sent as the
// commerce user AND X-IAM-Org-Id so the CALLER's ledger is checked, overriding
// the client default org — the anti-cross-tenant property. The balance check
// honors ctx (a client disconnect/timeout cancels it).
func (rm *ResourceMeter) Gate(ctx context.Context, org, kind string, costCents int64) error {
	if !rm.Enabled() || costCents <= 0 {
		return nil
	}
	return rm.m.Authorize(ctx, metering.AuthInput{User: org, Org: org})
}

// Meter records a successful charge to the caller's org ledger. It is the ONE
// metering entry point for BOTH the one-time create fee AND any recurring
// footprint charge (storage GB-month, GPU-hour): the caller supplies the amount,
// so a future recurring meter reuses this same method with a usage-derived
// amount. No-op when billing is not configured or amountCents<=0.
//
// The debit is fire-and-forget on a background context: the resource already
// exists, so the charge must never block or corrupt the response the caller
// received, and a request-context cancellation must not cancel the debit (mirror
// of BillingGate). A debit failure is logged for reconciliation, not swallowed.
func (rm *ResourceMeter) Meter(org, kind string, amountCents int64, requestID, clientIP string) {
	if !rm.Enabled() || amountCents <= 0 {
		return
	}
	usage := metering.Usage{
		User:        org, // per-ORG billing: ledger keyed on the org slug.
		Org:         org, // X-IAM-Org-Id -> caller's namespace (overrides client default).
		Currency:    "usd",
		AmountCents: amountCents,
		Provider:    rm.provider,
		RequestID:   requestID,
		Status:      "success",
		ClientIP:    clientIP,
	}
	m, log, env := rm.m, rm.log, rm.env
	go func() {
		if _, err := m.Record(context.Background(), usage); err != nil && log != nil {
			log.Error("resource debit failed (resource created, not billed)",
				"org", org, "kind", kind, "provider", usage.Provider,
				"cents", amountCents, "env", env, "err", err)
		}
	}()
}

// DenyResource renders the Gate denial outcomes as the SAME JSON the edge gate
// returns (denyBilling), so every Hanzo surface emits one error contract:
// 402 insufficient_balance / 503 balance_unavailable.
func DenyResource(c *zip.Ctx, err error) error {
	if errors.Is(err, metering.ErrInsufficientBalance) {
		return c.JSON(http.StatusPaymentRequired, map[string]any{
			"error": map[string]string{
				"code":    "insufficient_balance",
				"message": "Add credits at console.hanzo.ai",
			},
		})
	}
	return c.JSON(http.StatusServiceUnavailable, map[string]any{
		"error": map[string]string{
			"code":    "balance_unavailable",
			"message": "Billing temporarily unavailable",
		},
	})
}

// ResourceFeeCents resolves the flat create fee (in cents) for kind from
// operator config, most specific first:
//
//	<envPrefix>_<KIND>   e.g. CLOUD_PROVISION_FEE_CENTS_SQL=500
//	<envPrefix>          e.g. CLOUD_PROVISION_FEE_CENTS=100  (all kinds)
//	DefaultResourceFeeCents                                   ($1.00)
//
// A clearly-named, configurable policy knob — never a fabricated price. A value
// of 0 makes the kind free (and un-gated). Negative/invalid values are ignored
// (fall through), so a typo can never make a paid resource free by accident.
func ResourceFeeCents(envPrefix, kind string) int64 {
	if v, ok := parseNonNegCents(os.Getenv(envPrefix + "_" + strings.ToUpper(kind))); ok {
		return v
	}
	if v, ok := parseNonNegCents(os.Getenv(envPrefix)); ok {
		return v
	}
	return DefaultResourceFeeCents
}

func parseNonNegCents(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// ClientIP extracts the originating client IP from X-Forwarded-For (the gateway
// sets it); the left-most entry is the real client. Shared by the edge gate and
// the resource meter so usage records carry a consistent client_ip.
func ClientIP(c *zip.Ctx) string {
	xff := c.Header("X-Forwarded-For")
	if xff == "" {
		return ""
	}
	if i := strings.IndexByte(xff, ','); i > 0 {
		return strings.TrimSpace(xff[:i])
	}
	return strings.TrimSpace(xff)
}
