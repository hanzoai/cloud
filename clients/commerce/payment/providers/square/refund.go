package square

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// Refund processes a refund against a settled Square payment. Partial
// refunds are supported by setting req.Amount < the original; a zero
// amount refunds the full charge.
func (p *Provider) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	if err := p.checkAvailable(); err != nil {
		return nil, err
	}
	return p.inner.Refund(ctx, req)
}
