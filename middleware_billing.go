package cloud

// Billing gate — the ONE place the unified cloud binary enforces
// pay-for-everything on the request edge.
//
// It wraps the canonical metering client (github.com/hanzoai/cloud/clients/metering),
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

	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
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

// serviceAliases maps a /v1/<seg> path segment to the CANONICAL service label
// when a subsystem meters under a provider label that differs from its path
// segment — so the edge gate (canonicalService) and the resource meter (its
// NewResourceMeter provider) emit the SAME service axis and a per-scope cap binds
// on both surfaces (issue #70 INFO-7). This map is the ONE source of truth; a new
// subsystem whose provider != path segment adds itself here. Keep in lockstep with
// the NewResourceMeter(deps, "<provider>") calls.
var serviceAliases = map[string]string{
	"ml":       "compute",       // clients/ml    NewResourceMeter(deps, "compute")
	"visor":    "compute",       // clients/visor NewResourceMeter(deps, "compute")
	"agents":   "agent",         // clients/agents provider "agent"
	"security": "security.scan", // clients/security provider "security.scan"
}

// canonicalService derives the SERVER-SIDE service label for a request from its
// route: the subsystem segment after /v1 (e.g. "/v1/ai/chat" -> "ai"), mapped
// through serviceAliases to the canonical provider label. It is the scope's
// service axis — from the route, NEVER a client field, so a caller can never spoof
// another service's cap. Empty for non-/v1 paths.
func canonicalService(path string) string {
	p := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(p, "/", 3)
	if len(parts) < 2 || parts[0] != "v1" {
		return ""
	}
	seg := parts[1]
	if alias, ok := serviceAliases[seg]; ok {
		return alias
	}
	return seg
}

// identityFromCtx builds the commerce billing identity from the gateway-minted
// headers zip exposes. It agrees with metering.IdentityFromGatewayHeaders because
// both call the SAME rule (hanzoai/account.Payer), not because two copies are kept
// in step — so cloud and every other product key the SAME ledger entry:
//
//   - User is the ACCOUNT that pays and Org the HOME org whose ledger holds it.
//     Together they are the money's ADDRESS, resolved ONCE by principal.WalletOf:
//     home names the ledger (so a masquerading SuperAdmin debits their OWN books,
//     never the org acted on) and account.Payer names the wallet within it. This
//     used to be `user := org` — the pool, always — on the premise that prepaid
//     billing is per-org; that premise is false for a person in the shared signup
//     org, so the gate checked a pool balance while ai debited the person's. The
//     address lives in one place now precisely so the two cannot drift again.
//   - An unvalidated principal resolves to no wallet, and the empty AuthInput
//     carries no billing org — so an anonymous, forged X-Org-Id can neither probe
//     nor drain a victim org's ledger.
//
// The full "{org}/{sub}" actor identity belongs on the usage audit trail, not
// the gate — but metering v0.1.0's AuthInput/Usage carry no Actor field, so it
// is omitted here until the metering module ships the User/Actor split.
func identityFromCtx(c *zip.Ctx) metering.AuthInput {
	w, ok := principal.WalletOf(c)
	if !ok {
		return metering.AuthInput{}
	}
	// Scope axes for the per-scope spend cap (issue #70). Service is SERVER-DERIVED
	// from the route (canonicalService), so it cannot be spoofed. Project is the
	// caller's X-Project-Id; ValidatedProject reports whether it is claim-bound —
	// when it is not (today), commerce degrades a project-scoped hard cap to soft,
	// so a forgeable project can neither hard-stop nor be evaded.
	project, projectValidated := principal.ValidatedProject(c)
	return metering.AuthInput{
		User:             w.Account,
		Org:              w.Ledger, // balance check + debit → the HOME org's ledger (who pays)
		Project:          project,
		ProjectValidated: projectValidated,
		Service:          canonicalService(c.Path()),
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
//
//   - health/liveness probes (every /v1/<svc>/health and bare /health),
//
//   - /v1/ai/* : the ai subsystem self-meters its own LLM token costs to
//     commerce; charging again here would DOUBLE-BILL,
//
//   - /v1/agents/* : the canonical agents subsystem now gates + meters its OWN
//     per-run fee to commerce (clients/agents runAgent); the edge must stay 0 or
//     every agent run is billed twice,
//
//   - other subsystems that already self-meter their units (commerce billing
//     itself, o11y telemetry, mcp tool dispatch),
//
//   - /v1/agent* : the agent orchestrator self-meters, billing through the
//     completion it runs in-process, so an edge charge would double-bill.
//
// Every billable surface meters its own units downstream, so the edge charges
// nothing on its own; it stays wired as a uniform passthrough. Keep self-metering
// subsystems at 0 to preserve single-charge accounting.
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

	// The agent orchestrator owns /v1/agent (the POST round) and /v1/agent/* (the
	// preset and conversation reads). It self-meters: the reads are free, and the
	// round bills through the /v1/chat/completions it runs in-process (gated and
	// metered downstream), so the edge stays 0 or a round is billed twice.
	if path == "/v1/agent" || strings.HasPrefix(path, "/v1/agent/") {
		return 0
	}

	// Default: do not charge unknown/unpriced paths. Metering is opt-in per
	// path so a new route never silently starts billing.
	return 0
}

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
