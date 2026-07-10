package transaction

import (
	"errors"
)

var ErrorSourceRequired = errors.New("Source is required")
var ErrorDestinationRequired = errors.New("Destination is required")
var ErrorPointlessTransaction = errors.New("Amount cannot be 0")
var ErrorCurrencyRequired = errors.New("Currency is required")
var ErrorUseHoldApi = errors.New("Use transaction/hold api to create holds")
var ErrorCircularTransaction = errors.New("Source and Destination cannot be the same")
var ErrorInsufficientFunds = errors.New("Source has insufficient funds")
var ErrorInvalidType = errors.New("Type is invalid")
