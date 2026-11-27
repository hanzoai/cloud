package x402

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/zap-proto/zip"
)

// rpc.go — the payment rail, published for the processes that do not contain it.
//
// The tool plane prices a TOOL, and every tool call arrives on the one
// /v1/tool/call route with the name in the body. In one process that is
// x402.Settle, a function call. In the fleet — one binary per app — the tools
// process has no rail at all, which is why every priced dispatch answered a
// permanent 402 with no terms in it: a refusal no client could ever satisfy.
//
// So the rail answers here, over the internal plane, and it is the SAME flow: the
// op is a third caller of run, beside Enforce and Settle, and adds no policy of
// its own. What crosses the wire is exactly what the flow needs and cannot derive:
// the resource, the payer, and the client's proof.
//
// THE PAYER RIDES THE CAPABILITY. The edge that holds the request delegates the
// caller with cloud.As, naming the books it pays from (principal.Payer's org, which
// folds in the SuperAdmin masquerade), and every op here resolves the account from
// that caller ([payerAt]) — the books, and the wallet within them, which in the
// signup org is the person and never the platform's pool. A held authorization
// pays from the wallet its hold set the amount aside in ([rail.payerFor]). It is
// not a field, because a caller that could name the payer could spend another
// tenant's ledger.
//
// THE OUTCOME IS DATA, not a transport error. A 402 carries the terms the client
// must read to pay, and an error body has no room for them — so the op answers 200
// with the refusal inside it, exactly as the prepaid gate answers with a Verdict.
// A transport error therefore means something else entirely: the rail did not
// answer. Callers fail closed on that, never free.

// expose publishes the rail: the settlement, and what one completed settlement
// records, for the event plane to state (economic.go). What it settled is read back
// from the event plane, never from here.
func expose(s *cloud.Service[state]) {
	cloud.Plane().Post("/x402/settle", rail{s}.planeSettle,
		zip.WithOperationID(client.X402Settle),
		zip.WithSummary("Settle payment for one priced resource"))
	cloud.Plane().Post("/x402/economic", rail{s}.planeEconomic,
		zip.WithOperationID(client.X402Economic),
		zip.WithSummary("What one completed settlement records, as an economic event"))
	cloud.Plane().Post("/x402/authorize", rail{s}.planeAuthorize,
		zip.WithOperationID(client.X402Authorize),
		zip.WithSummary("Check one payment authorization without settling it"))
	cloud.Plane().Post("/x402/void", rail{s}.planeVoid,
		zip.WithOperationID(client.X402Void),
		zip.WithSummary("Give up one held authorization, returning its amount"))
}

// payerAt is the account the caller of a plane op pays from. A caller the edge
// delegated carries the person, and principal.PayerFrom resolves the books and the
// wallet within them. A caller a background job states names an org and no person
// — a job released or lapsed when its review window closes — and pays from that
// org's own account by the one rule (account.Payer), which names nobody in the
// signup org: there only a person pays, and a held authorization says which.
func payerAt(ctx context.Context) account.Account {
	if p := meter.PayerFrom(ctx); !p.Zero() {
		return p
	}
	if org, ok := cloud.Tenant(ctx); ok {
		return account.Payer(account.Credential{Owner: org})
	}
	return account.Account{}
}

// payerFor is the account a plane op on payment pays from, and false when the
// caller may not pay it.
//
// A HELD AUTHORIZATION PAYS FROM THE WALLET ITS HOLD SET THE AMOUNT ASIDE IN. The
// buyer agreed it signed in, so the hold knows the person; the release that pays it
// is stated later by a job for the buyer's org alone, and re-deriving a payer there
// would draw the org's own account — in the signup org the platform's pool — while
// the person's wallet kept the amount set aside. So the hold's account answers, for
// a caller stated in its books, or for the very account that held it; any other
// caller is refused. A hold written before it recorded an account was set aside in
// the org's own wallet of the caller that asked for it, and is drawn and returned
// there.
func (r rail) payerFor(ctx context.Context, payment string) (account.Account, bool, error) {
	caller := payerAt(ctx)
	pay, err := protocol.ParsePayment(payment)
	if err != nil {
		return caller, true, nil // nothing held under a payment that does not parse
	}
	a := pay.Payload.Authorization
	h, ok, err := r.s.State.store.held(ctx, settlementID(a.From, a.Nonce))
	if err != nil {
		return account.Account{}, false, err
	}
	if !ok {
		return caller, true, nil
	}
	org, _ := cloud.Tenant(ctx)
	held := h.payer()
	switch {
	case held.Zero():
		return account.Org(org), org != "", nil
	case caller.Zero():
		return held, org == held.Org(), nil
	}
	return held, caller == held, nil
}

// rail binds the subsystem to its plane handler. A TypedHandler has no parameter for
// the service, so it arrives as a RECEIVER and the op is a method value — the only
// bound form zipdoc can lift prose from.
type rail struct{ s *cloud.Service[state] }

// Settle enforces payment for one priced resource on behalf of the calling tenant,
// and answers what happened.
//
// FREE FIRST: the price table is asked before anything is required of the world,
// because the tool plane offers EVERY dispatch to this client and an unpriced call must
// not need a payer, a proof or a wallet. A price that cannot be looked up is NOT
// free — that is a refusal, and the caller must serve nothing.
//
// PAID: the client's signed authorization arrives on the request as proof, the
// signature is verified against exactly the terms it was challenged with, and the
// settlement runs ONCE — keyed on keccak(payer address | nonce), so a retried
// authorization moves money at most once and answers with the same receipt.
//
// UNPAID: the challenge comes back as data, not as an error. A 402 carries the
// amount, the payee address and the chain the client must sign over, and an error
// body has no room for them — so the caller reads the refusal off the reply and puts
// the challenge on the response it is writing.
//
// The PAYER is the caller's own account, resolved from the caller delegated on the
// call. It is not a field: a caller that could name the payer could spend another
// tenant's ledger.
func (r rail) planeSettle(ctx context.Context, in *client.SettleIn) (*client.Settled, error) {
	// CLONED, because this outlives the call. ZAP decodes the request against the
	// server's body buffer, which fasthttp recycles, and the resource is written to
	// the settlement row and to the audit record — the same aliasing that had a payee
	// address turn into the bytes of a later message on the reply side (cloud.detach).
	// It holds today only because both writes happen before this returns; the first
	// async audit or metering sink makes that timing a bug, and a copy makes it a
	// property.
	resource := strings.Clone(in.Resource)
	if resource == "" {
		return nil, zip.ErrBadRequest("settle: no resource")
	}
	terms, priced, err := priceOf(ctx, resource)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "settle: price %s: %v", resource, err)
	}
	if !priced {
		return &client.Settled{OK: true, Free: true}, nil
	}

	payer, mine, err := r.payerFor(ctx, in.Payment)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "settle: %v", err)
	}
	if !mine {
		return &client.Settled{Status: http.StatusPaymentRequired, Code: reasonReplay,
			Reason: "this payment is held for another payer"}, nil
	}
	g := run(r.s, ctx, payer, cloud.Who(ctx).User, in.Payment, resource, terms)
	// The header VALUES cross the plane, already encoded, because the process that
	// holds the request is the one that must write them and it does not link this
	// package: a caller that had to re-render them would be a second encoder of the
	// same wire, free to drift from this one.
	out := &client.Settled{Response: encode(g.settlement())}
	if req := g.required(); req != nil {
		out.Challenge = encode(req)
	}
	if g.receipt != nil {
		served(r.s, ctx, nil, g) // no request here: the audit row, not the header
		out.OK = true
		return out, nil
	}
	out.Status, out.Code, out.Reason = g.status, g.code, g.msg
	return out, nil
}

// Checks one payment authorization for a priced resource on behalf of the calling
// tenant, and settles nothing: the same terms Settle would challenge with, the same
// verification, and the time window asked at two moments — now, and Until, the
// latest the holder of the authorization may settle it.
//
// It exists for a payment that is AGREED before it is MADE. A job's buyer signs
// when the work begins and the money moves when the work is released, so the
// marketplace keeps the signed authorization and asks here, before the seller
// starts, whether it will settle: that it names this job's exact terms, that it
// was signed by the address it claims, that its nonce has not been spent, and that
// it is still acceptable when the review window closes. With hold, the nonce is
// then reserved for this resource, so the payment the seller works for cannot be
// spent on anything else first, and the amount is set aside in the payer's wallet,
// so the payment is there when the work is released: a wallet that cannot cover
// it is refused (402, insufficient_funds), and the same balance can never be
// promised twice. Nothing is claimed, so a payment checked here is settled once,
// by x402_settle, exactly as any other — from the amount set aside — or given up
// by x402_void, which returns it.
//
// With no payment it answers the challenge — the terms to sign — and nothing else.
// A resource that is not priced answers 409: there is nothing to authorize.
func (r rail) planeAuthorize(ctx context.Context, in *client.AuthorizationIn) (*client.Authorization, error) {
	resource := strings.Clone(in.Resource)
	if resource == "" {
		return nil, zip.ErrBadRequest("authorize: no resource")
	}
	terms, priced, err := priceOf(ctx, resource)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "authorize: price %s: %v", resource, err)
	}
	if !priced {
		return &client.Authorization{Status: http.StatusConflict, Code: reasonUnpriced, Reason: resource + " is not for sale"}, nil
	}
	// A hold sets the person's money aside for a payment; a support session makes none.
	if g, refused := supporting(ctx, resource); refused && in.Hold {
		return &client.Authorization{Status: g.status, Code: g.code, Reason: g.msg}, nil
	}
	payer := payerAt(ctx)
	c, g := verify(r.s, ctx, payer, in.Payment, resource, terms)
	out := &client.Authorization{Network: c.req.Network, Asset: c.req.Asset, PayTo: c.req.PayTo}
	if req := g.required(); req != nil {
		out.Challenge = encode(req)
	}
	if c.pay == nil {
		out.Status, out.Code, out.Reason = g.status, g.code, g.msg
		return out, nil
	}
	a := c.pay.Payload.Authorization
	out.From, out.ValidBefore = a.From, untilOf(a)
	refuse := func(code, reason string) (*client.Authorization, error) {
		out.Status, out.Code, out.Reason = http.StatusPaymentRequired, code, reason
		return out, nil
	}
	if err := protocol.InWindow(a, atTime()); err != nil {
		return refuse(reasonOf(err), err.Error())
	}
	if in.Until > 0 && untilOf(a) < in.Until {
		return refuse(protocol.ReasonValidBefore, fmt.Sprintf("authorization expires at %d, before %d, when it may still be settled", untilOf(a), in.Until))
	}
	id := settlementID(a.From, a.Nonce)
	// One settlement id at a time, on every replica: the money set aside here is what
	// a settlement draws and a void returns.
	release, err := r.s.State.settling.Across(ctx, r.s.State.store.db, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "authorize: %v", err)
	}
	defer release()
	if _, spent, err := r.s.State.store.get(ctx, id); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "authorize: %v", err)
	} else if spent {
		out.Spent = true
		return refuse(reasonReplay, "payment nonce already used: "+a.Nonce)
	}
	held, ok, err := r.s.State.store.held(ctx, id)
	if in.Hold && err == nil {
		held, err = r.s.State.store.hold(ctx, id, resource, untilOf(a), payer)
		ok = true
	}
	switch {
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "authorize: %v", err)
	case ok && held.resource != resource:
		return refuse(reasonReplay, "payment nonce is held for another payment: "+a.Nonce)
	case ok && held.given:
		return refuse(reasonReplay, "this payment was given up: sign another")
	case ok && !held.payer().Zero() && held.payer() != payer:
		// One authorization sets one amount aside, in one wallet: a second caller
		// holding it would set the same amount aside again in theirs.
		return refuse(reasonReplay, "this payment is held for another payer")
	}
	if in.Hold {
		err := holdPayer(ctx, payer, id, resource, terms.Amount)
		switch {
		case errors.Is(err, errShort):
			return refuse(protocol.ReasonInsufficientFunds, "the payer's wallet does not cover "+terms.Amount.String())
		case errors.Is(err, errGivenUp):
			return refuse(reasonReplay, "this payment was given up: sign another")
		case err != nil:
			return nil, zip.Errorf(http.StatusServiceUnavailable, "authorize: hold the payment: %v", err)
		}
	}
	out.OK = true
	return out, nil
}

// Gives up one held authorization on behalf of the calling tenant: the payment it
// agreed will never be made — the job it was for ended unpaid — so the amount set
// aside in the payer's wallet is returned, and answered. The nonce stays held for
// its resource: an authorization given up is spent on nothing else.
//
// The void is decided the moment it is asked. The ledger records the return —
// of whatever is set aside, or of nothing, when the hold has not reached it yet —
// and then the authorization is marked given up, so a hold of it delivered after
// is refused wherever it lands: here, or at the ledger, where a hold this rail
// stopped waiting for may still be on its way. A void asked again returns
// whatever is still set aside. A payment presented and never held here is given
// up the same way when it is one — signed by the address it names, over this
// resource's terms — so a void spends no nonce but one its caller signed; any
// other holds nothing and is given up nowhere. A payment the rail cannot check
// now — its payee does not resolve — is an outage, answered as one, for the
// caller to ask again, until its authorization expires: then nothing can hold
// it, and it is given up nowhere.
//
// An authorization already presented for settlement is NOT voided: its money
// moved, or is moving, and the answer says spent, so the caller finishes it as the
// payment it was. The payer is the caller's own account, as for every op here, so a
// void can return only the caller's own money to the caller's own wallet.
func (r rail) planeVoid(ctx context.Context, in *client.VoidIn) (*client.Voided, error) {
	resource := strings.Clone(in.Resource)
	pay, err := protocol.ParsePayment(in.Payment)
	if resource == "" || err != nil {
		return nil, zip.ErrBadRequest("void: a resource and the held payment")
	}
	a := pay.Payload.Authorization
	id := settlementID(a.From, a.Nonce)
	release, err := r.s.State.settling.Across(ctx, r.s.State.store.db, id)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
	}
	defer release()
	payer, mine, err := r.payerFor(ctx, in.Payment)
	switch {
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
	case !mine:
		return nil, zip.ErrForbidden("void: this payment is held for another payer")
	}
	if _, spent, err := r.s.State.store.get(ctx, id); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
	} else if spent {
		return &client.Voided{Spent: true, Returned: client.Amount(money.Zero().Unwrap())}, nil
	}
	held, ok, err := r.s.State.store.held(ctx, id)
	switch {
	case err != nil:
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
	case ok && held.resource != resource:
		return nil, zip.Errorf(http.StatusConflict, "void: payment nonce is held for another payment: %s", a.Nonce)
	case !ok:
		// An authorization past its time can no longer be held — authorize checks
		// the window before it holds — so one never held has nothing left to give
		// up, and needs no check: the answer does not wait on a payee that may never
		// resolve again.
		signed := false
		if untilOf(a) >= nowUnix() {
			if signed, err = r.signed(ctx, payer, resource, in.Payment); err != nil {
				return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
			}
		}
		if !signed {
			// Nothing was ever held under it: a row is kept until the payment it
			// held is settled or returned.
			return &client.Voided{Returned: client.Amount(money.Zero().Unwrap())}, nil
		}
	}
	back, err := returnPayer(ctx, payer, id, resource)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: return the payment: %v", err)
	}
	if err := r.s.State.store.giveUp(ctx, id, resource, untilOf(a)); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "void: %v", err)
	}
	return &client.Voided{Returned: client.Amount(back.Unwrap())}, nil
}

// signed reports whether payment is an authorization over resource's terms,
// signed by the address it names: false for one that is not, or for a resource
// no longer for sale, which has no terms to sign; an error when the terms could
// not be stated now.
func (r rail) signed(ctx context.Context, payer account.Account, resource, payment string) (bool, error) {
	terms, priced, err := priceOf(ctx, resource)
	if err != nil || !priced {
		return false, err
	}
	c, g := verify(r.s, ctx, payer, payment, resource, terms)
	if c.pay == nil && g.status >= http.StatusInternalServerError {
		return false, fmt.Errorf("the payment could not be checked: %s", g.msg)
	}
	return c.pay != nil, nil
}
