package host

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// x402FeeTimeout bounds one check or settle of a payment. The rail's own hops —
// the payee, the draw, the credit — are 10s each (apps/x402/peer.go), so this
// leaves each room to refuse first and the caller sees the rail's reason, not a
// timeout.
const x402FeeTimeout = 60 * time.Second

// x402TermsTimeout bounds the ask for a refusal's challenge. The terms are a
// courtesy on an answer already decided, so a slow rail costs the refusal its
// header, never its promptness.
const x402TermsTimeout = 5 * time.Second

// errRailSilent is a payment rail that answered nothing. Nothing said paid.
var errRailSilent = errors.New("x402: the payment rail answered nothing")

// x402Fee asks the payment rail about one metered fee for the request c, over the
// plane, and puts what it answered on c's response (sdk/x402fee.go says when each
// step is asked):
//
//	FeeTerms   x402_settle with no payment: its challenge on PAYMENT-REQUIRED.
//	FeeCheck   x402_authorize of the request's payment, holding nothing: on a
//	           refusal, the challenge on PAYMENT-REQUIRED.
//	FeeSettle  x402_settle of the request's payment: the settlement on
//	           PAYMENT-RESPONSE, success or failure, as the spec asks of both legs.
//
// No answer — the zero FeeAnswer and a nil error — is four things that mean the
// same to the route: x402 is not in this fleet (cloud.ErrNoPeer), the rail sells
// no such fee here, the request carries no payment to check or settle, or it has
// no payer the rail could charge. The route bills as it always has, and its gate
// refuses the last as itself.
//
// The payer is the one principal.Payer resolves, and its books are delegated with
// cloud.As, as the tool plane delegates a priced tool call (apps/tools
// charge_peer.go): the org rides the caller, and the wallet within it rides the
// delegated person, so a member of the signup org pays from their own wallet.
func x402Fee(c *zip.Ctx, resource string, step cloud.FeeStep) (cloud.FeeAnswer, error) {
	payment := strings.TrimSpace(c.Header(client.HeaderPaymentSignature))
	if step != cloud.FeeTerms && payment == "" {
		return cloud.FeeAnswer{}, nil
	}
	books := meter.Payer(c).Org()
	if books == "" {
		return cloud.FeeAnswer{}, nil
	}
	bound := x402FeeTimeout
	if step == cloud.FeeTerms {
		bound = x402TermsTimeout
	}
	call, cancel := context.WithTimeout(cloud.As(c, books), bound)
	defer cancel()

	if step == cloud.FeeCheck {
		out, err := cloud.Ask[client.AuthorizationIn, client.Authorization](call, "x402", client.X402Authorize,
			&client.AuthorizationIn{Resource: resource, Payment: payment})
		switch {
		case errors.Is(err, cloud.ErrNoPeer):
			return cloud.FeeAnswer{}, nil
		case err != nil:
			return cloud.FeeAnswer{}, err
		case out == nil:
			return cloud.FeeAnswer{}, errRailSilent
		case out.OK:
			return cloud.FeeAnswer{OK: true}, nil
		case out.Status == http.StatusConflict:
			return cloud.FeeAnswer{}, nil // the rail does not sell this fee here
		}
		if out.Challenge != "" {
			c.SetHeader(client.HeaderPaymentRequired, out.Challenge)
		}
		return cloud.FeeAnswer{Status: out.Status, Code: out.Code, Reason: out.Reason}, nil
	}

	in := client.SettleIn{Resource: resource}
	if step == cloud.FeeSettle {
		in.Payment = payment
	}
	out, err := cloud.Ask[client.SettleIn, client.Settled](call, "x402", client.X402Settle, &in)
	switch {
	case errors.Is(err, cloud.ErrNoPeer):
		return cloud.FeeAnswer{}, nil
	case err != nil:
		return cloud.FeeAnswer{}, err
	case out == nil:
		return cloud.FeeAnswer{}, errRailSilent
	case out.Free:
		return cloud.FeeAnswer{}, nil
	}
	// The values cross the plane already encoded and are set verbatim: the rail is
	// the one encoder of its wire.
	if step == cloud.FeeTerms {
		if out.Challenge != "" {
			c.SetHeader(client.HeaderPaymentRequired, out.Challenge)
		}
		return cloud.FeeAnswer{}, nil
	}
	if out.Response != "" {
		c.SetHeader(client.HeaderPaymentResponse, out.Response)
	}
	return cloud.FeeAnswer{OK: out.OK, Status: out.Status, Code: out.Code, Reason: out.Reason}, nil
}
