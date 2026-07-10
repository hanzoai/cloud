package checkout

import "errors"

var (
	errRefundInFlight            = errors.New("refund already in progress for this idempotency key")
	FailedToCreateCustomer       = errors.New("Failed to create customer")
	FailedToCreateUser           = errors.New("Failed to create user")
	FailedToDecodeRequestBody    = errors.New("Failed to decode request body")
	FailedToReserveItem          = errors.New("Item already reserved")
	FeeCalculationError          = errors.New("Failed to calculate fees")
	FundingAccountCreationError  = errors.New("Failed to create funding account")
	InvalidOrIncompleteOrder     = errors.New("Invalid or incomplete order")
	OnlyOneOfUserBuyerAllowed    = errors.New("Only one of user buyer allowed")
	OrderDoesNotExist            = errors.New("Order does not exist")
	PaymentCancelled             = errors.New("Payment was cancelled")
	TokenSaleNotFound            = errors.New("Token sale not found")
	TransactionLimitReached      = errors.New("Transaction Amount May Not Exceed Limit of 5000000")
	UnsupportedBitcoinCurrency   = errors.New("BTC/XBT is supported for 'bitcoin' payment method")
	UnsupportedEthereumCurrency  = errors.New("Only ETH is supported for 'ethereum' payment method")
	UnsupportedPaymentType       = errors.New("Unsupported payment type")
	UnsupportedFiatCurrency      = errors.New("XBT(BTC), ETH not supported by fiat payment method")
	UserDoesNotExist             = errors.New("User does not exist")
	UserNotProvided              = errors.New("None of user, user.id, order.userId is set")
	WalletCreationError          = errors.New("Failed to create wallet for user")
	MissingTokenSaleOrPassphrase = errors.New("order.tokenSaleId or tokenSale.passphrase is missing")
	OrderTotalIsLessThan1        = errors.New("order.total is less than 1")
	AuthorizationFailed          = errors.New("Failed to authorize")
)
