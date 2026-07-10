package square

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// ValidateWebhook verifies the HMAC-SHA256 signature on an incoming
// Square webhook and returns a normalised event. BD's
// /v1/bd/webhooks/square handler must:
//
//  1. Iterate enabled square payment_providers rows (for multi-tenant
//     webhook disambiguation — Square's webhook is headerless on tenant).
//  2. Call ValidateWebhook with the raw request body and the
//     x-square-hmacsha256-signature header.
//  3. Accept the first row whose WebhookSignatureKey validates, reject
//     with 401 if none match.
//
// The normalised event.Type will be one of:
//   - payment.created
//   - payment.updated   (includes status=COMPLETED for settled funds)
//   - refund.created / refund.updated
//   - dispute.created / dispute.updated
//
// See thirdparty/square/processor.go::mapSquareEventType for the full
// mapping.
func (p *Provider) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.ValidateWebhook(ctx, payload, signature)
}
