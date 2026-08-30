package cloud

// payer.go — who a debit lands on, resolved ONCE.
//
// A metered handler needs six facts about its caller before it can charge them,
// and every one of them is read off the request: the wallet to debit, the project
// a per-scope cap sums over and whether that project is claim-bound, plus the
// actor, request id and client address a ledger row is attributed with. Read
// separately at the gate and again at the debit, the two halves can describe
// different callers; read separately in each app, they can pick different
// accessors. Both of those have happened.
//
// THE ACCESSOR IS THE POINT. Money is addressed by [principal.Payer] — the WALLET
// — and not by [principal.Ledger], which is the ORG. For a tenant org the two are
// the same string, so nothing looks wrong anywhere it is tested; in the shared
// signup org they are not, and that is exactly where a self-serve stranger lives.
// There, Ledger debits the bare org — the platform's own pool — while the balance
// the customer topped up sits under <org>/<username>, unspendable. Choosing
// between the two is a decision no metered surface should be making for itself,
// so it is made here and nowhere else.

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// Payer is the party a charge is billed to, with everything the ledger row
// carries about them.
//
// A ZERO Wallet means there is nobody to bill, which is NOT a refusal: an
// in-process composer and a shared-service-key caller both arrive that way by
// design, and neither is gated nor debited. It is total rather than partial for
// the reason principal.Payer is — a caller hands the empty value straight to the
// gate and gets the gate's own fail-closed refusal (ErrNoLedger), rather than
// writing a second branch of its own that could disagree with it.
type Payer struct {
	// Wallet is the money's ADDRESS — both halves, never one string standing in for
	// both. Org() names the books, Subject() the account within them.
	Wallet account.Account
	// Project is the caller's org sub-scope, and Validated says it is bound to a
	// verified claim. Together they are what lets a project-scoped spend cap
	// enforce hard rather than degrade — see [Meter.Authorize].
	Project   string
	Validated bool
	// Attribution, carried so the gate and the debit describe the same caller.
	Actor     string
	RequestID string
	ClientIP  string
}

// Billable reports that there is somebody to charge. A surface asks this before
// gating so a caller with no wallet is neither refused nor billed.
func (p Payer) Billable() bool { return !p.Wallet.Zero() }

// payerKey names the slot a RESOLVED payer crosses a detach in. Unexported
// zero-size type, so only this package can mint or read one.
type payerKey struct{}

// PayerOf resolves the payer from whichever side of the client this call arrived on.
//
// TWO PATHS, ONE ANSWER, and that is the whole point. A request-bearing call reads
// the request; a call with no request at all — the ZAP plane, MCP's tools/call, the
// fleet-agent path — reads the caller the identity boundary stated on the context.
// Both resolve through principal's own rules (Payer / PayerFrom), so the two paths
// cannot disagree about who pays.
//
// IT USED TO READ ONLY THE FIRST, and that was a free pass. Off the HTTP path the
// wallet came back empty, empty means "nobody to bill", and every meter correctly
// skips both the gate and the debit for that — so an identified caller reaching a
// paid operation over the agent plane spent the platform's money unbilled and
// unrecorded. The caller was never anonymous there; only the transport was
// different. Money must not be a property of the transport.
//
// The zero Payer now means what it says: no attested caller anywhere on this
// context — an in-process composer, a background job, a CLI invocation. That is
// the only case a metered surface waves through.
func PayerOf(ctx context.Context) Payer {
	// A payer RESOLVED while the request still existed wins over re-deriving one,
	// and it has to: the signed billing_account claim rides an HTTP header, and
	// there is no field for it on a stated caller. Re-deriving across a detach
	// therefore silently skipped the rule where a claim naming an account inside
	// the caller's own org wins (account.Payer), so one request answered "acme/bob"
	// on the direct path and "acme" — the org pool — on the streamed one. The gate
	// read one balance and the debit landed on the other. See [Detach].
	if p, ok := ctx.Value(payerKey{}).(Payer); ok {
		return p
	}
	c, ok := Request(ctx)
	if !ok {
		project, validated := principal.ValidatedProjectFrom(ctx)
		return Payer{
			Wallet:    principal.PayerFrom(ctx),
			Project:   project,
			Validated: validated,
			Actor:     zip.CallerOf(ctx).User,
			RequestID: zip.CallerOf(ctx).RequestID,
			ClientIP:  zip.CallerOf(ctx).IP,
		}
	}
	project, validated := principal.ValidatedProject(c)
	return Payer{
		Wallet:    principal.Payer(c),
		Project:   project,
		Validated: validated,
		Actor:     c.User(),
		RequestID: c.RequestID(),
		ClientIP:  ClientIP(c),
	}
}

// Reserve authorizes costCents against this payer and returns the CHARGE it opened.
//
// It is [Meter.Authorize] asked with a resolved Payer instead of three loose
// strings, plus the two things every caller of Authorize has to get right and one
// of them could not get right at all:
//
//   - An absent payer is "no charge", not "refuse". Authorize is fail-closed on an
//     empty subject, which is correct when a wallet was expected and wrong when
//     the caller is an in-process composer that was never going to pay. Leaving
//     that to each surface is how one of them eventually 402s a background job.
//   - A BALANCE IS NOT A PER-CALL LIMIT. Authorize reads the ledger, and the ledger
//     does not yet know about the calls this pod already has in flight — so N
//     simultaneous callers each saw the same untouched balance and each cleared
//     it. Four numbers were ordered against a balance covering one. The fix is
//     not a smaller window; a window cannot be small enough. This COMMITS the
//     cost before it weighs it, so the figure the balance must cover already
//     includes every other call in flight and the second caller must clear both.
//     Same mechanism the inference plane has used since reserve.go was written.
//
// The commitment is held until the Charge is settled or released, so a caller
// must always do one of the two — `defer ch.Release()` is the safety net, and it
// is a no-op once Debit has taken it. A charge that is never released is a
// customer locked out of their own balance.
func (rm *Meter) Reserve(ctx context.Context, p Payer, kind string, costCents int64) (*Charge, error) {
	if rm == nil || !p.Billable() || costCents <= 0 {
		return &Charge{}, nil
	}
	h := &hold{to: &rm.inflight, org: p.Wallet.Subject(), cost: costCents}
	// GIVE IT BACK ON EVERY EXIT THIS FUNCTION HAS, including the one it does not
	// write down. Releasing only on the error return leaks the commitment when Authorize
	// panics — and a leaked commitment is not a lost cent, it is a wallet that can
	// never clear the gate again until the pod restarts, because the phantom is
	// added to every later weigh-in. Handing the hold to the returned Charge is the
	// only exit that keeps it.
	kept := false
	defer func() {
		if !kept {
			h.release()
		}
	}()
	// Commit FIRST, then weigh — see above. committed is this call's cost plus
	// everything else outstanding for the same wallet.
	committed := rm.inflight.commit(p.Wallet.Subject(), costCents)
	if err := rm.Authorize(ctx, p.Wallet, p.Project, p.Validated, kind, committed); err != nil {
		return &Charge{}, err
	}
	kept = true
	ch := &Charge{meter: rm, payer: p, kind: kind}
	ch.hold.Store(h)
	return ch, nil
}

// Charge is one AUTHORIZED act: the amount held against a payer's balance until
// the work either happens (Debit) or does not (Release).
//
// The zero Charge is the un-billed case — no payer, or a kind priced at zero —
// and both of its methods are no-ops on it, so a surface never branches on
// whether money is in play. It only has to say what happened.
type Charge struct {
	meter *Meter
	payer Payer
	kind  string
	// hold is taken by whichever of Debit and Release runs first, atomically, so
	// exactly one of them owns it. It is a plain field's worth of state guarded the
	// only way a value with a `defer x.Release()` contract can be: every call site
	// today settles and releases on one goroutine, but a Charge is a handle a caller
	// holds, and a handle that invites a defer invites being passed somewhere.
	hold atomic.Pointer[hold]
}

// Debit settles the charge: it records what was ACTUALLY spent and gives back the
// whole commitment once the ledger has it.
//
// The recorded amount is the caller's, not the held one, because they legitimately
// differ: a search authorizes every paid engine it is about to ask and bills only
// the ones that answered. The hold is released when the debit LANDS rather than
// when this returns — until then the money is spent but not yet visible, which is
// the exact window the commitment exists to cover.
//
// usage supplies the amount and the per-surface fields (Model, token counts);
// Project and the request identity come from the Payer, so a surface cannot bill
// one caller and attribute the row to another.
//
// IT TAKES THE HOLD BEFORE IT HANDS IT OFF, and that one line is the difference
// between the bound holding and merely looking like it does. Every caller also
// writes `defer ch.Release()`, correctly, as the safety net for the paths that
// never settle — and hold.release is once-only, so whichever call arrives first
// wins. Handing the SHARED hold to the recording goroutine meant the deferred
// Release almost always won: it runs when the handler returns, while the debit is
// still crossing to the ledger. The commitment came back before the money was
// visible, which is exactly the window it exists to cover, and a caller pipelining
// the instant the response landed got one extra act per window per wallet per pod.
// Clearing c.hold here makes the later Release a true no-op and leaves the
// goroutine the sole owner.
func (c *Charge) Debit(usage metering.Usage) {
	if c == nil || c.meter == nil {
		return
	}
	h := c.hold.Swap(nil)
	usage.Project = c.payer.Project
	usage.Actor = c.payer.Actor
	usage.RequestID = c.payer.RequestID
	usage.ClientIP = c.payer.ClientIP
	c.meter.record(c.payer.Wallet, c.kind, usage, h.release)
}

// Release gives back the commitment without spending it — the work did not
// happen. Idempotent, safe on the zero Charge, and a no-op once Debit has taken
// it, so `defer ch.Release()` is always correct.
func (c *Charge) Release() {
	if c == nil {
		return
	}
	c.hold.Swap(nil).release()
}

// Detach carries the caller onto a context that OUTLIVES the request.
//
// A streamed response is written from a callback that runs after the handler has
// returned and the *zip.Ctx has been recycled, so the work inside it must run on a
// context built from context.Background(). That detach is correct — the alternative
// is reading a recycled request — but it drops the identity, and every metered
// thing the callback then reaches resolves no payer and charges nobody. The result
// was a surface that billed one way and not the other for the same request, which
// is money as a property of the transport: `stream:true` bought the same searches
// and page renders for free that `stream:false` paid for.
//
// It states the caller rather than parking a resolved Payer, because the payer is
// not the only thing downstream reads: the org scopes an archive, the project keys
// a store, and a cap sums over both. One value carries all of them, and every
// existing reader — principal.OrgFrom, ProjectFrom, PayerFrom — already knows how
// to find it. See [PayerOf].
//
// It is not a laundering hole: zip reads a STATED caller only where there is no
// request, so this can never override an authenticated one. Here there is no
// request by construction — that is the whole reason it is needed.
func Detach(ctx context.Context, c *zip.Ctx) context.Context {
	if c == nil {
		return ctx
	}
	// BOTH, and they answer different questions. The stated caller carries the org,
	// the project and the actor, which scope archives, key stores and caps — every
	// reader downstream already knows how to find it there. The resolved payer
	// carries WHO PAYS, and it is parked rather than re-derived because the wallet
	// rule reads a signed claim off an HTTP header that a stated caller has no field
	// for. Resolving it here, while the request is still real, is what makes the two
	// paths agree on the wallet rather than merely on the org.
	ctx = context.WithValue(ctx, payerKey{}, PayerOf(c.Context()))
	return zip.WithCaller(ctx, zip.Caller{
		Org:       strings.Clone(c.Org()),
		Project:   strings.Clone(c.Project()),
		User:      strings.Clone(c.User()),
		Name:      strings.Clone(c.UserName()),
		Email:     strings.Clone(c.UserEmail()),
		Owner:     strings.Clone(principal.Owner(c)),
		Admin:     c.IsAdmin(),
		OrgAdmin:  c.IsOrgAdmin(),
		RequestID: strings.Clone(c.RequestID()),
		IP:        strings.Clone(ClientIP(c)),
	})
}
