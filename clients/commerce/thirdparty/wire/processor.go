package wire

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
)

// WireProcessor implements the processor.PaymentProcessor interface
// for bank wire transfers. Wire payments are manually confirmed when
// the bank transfer is received.
type WireProcessor struct {
	*processor.BaseProcessor
	wire integration.WireTransfer
}

func init() {
	processor.Register(&WireProcessor{
		BaseProcessor: processor.NewBaseProcessor(processor.Wire, WireSupportedCurrencies()),
	})
}

// NewProcessor creates a new wire transfer processor
func NewProcessor(wire integration.WireTransfer) *WireProcessor {
	wp := &WireProcessor{
		BaseProcessor: processor.NewBaseProcessor(processor.Wire, WireSupportedCurrencies()),
		wire:          wire,
	}

	// Configured if at least bank name and account holder are set
	if wire.BankName != "" && wire.AccountHolder != "" {
		wp.SetConfigured(true)
	}

	return wp
}

// Configure sets up the processor with wire transfer details
func (wp *WireProcessor) Configure(wire integration.WireTransfer) {
	wp.wire = wire
	wp.SetConfigured(wire.BankName != "" && wire.AccountHolder != "")
}

// WireSupportedCurrencies returns fiat currencies supported by wire transfer
func WireSupportedCurrencies() []currency.Type {
	return []currency.Type{
		currency.USD,
		currency.EUR,
		currency.GBP,
		currency.CAD,
		currency.AUD,
		currency.JPY,
	}
}

// Type returns the processor type
func (wp *WireProcessor) Type() processor.ProcessorType {
	return processor.Wire
}

// Charge creates a pending wire payment with instructions in metadata
func (wp *WireProcessor) Charge(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	if err := processor.ValidateRequest(req); err != nil {
		return nil, err
	}

	txID := fmt.Sprintf("wire_tx_%s", uuid.New().String())

	return &processor.PaymentResult{
		Success:       true,
		TransactionID: txID,
		ProcessorRef:  txID,
		Fee:           currency.Cents(0),
		Status:        "pending_wire",
		Metadata: map[string]interface{}{
			"bankName":      wp.wire.BankName,
			"accountHolder": wp.wire.AccountHolder,
			"routingNumber": wp.wire.RoutingNumber,
			"accountNumber": wp.wire.AccountNumber,
			"swift":         wp.wire.SWIFT,
			"iban":          wp.wire.IBAN,
			"bankAddress":   wp.wire.BankAddress,
			"reference":     wp.wire.Reference,
			"instructions":  wp.wire.Instructions,
		},
	}, nil
}

// Authorize creates a pending wire payment (same as Charge for wire)
func (wp *WireProcessor) Authorize(ctx context.Context, req processor.PaymentRequest) (*processor.PaymentResult, error) {
	return wp.Charge(ctx, req)
}

// Capture marks a wire payment as confirmed/credited (called when wire arrives)
func (wp *WireProcessor) Capture(ctx context.Context, transactionID string, amount currency.Cents) (*processor.PaymentResult, error) {
	return &processor.PaymentResult{
		Success:       true,
		TransactionID: transactionID,
		ProcessorRef:  transactionID,
		Fee:           currency.Cents(0),
		Status:        "credited",
		Metadata: map[string]interface{}{
			"confirmed_at": time.Now().Unix(),
		},
	}, nil
}

// Refund marks a wire payment as refunded
func (wp *WireProcessor) Refund(ctx context.Context, req processor.RefundRequest) (*processor.RefundResult, error) {
	refundID := fmt.Sprintf("wire_refund_%s", uuid.New().String())

	return &processor.RefundResult{
		Success:      true,
		RefundID:     refundID,
		ProcessorRef: refundID,
	}, nil
}

// GetTransaction returns the pending wire status
func (wp *WireProcessor) GetTransaction(ctx context.Context, txID string) (*processor.Transaction, error) {
	return &processor.Transaction{
		ID:           txID,
		ProcessorRef: txID,
		Type:         "wire_transfer",
		Amount:       currency.Cents(0),
		Currency:     currency.USD,
		Status:       "pending_wire",
		CreatedAt:    time.Now().Unix(),
		UpdatedAt:    time.Now().Unix(),
		Metadata: map[string]interface{}{
			"type": "wire_transfer",
		},
	}, nil
}

// ValidateWebhook returns error (no webhooks for wire transfers)
func (wp *WireProcessor) ValidateWebhook(ctx context.Context, payload []byte, signature string) (*processor.WebhookEvent, error) {
	return nil, processor.ErrWebhookValidationFailed
}

// IsAvailable checks if the processor has wire instructions configured
func (wp *WireProcessor) IsAvailable(ctx context.Context) bool {
	return wp.wire.BankName != "" && wp.wire.AccountHolder != ""
}

// WireInstructions returns the configured wire transfer details
func (wp *WireProcessor) WireInstructions() integration.WireTransfer {
	return wp.wire
}

// Ensure WireProcessor implements PaymentProcessor
var _ processor.PaymentProcessor = (*WireProcessor)(nil)
