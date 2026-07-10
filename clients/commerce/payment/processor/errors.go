package processor

import "errors"

var (
	// ErrProcessorNotFound is returned when a processor is not registered
	ErrProcessorNotFound = errors.New("payment processor not found")

	// ErrProcessorNotAvailable is returned when a processor is not available
	ErrProcessorNotAvailable = errors.New("payment processor not available")

	// ErrProcessorDisabled is returned when a processor is disabled
	ErrProcessorDisabled = errors.New("payment processor is disabled")

	// ErrCurrencyNotSupported is returned when a currency is not supported
	ErrCurrencyNotSupported = errors.New("currency not supported by processor")

	// ErrInvalidPaymentRequest is returned when a payment request is invalid
	ErrInvalidPaymentRequest = errors.New("invalid payment request")

	// ErrInsufficientFunds is returned when there are insufficient funds
	ErrInsufficientFunds = errors.New("insufficient funds")

	// ErrPaymentDeclined is returned when a payment is declined
	ErrPaymentDeclined = errors.New("payment declined")

	// ErrPaymentFailed is returned when a payment fails
	ErrPaymentFailed = errors.New("payment failed")

	// ErrRefundFailed is returned when a refund fails
	ErrRefundFailed = errors.New("refund failed")

	// ErrTransactionNotFound is returned when a transaction is not found
	ErrTransactionNotFound = errors.New("transaction not found")

	// ErrWebhookValidationFailed is returned when webhook validation fails
	ErrWebhookValidationFailed = errors.New("webhook validation failed")

	// ErrSubscriptionNotSupported is returned when subscriptions are not supported
	ErrSubscriptionNotSupported = errors.New("subscriptions not supported by processor")

	// ErrCryptoNotSupported is returned when crypto operations are not supported
	ErrCryptoNotSupported = errors.New("crypto operations not supported by processor")

	// ErrCustomerNotSupported is returned when customer operations are not supported
	ErrCustomerNotSupported = errors.New("customer management not supported by processor")

	// ErrThresholdNotMet is returned when MPC threshold is not met
	ErrThresholdNotMet = errors.New("signing threshold not met")

	// ErrSigningFailed is returned when MPC signing fails
	ErrSigningFailed = errors.New("signing ceremony failed")
)

// PaymentError wraps a processor error with additional context
type PaymentError struct {
	Processor ProcessorType
	Code      string
	Message   string
	Err       error
}

func (e *PaymentError) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *PaymentError) Unwrap() error {
	return e.Err
}

// NewPaymentError creates a new payment error
func NewPaymentError(processor ProcessorType, code, message string, err error) *PaymentError {
	return &PaymentError{
		Processor: processor,
		Code:      code,
		Message:   message,
		Err:       err,
	}
}
