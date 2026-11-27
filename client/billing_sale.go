// billing_sale.go holds card top-up/plan sale types and the headless agent purchase surface.

package client

// ---- the card endpoints: top up a wallet, or buy a plan ----------------------
//
// All three MOVE MONEY, so all three carry the same two properties. The SUBJECT
// is resolved at the endpoint from the caller's own credential and is a field
// here only so the store is never asked to re-derive an identity nobody proved.
// And none of them carries an AMOUNT the caller chose for a plan: a subscription
// is priced from the catalog, so there is no field an underpayment could be
// written in — it is a request that cannot be expressed rather than a check that
// can be forgotten.

const (
	BillingSubscriptions = "billing_subscriptions"
	// BillingFleetSubscriptions is every org's subscriptions in one read, for the
	// platform's SuperAdmin alone.
	BillingFleetSubscriptions = "billing_fleet_subscriptions"

	// BillingTopupCard charges a single-use card token; BillingTopup charges a
	// card the subject already saved. Two ops rather than one with a mode,
	// because they take different things and fail in different places: one
	// vaults nothing and one must find a saved row first.
	BillingTopupCard = "billing_topup_card"
	BillingTopup     = "billing_topup"

	BillingSubscribe   = "billing_subscribe"
	BillingRecharge    = "billing_recharge"
	BillingPayPalOrder = "billing_paypal_order"

	// The processor a buyer's new checkout goes to, the Stripe intent a local method
	// pays, where that intent stands, and the platform's policy that decides the first.
	BillingRoute            = "billing_route"
	BillingTopupIntent      = "billing_topup_intent"
	BillingTopupIntentState = "billing_topup_intent_state"
	// BillingTopupIntentCancel ends one of the buyer's own payments that is not yet confirmed.
	BillingTopupIntentCancel = "billing_topup_intent_cancel"
	// The headless purchase surface an agent buys through: what is for sale and how it is
	// paid, a checkout opened, changed, read and completed, the order it confirms, a
	// stablecoin top-up, a shared payment token for an invoice or a subscription, and a
	// bank wire reconciled to a wallet.
	BillingCatalog           = "billing_catalog"
	BillingDiscovery         = "billing_discovery"
	BillingCheckoutOpen      = "billing_checkout_open"
	BillingCheckoutUpdate    = "billing_checkout_update"
	BillingCheckout          = "billing_checkout"
	BillingCheckoutComplete  = "billing_checkout_complete"
	BillingOrder             = "billing_order"
	BillingTopupCrypto       = "billing_topup_crypto"
	BillingInvoiceToken      = "billing_invoice_token"
	BillingSubscriptionToken = "billing_subscription_token"
	BillingWireReconcile     = "billing_wire_reconcile"
	BillingRouting           = "billing_routing"
	BillingRoutingSet        = "billing_routing_set"
	// The price of what a buyer is about to buy, the sample route an admin asks, the
	// policy's history and its restore, and the reset of payers' placements.
	BillingQuote          = "billing_quote"
	BillingRoutingPreview = "billing_routing_preview"
	BillingRoutingHistory = "billing_routing_history"
	BillingRoutingRestore = "billing_routing_restore"
	BillingRoutingReset   = "billing_routing_reset"

	// BillingRenew renews every subscription that has fallen due, in every org:
	// the scheduled renewal sweep, as a plane call the cron app asks by name as
	// the platform.
	BillingRenew = "billing_renew"

	// CatalogRefresh pulls the upstream model catalog and lands it in commerce:
	// the scheduled sync, as a plane call. The cron app asks it by name as the
	// platform, which replaced a POST to our own writer bearing a shared
	// COMMERCE_SERVICE_TOKEN — the gate on that route admitted the token, and
	// this op takes the caller the plane states.
	CatalogRefresh = "commerce_catalog_refresh"
)

// Refreshed is what one catalog sync did — the module's RefreshResult, on the
// wire.
type Refreshed struct {
	Serves    string   `json:"serves"`
	Upstream  int      `json:"upstream"`
	Created   int      `json:"created"`
	Updated   int      `json:"updated"`
	Withdrawn []string `json:"withdrawn,omitempty"`
	Restored  []string `json:"restored,omitempty"`
	SyncedAt  string   `json:"syncedAt"`
}

// CardIn charges a single-use card token and credits the subject's wallet.
//
// SourceID is the processor's own single-use nonce, so it is spent by the charge
// and is worthless to anyone who reads it afterwards. IdempotencyKey is the
// caller's retry key; empty falls back to the store's windowed derivation over
// the amount and subject, which is what a browser sending no header has always
// relied on.
type CardIn struct {
	SourceID       string `json:"sourceId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency,omitempty"`
	Subject        string `json:"subject"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Provider names the explicit processor ("square" or "paypal") when SourceID
	// is a provider-specific order/token (e.g. a PayPal v2 Order ID). Empty uses
	// the org's default priority. Appended last for ZAP wire.
	Provider string `json:"provider,omitempty"`
	// Name and Email are the caller's own, off the identity IAM holds for them;
	// Phone and Billing are what the buyer typed in the payment form. After Provider,
	// for the ZAP wire.
	Name    string          `json:"name,omitempty"`
	Email   string          `json:"email,omitempty"`
	Phone   string          `json:"phone,omitempty"`
	Billing *BillingAddress `json:"billing,omitempty"`
}

// PayPalOrderIn opens a server-priced PayPal REST v2 order for either a
// bounded credit top-up (when PlanID is empty, intent CAPTURE) or a plan
// subscription's first period (when PlanID is set, intent AUTHORIZE).
// Subject is resolved at the billing endpoint from the caller's credential.
type PayPalOrderIn struct {
	Host           string     `json:"host,omitempty"`
	AmountCents    int64      `json:"amountCents,omitempty"`
	PlanID         string     `json:"planId,omitempty"`
	StoreID        string     `json:"storeId,omitempty"`
	Quantity       int        `json:"quantity,omitempty"`
	Lines          []SaleLine `json:"lines,omitempty"`
	Level          int        `json:"level,omitempty"`
	Interval       string     `json:"interval,omitempty"`
	Currency       string     `json:"currency,omitempty"`
	Coupon         string     `json:"coupon,omitempty"`
	BalanceCents   int64      `json:"balanceCents,omitempty"`
	Subject        string     `json:"subject"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	Chosen         bool       `json:"chosen,omitempty"`
}

// PayPalOrderOut is the created PayPal REST v2 order handed back to the
// browser's PayPal Smart Buttons SDK for buyer approval.
type PayPalOrderOut struct {
	OrderID     string `json:"orderId"`
	AmountCents int64  `json:"amountCents"`
	Currency    string `json:"currency"`
	Intent      string `json:"intent"`
	Status      string `json:"status"`
	Environment string `json:"environment"`
	Test        bool   `json:"test"`
}

// SavedCardIn charges a card the subject already saved.
//
// The method must belong to the subject; one that does not is a MISS rather than
// a refusal, so a guessed id cannot confirm that somebody else's card exists.
type SavedCardIn struct {
	MethodID       string `json:"methodId"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency,omitempty"`
	Subject        string `json:"subject"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Description rides on the charge to the processor, which is where the
	// customer reads it. A top-up and an auto-recharge say different true things
	// there, so it is a value rather than a sentence invented in the store.
	Description string `json:"description,omitempty"`
}

// Charged is the receipt for a settled card charge.
//
// ProcessorRef is the only field that proves money moved at the GATEWAY rather
// than merely in our ledger, which is why it is answered rather than only
// logged. Test states which bucket was credited so no reader has to guess
// whether a receipt is real.
type Charged struct {
	// TransactionID is the ledger entry this charge created. It is the handle a
	// later read or a refund names, and it is minted by the ledger rather than by
	// the caller.
	TransactionID string `json:"transactionId"`
	// BalanceCents is the subject's balance AFTER the charge settled, in cents, read
	// from the ledger GET /v1/billing/balance reads, so a caller does not have to
	// re-read to show the new number. It is absent when that ledger could not say.
	BalanceCents int64 `json:"balanceCents,omitempty"`
	// Status is how the charge ended. Read it rather than inferring success from
	// the HTTP status: the call succeeded whenever this field is present, and what
	// the PROCESSOR did is what this says.
	Status string `json:"status"`
	// ProcessorRef is the payment processor's own reference. It is the only field
	// that proves money moved at the GATEWAY rather than merely in our ledger,
	// which is why it is answered and not only logged. Absent where the processor
	// returned none.
	ProcessorRef string `json:"processorRef,omitempty"`
	// Test states which bucket was credited — sandbox money or real money — so no
	// reader has to guess whether a receipt is real. Sandbox and live funds are
	// physically separate ledgers, and a reader that conflates them restates the
	// company's revenue.
	Test bool `json:"test"`
}

// SaleIn buys a plan with a card — either a fresh single-use token, which is
// vaulted first, or a card the subject already saved.
//
// There is no amount. The price is the plan's catalog price at the named Level,
// which is an INDEX into its published prices and never a number, so what the
// card is charged is decided by the catalog on both sides of this wire.
type SaleIn struct {
	// Host is the host the plan is bought on. The catalog is the serving brand's,
	// so a plan is sold only where the host's brand sells it.
	Host     string `json:"host,omitempty"`
	SourceID string `json:"sourceId,omitempty"`
	MethodID string `json:"methodId,omitempty"`
	PlanID   string `json:"planId"`
	Subject  string `json:"subject"`
	StoreID  string `json:"storeId,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Level    int    `json:"level,omitempty"`
	// Interval is the term bought: "month" (the default) or "year".
	Interval       string `json:"interval,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Email          string `json:"email,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Coupon is a plan coupon code for a percent off the first months it names,
	// monthly billing only. It is a code, never an amount; one that cannot be used
	// refuses the sale before any card is charged.
	Coupon string `json:"coupon,omitempty"`
	// Wallet says SourceID is a wallet's single-use token (Apple Pay, Google Pay,
	// Cash App Pay): it pays the first period itself, and renewals are drawn from
	// the prepaid balance.
	Wallet bool `json:"wallet,omitempty"`
	// Consent is the choice the buyer's checkout states, as @hanzo/event spells it
	// ("analytics,marketing,ads"; empty when they allowed nothing), and nil when it
	// stated none. The sale reaches an ad platform only under it.
	Consent *string `json:"consent,omitempty"`
	// Lines are a team's other seat types, bought on the same subscription: a
	// team of one standard and one premium seat is PlanID "team", Quantity 1 and
	// one line of "team-premium". After Consent, so the fields before them keep
	// their places on the ZAP wire.
	Lines []SaleLine `json:"lines,omitempty"`
	// BalanceCents is an organization's starting usage balance, charged in the
	// same payment as the plan and credited to its prepaid balance: whole dollars,
	// at most the plan row's limits.balanceCeiling. After Lines, for the ZAP wire.
	BalanceCents int64 `json:"balanceCents,omitempty"`
	// Chosen says the buyer chose the organization the sale is for: the request
	// selected it with X-Org-Id and the identity boundary opened it, rather than
	// acting in the buyer's default org. An organization's order is sold only when
	// it is true. After BalanceCents, for the ZAP wire.
	Chosen bool `json:"chosen,omitempty"`
	// Provider names the explicit wallet/card rail ("square" or "paypal") when
	// SourceID is a provider-specific order/token. Appended last for ZAP wire.
	Provider string `json:"provider,omitempty"`
	// DeclineReload is the buyer turning off, at checkout, the automatic reload the
	// plan's terms show: the sale switches nothing on and answers reload state
	// declined. After Provider, for the ZAP wire.
	DeclineReload bool `json:"declineReload,omitempty"`
	// Name is the caller's own name off the identity IAM holds for them; Phone and
	// Billing are what the buyer typed in the payment form. After DeclineReload, for
	// the ZAP wire. (Email is the caller's, above.)
	Name    string          `json:"name,omitempty"`
	Phone   string          `json:"phone,omitempty"`
	Billing *BillingAddress `json:"billing,omitempty"`
	// Country is the buyer's country (ISO 3166-1 alpha-2). A sale with a country is
	// billed in the currency the policy resolves for it; without one it stays in the
	// plan's. QuotedAmount and QuotedCurrency are the price the buyer was shown, and the
	// sale refuses with the new price when it no longer matches. Last, for the ZAP wire.
	Country        string `json:"country,omitempty"`
	QuotedAmount   int64  `json:"quotedAmount,omitempty"`
	QuotedCurrency string `json:"quotedCurrency,omitempty"`
}

// SaleLine is one more seat type a team buys: a plan of the same catalog group,
// and how many seats of it.
type SaleLine struct {
	// PlanID is the seat type, e.g. "team-premium".
	PlanID string `json:"planId"`
	// Quantity is the seats of it bought, at least 1.
	Quantity int `json:"quantity"`
}

// Sale is the receipt for a card subscription: what was opened, what it cost,
// and which card renewals will charge.
type Sale struct {
	SubscriptionID string `json:"subscriptionId"`
	InvoiceID      string `json:"invoiceId"`
	PlanID         string `json:"planId"`
	Level          int    `json:"level"`
	// Interval is the term bought and the term the subscription renews on.
	Interval        string `json:"interval"`
	PaymentMethodID string `json:"paymentMethodId"`
	AmountCents     int64  `json:"amountCents"`
	Currency        string `json:"currency"`
	Status          string `json:"status"`
	// Test is true when the sale was charged to the org's sandbox, not to real
	// money.
	Test bool `json:"test,omitempty"`
	// Coupon and CouponPercent name the coupon the subscription was bought with,
	// absent when none was: CouponPercent off each of its first CouponMonths
	// monthly periods, this one the first. Renewals after them bill without it.
	Coupon        string `json:"coupon,omitempty"`
	CouponPercent int    `json:"couponPercent,omitempty"`
	// EventID is the id the sale is stated under on the event plane: the
	// `event_id` of its server-side order_completed, which every connected ad
	// platform keeps one conversion per. A page that fires its browser pixel for
	// the sale fires it under this id, so the two count once.
	EventID string `json:"eventId,omitempty"`
	// CouponMonths is how many monthly periods the coupon discounts. Last, so
	// the fields before it keep their places on the ZAP wire.
	CouponMonths int `json:"couponMonths,omitempty"`
	// Reload is what the sale did with the automatic reload its plan switches on —
	// every paid plan switches one on unless its row states none — absent for a plan
	// with none. After CouponMonths, for the same reason.
	Reload *SaleReload `json:"reload,omitempty"`
	// BalanceCents is the part of AmountCents credited to the buyer's prepaid
	// balance — an organization's starting balance — and BalanceID the receipt of
	// that credit. Absent when the sale bought none. After Reload, for the ZAP
	// wire.
	BalanceCents int64  `json:"balanceCents,omitempty"`
	BalanceID    string `json:"balanceId,omitempty"`
	// Seats is how many seats the sale bought, every seat type counted; 1 on a
	// flat plan.
	Seats int64 `json:"seats,omitempty"`
	// Category is the ladder the plan sits on: personal, team or enterprise.
	Category string `json:"category,omitempty"`
	// Replaced is the paid subscription the sale replaced; absent when the
	// account held none.
	Replaced string `json:"replaced,omitempty"`
	// StartsAt is when the plan bought starts and its card is first charged (RFC
	// 3339): the end of the plan the account held, paid outside checkout or from
	// the balance (Follows), so nothing is paid twice. StartsCents is what that
	// first period costs. All three are absent when the sale charged today. Last,
	// for the ZAP wire.
	StartsAt    string `json:"startsAt,omitempty"`
	StartsCents int64  `json:"startsCents,omitempty"`
	Follows     string `json:"follows,omitempty"`
	// Quote is the price the buyer paid when the sale was billed in their own
	// currency; absent when it was billed in the plan's. Last, for the ZAP wire.
	Quote *PriceQuote `json:"quote,omitempty"`
}

// SaleReload is what a sale did with its plan's automatic reload.
type SaleReload struct {
	// State is armed (switched on at the plan's figures), kept (the payer's own
	// enabled rule left as it is), needs_card (no card on file to charge, so not
	// switched on: saving a card switches it on) or declined (the buyer turned it
	// off at checkout).
	State string `json:"state"`
}

// Sold is what a sale answered with, and it keeps the two shapes APART: a fresh
// receipt, or the sealed body of an identical earlier sale replayed verbatim.
//
// Exactly one field is ever set, and the endpoint owes a different status for
// each — 201 for the first, 200 for the second. A retry that got 201 back would
// read as a second subscription having been opened. Replayed is RAW so the retry
// gets the bytes the first answer was rather than a second rendering of them.
type Sold struct {
	Sale     *Sale  `json:"sale,omitempty"`
	Replayed []byte `json:"replayed,omitempty"`
}


// ---- the headless purchase surface an agent buys through -------------------

// CatalogIn asks what is for sale on a host. The brand the host names decides which
// catalog it is; a caller with an org is answered for that org's processors.
type CatalogIn struct {
	// Host is the customer-facing host the catalog is read on.
	Host string `json:"host,omitempty"`
}

// AgentItem is the one thing a checkout sells: USD credit, or a plan's first period.
type AgentItem struct {
	// Kind is credit or plan.
	Kind string `json:"kind"`
	// UsdCents is the credit bought, in cents; for a credit only.
	UsdCents int64 `json:"usdCents,omitempty"`
	// Plan is the plan's slug; for a plan only.
	Plan string `json:"plan,omitempty"`
	// Interval is the billing term, month or year; for a plan only.
	Interval string `json:"interval,omitempty"`
}

// CheckoutOpenIn opens a checkout for the subject's one item.
type CheckoutOpenIn struct {
	Subject string      `json:"subject"`
	Host    string      `json:"host,omitempty"`
	Items   []AgentItem `json:"items"`
}

// CheckoutUpdateIn replaces the item of one of the subject's open checkouts.
type CheckoutUpdateIn struct {
	Subject string      `json:"subject"`
	Host    string      `json:"host,omitempty"`
	ID      string      `json:"id"`
	Items   []AgentItem `json:"items"`
}

// CheckoutRef names one of a subject's own checkouts or the order it made.
type CheckoutRef struct {
	Subject string `json:"subject"`
	ID      string `json:"id"`
}

// DepositAddress is where to send a stablecoin: a network, its address and the tokens
// it takes. The JSON names are commerce's processor.DepositAddress tags, camelCase since
// commerce v1.50.233 (network, address, tokens); TestEveryShapeIsCommercesShape holds
// the two equal.
type DepositAddress struct {
	// Network is the network the address is on.
	Network string `json:"network"`
	// Address is the address to send to.
	Address string `json:"address"`
	// Tokens are the tokens the address takes.
	Tokens []DepositToken `json:"tokens"`
}

// DepositToken is one token a deposit address takes.
type DepositToken struct {
	// Currency is the token's symbol.
	Currency string `json:"currency"`
	// Contract is the token's contract or mint.
	Contract string `json:"contract"`
}

// AgentNext is a step the agent must take for a checkout to go on.
type AgentNext struct {
	// Type is authenticate or send_deposit.
	Type string `json:"type"`
	// IntentID is the payment the step belongs to.
	IntentID string `json:"intentId,omitempty"`
	// ClientSecret is the bank's step, for authenticate.
	ClientSecret string `json:"clientSecret,omitempty"`
	// PublishableKey is the public key the bank's step is taken with.
	PublishableKey string `json:"publishableKey,omitempty"`
	// Addresses are where to send the stablecoin, for send_deposit.
	Addresses []DepositAddress `json:"addresses,omitempty"`
	// Message says what to do.
	Message string `json:"message,omitempty"`
}

// AgentError is a refusal an agent can act on, with a code to branch on.
type AgentError struct {
	// Code is the refusal, such as token_over_limit or authentication_required.
	Code string `json:"code"`
	// Message says why.
	Message string `json:"message"`
}

// TokenRoom is what is left on the shared payment token a subscription renews on, and
// whether it covers the next renewal.
type TokenRoom struct {
	// RemainingCents is what the token can still be charged.
	RemainingCents int64 `json:"remainingCents"`
	// MaxCents is the most the token may be charged in all.
	MaxCents int64 `json:"maxCents"`
	// RenewalCents is what the next renewal charges.
	RenewalCents int64 `json:"renewalCents"`
	// Covers says the token covers the next renewal.
	Covers bool `json:"covers"`
	// RecurringInterval is the interval the token may renew on.
	RecurringInterval string `json:"recurringInterval,omitempty"`
	// ExpiresAt is when the token expires.
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// AgentOrder is the confirmation of a completed checkout.
type AgentOrder struct {
	// ID is the order's id.
	ID string `json:"id"`
	// CheckoutID is the checkout it confirms.
	CheckoutID string `json:"checkoutId"`
	// Status is paid.
	Status string `json:"status"`
	// Currency is the currency it was paid in.
	Currency string `json:"currency"`
	// TotalCents is what it cost.
	TotalCents int64 `json:"totalCents"`
	// CreditedCents is the credit the wallet was given, for a credit.
	CreditedCents int64 `json:"creditedCents,omitempty"`
	// PaymentID is the receipt of the payment.
	PaymentID string `json:"paymentId,omitempty"`
	// SubscriptionID is the subscription it opened, for a plan.
	SubscriptionID string `json:"subscriptionId,omitempty"`
	// Plan is the plan bought.
	Plan string `json:"plan,omitempty"`
	// Token is what is left on the token the subscription renews on.
	Token *TokenRoom `json:"token,omitempty"`
	// CompletedAt is when it was paid.
	CompletedAt string `json:"completedAt,omitempty"`
}

// AgentCheckout is a checkout as the agent reads it.
type AgentCheckout struct {
	// ID is the checkout's id.
	ID string `json:"id"`
	// Status is open, pending, complete or failed.
	Status string `json:"status"`
	// Currency is the currency it sells in.
	Currency string `json:"currency"`
	// Items is the one thing it sells.
	Items []AgentItem `json:"items"`
	// TotalCents is the price.
	TotalCents int64 `json:"totalCents"`
	// Order is the confirmation, once complete.
	Order *AgentOrder `json:"order,omitempty"`
	// Next is what the agent does next when the status is not yet complete.
	Next *AgentNext `json:"next,omitempty"`
	// Error is why the last completion was refused.
	Error *AgentError `json:"error,omitempty"`
}

// AgentPay is how the agent pays: a shared payment token, or a stablecoin deposit.
type AgentPay struct {
	// Type is shared_payment_token or stablecoin.
	Type string `json:"type"`
	// Token is the shared payment token (spt_...), for shared_payment_token.
	Token string `json:"token,omitempty"`
	// Network is the network to deposit on, for stablecoin; the first the account takes when absent.
	Network string `json:"network,omitempty"`
}

// CheckoutCompleteIn pays one of the subject's checkouts. Key binds the charge to the
// checkout, so completing twice is one purchase.
type CheckoutCompleteIn struct {
	Subject string   `json:"subject"`
	Host    string   `json:"host,omitempty"`
	ID      string   `json:"id"`
	Key     string   `json:"key"`
	Payment AgentPay `json:"payment"`
}

// CheckoutCompletion is the outcome of paying a checkout: the checkout, and the HTTP
// status it is answered with. 200 is complete, 202 waits on the buyer, and a refusal
// keeps its own status (402 a declined payment) with the checkout's error.
type CheckoutCompletion struct {
	// Status is the HTTP status the completion is answered with.
	Status int `json:"status"`
	// Checkout is the checkout after the attempt.
	Checkout AgentCheckout `json:"checkout"`
}

// CryptoTopupIn opens a stablecoin payment for AmountCents of credit.
type CryptoTopupIn struct {
	Subject        string `json:"subject"`
	AmountCents    int64  `json:"amountCents"`
	Network        string `json:"network,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// CryptoTopup is where to send the stablecoin. Nothing is credited until the deposit
// is confirmed on chain and the payment reported succeeded, once.
type CryptoTopup struct {
	// IntentID is the payment's id.
	IntentID string `json:"intentId"`
	// AmountCents is the credit the payment buys.
	AmountCents int64 `json:"amountCents"`
	// Currency is usd.
	Currency string `json:"currency"`
	// UsdOnly says only dollar stablecoins are taken.
	UsdOnly bool `json:"usdOnly"`
	// Addresses are where to send it, per network.
	Addresses []DepositAddress `json:"addresses"`
	// Status is awaiting_deposit.
	Status string `json:"status"`
}

// AgentTokenIn supplies a shared payment token for one invoice or subscription of the subject.
type AgentTokenIn struct {
	Subject string `json:"subject"`
	ID      string `json:"id"`
	Token   string `json:"token"`
}

// AgentPayment is the result of paying a past-due invoice with a token. It is answered
// 200 when paid and 402 when the token or the card declined.
type AgentPayment struct {
	// Status is paid, declined or authentication_required.
	Status string `json:"status"`
	// Error is why it was not paid.
	Error *AgentError `json:"error,omitempty"`
	// Invoice is the invoice as it stands.
	Invoice *Invoice `json:"invoice,omitempty"`
}

// StatusCode is 200 for a paid invoice and 402 for any other outcome.
func (p *AgentPayment) StatusCode() int {
	if p.Status == "paid" {
		return 200
	}
	return 402
}

// TokenSupplied is the result of replacing the token a subscription renews on. It is
// answered 200 when the token covers the next renewal and 402 when it was declined.
type TokenSupplied struct {
	// Status is ok or declined.
	Status string `json:"status"`
	// Error is why the token was declined.
	Error *AgentError `json:"error,omitempty"`
	// Token is what is left on the token now.
	Token *TokenRoom `json:"token,omitempty"`
}

// StatusCode is 200 for an accepted token and 402 for a declined one.
func (s *TokenSupplied) StatusCode() int {
	if s.Status == "declined" {
		return 402
	}
	return 200
}

// WireReconcileIn credits the wallet that holds a wire's reference with the cents the
// bank received. By is the SuperAdmin attesting the receipt.
type WireReconcileIn struct {
	Reference     string `json:"reference"`
	AmountCents   int64  `json:"amountCents"`
	BankReference string `json:"bankReference,omitempty"`
	By            string `json:"by"`
}

// WireCredit is the outcome of reconciling a wire.
type WireCredit struct {
	// Reference is the wire's reference.
	Reference string `json:"reference"`
	// Subject is the wallet it credits.
	Subject string `json:"subject"`
	// AmountCents is what the bank received.
	AmountCents int64 `json:"amountCents"`
	// Credited is true for the call that posted the credit; a repeat answers false and posts nothing.
	Credited bool `json:"credited"`
}

