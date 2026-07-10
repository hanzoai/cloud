package authorizenet

import (
	"errors"
)

var (
	FailedToCreateCustomerError          = errors.New("Failed to create Authorize customer.")
	FailedToUpdateCustomerError          = errors.New("Failed to update Authorize customer.")
	MinimumRefundTimeNotReachedError	 = errors.New("Minimum refund time not reached.")
	AuthorizeNotApprovedError			 = errors.New("Authorize attempt rejected")
	CaptureNotApprovedError				 = errors.New("Capture attempt rejected")
	ChargeNotApprovedError				 = errors.New("Capture attempt rejected")
	RefundGreaterThanPaymentError        = errors.New("The requested refund amount is greater than the paid amount")
	UnableToRefundUnpaidTransactionError = errors.New("Unable to refund unpaid transaction")
	NewSubscriptionFailedError			 = errors.New("NewSubscription Failed Error")
	UpdateSubscriptionFailedError		 = errors.New("UpdateSubscription Failed Error")
)
