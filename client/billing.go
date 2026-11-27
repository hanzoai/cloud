// billing.go is the billing capability's half of the contract: the questions a
// customer's money endpoint asks, answered by the process that holds the
// merchant store.
//
// The two are not the same app and cannot be. `/v1/billing` belongs to the
// billing capability (HIP-0018 carries `capability: billing`), and the rows
// behind it — invoices, subscriptions, saved cards, spend caps, credit grants —
// live in commerce's per-tenant datastore, which has ONE owner. So billing serves
// the address and commerce answers the question, and these are the questions.
//
// Every rule in peer.go's package doc applies here without exception. There is
// no Org field anywhere below: the tenant rides the caller. A SUBJECT does
// travel on the inputs that need one, and that is not a loophole — a billing
// subject is a wallet INSIDE the ledger the caller's identity already pinned, so
// naming one can widen a read to another account of the caller's own org and to
// nothing else. The endpoint resolves it server-side (apps/billing's payer) and
// a client-supplied one never reaches here.
//
// The outputs carry the json tags the HTTP surface publishes, because they ARE
// that surface: billing answers with the value it was handed rather than
// re-rendering it. One shape, declared once, so the wire a customer reads cannot
// drift from the wire commerce produced.

package client

// ---- billing.invoices — raise, issue, collect, void ------------------------

const (
	// BillingInvoices lists the caller's invoices.
	BillingInvoices = "billing_invoices"

	// The lifecycle. Raising and issuing are separate acts on purpose: a raised
	// invoice is a DRAFT and is not collectible, and issuing is what turns it
	// into a demand for payment and gives it its number. An op that did both at
	// once could never let a human read a draft before it went out.
	BillingInvoiceRaise   = "billing_invoice_raise"
	BillingInvoiceRead    = "billing_invoice_read"
	BillingInvoiceIssue   = "billing_invoice_issue"
	BillingInvoiceCollect = "billing_invoice_collect"
	// BillingInvoicePay pays an invoice with the buyer present.
	BillingInvoicePay  = "billing_invoice_pay"
	BillingInvoiceVoid = "billing_invoice_void"

	// BillingInvoicePDF renders one invoice as an attachment. It is the one op
	// on this plane whose answer is not a JSON value, and it is here anyway
	// because the render IS a value: a bounded, single-page, deterministic byte
	// slice plus the name it is offered under. Leaving it out would have cost
	// the download its address for the sake of a rule about shapes.
	BillingInvoicePDF = "billing_invoice_pdf"
)

// Document is a rendered attachment: the bytes and the name they are offered
// under. The name travels with them because it is derived from the row, and a
// reader that rebuilt it would need its own copy of the rule.
type Document struct {
	Filename string `json:"filename"`
	Body     []byte `json:"body"`
}

// InvoiceLine is one charge on an invoice.
type InvoiceLine struct {
	// Description is the human-readable line, e.g. "Advisory retainer — August".
	Description string `json:"description"`
	// Amount is the line total in whole cents (250000 is $2,500.00).
	Amount int64 `json:"amount"`
	// Quantity is the number of units, when the line is metered. Optional.
	Quantity int64 `json:"quantity,omitempty"`
	// UnitPrice is the per-unit price in cents, when the line is metered. Optional.
	UnitPrice int64 `json:"unitPrice,omitempty"`
}

// RaiseIn is a draft invoice to raise against a customer.
type RaiseIn struct {
	// UserID identifies the customer being billed, within the caller's own org.
	// Required — an invoice with no addressee is not an invoice.
	UserID string `json:"userId"`
	// CustomerEmail is where the invoice is sent. Optional.
	CustomerEmail string `json:"customerEmail,omitempty"`
	// Currency is the ISO 4217 code, lower-cased. Empty means usd.
	Currency string `json:"currency,omitempty"`
	// Lines are the charges. The invoice subtotal and amount due are COMPUTED
	// from these — there is no total field to send, because a total that
	// disagreed with its own lines would bill a number nobody could derive.
	Lines []InvoiceLine `json:"lines,omitempty"`
}

// InvoiceRef names one invoice to act on.
type InvoiceRef struct {
	// ID is the invoice id.
	ID string `json:"id"`
	// Brand is the brand that bills (hanzo, lux, zoo, pars), whose mark and
	// address a rendered invoice carries. The edge sets it from the caller's
	// issuer; empty is Hanzo.
	Brand string `json:"brand,omitempty"`
}

// Invoice is an invoice.
type Invoice struct {
	// ID is the invoice id — what the issue, collect and void ops address.
	ID string `json:"id"`
	// Number is the human-facing invoice number, e.g. "INV-0042". A draft has
	// none; issuing assigns it.
	Number string `json:"number,omitempty"`
	// UserID is the customer billed.
	UserID string `json:"userId"`
	// CustomerEmail is where it is sent.
	CustomerEmail string `json:"customerEmail,omitempty"`
	// Status is draft, open, paid, void or uncollectible. A draft is not
	// collectible; issuing moves it to open.
	Status string `json:"status"`
	// Currency is the ISO 4217 code.
	Currency string `json:"currency"`
	// SubtotalCents is the sum of the lines.
	SubtotalCents int64 `json:"subtotalCents"`
	// AmountDueCents is what remains collectible.
	AmountDueCents int64 `json:"amountDueCents"`
	// AmountPaidCents is what has been collected so far.
	AmountPaidCents int64 `json:"amountPaidCents"`
	// Lines are the charges on the invoice.
	Lines []InvoiceLine `json:"lines,omitempty"`
	// PaymentRef is the processor reference for the collection, once paid.
	PaymentRef string `json:"paymentRef,omitempty"`
	// CreatedAt is when the draft was raised, RFC3339.
	CreatedAt string `json:"createdAt,omitempty"`
	// Quote is the invoice's price in the buyer's own currency: the bound quote once
	// paid, else the quote an awaiting-bank attempt is bound to, else the displayed
	// price of what is due. Absent in the plan's currency.
	Quote *PriceQuote `json:"quote,omitempty"`
}

// AuthenticationStep is what the buyer must finish for a card the bank asks to
// authenticate.
type AuthenticationStep struct {
	// Code is authentication_required.
	Code string `json:"code"`
	// ID is the payment intent to finish.
	ID string `json:"id"`
	// ClientSecret finishes it in the browser.
	ClientSecret string `json:"clientSecret"`
	// PublishableKey is the key to finish it with.
	PublishableKey string `json:"publishableKey"`
	// Quote is the price the buyer is authenticating at.
	Quote *PriceQuote `json:"quote,omitempty"`
	// Message says why.
	Message string `json:"message,omitempty"`
}

// InvoicePayment is the answer to paying an invoice with the buyer present: exactly
// one of paid, authentication_required (Authentication is set) or declined (Reason).
type InvoicePayment struct {
	// Status is paid, authentication_required or declined.
	Status string `json:"status"`
	// Invoice is the invoice after the attempt.
	Invoice *Invoice `json:"invoice"`
	// Authentication is set when Status is authentication_required.
	Authentication *AuthenticationStep `json:"authentication,omitempty"`
	// Reason is set when Status is declined.
	Reason string `json:"reason,omitempty"`
}

// Collected is the outcome of attempting to collect an invoice.
type Collected struct {
	// Invoice is the invoice AFTER the attempt — its status is the authority on
	// what happened, not this struct's other fields.
	Invoice *Invoice `json:"invoice"`
	// Paid reports whether the invoice is now settled in full. A false here with
	// no error is a DECLINE: the invoice stays open and may be collected again.
	Paid bool `json:"paid"`
	// CreditUsedCents is how much was covered by credit grants.
	CreditUsedCents int64 `json:"creditUsedCents"`
	// BalanceUsedCents is how much was covered by prepaid balance.
	BalanceUsedCents int64 `json:"balanceUsedCents"`
	// CardChargedCents is how much was charged to the card on file.
	CardChargedCents int64 `json:"cardChargedCents"`
	// ProcessorRef is the processor's reference for any card charge — the field
	// that proves money moved at the gateway rather than only in our ledger.
	ProcessorRef string `json:"processorRef,omitempty"`
	// Reason explains a decline or partial collection. Empty on success.
	Reason string `json:"reason,omitempty"`
}

// ---- billing.invoices — the listing ---------------------------------------

// InvoicesIn selects which of the caller's invoices to list. Every field
// narrows and an empty one does not filter, which is the meaning an absent
// query parameter has always had on the endpoint this shares its query with.
//
// Subject is the billing subject the endpoint resolved for the caller — a wallet
// inside the caller's own org, never an org.
//
// The list is paged: Limit is the page size (absent answers the default, past
// the max answers the max) and Cursor is the position a previous page returned.
type InvoicesIn struct {
	Subject        string `json:"subject,omitempty"`
	Status         string `json:"status,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
	Limit          int    `json:"limit,omitempty"`
	Cursor         string `json:"cursor,omitempty"`
}

// Invoices is the listing the customer's invoice page renders. It carries the
// count beside the rows because that is the wire commerce publishes and a reader
// deriving it would be deriving a number the store already has, and the cursor
// beside them when a full page answered, because that is the one shape that can
// hold a next page.
type Invoices struct {
	Rows   []BillingInvoice `json:"invoices"`
	Count  int              `json:"count"`
	Cursor string           `json:"cursor,omitempty"`
}

// BillingInvoice is one row of that listing — commerce's own projection of an
// invoice, which carries the billing period, the tax and discount lines and the
// attempt count that [Invoice] does not, because it answers a different question
// ("what have I been billed") from the lifecycle's ("what is on this invoice").
// The two are not folded together: shapes that overlap are still two shapes, and
// merging them would change one wire to tidy the other.
//
// Every timestamp is RFC3339 text rather than a time value. A time.Time crosses
// this plane as an EMPTY struct — its fields are unexported, and unexported
// fields cannot be read by reflection — so a date sent that way arrives as the
// zero instant with nothing reporting a loss. Text is the shape that survives,
// and it renders byte-identically to what a time.Time marshals to.
//
// The name carries its product for the reason [BillingAccount] does: the schema
// namespace is FLAT across the fleet, and the bare row name is already
// apps/admin's — an operator's invoice board line, a different shape answering a
// different question. The published side keeps the name; this one qualifies.
type BillingInvoice struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	CustomerEmail  string `json:"customerEmail"`
	SubscriptionID string `json:"subscriptionId"`
	PeriodStart    string `json:"periodStart"`
	PeriodEnd      string `json:"periodEnd"`
	Subtotal       int64  `json:"subtotal"`
	Tax            int64  `json:"tax"`
	Discount       int64  `json:"discount"`
	CreditApplied  int64  `json:"creditApplied"`
	AmountDue      int64  `json:"amountDue"`
	AmountPaid     int64  `json:"amountPaid"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	PaymentMethod  string `json:"paymentMethod"`
	PaymentRef     string `json:"paymentRef"`
	Number         int    `json:"number"`
	NumberStr      string `json:"numberStr"`
	AttemptCount   int    `json:"attemptCount"`
	// LineItems carries no omitempty and is never allocated empty, because the
	// wire it reproduces sends `null` for an invoice with no lines. An empty
	// array there would be a different answer to "were there lines".
	LineItems []InvoiceLineItem `json:"lineItems"`
	CreatedAt string            `json:"createdAt"`
	UpdatedAt string            `json:"updatedAt"`
	DueDate   string            `json:"dueDate,omitempty"`
	PaidAt    string            `json:"paidAt,omitempty"`
	VoidedAt  string            `json:"voidedAt,omitempty"`
	// DiscountName names what Discount took off ("Customer price: $195.00/month",
	// a promo, a coupon's month). Last, for the ZAP wire.
	DiscountName string `json:"discountName,omitempty"`
}

// InvoiceLineItem is one line of a billed invoice — a usage line, a subscription
// line, or a one-off charge, told apart by Type.
type InvoiceLineItem struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	MeterID     string `json:"meterId,omitempty"`
	Quantity    int64  `json:"quantity,omitempty"`
	UnitPrice   int64  `json:"unitPrice,omitempty"`
	PlanID      string `json:"planId,omitempty"`
	PlanName    string `json:"planName,omitempty"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	// The billed period. Both carry omitempty and neither is ever empty, which
	// looks contradictory and is not: the shape they reproduce is a time value,
	// and omitempty does nothing to a struct — so those keys render even for the
	// zero instant. The adapter formats the zero instant rather than skipping it,
	// which is what keeps the two wires the same.
	PeriodStart string `json:"periodStart,omitempty"`
	PeriodEnd   string `json:"periodEnd,omitempty"`
}


// RouteIn asks which processor takes a subject's NEW card checkout. Subject is
// resolved at the billing endpoint from the caller's credential; Country is the
// buyer's, else the edge's geo country.
type RouteIn struct {
	Subject string `json:"subject"`
	Country string `json:"country,omitempty"`
	// Currency is what the checkout is paid in; usd when absent. A processor takes a
	// checkout only in a currency one of its merchant accounts charges in.
	Currency string `json:"currency,omitempty"`
	// AmountCents, when given, asks for the quote of a local-currency payment of that
	// many dollars of credit.
	AmountCents int64 `json:"amountCents,omitempty"`
	// Method is how the buyer chose to pay, when they have (card, cash_app, pix, ...).
	Method string `json:"method,omitempty"`
}

// RouteStep is one step of the reason chain a routing decision records.
type RouteStep struct {
	// Step is saved, eligibility, method, overrides, sticky, weight or fallback.
	Step string `json:"step"`
	// Applied says the step decided, or narrowed, the route.
	Applied bool `json:"applied"`
	// Why says what the step found.
	Why string `json:"why"`
}

// PriceQuote is what a local-currency payment will cost, disclosed before the buyer
// authorizes. It names no processor: Source says where the rate came from (processor,
// reference or none) and FXFee who bears the conversion cost (pass or absorb).
type PriceQuote struct {
	// Amount is the price in the currency's minor unit; render Amount/10^Decimals.
	Amount int64 `json:"amount"`
	// Decimals is how many digits the minor unit is past the point.
	Decimals int `json:"decimals"`
	// Currency is the lower-case ISO 4217 code the buyer pays in.
	Currency string `json:"currency"`
	// UsdCents is the credit the payment buys, exactly.
	UsdCents int64 `json:"usdCents"`
	// Rate is the rate the amount was made at, per major unit.
	Rate float64 `json:"rate"`
	// Source is where Rate came from: processor, reference or none.
	Source string `json:"source"`
	// FXFee says who bears the conversion cost: pass (in the rate) or absorb.
	FXFee string `json:"fxFee"`
	// ExchangeRate is the processor's effective rate, when the quote is the processor's.
	ExchangeRate float64 `json:"exchangeRate,omitempty"`
	// LockExpiresAt is when the processor's lock lapses, in unix seconds.
	LockExpiresAt int64 `json:"lockExpiresAt,omitempty"`
	// QuoteID is the processor's locked quote the payment is bound to; absent for a
	// price shown and not yet bound.
	QuoteID string `json:"quoteId,omitempty"`
	// ReferenceRate is the market reference the processor's rate was measured against.
	ReferenceRate float64 `json:"referenceRate,omitempty"`
	// ReferenceProvider names the source of ReferenceRate.
	ReferenceProvider string `json:"referenceProvider,omitempty"`
	// BaseRate is the processor's rate before its fee and premium.
	BaseRate float64 `json:"baseRate,omitempty"`
	// FeeRate is the processor's conversion fee, as a fraction.
	FeeRate float64 `json:"feeRate,omitempty"`
	// Premium is the processor's lock premium, as a fraction.
	Premium float64 `json:"premium,omitempty"`
	// Increment is the minor-unit step the amount was rounded up to.
	Increment int64 `json:"increment"`
	// Disclosure is the sentence the buyer is shown with the price.
	Disclosure string `json:"disclosure"`
}

// RoutingCryptoOffer is one asset a buyer may pay in and who takes it.
type RoutingCryptoOffer struct {
	// Asset is the coin or token.
	Asset string `json:"asset"`
	// Processor takes it.
	Processor string `json:"processor"`
	// Currency is the currency the payment is presented in; stablecoins are usd only.
	Currency string `json:"currency"`
	// Networks are the networks it is taken on, when the processor names them.
	Networks []string `json:"networks,omitempty"`
}

// Route is the answer: the processor that takes the checkout, why, and the
// methods its card slot mounts. A buyer with a saved card stays on that card's
// processor.
type Route struct {
	// Processor is the processor that takes the checkout.
	Processor string `json:"processor"`
	// Reason is why: saved, method, currency, country, issuer, brand, assigned, split,
	// merchant, toggle, fallback or none-eligible.
	Reason string `json:"reason"`
	// Methods are what its card slot mounts.
	Methods []string `json:"methods"`
	// Country is the buyer's country the answer used.
	Country string `json:"country"`
	// Rule names the rule that decided, in the vocabulary of Reason.
	Rule string `json:"rule"`
	// Chain is the seven steps in the order they are taken.
	Chain []RouteStep `json:"chain"`
	// LocalCurrency is the currency a local-method payment would be quoted in.
	LocalCurrency string `json:"localCurrency"`
	// Version is the policy version the decision was made under.
	Version int `json:"version"`
	// Quote is the price of a local-currency payment, when one was asked for.
	Quote *PriceQuote `json:"quote,omitempty"`
	// Crypto says, per asset the crypto rails name, who takes it for this buyer, in the order
	// the policy chose, the currency it is presented in and the networks it is taken on.
	Crypto []RoutingCryptoOffer `json:"crypto"`
	// Alternatives are the other ways this buyer may pay, each its own button beside
	// the card slot, offered only where each can take money now.
	Alternatives []string `json:"alternatives"`
}

// BillingAddress is the buyer's billing address as they typed it in the payment
// form. Country is an ISO 3166-1 alpha-2 code.
type BillingAddress struct {
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

// TopupIntentIn opens a payment-element checkout for a top-up of AmountCents US
// dollars that the buyer pays with their country's native methods.
type TopupIntentIn struct {
	Subject        string `json:"subject"`
	Country        string `json:"country,omitempty"`
	AmountCents    int64  `json:"amountCents"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Currency is the currency the buyer chose to pay in; empty takes the country's
	// default. A currency the merchant or the chosen method cannot take is refused.
	Currency string `json:"currency,omitempty"`
	// Method is the method the buyer chose, when they have.
	Method string `json:"method,omitempty"`
	// Name and Email are the caller's own, off the identity IAM holds for them;
	// Phone and Billing are what the buyer typed in the payment form.
	Name    string          `json:"name,omitempty"`
	Email   string          `json:"email,omitempty"`
	Phone   string          `json:"phone,omitempty"`
	Billing *BillingAddress `json:"billing,omitempty"`
	// Optional plan fields when paying a plan's first period via bank transfer (ACH).
	PlanID   string `json:"planId,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Level    int    `json:"level,omitempty"`
	Interval string `json:"interval,omitempty"`
	Coupon   string `json:"coupon,omitempty"`
}

// TopupIntentOut is what the browser mounts the payment element with. Quote is the
// disclosure the buyer authorizes: Amount of Currency buys UsdCents of credit, and the
// amount confirm charges is Quote.Amount.
type TopupIntentOut struct {
	// IntentID is the payment's id.
	IntentID string `json:"intentId"`
	// ClientSecret mounts the payment element.
	ClientSecret string `json:"clientSecret"`
	// PublishableKey is the public key the element is mounted with.
	PublishableKey string `json:"publishableKey"`
	// Currency is what the buyer pays in.
	Currency string `json:"currency"`
	// Amount is the price in the currency's minor unit.
	Amount int64 `json:"amount"`
	// UsdCents is the credit the payment buys.
	UsdCents int64 `json:"usdCents"`
	// Rate is the rate the amount was made at, per major unit.
	Rate float64 `json:"rate"`
	// Quote is the disclosure the buyer authorizes.
	Quote PriceQuote `json:"quote"`
	// PolicyVersion is the routing policy version the payment was opened under.
	PolicyVersion int `json:"policyVersion"`
}

// TopupIntentRef names one of a subject's own payments.
type TopupIntentRef struct {
	Subject string `json:"subject"`
	ID      string `json:"id"`
}

// TopupIntentState is where a payment stands: created, pending, succeeded or
// canceled. Credited says the wallet holds the money; only it means paid.
type TopupIntentState struct {
	// State is created, pending, succeeded or canceled.
	State string `json:"state"`
	// Credited says the wallet holds the money.
	Credited bool `json:"credited"`
	// PaymentID is the receipt of the credit, set once Credited.
	PaymentID string `json:"paymentId,omitempty"`
	// SubscriptionID is set when a plan-bound bank transfer settles and opens the plan.
	SubscriptionID string `json:"subscriptionId,omitempty"`
}

// TopupIntentCancelled is the answer to cancelling a payment: it is canceled, whether
// this call ended it or an earlier one did.
type TopupIntentCancelled struct {
	IntentID string `json:"intentId"`
	// State is canceled.
	State string `json:"state"`
}


// QuoteIn asks the price of what a buyer is about to buy, before any sale: a plan's
// first charge, or an amount of credit. Subject is resolved at the billing endpoint.
type QuoteIn struct {
	Subject string `json:"subject"`
	// Plan is the plan to price; empty when UsdCents asks for a top-up.
	Plan string `json:"plan,omitempty"`
	// Level and Interval select the plan's published price.
	Level    int    `json:"level,omitempty"`
	Interval string `json:"interval,omitempty"`
	// UsdCents asks for an amount of credit when no plan is named.
	UsdCents int64 `json:"usdCents,omitempty"`
	// Country is the buyer's, else the edge's geo country.
	Country string `json:"country,omitempty"`
	// Currency is the currency the buyer chose; empty takes the country's default.
	Currency string `json:"currency,omitempty"`
	// Method is the method the buyer chose, when they have.
	Method string `json:"method,omitempty"`
}

// RoutingProcessorRule is one processor in the policy: whether it takes new checkouts
// and its share of the split.
type RoutingProcessorRule struct {
	// Processor names the processor, or the other ways to pay (paypal, crypto).
	Processor string `json:"processor"`
	// On takes the processor in or out of new checkouts.
	On bool `json:"on"`
	// Weight is its share of the buyers eligible for it; only proportions count.
	Weight int `json:"weight"`
}

// RoutingAccount is one merchant account held with a processor.
type RoutingAccount struct {
	// Processor is the processor the account is with.
	Processor string `json:"processor"`
	// ID is the processor's id for the account.
	ID string `json:"id"`
	// Country is where the account is; information, never a condition on a buyer.
	Country string `json:"country,omitempty"`
	// Currency is what the account charges in.
	Currency string `json:"currency,omitempty"`
	// Cards says the account takes card payments.
	Cards bool `json:"cards"`
	// On takes the account in or out of checkouts without deleting it.
	On bool `json:"on"`
	// Network is the chain a deposit account watches.
	Network string `json:"network,omitempty"`
	// Asset is the coin or token the account takes.
	Asset string `json:"asset,omitempty"`
	// Token is the token's contract on the network.
	Token string `json:"token,omitempty"`
	// Endpoint is where the account is reached.
	Endpoint string `json:"endpoint,omitempty"`
	// Address is where a deposit is sent.
	Address string `json:"address,omitempty"`
	// Confirmations is how many blocks a deposit waits for.
	Confirmations int `json:"confirmations,omitempty"`
	// FeeCents is the account's fee in cents.
	FeeCents int64 `json:"feeCents,omitempty"`
	// SlippageBps is the price movement tolerated, in basis points.
	SlippageBps int `json:"slippageBps,omitempty"`
	// SecretRef is a KMS path. It is never a secret, and cloud never reads what it names.
	SecretRef string `json:"secretRef,omitempty"`
	// Endpoints are the account's other named endpoints.
	Endpoints []RoutingNamed `json:"endpoints,omitempty"`
	// Secrets are the account's named credentials; each value is a KMS path, never a secret.
	Secrets []RoutingNamed `json:"secrets,omitempty"`
	// Settings are the account's other plain configuration (environment, location, public
	// keys, webhook URL), by name.
	Settings []RoutingNamed `json:"settings,omitempty"`
}

// RoutingNamed is a name and its value, in a list because the plane holds no maps.
type RoutingNamed struct {
	// Name says what the value is.
	Name string `json:"name"`
	// Value is the setting; for a secret, a KMS path.
	Value string `json:"value"`
}

// RoutingCryptoRail sends payments in an asset ("*" for any) to a processor.
type RoutingCryptoRail struct {
	// Asset is the coin or token, or "*" for any.
	Asset string `json:"asset"`
	// Processor takes it.
	Processor string `json:"processor"`
}

// RoutingCurrencyRule sends a checkout in a currency to a processor.
type RoutingCurrencyRule struct {
	// Currency is the checkout currency.
	Currency string `json:"currency"`
	// Processor takes it.
	Processor string `json:"processor"`
}

// RoutingCountryRule sends buyers in a country to a processor.
type RoutingCountryRule struct {
	// Country is the buyer's country; a preference and never a condition of eligibility.
	Country string `json:"country"`
	// Processor takes them.
	Processor string `json:"processor"`
}

// RoutingBrandRule sends a card brand to a processor; applies when the brand is known.
type RoutingBrandRule struct {
	// Brand is visa, mastercard, amex, discover, jcb, unionpay or diners.
	Brand string `json:"brand"`
	// Processor takes it.
	Processor string `json:"processor"`
}

// RoutingIssuerRule sends cards issued in a country to a processor; applies when the
// issuing country is known.
type RoutingIssuerRule struct {
	// Country is the issuing country.
	Country string `json:"country"`
	// Processor takes the card.
	Processor string `json:"processor"`
}

// RoutingRounding sets the increment a currency's local price is rounded up to.
type RoutingRounding struct {
	// Currency is the currency.
	Currency string `json:"currency"`
	// Increment is the step in major units.
	Increment float64 `json:"increment"`
}

// RoutingLocalCurrency sets the currency a buyer in a country is quoted local methods in.
type RoutingLocalCurrency struct {
	// Country is the buyer's country.
	Country string `json:"country"`
	// Currency is the default; usd switches local currency off for the country.
	Currency string `json:"currency"`
}

// RoutingMethod is one payment method's switch.
type RoutingMethod struct {
	// Method names the payment method.
	Method string `json:"method"`
	// On says it is offered.
	On bool `json:"on"`
}

// RoutingPolicy is how an org's new card checkouts are divided and priced.
type RoutingPolicy struct {
	// Processors are the processors a checkout may be placed on, with their switches
	// and shares.
	Processors []RoutingProcessorRule `json:"processors"`
	// Accounts are the merchant accounts held with each processor.
	Accounts []RoutingAccount `json:"accounts"`
	// CryptoRails say, in order, which processor takes a crypto payment in an asset.
	CryptoRails []RoutingCryptoRail `json:"cryptoRails"`
	// LocalOn lets the payment element offer each country's native methods.
	LocalOn bool `json:"localOn"`
	// Sticky keeps a payer on the processor first chosen for them.
	Sticky bool `json:"sticky"`
	// CurrencyRules send a checkout in a currency to a processor.
	CurrencyRules []RoutingCurrencyRule `json:"currencyRules"`
	// CountryRules send buyers in a country to a processor.
	CountryRules []RoutingCountryRule `json:"countryRules"`
	// BrandRules send a card brand to a processor, when the brand is known.
	BrandRules []RoutingBrandRule `json:"brandRules"`
	// IssuerCountryRules send cards issued in a country to a processor, when known.
	IssuerCountryRules []RoutingIssuerRule `json:"issuerCountryRules"`
	// FXSource is where the rate comes from: processor or reference.
	FXSource string `json:"fxSource"`
	// FXFee says who bears the processor's conversion cost: pass or absorb.
	FXFee string `json:"fxFee"`
	// NoRounding turns off rounding a local price up to an increment.
	NoRounding bool `json:"noRounding"`
	// RoundingIncrements set the increment per currency.
	RoundingIncrements []RoutingRounding `json:"roundingIncrements"`
	// LocalCurrencies set the default local currency per country.
	LocalCurrencies []RoutingLocalCurrency `json:"localCurrencies"`
	// Fallback is the order of processors tried before a payment starts.
	Fallback []string `json:"fallback"`
	// Methods are the per-method switches; a method with no entry is on.
	Methods []RoutingMethod `json:"methods"`
}

// RoutingScope names whose policy to read: an org, or the platform default when empty.
type RoutingScope struct {
	// Org is the org whose policy to read; the platform's when empty.
	Org string `json:"org,omitempty"`
}

// RoutingSetIn states a policy for an org, or for the platform when Org is empty.
type RoutingSetIn struct {
	// Org is the org the policy is for; the platform's default when empty.
	Org string `json:"org,omitempty"`
	// Policy is the statement; it replaces the stored one whole and becomes a new version.
	Policy RoutingPolicy `json:"policy"`
	// Note says why, for the version's history.
	Note string `json:"note,omitempty"`
	// By names the SuperAdmin who made the change, for the record.
	By string `json:"by,omitempty"`
}

// RoutingCount is how many of the recent checkouts a processor took.
type RoutingCount struct {
	// Processor is a processor the policy lists.
	Processor string `json:"processor"`
	// Count is how many of the recent decisions it took.
	Count int `json:"count"`
}

// RoutingRecent is one recent routing decision.
type RoutingRecent struct {
	// At is when it was decided, UTC.
	At string `json:"at"`
	// Org is the org whose buyer it was.
	Org string `json:"org"`
	// Processor is the processor that took the checkout.
	Processor string `json:"processor"`
	// Country is the buyer's country, empty when unknown.
	Country string `json:"country"`
	// Reason is why, in the vocabulary of Route.Reason.
	Reason string `json:"reason"`
}

// RoutingProcessor says whether a processor can take a payment now.
type RoutingProcessor struct {
	// Processor is a processor the policy lists.
	Processor string `json:"processor"`
	// Available says it is configured, allowed by the deployment and reachable.
	Available bool `json:"available"`
	// Kind is what sort of processor it is, as commerce reports it: card,
	// alternative, or custody (Kraken, MPC).
	Kind string `json:"kind,omitempty"`
	// Currencies are the ISO currencies it can charge in now.
	Currencies []string `json:"currencies,omitempty"`
}

// RoutingEligibility says which processors can take a currency now.
type RoutingEligibility struct {
	// Currency is the lower-case ISO 4217 code.
	Currency string `json:"currency"`
	// Processors are the processors whose merchant accounts can take it.
	Processors []string `json:"processors"`
}

// RoutingMethodNote says what a method's switch does and cannot do.
type RoutingMethodNote struct {
	// Method names the payment method.
	Method string `json:"method"`
	// Enforceable says the switch is honoured by the processor; when false the admin
	// shows the note and no switch.
	Enforceable bool `json:"enforceable"`
	// Note is the exact sentence to show.
	Note string `json:"note"`
}

// RoutingView is the platform admin's page: the policy, the default, and where the
// latest checkouts went.
type RoutingView struct {
	// Policy is the one in force for the scope asked.
	Policy RoutingPolicy `json:"policy"`
	// Default is the platform policy every org without its own inherits.
	Default RoutingPolicy `json:"default"`
	// Counts is how many of the latest decisions each listed processor took.
	Counts []RoutingCount `json:"counts"`
	// Recent is the newest decisions, newest first.
	Recent []RoutingRecent `json:"recent"`
	// Processors says which listed processors can take a payment now.
	Processors []RoutingProcessor `json:"processors"`
	// Accounts are the merchant accounts the processors report holding now.
	Accounts []RoutingAccount `json:"accounts"`
	// Eligibility is which processors can take which currency now.
	Eligibility []RoutingEligibility `json:"eligibility"`
	// Brands are the card brand names a rule may use.
	Brands []string `json:"brands"`
	// Methods are the payment method names a rule may use.
	Methods []string `json:"methods"`
	// MethodNotes label each method's switch.
	MethodNotes []RoutingMethodNote `json:"methodNotes"`
	// Problems are what keeps this policy from charging as configured: a processor that is on
	// and holds no account, or no way to charge a card at all. Empty when nothing does.
	Problems []string `json:"problems"`
}

// RoutingPreviewIn is a sample checkout the admin asks the effective route of.
type RoutingPreviewIn struct {
	// Org is the org whose policy applies; the platform's when empty.
	Org string `json:"org,omitempty"`
	// Subject, when given, makes the sample that payer (their saved card and placement count).
	Subject string `json:"subject,omitempty"`
	// Country is the buyer's country.
	Country string `json:"country,omitempty"`
	// Currency is the checkout currency; usd when empty.
	Currency string `json:"currency,omitempty"`
	// Method is the payment method chosen, when one is.
	Method string `json:"method,omitempty"`
	// Brand is the card brand, when known.
	Brand string `json:"brand,omitempty"`
	// IssuerCountry is the card's issuing country, when known.
	IssuerCountry string `json:"issuerCountry,omitempty"`
	// AmountCents asks for the quote of that many dollars of credit.
	AmountCents int64 `json:"amountCents,omitempty"`
}

// RoutingVersion is one saved version of a policy.
type RoutingVersion struct {
	// N is the version number.
	N int `json:"n"`
	// Active says it is the version in force.
	Active bool `json:"active"`
	// By is who saved it.
	By string `json:"by"`
	// At is when, UTC.
	At string `json:"at"`
	// Note is why.
	Note string `json:"note"`
	// Diff lists what changed from the version before it.
	Diff []string `json:"diff"`
	// Restored is the version this one restored, 0 when none.
	Restored int `json:"restored"`
	// Policy is the policy it holds.
	Policy RoutingPolicy `json:"policy"`
}

// RoutingVersions is a scope's history, newest first.
type RoutingVersions struct {
	// Versions are the saved versions.
	Versions []RoutingVersion `json:"versions"`
}

// RoutingRestoreIn makes an earlier version current as a new version.
type RoutingRestoreIn struct {
	// Org is the org; the platform's when empty.
	Org string `json:"org,omitempty"`
	// Version is the version to restore.
	Version int `json:"version"`
	// Note says why.
	Note string `json:"note,omitempty"`
	// By names the SuperAdmin, for the record.
	By string `json:"by,omitempty"`
}

// RoutingResetIn drops payers' placements so their next checkout is placed afresh.
type RoutingResetIn struct {
	// Org is the org whose placements to drop.
	Org string `json:"org"`
	// Subject, when given, drops only that payer's.
	Subject string `json:"subject,omitempty"`
	// By names the SuperAdmin, for the record.
	By string `json:"by,omitempty"`
}

// RoutingReset is how many placements were dropped.
type RoutingReset struct {
	// Placements is the count.
	Placements int `json:"placements"`
}
