package paymentmethods

// Generic interface for exchanging for Pay Tokens
type PaymentMethod interface {
	GetPayToken(PaymentMethodParams) (*PaymentMethodOutput, error)
}
