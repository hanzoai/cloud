package processor

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// BaseProcessor provides common functionality for all processors
type BaseProcessor struct {
	processorType ProcessorType
	currencies    []currency.Type
	configured    bool
}

// NewBaseProcessor creates a new base processor
func NewBaseProcessor(t ProcessorType, currencies []currency.Type) *BaseProcessor {
	return &BaseProcessor{
		processorType: t,
		currencies:    currencies,
		configured:    false,
	}
}

// Type returns the processor type
func (b *BaseProcessor) Type() ProcessorType {
	return b.processorType
}

// SupportedCurrencies returns the supported currencies
func (b *BaseProcessor) SupportedCurrencies() []currency.Type {
	return b.currencies
}

// IsAvailable returns whether the processor is available
func (b *BaseProcessor) IsAvailable(ctx context.Context) bool {
	return b.configured
}

// SetConfigured marks the processor as configured
func (b *BaseProcessor) SetConfigured(configured bool) {
	b.configured = configured
}

// Authorize provides a default implementation that calls Charge
// Processors that support auth/capture should override this
func (b *BaseProcessor) Authorize(ctx context.Context, req PaymentRequest) (*PaymentResult, error) {
	return nil, NewPaymentError(b.processorType, "NOT_IMPLEMENTED", "authorize not implemented", nil)
}

// Capture provides a default implementation
// Processors that support auth/capture should override this
func (b *BaseProcessor) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*PaymentResult, error) {
	return nil, NewPaymentError(b.processorType, "NOT_IMPLEMENTED", "capture not implemented", nil)
}

// ValidateRequest validates a payment request
func ValidateRequest(req PaymentRequest) error {
	if req.Amount <= 0 {
		return ErrInvalidPaymentRequest
	}
	if req.Currency == "" {
		return ErrInvalidPaymentRequest
	}
	return nil
}

// SupportsCurrency checks if a processor supports a currency
func SupportsCurrency(p PaymentProcessor, c currency.Type) bool {
	for _, supported := range p.SupportedCurrencies() {
		if supported == c {
			return true
		}
	}
	return false
}

// CommonFiatCurrencies returns common fiat currencies
func CommonFiatCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD,
		currency.EUR,
		currency.GBP,
		currency.CAD,
		currency.AUD,
		currency.JPY,
		currency.CNY,
	}
}

// CommonCryptoCurrencies returns common crypto currencies
func CommonCryptoCurrencies() []currency.Type {
	return []currency.Type{
		currency.BTC,
		currency.ETH,
		"sol",
		"usdc",
		"usdt",
	}
}
