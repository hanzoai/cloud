package paypal

import "errors"

var (
	OrderDoesNotExist   = errors.New("Order does not exist")
	PaymentDoesNotExist = errors.New("Payment does not exist")
	PaymentCancelled    = errors.New("Payment was cancelled")
)
