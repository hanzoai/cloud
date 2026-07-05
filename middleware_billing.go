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
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/commerce/metering"
	"github.com/zap-proto/zip"
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
		// Gate on the actual request price, so the balance check is
		// available>=price (not merely >0) AND the per-scope spend cap is
		// measured against this request's cost — the anti-overshoot property the
		// resource meter already has.
		in.AmountCents = cents

		// Pre-request gate. AuthorizeVerdict encodes fail-open/closed internally
		// and returns the spend-cap verdict + soft-warn utilization in ONE round
		// trip.
		v, err := m.AuthorizeVerdict(c.Context(), in)
		if err != nil {
			// Balance unknown -> fail-closed -> 503.
			return denyUnavailable(c)
		}
		if !v.Allow {
			return denyVerdict(c, v, in)
		}
		// At/over a covering cap's soft threshold: signal the client but let the
		// request through (the request still succeeds).
		if v.WarnPct > 0 {
			c.SetHeader("X-Spend-Warn", strconv.Itoa(v.WarnPct))
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
			Project:     in.Project, // scope attribution → the per-scope cap sums over it.
			Service:     in.Service,
			RequestID:   c.RequestID(),
			Status:      "success",
			ClientIP:    clientIP(c),
		}
		go func() { _, _ = m.Record(context.Background(), usage) }()
		return nil
	}
}

// denyVerdict renders the edge gate's denial for a non-allow Verdict — the ONE
// place the edge maps a metering verdict to the frozen HTTP contract:
//
//	Reason "spend_cap"           -> 402 spend_cap_exceeded (+ scope/cap/spent detail).
//	Reason "insufficient_balance"-> 402 insufficient_balance.
//
// The spend_cap body carries the scope (project/service) and the cap/spent so the
// console can show the caller exactly which ceiling stopped them and how far over.
func denyVerdict(c *zip.Ctx, v metering.Verdict, in metering.AuthInput) error {
	if v.Reason == "spend_cap" {
		return c.JSON(402, map[string]any{
			"error": map[string]any{
				"code": "spend_cap_exceeded",
				"scope": map[string]string{
					"project": in.Project,
					"service": in.Service,
				},
				"capCents":   v.CapCents,
				"spentCents": v.SpentCents,
				"message":    "Spend cap reached for this scope. Raise it at console.hanzo.ai/limits",
			},
		})
	}
	return c.JSON(402, map[string]any{
		"error": map[string]string{
			"code":    "insufficient_balance",
			"message": "Add credits at console.hanzo.ai",
		},
	})
}

// denyUnavailable renders the fail-closed "balance unknown" shape (503), matching
// the metering middleware's net/http default so every Hanzo surface is identical.
func denyUnavailable(c *zip.Ctx) error {
	return c.JSON(503, map[string]any{
		"error": map[string]string{
			"code":    "balance_unavailable",
			"message": "Billing temporarily unavailable",
		},
	})
}

// serviceFromPath derives the SERVER-SIDE service label for a request from its
// path: the subsystem segment after /v1 (e.g. "/v1/ai/chat" -> "ai"). This is the
// scope's service axis — derived from the route, NEVER a client field, so a
// caller can never spoof another service's cap. Empty for non-/v1 paths.
func serviceFromPath(path string) string {
	p := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(p, "/", 3)
	if len(parts) >= 2 && parts[0] == "v1" {
		return parts[1]
	}
	return ""
}

// identityFromCtx builds the commerce billing identity from the gateway-minted
// headers zip exposes. It mirrors metering.IdentityFromGatewayHeaders exactly so
// cloud and every other product key the SAME ledger entry:
//
//   - User is the per-org billing key — the org slug alone (e.g. "maxpower"),
//     falling back to the bare sub only when org is absent. Prepaid balance is
//     per-org (one credit covers the whole org); keying it on "{org}/{sub}"
//     would query an empty per-user ledger and 402 a fully funded org.
//   - Org is the tenant namespace (X-Org-Id) commerce resolves the ledger in.
//
// The full "{org}/{sub}" actor identity belongs on the usage audit trail, not
// the gate — but metering v0.1.0's AuthInput/Usage carry no Actor field, so it
// is omitted here until the metering module ships the User/Actor split.
func identityFromCtx(c *zip.Ctx) metering.AuthInput {
	if !principal.Validated(c) {
		// No validated principal — never key billing on a restored, client-forged
		// X-Org-Id. Anonymous requests to priced paths are refused by each
		// subsystem's own principal gate; here they simply carry no billing org,
		// so an attacker cannot probe or drain a victim org's ledger.
		return metering.AuthInput{}
	}
	org := strings.TrimSpace(c.Org())
	sub := strings.TrimSpace(c.User())
	user := org // per-org billing key
	if user == "" {
		user = sub // org-less fallback
	}
	// Scope axes for the per-scope spend cap (issue #70), both SERVER-DERIVED:
	// project from the validated X-Project-Id (principal.Project narrows only the
	// caller's OWN org), service from the request path. Never a client field, so a
	// caller cannot target another scope's cap.
	return metering.AuthInput{
		User:    user,
		Org:     org,
		Project: principal.Project(c),
		Service: serviceFromPath(c.Path()),
	}
}

// clientIP extracts the originating IP from X-Forwarded-For (the gateway sets
// it); the left-most entry is the real client. Delegates to the exported
// ClientIP so the edge gate and the resource meter share ONE implementation.
func clientIP(c *zip.Ctx) string { return ClientIP(c) }

// billingEnabled reports whether the gate should enforce. False when the client
// is nil or has no commerce URL (Enabled()==false), making the gate a no-op.
func billingEnabled(m *metering.Client) bool { return m != nil && m.Enabled() }

// DefaultPrice is cloud's per-request price (in cents) for the edge gate.
//
// It returns 0 — meaning "do not gate, do not charge" — for:
//   - health/liveness probes (every /v1/<svc>/health and bare /health),
//   - /v1/ai/* : the ai subsystem self-meters its own LLM token costs to
//     commerce; charging again here would DOUBLE-BILL,
//   - /v1/agents/* : the canonical agents subsystem now gates + meters its OWN
//     per-run fee to commerce (clients/agents runAgent); the edge must stay 0 or
//     every agent run is billed twice,
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

	// Legacy singular /v1/agent/* edge (the bot reverse-proxy) has no finer
	// meter of its own: a flat per-request charge. The canonical plural
	// /v1/agents/* is self-metered (above) and never reaches here.
	if strings.HasPrefix(path, "/v1/agent/") {
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
// commerce via the shared cloud.ResourceMeter (in-handler Gate+Meter). The edge
// gate must return 0 for these to avoid double-billing — the subsystem owns the
// per-org charge (fail-closed pre-auth + debit-on-success), attributed to its own
// product label, so an additional flat edge charge would double-bill.
var selfMeteredPrefixes = []string{
	"/v1/ai/",        // LLM token costs metered by the ai subsystem.
	"/v1/agents/",    // per-run agent fee metered by the agents subsystem.
	"/v1/commerce/",  // billing itself; not metered as usage.
	"/v1/o11y/",      // telemetry ingest; not user-billable here.
	"/v1/mcp/",       // tool dispatch meters per-tool downstream.
	"/v1/functions/", // serverless invoke self-meters (product "functions").
	"/v1/s3/",        // object-storage data plane self-meters per-op (product "s3").
}
