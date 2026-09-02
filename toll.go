package cloud

// toll.go — the money gate at op.invoke, which is the one point EVERY way of
// reaching an operation funnels through.
//
// THE HOLE THIS CLOSES, MEASURED. Money was gated by HTTP middleware, and HTTP
// middleware reads the path the TRANSPORT carried. For a plain REST call that path
// IS the operation, so the gate looked right and tested green. It is not the
// operation anywhere else. One typed op, `probe_run`, registered once at
// /v1/probe/run, reached four ways, is seen by the edge as four different things:
//
//	REST          POST /v1/probe/run              <- the operation
//	MCP           POST /mcp                       <- an envelope
//	ZAP plane     POST /.well-known/zip/op/probe_run  <- an envelope
//	CLI           (no request at all)             <- nothing to read
//
// PriceOf("/mcp") is Undeclared and Billable("/.well-known/...") is false, so the
// two envelopes priced at zero and required no standing — not by a decision anyone
// made, but because the gate was asking about the wrong value. Every priced
// operation reached over MCP or over the plane was free, silently, for as long as
// those paths have existed. Free never errors, so nothing anywhere said so.
//
// That was survivable while everything spoke HTTP. It stops being survivable the
// moment internal hops move to ZAP over UDS: each converted call site is a hop
// that leaves the only surface where the gate exists, and the leak grows with the
// migration rather than with traffic.
//
// THE FIX IS TO ASK ABOUT THE OPERATION. zip hands every projection of a typed
// handler — REST, MCP tools/call, the by-name call plane, CLI LocalInvoke — through
// ONE dispatcher, op.invoke, and offers ONE hook inside it: App.Authorize. Its
// argument is a zip.Op, which is exactly {Method, Path, OperationID} — the operation
// as a value, identical on all four paths (measured; see toll_test.go). So the same
// two rules the edge always used, DefaultPrice and Billable, are asked here about
// op.Method and op.Path and answer the same thing however the call arrived.
//
// ONE OPERATION, ONE ANSWER. The edge gates stay, because an UNTYPED handler is not
// an operation: it has no registry entry, so it is invisible to MCP, to the plane
// and to the CLI, and HTTP is the only way to reach it. Untyped routes are the
// edge's, typed ops are this client's, and the only request both can see is a typed op
// reached over REST. That one is settled by BillingGate SAYING it took the request
// (middleware_billing.go's answer/answered) rather than by this client guessing that
// it must have — a guess reads true and stays true right up until somebody unmounts
// the edge gate, at which point both clients stand down and nothing charges anything.
//
// WHAT IS AVAILABLE ON EVERY PATH, AND WHAT IS NOT. The OPERATION is: zip.Op is
// the same value four times. The PAYER is not, and pretending otherwise would be
// the same mistake one layer up. principal.WalletOf resolves an address from six
// gateway-minted headers, one of which (X-Billing-Account-Id, IAM's signed
// billing_account claim) is cloud's and not in zip's forwarded identity set — so it
// is readable only where a request exists. It exists on three of the four paths:
// REST, MCP-over-HTTP and the ZAP plane all terminate in fasthttp, so Bridge has
// run and cloud.Request answers. An in-process CLI invoke has no request, no
// bearer, and therefore no signed claim about who pays. That is not a gap to paper
// over — there is genuinely nobody to charge — so a priced operation reached that
// way is REFUSED (denial maps ErrNoLedger to the tenant gate's own 403), and a free
// one runs as it always has. Refusing is the fail-secure reading and it is also the
// honest one: the alternative is serving paid work to a principal that does not
// exist.
//
// WHEN THE DEBIT LANDS. The edge authorizes before the handler and records after
// it, so it can decline to bill work that failed. That property belongs to a
// WRAPPER, and op.invoke offers a decision, not a wrapper: there is no post-invoke
// hook in zip, and inventing a second client to get one would put the money in two
// places again. So on the three paths this gate owns, the charge is levied at
// ADMISSION — check standing, then debit, then run. The trade is stated rather than
// discovered: a failed operation is billed here, which is loud, complained about
// and refundable; the status quo is that a SUCCESSFUL one is not billed at all,
// which is silent and permanent. It is also the tighter of the two against
// concurrency — reserve.go's opening paragraph is about the window between the edge
// gate's read and its debit, and at admission that window is two statements wide
// instead of one whole handler.

import (
	"context"

	"github.com/hanzoai/cloud/apps/metering"
	"github.com/zap-proto/zip"
)

// Toll returns the op-invoke authorizer: the money question, asked once about the
// OPERATION, whatever transport reached it.
//
// It composes the two orthogonal legs in the order the edge composes them, and it
// asks each through the SAME function the edge asks — standing (may this principal
// spend at all) then charge (does this operation cost). Neither rule lives here;
// this is where they are applied to op.Method and op.Path.
//
// Install it once, at the composition root, before Build/Listen:
//
//	app.Authorize(Toll(deps.Metering, deps.Commerce))
//
// A nil or unconfigured metering client leaves the charge leg inert exactly as it
// leaves BillingGate inert, so an unwired deployment is never blocked.
func Toll(m *metering.Client, commerce CommerceClient) zip.Authorizer {
	plans, _ := commerce.(PlanChecker)
	allow, _ := commerce.(AllowanceChecker)
	charging := billingEnabled(m)
	return func(ctx context.Context, op zip.Op, _ any) error {
		if answered(ctx) {
			// BillingGate said it owns this request's money — it is upstream of
			// here, it read the same rule off the same two values, and over plain
			// REST those values ARE this operation. One operation, one answer. It
			// claims nothing for a request it waved through, and nothing at all for
			// an envelope, which is how those reach the lines below.
			return nil
		}
		c, live := Request(ctx)

		// ── standing ────────────────────────────────────────────────────────────
		// The whole SpendGate ladder, from the one function that holds it. Dark by
		// default: one atomic load and out, so an op pays nothing for a gate the
		// operator has not armed.
		if reason := standing(c, op.Method, op.Path, plans, allow); reason != "" {
			return Denied(refusal(reason))
		}

		// ── the charge ──────────────────────────────────────────────────────────
		cents := DefaultPrice(op.Method, op.Path)
		if cents <= 0 || !charging {
			return nil
		}
		if !live {
			// Priced work, and no request behind the call — so no attested
			// principal, no signed billing_account claim, and no wallet. There is
			// nobody to charge, and serving it anyway is the leak this file exists
			// to close. ErrNoLedger is the fleet's word for it and denial renders
			// it as the tenant gate's 403, not as a money fault of the biller.
			return Denied(ErrNoLedger)
		}

		in := identity(c, op.Path)
		in.AmountCents = cents
		v, err := m.AuthorizeVerdict(ctx, in)
		if err != nil {
			return Denied(err) // balance unknown -> fail-closed -> 503.
		}
		if !v.Allow {
			return Denied(verdict(v))
		}

		// The debit, on the SAME goroutine and the SAME context that authorized it.
		// The edge detaches its Record because a client disconnect must not cancel a
		// debit for work already delivered; here the work has not run yet, so there
		// is nothing delivered to protect and every reason to know the money moved
		// before the handler starts. A debit that fails is a refusal: charging
		// nothing and serving anyway is exactly the hole above.
		u := metering.Usage{
			User:        in.User,
			Org:         in.Org,
			Currency:    in.Currency,
			AmountCents: cents,
			Provider:    meteringProvider,
			Project:     in.Project,
			Service:     in.Service,
			RequestID:   c.RequestID(),
			Status:      "success",
			ClientIP:    ClientIP(c),
		}
		if _, err := m.Record(ctx, u); err != nil {
			return Denied(err)
		}
		return nil
	}
}

// refusal turns a standing reason into the error the money wire classifies. The
// reasons are spelled once, in middleware_spend.go, and this is the only place they
// cross into the error channel a typed op refuses through — so the sentence a caller
// reads is the same sentence SpendGate's body carries, and neither is a second
// vocabulary for the other.
func refusal(reason string) error {
	if reason == ReasonUnresolved {
		return zip.Errorf(402, "billing could not be verified; retry shortly")
	}
	return zip.Errorf(402, "no active subscription and no prepaid credit")
}

// verdict turns a non-allow metering Verdict into the same error the edge renders
// as a status. It maps to the errors errmap.go already classifies, so the op client
// and the edge answer ONE refusal identically: a per-scope cap is never reshaped
// into out-of-funds, which would send a caller to top up against a ceiling that
// will not clear until the period rolls over.
func verdict(v metering.Verdict) error {
	if v.Reason == "spend_cap" {
		return metering.ErrSpendCapExceeded
	}
	return metering.ErrInsufficientBalance
}
