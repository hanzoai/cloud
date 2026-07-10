package account

import (
	"errors"
)

var ErrorAccountNotWithdrawable = errors.New("account not withdrawable")
var ErrorInsufficientFunds = errors.New("source has insufficient funds")
var ErrorInvalidPaymentMethod = errors.New("invalid payment method")
var ErrorMissingCredentials = errors.New("missing credentials")
