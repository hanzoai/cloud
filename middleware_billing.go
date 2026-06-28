package cloud

// Billing gate — the ONE place the unified cloud binary enforces
// pay-for-everything on the request edge.
//
// It wraps the canonical metering client (github.com/hanzoai/commerce/metering),
// the single billing source of truth shared by every Hanzo product. That client
// is net/http-based, so its own Middleware can't drop into zip (Fiber v3); but
// its CLIENT CORE — New + Authorize + Record — is transport-agnostic and
// reusable. BillingGate is the thin zip-native adapter that calls Authorize
// before the handler and Record after it, mapping outcomes to the same HTTP
// status contract the gateway uses (402 out-of-funds, 503 balance-unknown).
//
// Fail-closed is the default and lives inside the metering client: when commerce
// cannot be reached Authorize denies (returns a non-ErrInsufficientBalance
// error) unless the client was built FailOpen. The gate does not re-implement
// that policy — it only renders the two denial shapes.

import (
	"context"
	"strings"

	"github.com/hanzoai/commerce/metering"
	"github.com/hanzoai/zip"
)

// meteringProvider labels usage this binary records so spend is attributable to
// the cloud edge (vs a subsystem that meters its own units, e.g. ai).
const meteringProvider = "cloud"

// BillingGate returns a zip middleware that gates every request on the caller's
// commerce balance and records usage for priced paths.
//
// Order of operations:
//  1. price(c) == 0 AND not gated → pass straight through (free path).
//  2. Authorize(ctx, identity) BEFORE c.Next():
//     nil                    → allow.
//     ErrInsufficientBalance → 402 insufficient_balance, c.Next() NOT called.
//     other error            → 503 balance_unavailable, c.Next() NOT called
//     (fail-closed; the client returns nil here when
//     built FailOpen, so this branch never fires in
//     fail-open mode).
//  3. c.Next() runs the handler chain.
//  4. After a successful chain, if price(c) > 0, fire `go m.Record(...)` so the
//     debit never blocks or corrupts the response the user already received.
//
// When m is nil or not configured (no commerce URL) the gate is a no-op: it
// returns c.Next() directly so an unconfigured deployment is never blocked.
// price must not be nil; pass DefaultPrice.
func BillingGate(m *metering.Client, price func(c *zip.Ctx) int64) zip.Handler {
	if !billingEnabled(m) || price == nil {
		// No-op passthrough — keeps the middleware chain uniform whether or
		// not billing is wired, so callers always Use() it unconditionally.
		return func(c *zip.Ctx) error { return c.Next() }
	}

	return func(c *zip.Ctx) error {
		cents := price(c)

		// A request that is both free (price 0) and on a path we never gate
		// short-circuits. We still gate priced paths AND any path the operator
		// wants metered; DefaultPrice returns 0 for free/self-metered paths, so
		// price==0 here means "do not gate, do not charge".
		if cents <= 0 {
			return c.Next()
		}

		in := identityFromCtx(c)

		// Pre-request gate. Authorize encodes fail-open/closed internally.
		if err := m.Authorize(c.Context(), in); err != nil {
			return denyBilling(c, err)
		}

		if err := c.Next(); err != nil {
			// Handler failed — surface the error; do not bill failed work.
			return err
		}

		// Post-request record, best-effort and detached so a client
		// disconnect can't cancel the debit and recording never blocks the
		// reply. price() already vetoed zero-cost above. Capture usage by
		// value and use a background context: the Fiber request context is
		// recycled once the handler returns, so it must NOT be read in the
		// goroutine (mirrors metering.recordAsync).
		usage := metering.Usage{
			User:        in.User,
			Org:         in.Org,
			Currency:    in.Currency,
			AmountCents: cents,
			Provider:    meteringProvider,
			RequestID:   c.RequestID(),
			Status:      "success",
			ClientIP:    clientIP(c),
		}
		go func() { _, _ = m.Record(context.Background(), usage) }()
		return nil
	}
}

// denyBilling renders the two denial shapes, matching the metering middleware's
// net/http defaults so every Hanzo surface returns an identical error body.
func denyBilling(c *zip.Ctx, err error) error {
	if err == metering.ErrInsufficientBalance {
		return c.JSON(402, map[string]any{
			"error": map[string]string{
				"code":    "insufficient_balance",
				"message": "Add credits at console.hanzo.ai",
			},
		})
	}
	// Balance unknown -> fail-closed -> 503.
	return c.JSON(503, map[string]any{
		"error": map[string]string{
			"code":    "balance_unavailable",
			"message": "Billing temporarily unavailable",
		},
	})
}

// identityFromCtx builds the commerce billing identity from the gateway-minted
// headers zip exposes. The user is "{org}/{sub}" (commerce's iam-user form) when
// both are present, else the bare sub — identical to metering's
// IdentityFromGatewayHeaders so cloud and every other product key the same
// ledger entry.
func identityFromCtx(c *zip.Ctx) metering.AuthInput {
	org := strings.TrimSpace(c.Org())
	sub := strings.TrimSpace(c.User())
	user := sub
	if org != "" && sub != "" {
		user = org + "/" + sub
	}
	return metering.AuthInput{User: user, Org: org}
}

// clientIP extracts the originating IP from X-Forwarded-For (the gateway sets
// it); the left-most entry is the real client.
func clientIP(c *zip.Ctx) string {
	xff := c.Header("X-Forwarded-For")
	if xff == "" {
		return ""
	}
	if i := strings.IndexByte(xff, ','); i > 0 {
		return strings.TrimSpace(xff[:i])
	}
	return strings.TrimSpace(xff)
}

// billingEnabled reports whether the gate should enforce. False when the client
// is nil or has no commerce URL (Enabled()==false), making the gate a no-op.
func billingEnabled(m *metering.Client) bool { return m != nil && m.Enabled() }

// DefaultPrice is cloud's per-request price (in cents) for the edge gate.
//
// It returns 0 — meaning "do not gate, do not charge" — for:
//   - health/liveness probes (every /v1/<svc>/health and bare /health),
//   - /v1/ai/* : the ai subsystem self-meters its own LLM token costs to
//     commerce; charging again here would DOUBLE-BILL,
//   - other subsystems that already self-meter their units (commerce billing
//     itself, o11y telemetry, mcp tool dispatch),
//
// and a non-zero flat price only on the generic agent/compute edge that has no
// finer-grained meter of its own. Tune per path as cloud grows its own metered
// surfaces; keep self-metering subsystems at 0 to preserve single-charge
// accounting.
func DefaultPrice(c *zip.Ctx) int64 {
	path := c.Path()

	// Liveness/health probes are never billed.
	if path == "/health" || path == "/healthz" || strings.HasSuffix(path, "/health") {
		return 0
	}

	// Subsystems that meter their own usage to commerce — never double-bill at
	// the edge. ai is the load-bearing case (LLM token costs); the rest either
	// bill their own units or are not billable here.
	for _, selfMetered := range selfMeteredPrefixes {
		if strings.HasPrefix(path, selfMetered) {
			return 0
		}
	}

	// Generic agent/compute edge with no finer meter: a flat per-request charge.
	if strings.HasPrefix(path, "/v1/agent/") || strings.HasPrefix(path, "/v1/agents/") {
		return cloudEdgePriceCents
	}

	// Default: do not charge unknown/unpriced paths. Metering is opt-in per
	// path so a new route never silently starts billing.
	return 0
}

// cloudEdgePriceCents is the flat charge for the generic agent/compute edge.
// Conservative single cent; revise alongside the pricing subsystem.
const cloudEdgePriceCents int64 = 1

// selfMeteredPrefixes are path prefixes whose subsystem records its own usage to
// commerce. The edge gate must return 0 for these to avoid double-billing.
var selfMeteredPrefixes = []string{
	"/v1/ai/",       // LLM token costs metered by the ai subsystem.
	"/v1/commerce/", // billing itself; not metered as usage.
	"/v1/o11y/",     // telemetry ingest; not user-billable here.
	"/v1/mcp/",      // tool dispatch meters per-tool downstream.
}
