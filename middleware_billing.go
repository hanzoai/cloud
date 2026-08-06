package cloud

// Billing gate — the ONE place the unified cloud binary enforces
// pay-for-everything on the request edge.
//
// It wraps the canonical metering client (github.com/hanzoai/cloud/apps/metering),
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

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
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
//
// IT COVERS THE ROUTES THAT ARE NOT OPERATIONS, and only those. A typed op is
// reachable over four transports and this seam sees one of them, so the op seam
// (Toll) owns it; an UNTYPED handler is reachable over HTTP and nowhere else, so
// this seam owns that. The two sets are disjoint by construction and the split is
// not a rule either of them keeps — Toll reads PriceOf on the REQUEST's own path
// and stands down when it names a declared surface, which is exactly when this
// gate has already answered.
func BillingGate(m *metering.Client, price func(method, path string) int64) zip.Handler {
	if !billingEnabled(m) || price == nil {
		// No-op passthrough — keeps the middleware chain uniform whether or
		// not billing is wired, so callers always Use() it unconditionally.
		return func(c *zip.Ctx) error { return c.Next() }
	}

	return func(c *zip.Ctx) error {
		cents := price(c.Method(), c.Path())

		// A request that is both free (price 0) and on a path we never gate
		// short-circuits. We still gate priced paths AND any path the operator
		// wants metered; DefaultPrice returns 0 for free/self-metered paths, so
		// price==0 here means "do not gate, do not charge".
		if cents <= 0 {
			return c.Next()
		}

		in := identity(c, c.Path())
		// Gate on the actual request price, so the balance check is
		// available>=price (not merely >0) AND the per-scope spend cap is
		// measured against this request's cost — the anti-overshoot property the
		// resource meter already has.
		in.AmountCents = cents

		// From here on this gate OWNS this request's money, so say so before the
		// chain runs — the op seam runs INSIDE c.Next() and has to know not to
		// answer a second time. It is written here rather than at the top because
		// everything above this line is a decision NOT to charge, and a request
		// this gate waved through is one the op seam must still weigh: over MCP
		// and over the plane the path read above is an envelope, not the operation.
		answer(c)

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
			// CORRELATION, never the money key. This is the inbound X-Request-Id when
			// the client sent one — zip propagates it verbatim and the gateway
			// CORS-allows it from a browser — so a caller pinning it used to make every
			// call after the first dedup into the first one's debit: free inference and
			// a spend cap that never moved. The debit's key is minted inside the meter
			// (metering.Usage.Seal), one per act, out of the caller's reach.
			RequestID: c.RequestID(),
			Status:    "success",
			ClientIP:  clientIP(c),
		}
		// Contained: this fires on EVERY billable request, so it is the highest-
		// frequency spawn in the binary. It is also fire-and-forget — nothing reads
		// its result — which means without containment a single panic inside Record
		// (a nil meter, a store fault, a bad usage row) would kill the process for
		// every tenant, on a path whose whole point is that the caller does not wait
		// for it. The request logger names the org, so the line is actionable.
		Go(c.Log(), "billing.record", []any{"service", in.Service, "request_id", c.RequestID()}, func() {
			_, _ = m.Record(context.Background(), usage)
		})
		return nil
	}
}

// ── one operation, one answer ────────────────────────────────────────────────────

// answeredKey names the request-scoped slot this gate parks its claim in.
// Unexported zero-size type, so nothing outside the package can forge one.
type answeredKey struct{}

// answer records that this gate has decided the money question for this request,
// and answered reports it. They are the seam between the two places money is
// gated — here, over the transport, and at op.invoke (toll.go), over the
// operation — and they exist so an operation reached over REST is charged ONCE.
//
// IT IS A FACT, NOT AN INFERENCE, and that distinction is the whole reason it is
// written down. The first version of this had the op seam infer it: "if the
// request's own path names a declared surface then the edge must have answered".
// That reads true and is true — right up until somebody unmounts BillingGate, at
// which point the inference still says yes, the op seam still stands down, and
// nothing charges anything. An invariant spread across two lines of a composition
// root is an invariant nobody is keeping. So the gate that answers says that it
// answered, and the gate that would answer second reads it. Unmount this one and
// the claim stops being made, which is exactly what makes the other one take over.
func answer(c *zip.Ctx) {
	c.SetContext(context.WithValue(c.Context(), answeredKey{}, true))
}

func answered(ctx context.Context) bool {
	claimed, _ := ctx.Value(answeredKey{}).(bool)
	return claimed
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
//
// THE ROUTE IS WHAT THE ROUTER MATCHED, not how the client spelled it. fiber
// routes case-insensitively and ignores a trailing slash, so "/V1/AI/chat" reaches
// exactly the same handler as "/v1/ai/chat" — and read raw, it produced the
// service label "AI", which is a DIFFERENT scope key: a different rate bucket and
// a different spend-cap axis, reachable by holding down the shift key. The label
// is derived from cloud.RoutePath for the same reason the grant list is compared
// against it: a scope key must name the route, and the route is what the router
// says it is.
func canonicalService(path string) string {
	p := strings.TrimPrefix(RoutePath(path), "/")
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

// identity builds the commerce billing identity from the gateway-minted headers
// zip exposes, for the operation served at path.
//
// PATH IS AN ARGUMENT because the request's path and the OPERATION's path are the
// same string only over plain REST. Over MCP the request is POST /mcp and over the
// ZAP plane it is POST /.well-known/zip/op/<name>, while the operation inside is
// /v1/<surface>/<verb> — so a service label derived from the request would file a
// tools/call under no scope at all, and a per-scope spend cap would never bind to
// it. The MONEY (who pays) comes off the request, because that is where identity
// is; the SCOPE (what was done) comes off the operation. One builder, so the edge
// and the op seam can never key two different ledger entries for one call.
//
// It agrees with metering.IdentityFromGatewayHeaders because both call the SAME
// rule (hanzoai/account.Payer), not because two copies are kept in step — so cloud
// and every other product key the SAME ledger entry:
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
func identity(c *zip.Ctx, path string) metering.AuthInput {
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
		Service:          canonicalService(path),
	}
}

// clientIP extracts the originating IP from X-Forwarded-For (the gateway sets
// it); the left-most entry is the real client. Delegates to the exported
// ClientIP so the edge gate and the resource meter share ONE implementation.
func clientIP(c *zip.Ctx) string { return ClientIP(c) }

// billingEnabled reports whether the gate should enforce. False when the client
// is nil or has no commerce URL (Enabled()==false), making the gate a no-op.
func billingEnabled(m *metering.Client) bool { return m != nil && m.Enabled() }

// DefaultPrice moved to price.go when it stopped taking a request. price.go is
// where a surface's cost is COMPUTED and this file is where it is APPLIED — the
// same split spend.go and middleware_spend.go already keep — and the function
// belonged on the computing side the moment its arguments became two strings.
