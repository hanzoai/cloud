package subscription

import "errors"

var (
	SubscriptionDoesNotExist  = errors.New("subscription does not exist")
	PlanDoesNotExist          = errors.New("plan does not exist")
	UserDoesNotExist          = errors.New("user does not exist")
	PaymentCancelled          = errors.New("subscription was cancelled")
	FailedToDecodeRequestBody = errors.New("failed to decode request body")
	FailedToCreateCustomer    = errors.New("failed to create customer")
	FailedToCreateUser        = errors.New("failed to create user")
	OnlyOneOfUserBuyerAllowed = errors.New("only one of user buyer allowed")
	CannotChangeUser          = errors.New("subscription user cannot be changed")
)
