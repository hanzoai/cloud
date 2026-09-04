package x402

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
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
// THE PAYER RIDES THE CAPABILITY. It is the caller's org — resolved once, at the
// edge that holds the request, by principal.Ledger (which folds in the SuperAdmin
// masquerade) and delegated with cloud.As. It is not a field, because a caller
// that could name the payer could spend another tenant's ledger.
//
// THE OUTCOME IS DATA, not a transport error. A 402 carries the terms the client
// must read to pay, and an error body has no room for them — so the op answers 200
// with the refusal inside it, exactly as the prepaid gate answers with a Verdict.
// A transport error therefore means something else entirely: the rail did not
// answer. Callers fail closed on that, never free.

// exposeSettle publishes the rail. Mount calls it.
func exposeSettle(s *cloud.Service[state]) {
	zip.Post[client.SettleIn, client.Settled](cloud.Plane(), "/x402/settle", rail{s}.planeSettle,
		zip.WithOperationID(client.X402Settle),
		zip.WithSummary("Settle payment for one priced resource"))
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
// The PAYER is the caller's own tenant, resolved at the edge that holds the request
// and delegated on the call. It is not a field: a caller that could name the payer
// could spend another tenant's ledger.
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

	g := run(r.s, ctx, cloud.Who(ctx).Org, in.Payment, resource, terms)
	// The header VALUES cross the plane, already encoded, because the process that
	// holds the request is the one that must write them and it does not link this
	// package: a caller that had to re-render them would be a second encoder of the
	// same wire, free to drift from this one.
	out := &client.Settled{Response: EncodeHeader(g.settlement())}
	if req := g.required(); req != nil {
		out.Challenge = EncodeHeader(req)
	}
	if g.receipt != nil {
		served(r.s, ctx, nil, g) // no request here: the audit row, not the header
		out.OK = true
		return out, nil
	}
	out.Status, out.Code, out.Reason = g.status, g.code, g.msg
	return out, nil
}
