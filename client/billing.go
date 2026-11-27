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

import (
	"encoding/json"
	"strings"
	"time"
)

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

// ---- billing.statement — accounts, payouts, transactions ------------------

const (
	// BillingAccounts and BillingAccountMembers answer the two questions the
	// team tab asks. Commerce keeps no membership roster — that is IAM's — so
	// the only member it can name is the caller, and the ops say so by taking
	// the caller's identity as values rather than pretending to a roster.
	BillingAccounts       = "billing_accounts"
	BillingAccountMembers = "billing_account_members"

	// BillingPayouts lists the org's outbound payouts.
	BillingPayouts = "billing_payouts"

	// BillingTransactions is one page of a subject's ledger, and
	// BillingTransaction is ONE entry of it by id — the member read of that same
	// collection rather than a second way to ask.
	//
	// It replaced a `getPayment` op that lived under the merchant product and read
	// exactly this row: commerce's ReadPayment loads a transaction by id and
	// refuses anything that is not a deposit. A top-up receipt is a ledger entry,
	// so it is read where ledger entries are read.
	BillingTransactions = "billing_transactions"
	BillingTransaction  = "billing_transaction"
)

// CallerIn carries the identity the endpoint already validated: who is asking,
// what they are called, and what standing they hold in the org.
//
// It is not a tenant claim and cannot become one — there is no org field, and
// the subject names a wallet inside the org the caller's own principal pinned.
// It travels because commerce cannot resolve it: the roster is IAM's, the
// request is the endpoint's, and a callee that guessed would be inventing a
// member.
type CallerIn struct {
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Role    string `json:"role,omitempty"`
}

// HoldersIn asks for one billing account's roster. Account is the account id the
// caller named; a foreign one is refused rather than filtered, so a guessed id
// is not an oracle for which accounts exist.
type HoldersIn struct {
	Account string `json:"account"`
	Subject string `json:"subject,omitempty"`
	Email   string `json:"email,omitempty"`
	Role    string `json:"role,omitempty"`
}

// BillingAccount is one billing account. In commerce an org IS its billing
// account, so the list has one row and both id fields hold the same value —
// which is the honest rendering of that fact rather than a shape that implies
// more.
//
// The name carries its product because the schema namespace is FLAT across the
// whole fleet and the bare noun is already taken: apps/books publishes an
// Account of its own — a chart-of-accounts line, a different thing entirely —
// and one name with two shapes is what openapi.Compose refuses, since a generated
// SDK would bind whichever it read last. The already-published side keeps the
// name; this one, arriving later, qualifies.
type BillingAccount struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	OrgID     string `json:"orgId"`
	OrgName   string `json:"orgName"`
	Currency  string `json:"currency"`
	CreatedAt string `json:"createdAt"`
	// Role is the caller's standing. Absent when nobody was named, so an
	// anonymous read returns an account rather than an implied membership.
	Role string `json:"role,omitempty"`
}

// Accounts is that list. It is a wrapper because a bare slice cannot cross this
// plane as a root value, and the endpoint unwraps it back to the array its wire
// has always been.
type Accounts struct {
	Rows []BillingAccount `json:"rows"`
}

// Holder is one person on a billing account. It is not [Member], which answers
// whether a subject holds a space row at all — a verdict rather than a
// person — and the two would be one word for two questions.
type Holder struct {
	ID      string `json:"id"`
	UserID  string `json:"userId"`
	Email   string `json:"email"`
	Role    string `json:"role"`
	AddedAt string `json:"addedAt"`
}

// Holders is the roster.
type Holders struct {
	Rows []Holder `json:"rows"`
}

// Payout is one outbound payout.
//
// FailureMessage is a POINTER where its siblings are values, and that is the
// wire being reproduced exactly rather than tidily: the shape emits the message
// key whenever a failure CODE is set, even when the message itself is empty. A
// plain string with omitempty would drop the key in that case, which reads as a
// failure with no explanation rather than one with an empty explanation.
type Payout struct {
	ID              string          `json:"id"`
	Amount          int64           `json:"amount"`
	Currency        string          `json:"currency"`
	Status          string          `json:"status"`
	DestinationType string          `json:"destinationType"`
	DestinationID   string          `json:"destinationId"`
	Created         string          `json:"created"`
	Description     string          `json:"description,omitempty"`
	ArrivalDate     string          `json:"arrivalDate,omitempty"`
	ProviderRef     string          `json:"providerRef,omitempty"`
	FailureCode     string          `json:"failureCode,omitempty"`
	FailureMessage  *string         `json:"failureMessage,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
}

// Payouts is the list.
type Payouts struct {
	Rows []Payout `json:"rows"`
}

// TransactionsIn is one page of a subject's ledger: whose, in what currency, and
// how much of it. An empty currency means every currency; a limit at or below
// zero takes the default page.
type TransactionsIn struct {
	Subject  string `json:"subject"`
	Currency string `json:"currency,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
}

// Transaction is one ledger entry as the customer's statement shows it.
type Transaction struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Amount    int64           `json:"amount"`
	Currency  string          `json:"currency"`
	Tags      string          `json:"tags,omitempty"`
	Notes     string          `json:"notes,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt string          `json:"createdAt"`
	ExpiresAt string          `json:"expiresAt,omitempty"`
}

// TransactionRef names one ledger entry inside the caller's own books. It carries
// no org and cannot: the books are the caller's, so a guessed id can only ever
// miss rather than reach another tenant's row.
type TransactionRef struct {
	ID string `json:"id"`
}

// Transactions is one page of that ledger. Count is the size of the WHOLE
// history rather than of the page — that difference is how a reader knows there
// is more to ask for — and User echoes the subject the page was read for, so a
// caller can see which wallet answered.
type Transactions struct {
	Rows  []Transaction `json:"transactions"`
	Count int           `json:"count"`
	User  string        `json:"user"`
}

// ---- billing.recharge — the customer's own auto-reload rule ---------------

const (
	// BillingAutoRecharge reads the org's auto-reload rule; BillingAutoRechargeSet
	// writes it. The SWEEP that acts on it is BillingRecharge, and the two are not
	// the same question: one is what the customer chose, the other is the schedule
	// spending it. Commerce ran the sweep for months with no reachable way to set a
	// threshold, so it recharged nobody.
	BillingAutoRecharge    = "billing_auto_recharge"
	BillingAutoRechargeSet = "billing_auto_recharge_set"
)

// AutoRecharge is one org's auto-reload rule as its readers see it: top the
// balance up by Amount whenever it falls below Threshold, charging the default
// card off-session. It is the "auto-reload" every prepaid AI account has.
//
// Stored says whether a rule was ever written. An org that has never set one
// reads as disabled with zeroes, which is the honest answer and is not the same
// fact as a rule someone deliberately turned off — a caller that must tell those
// apart reads this rather than inferring it from Enabled.
type AutoRecharge struct {
	Subject         string `json:"subject"`
	Enabled         bool   `json:"enabled"`
	ThresholdCents  int64  `json:"thresholdCents"`
	AmountCents     int64  `json:"amountCents"`
	Currency        string `json:"currency"`
	LastRechargedAt string `json:"lastRechargedAt,omitempty"`
	Stored          bool   `json:"stored"`
	// PerDay is the most times the sweep reloads the balance in one UTC day.
	PerDay int `json:"perDay"`
}

// AutoRechargeEdit is what a caller may change. It names no org and no subject:
// the rule is the caller's own, resolved from the validated principal, so there
// is no field a write could be steered through.
type AutoRechargeEdit struct {
	Enabled        bool   `json:"enabled"`
	ThresholdCents int64  `json:"thresholdCents"`
	AmountCents    int64  `json:"amountCents"`
	Currency       string `json:"currency,omitempty"`
}

// AutoRechargeIn names the payer whose auto-reload rule is read: the org's own
// account, or a person's wallet in it. The billing app resolves it from the
// validated principal and never from a request body.
type AutoRechargeIn struct {
	// Subject is the payer: the org's own account or "<org>/<name>".
	Subject string `json:"subject"`
}

// AutoRechargeSetIn is one payer's auto-reload rule as written: the payer, resolved
// as AutoRechargeIn's is, and the rule.
type AutoRechargeSetIn struct {
	// Subject is the payer: the org's own account or "<org>/<name>".
	Subject string `json:"subject"`
	// Edit is the whole rule.
	Edit AutoRechargeEdit `json:"edit"`
}

// ---- billing.credits — grants, balance, breakdown -------------------------

const (
	BillingCredits         = "billing_credits"
	BillingCreditBalance   = "billing_credit_balance"
	BillingCreditBreakdown = "billing_credit_breakdown"
)

// SubjectIn names one wallet inside the caller's own org. It is the input every
// read that is per-subject rather than per-org takes, so there is one spelling
// of "whose" on this plane rather than one per family.
type SubjectIn struct {
	Subject string `json:"subject"`
}

// CreditGrant is one grant — spent, expired and voided ones included, because a
// grant list is a LEDGER and one that hides its spent rows cannot be reconciled
// against a burn-down.
type CreditGrant struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	Name           string `json:"name"`
	AmountCents    int64  `json:"amountCents"`
	RemainingCents int64  `json:"remainingCents"`
	Currency       string `json:"currency"`
	Priority       int    `json:"priority"`
	EffectiveAt    string `json:"effectiveAt"`
	Tags           string `json:"tags"`
	Voided         bool   `json:"voided"`
	Active         bool   `json:"active"`
	CreatedAt      string `json:"createdAt"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
}

// CreditGrants is that list, with the count beside it — the envelope the grants
// page reads, carried whole rather than rebuilt at the endpoint.
type CreditGrants struct {
	Rows  []CreditGrant `json:"grants"`
	Count int           `json:"count"`
}

// CreditBalance is what a subject has left to spend, one entry per currency.
type CreditBalance struct {
	UserID   string        `json:"userId"`
	Balances []CreditEntry `json:"balances"`
}

// CreditEntry is one currency's share of that balance.
type CreditEntry struct {
	Currency  string `json:"currency"`
	Available int64  `json:"available"`
}

// CreditTag is one tag's cents and the earliest moment any of them lapse. An
// absent expiry means nothing under this tag expires.
type CreditTag struct {
	Tag       string `json:"-"`
	Cents     int64  `json:"cents"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// CreditTotal is the sum across every tag.
type CreditTotal struct {
	Cents int64 `json:"cents"`
}

// CreditBreakdown is the same balance split by grant tag — trial credit told
// apart from purchased, which is the question a chat surface asks before it
// spends any.
//
// It holds the tags as a SLICE and publishes them as an OBJECT, and the two
// halves are the point. A map cannot cross this plane: the wire is derived from
// the type, and a map has no fixed layout, so it would arrive empty with nothing
// reporting the loss. The published shape is keyed by tag, though, and changing
// it to an array would change every reader. So the slice is the transport and
// MarshalJSON is the renderer — written once, here, where both ends compile
// against it, rather than once per endpoint.
type CreditBreakdown struct {
	UserID string      `json:"userId"`
	Tags   []CreditTag `json:"-"`
	Total  CreditTotal `json:"total"`
}

// MarshalJSON renders the breakdown keyed by tag. Keys come out sorted, which is
// what a map renders as, so the bytes match the shape this replaced.
func (b CreditBreakdown) MarshalJSON() ([]byte, error) {
	type tag struct {
		Cents     int64  `json:"cents"`
		ExpiresAt string `json:"expiresAt,omitempty"`
	}
	by := make(map[string]tag, len(b.Tags))
	for _, t := range b.Tags {
		by[t.Tag] = tag{Cents: t.Cents, ExpiresAt: t.ExpiresAt}
	}
	return json.Marshal(struct {
		UserID    string         `json:"userId"`
		Breakdown map[string]tag `json:"breakdown"`
		Total     CreditTotal    `json:"total"`
	}{UserID: b.UserID, Breakdown: by, Total: b.Total})
}

// ---- billing.alerts — the customer's own spend caps -----------------------

const (
	BillingAlerts     = "billing_alerts"
	BillingAlertRaise = "billing_alert_raise"
	BillingAlertAmend = "billing_alert_amend"
	BillingAlertDrop  = "billing_alert_drop"

	// BillingCapAuthorize is the per-request verdict the metering edge consumes
	// on every priced call. It is separate from the CRUD for the reason the two
	// are separate addresses: one is a customer editing a budget, the other is a
	// gate asking whether this act fits inside it.
	BillingCapAuthorize = "billing_cap_authorize"
)

// Alert is one spend cap: the stored policy row plus the DERIVED period spend
// for its scope.
//
// PeriodSpentCents, Over and Warn are POINTERS because their absence carries
// meaning of its own. When the aggregation cannot be read the policy row is
// still reported, without derived spend, rather than failing the whole read — and
// a zero cannot say that, since "nothing spent" and "spend unknown" are different
// answers.
type Alert struct {
	ID           string `json:"id"`
	UserID       string `json:"userId"`
	Title        string `json:"title"`
	Threshold    int64  `json:"threshold"`
	Currency     string `json:"currency"`
	Project      string `json:"project"`
	Service      string `json:"service"`
	Enforce      bool   `json:"enforce"`
	SoftPct      int    `json:"softPct"`
	RateLimitRpm int    `json:"rateLimitRpm"`
	TriggeredAt  string `json:"triggeredAt"`
	Period       string `json:"period"`
	ResetsAt     string `json:"resetsAt"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`

	PeriodSpentCents *int64 `json:"periodSpentCents,omitempty"`
	Over             *bool  `json:"over,omitempty"`
	Warn             *bool  `json:"warn,omitempty"`
}

// Alerts is the org's caps.
type Alerts struct {
	Rows []Alert `json:"rows"`
}

// AlertSpec is a new cap. There is no subject field: the wallet a cap binds is
// the caller's own, resolved at the endpoint.
type AlertSpec struct {
	Subject      string `json:"subject,omitempty"`
	Title        string `json:"title"`
	Threshold    int64  `json:"threshold"`
	Currency     string `json:"currency"`
	Project      string `json:"project"`
	Service      string `json:"service"`
	Enforce      *bool  `json:"enforce"`
	SoftPct      int    `json:"softPct"`
	RateLimitRpm int    `json:"rateLimitRpm"`
}

// AlertPatch is a partial change: every mutable field is a POINTER so an absent
// one is preserved rather than reset. A change that only flips enforcement must
// not silently wipe the threshold or the rate limit.
type AlertPatch struct {
	Subject      string  `json:"subject,omitempty"`
	ID           string  `json:"id"`
	Title        *string `json:"title"`
	Threshold    *int64  `json:"threshold"`
	Project      *string `json:"project"`
	Service      *string `json:"service"`
	Enforce      *bool   `json:"enforce"`
	SoftPct      *int    `json:"softPct"`
	RateLimitRpm *int    `json:"rateLimitRpm"`
}

// AlertRef names one cap to drop.
type AlertRef struct {
	Subject string `json:"subject,omitempty"`
	ID      string `json:"id"`
}

// Dropped is what a delete answers with. It carries a field rather than being
// empty because a void reply and an empty one are told apart on this plane, and
// a delete that succeeded must not look like a peer that said nothing.
type Dropped struct {
	OK bool `json:"ok"`
}

// CapIn asks whether one proposed spend fits inside the org's caps.
//
// ProjectValidated says whether the project axis was ESTABLISHED by the endpoint
// rather than merely claimed. It travels because only the endpoint knows: a
// project-scoped enforce row whose axis is unvalidated must not block, and a
// callee that assumed validation would turn an unproven claim into a refusal.
type CapIn struct {
	Project          string `json:"project,omitempty"`
	Service          string `json:"service,omitempty"`
	Amount           int64  `json:"amount"`
	ProjectValidated bool   `json:"projectValidated,omitempty"`
}

// CapVerdict is that answer. It is not [Verdict] — that one is the prepaid
// gate's, about whether there is money — and the two are kept apart because they
// refuse for different reasons and a reader that confused them would report an
// unfunded account as an exceeded budget.
type CapVerdict struct {
	Allow      bool   `json:"allow"`
	Reason     string `json:"reason"`
	CapCents   int64  `json:"capCents"`
	SpentCents int64  `json:"spentCents"`
	WarnPct    int    `json:"warnPct"`
}

// ---- billing.crypto and billing.wire — the other two rails ----------------

const (
	BillingCryptoOptions = "billing_crypto_options"
	BillingCryptoMint    = "billing_crypto_mint"
	BillingCryptoDeposit = "billing_crypto_deposit"

	BillingWire = "billing_wire"
)

// CryptoOptions is the menu of what the crypto rails accept: the chains a payer
// may send to a deposit address on and the tokens they may send, the networks a
// signed USDC payment may be made on through Circle Gateway, and the pairs.
type CryptoOptions struct {
	Chains []string `json:"chains"`
	Tokens []string `json:"tokens"`
	// Assets is every (chain, token) pair the deposit-address rail accepts.
	// Chains and Tokens are its two axes: a chain and a token each on offer are
	// not a pair on offer, so a picker that crosses them can name an asset the
	// rail will refuse.
	Assets  []CryptoPair `json:"assets"`
	Gateway []string     `json:"gateway"`
}

// CryptoPair is one asset the deposit-address rail accepts: a token on a chain.
type CryptoPair struct {
	// Chain is the network, as a deposit names it ("base").
	Chain string `json:"chain"`
	// Token is the asset on that chain, as a deposit names it ("usdc").
	Token string `json:"token"`
}

// CryptoMintIn asks for a deposit address for one payer and one asset.
type CryptoMintIn struct {
	Payer       string `json:"payer"`
	Chain       string `json:"chain"`
	Token       string `json:"token"`
	AmountCents int64  `json:"amountCents,omitempty"`
	// PlanID, Quantity, Level, Interval and Coupon bind the deposit to a plan's first period.
	PlanID   string `json:"planId,omitempty"`
	Quantity int    `json:"quantity,omitempty"`
	Level    int    `json:"level,omitempty"`
	Interval string `json:"interval,omitempty"`
	Coupon   string `json:"coupon,omitempty"`
}

// CryptoDepositIn reads one intent back. A foreign id is a miss, not a refusal,
// so a guessed id cannot confirm that somebody else's deposit exists.
type CryptoDepositIn struct {
	Payer string `json:"payer"`
	ID    string `json:"id"`
}

// CryptoDeposit is one deposit intent: where to send, what is credited, and how
// far the chain has got.
//
// AddressTag is the routing tag a payer MUST send with the payment on a chain
// where the address is shared. omitempty is load-bearing and safe: the tag is
// decimal text, so the first tag ever issued is "0" — not empty, and therefore
// sent. A payment to a pooled address with no tag names nobody and is credited
// to nobody.
//
// The fields after ExpiresAt were added after it, and stay after it: this type
// crosses the plane by field position.
type CryptoDeposit struct {
	// ID names the intent; read it back at GET /v1/billing/crypto/deposit/{id}.
	ID string `json:"id"`
	// Status is pending until a transfer to the address is seen, confirming while
	// the chain buries it, and succeeded once it is credited as cash in the
	// payer's balance (GET /v1/billing/balance, cash).
	Status string `json:"status"`
	// Chain is the network to send on.
	Chain string `json:"chain"`
	// Token is the asset to send; this intent credits that token alone.
	Token string `json:"token"`
	// DepositAddress is where to send it.
	DepositAddress string `json:"depositAddress"`
	// AddressTag is the destination tag the payment must carry, on a chain whose
	// address is shared. Absent where the address alone names the payer.
	AddressTag string `json:"addressTag,omitempty"`
	// ExpiresAt is when this address stops being handed out again (RFC3339). A
	// transfer that arrives after it is still credited.
	ExpiresAt string `json:"expiresAt,omitempty"`
	// AmountCents is what the payer said they would send, in USD cents, when the
	// address was asked for: a record of intent, not a quote. What is credited is
	// what arrives. Absent when nothing was named.
	AmountCents int64 `json:"amountCents,omitempty"`
	// MinimumCents is the smallest deposit, in USD cents, that credits anything;
	// less credits nothing. Absent when nothing is watching this asset now, and
	// then nothing sent to it is credited.
	MinimumCents int64 `json:"minimumCents,omitempty"`
	// FeeCents is deducted from every deposit before it is credited: the cost of
	// sweeping it out of the custody address. Absent deducts nothing.
	FeeCents int64 `json:"feeCents,omitempty"`
	// RequiredConfirmations is how many blocks deep the transfer must be before it
	// is credited.
	RequiredConfirmations int `json:"requiredConfirmations"`
	// Confirmations is how deep the transfer being confirmed is now; zero until one
	// is seen.
	Confirmations int `json:"confirmations"`
	// CreditedCents is what the transfer this intent confirmed credited, in USD
	// cents, once it succeeded. A later transfer to the same address is credited
	// too, and shows in the balance rather than here.
	CreditedCents int64 `json:"creditedCents,omitempty"`
	// Instructions says, in one line, what to send where and when it is credited.
	Instructions string `json:"instructions"`
	// PlanID and SubscriptionID are set when the deposit pays a plan's first period.
	PlanID         string `json:"planId,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
}

// ---- billing.gateway — USDC through Circle Gateway ---------------------------

const (
	BillingGatewayTerms  = "billing_gateway_terms"
	BillingGatewaySubmit = "billing_gateway_submit"
	BillingGatewayRead   = "billing_gateway_read"
)

// GatewayTermsIn asks for payment terms for one payer. The nonce in the answer
// is issued to that payer and no other.
type GatewayTermsIn struct {
	Payer string `json:"payer"`
}

// GatewayField is one member of an EIP-712 struct type.
type GatewayField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// GatewayDomain is the EIP-712 domain a payment is signed under, in the shape
// viem and ethers sign with.
type GatewayDomain struct {
	Name              string `json:"name"`
	Version           string `json:"version"`
	ChainID           int64  `json:"chainId"`
	VerifyingContract string `json:"verifyingContract"`
}

// GatewayAccept is one network a payment may be signed on.
type GatewayAccept struct {
	Network     string        `json:"network"`
	Asset       string        `json:"asset"`
	Symbol      string        `json:"symbol"`
	Decimals    int           `json:"decimals"`
	MinAmount   string        `json:"minAmount"`
	MaxAmount   string        `json:"maxAmount"`
	MinValidity int64         `json:"minValiditySeconds"`
	Domain      GatewayDomain `json:"domain"`
}

// GatewayTerms is everything a payer needs to sign a USDC top-up: the treasury
// to pay, the networks, the EIP-712 domain and types, the limits, and a nonce
// issued to the caller with its salt.
//
// Types is raw JSON because a Go map cannot cross the plane (zapenc), while
// raw JSON can; the shape stays exactly the EIP-712 types map the pay page
// prints. The bytes are the map's own marshalling — encoding/json orders a
// map's keys, so they are deterministic and byte-identical to what the old
// map marshalled into. The fleet's precedent is crawl.metadata ("raw JSON
// publishes {} — 'any JSON', which is true; the bytes are identical either
// way").
type GatewayTerms struct {
	Version     int             `json:"x402Version"`
	Scheme      string          `json:"scheme"`
	PayTo       string          `json:"payTo"`
	Nonce       string          `json:"nonce"`
	Salt        string          `json:"salt"`
	MinCents    int64           `json:"minCents"`
	MaxCents    int64           `json:"maxCents"`
	MaxValidity int64           `json:"maxValiditySeconds"`
	Primary     string          `json:"primaryType"`
	Types       json.RawMessage `json:"types"`
	Accepts     []GatewayAccept `json:"accepts"`
}

// GatewayAuthorization is an EIP-3009 TransferWithAuthorization as signed.
type GatewayAuthorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// GatewaySubmitIn is one payer's signed payment.
type GatewaySubmitIn struct {
	Payer         string               `json:"payer"`
	Network       string               `json:"network"`
	Salt          string               `json:"salt"`
	Authorization GatewayAuthorization `json:"authorization"`
	Signature     string               `json:"signature"`
}

// GatewayReadIn reads one payment back. A foreign id is a miss, exactly as an
// id that names nothing.
type GatewayReadIn struct {
	Payer string `json:"payer"`
	ID    string `json:"id"`
}

// GatewayPayment is one USDC top-up and its fate: pending, accepted, settled,
// failed or refused. Credited means the money is in the payer's account;
// Deferred means it will be once Gateway confirms the payment onchain.
type GatewayPayment struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Network     string `json:"network"`
	From        string `json:"from"`
	Value       string `json:"value"`
	AmountCents int64  `json:"amountCents"`
	Credited    bool   `json:"credited"`
	Deferred    bool   `json:"deferred,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Payment     string `json:"payment,omitempty"`
	TxHash      string `json:"txHash,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

// WireIn asks for the receiving bank details a wire top-up should be sent to.
//
// Host is the address the customer is paying on. It travels as the HOST rather
// than as a brand slug because the table that reduces one to the other belongs
// to the store, and a caller that did the reduction would be keeping a second
// copy of which hostnames are whose. Payer goes into the payment reference,
// which is how an arriving wire names who it credits.
type WireIn struct {
	Host  string `json:"host"`
	Payer string `json:"payer"`
}

// WireInstructions is that account. It is all-or-nothing by design: a customer
// cannot wire to three fields out of five, so a half-empty form is refused
// upstream rather than rendered.
type WireInstructions struct {
	BankName      string `json:"bankName"`
	BankAddress   string `json:"bankAddress,omitempty"`
	AccountNumber string `json:"accountNumber"`
	RoutingNumber string `json:"routingNumber,omitempty"`
	SwiftCode     string `json:"swiftCode,omitempty"`
	IBAN          string `json:"iban,omitempty"`
	AccountName   string `json:"accountName"`
	Memo          string `json:"memo,omitempty"`
	Reference     string `json:"reference"`
}

// WireReference is the payment reference a payer is shown beside the bank details,
// and the one an arriving wire carries back on the bank statement: "TOPUP-" and the
// payer's billing key, upper-cased, every character outside [A-Z0-9] a hyphen
// ("hanzo/z" → "TOPUP-HANZO-Z"). Commerce renders it (api/billing GetWireInstructions);
// the admin console reads a statement's reference back to the account with it.
// apps/commerce TestWireReferenceIsCommerces holds the two renderings equal.
func WireReference(payer string) string {
	var b strings.Builder
	b.WriteString("TOPUP-")
	for _, r := range strings.ToUpper(payer) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// ---- billing.rate — what one unit of metered work costs ---------------------

// BillingRate answers what the platform charges for one unit of something it
// meters, from the authority an operator edits at admin.hanzo.ai.
//
// It is PLATFORM-GLOBAL and takes no subject. What a GB-month costs does not
// depend on who is storing the bytes — a plan may include some of it, and a
// wallet pays for the rest, but those are different questions asked elsewhere.
// So unlike tier and rollup this op needs no validated org, which is also what
// lets an app that is metering work ask it without one.
const BillingRate = "billing_rate"

// RateIn names the metered thing: the product it belongs to and what is metered
// inside it. Both are required — together they are the identity, and a rate keyed
// on the metered thing alone would let one product's price answer for another's.
type RateIn struct {
	Product string `json:"product"`
	Meter   string `json:"meter"`
}

// Rate is what one unit costs.
//
// FOUND IS NOT REDUNDANT WITH NANO. Zero is a real price — something the platform
// meters and gives away — and it must not read the same as "no rate is
// published", which is what sends a caller to its own floor instead. A bool
// carries the difference a number cannot.
type Rate struct {
	Product string `json:"product"`
	Meter   string `json:"meter"`
	// Unit is what ONE of the billed quantity is, so a reader can tell a
	// GB-month from a thousand characters without knowing the meter.
	Unit string `json:"unit,omitempty"`
	// Nano is the price of one Unit in nano-dollars, a billionth of a dollar.
	// Cents cannot hold these: fractions of a cent per million tokens round to
	// zero or to a hundred times the price.
	Nano  int64 `json:"nano"`
	Found bool  `json:"found"`
}

// ---- billing.entitlement — what an org's plan licenses ----------------------

// BillingEntitlement answers whether the calling org holds an active entitlement for
// a licensed product, and what its plan grants.
const BillingEntitlement = "billing_entitlement"

// EntitlementIn names the licensed product an org's entitlement is asked about.
type EntitlementIn struct {
	// Product is the licensed product: "engine", "team", a plugin id.
	Product string `json:"product"`
}

// Entitlement is whether an org holds an active entitlement for a product, and what
// its plan grants.
type Entitlement struct {
	// Product is the product asked about.
	Product string `json:"product"`
	// Active reports an entitlement that is paid and has not lapsed.
	Active bool `json:"active"`
	// Plan is the plan the entitlement resolved to ("pro", "max").
	Plan string `json:"plan,omitempty"`
	// Features are the plan's flat license features, copied into a license as they are.
	Features []string `json:"features,omitempty"`
	// Expires bounds the entitlement in unix seconds; 0 is unbounded.
	Expires int64 `json:"expires,omitempty"`
	// EarlyAccess is whether a live plan of the org carries early access, which opens
	// every product in beta to it without applying. It answers for the org, whatever
	// product was asked.
	EarlyAccess bool `json:"earlyAccess,omitempty"`
}

// ---- billing.tier and billing.rollup — what a plan allows, and what is left --

const (
	BillingTier   = "billing_tier"
	BillingRollup = "billing_rollup"
)

// TierLimits is what a tier allows. The fields are flat rather than nested
// because the shape they reproduce embeds its config struct, and an embedded
// struct marshals flat.
type TierLimits struct {
	Name              string   `json:"name"`
	DisplayName       string   `json:"displayName"`
	MaxAgents         int      `json:"maxAgents"`
	DailyCreditsCents int64    `json:"dailyCreditsCents"`
	AllowedModels     []string `json:"allowedModels"`
	// UnlimitedAgents reports that MaxAgents 0 means "no ceiling" rather than
	// "no agents" — the reading a bare zero cannot carry.
	UnlimitedAgents bool `json:"unlimitedAgents"`
}

// TierBalance is what the subject can actually spend.
//
// EffectiveAvailable is the ONLY figure a gate should compare against zero. The
// others are its parts: prepaid, credits and the daily term are three sources of
// one spend, not three balances to add up a second time.
type TierBalance struct {
	Currency           string `json:"currency"`
	PrepaidAvailable   int64  `json:"prepaidAvailable"`
	CreditsRemaining   int64  `json:"creditsRemaining"`
	DailyRemaining     int64  `json:"dailyRemaining"`
	EffectiveAvailable int64  `json:"effectiveAvailable"`
}

// TierIn asks for one subject's tier. The tier is derived from the subject's own
// subscriptions and is never named by a caller, a SuperAdmin included.
type TierIn struct {
	Subject string `json:"subject"`
}

// Tier is a subject's tier: which one they are on, what it allows, what they can
// spend, and how much of each plan window is left.
type Tier struct {
	User string `json:"user"`
	// Plan is the catalog rung the subject is served as ("" with no plan). A tier
	// is a class of rungs; a bound that differs within one class keys on this.
	Plan string `json:"plan,omitempty"`
	// Subscription is the id of the subscription row whose plan is the served rung,
	// "" when the tier is not a subscription's.
	Subscription string      `json:"subscription,omitempty"`
	Tier         TierLimits  `json:"tier"`
	Balance      TierBalance `json:"balance"`
	// PlanName is the display name of the served plan ("Unlimited"), "" with no
	// plan. A plan only the plan authority knows has no catalog name, so a reader
	// shows this, not the tier. Last, for the ZAP wire.
	PlanName string `json:"planName,omitempty"`
}

// RollupIn asks for one subject's month. Plan overrides which plan the figures
// are computed against; empty resolves the subject's own.
type RollupIn struct {
	Subject string `json:"subject"`
	Plan    string `json:"plan,omitempty"`
}

// RollupAllotment is the PLAN side of a month: what the plan includes, what has
// actually been granted, and what is left of it.
type RollupAllotment struct {
	MonthlyCents   int64 `json:"monthlyCents"`
	GrantedCents   int64 `json:"grantedCents"`
	ConsumedCents  int64 `json:"consumedCents"`
	RemainingCents int64 `json:"remainingCents"`
}

// RollupBalance is the WALLET side: prepaid money the holder bought.
type RollupBalance struct {
	BalanceCents   int64 `json:"balanceCents"`
	HoldsCents     int64 `json:"holdsCents"`
	AvailableCents int64 `json:"availableCents"`
}

// Rollup is a subject's month. The two blocks stay separate because they are
// separate monies — one was sold with the plan, one was bought with a card — and
// their sum is not a number anyone holds.
type Rollup struct {
	User          string          `json:"user"`
	Plan          string          `json:"plan"`
	Currency      string          `json:"currency"`
	Period        string          `json:"period"`
	Included      RollupAllotment `json:"included"`
	ConsumedCents int64           `json:"consumedCents"`
	OverageCents  int64           `json:"overageCents"`
	Balance       RollupBalance   `json:"balance"`
}

// ---- billing.settings and billing.mode — the org's processor posture --------

const (
	BillingSettings = "billing_settings"
	BillingMode     = "billing_mode"
)

// PayPalPublicConfig is the public half of an org's PayPal REST v2 posture —
// the client id the PayPal JS SDK loads with, and the environment ("live" or
// "sandbox") it transacts against. Never carries a secret.
type PayPalPublicConfig struct {
	Enabled     bool   `json:"enabled"`
	ClientID    string `json:"clientId,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// StripePublicConfig is the public half of an org's Stripe posture — the
// publishable key Stripe's card form mounts with and the environment it lives
// in. Enabled says some currency is paid through Stripe's card form here. Never
// carries a secret.
type StripePublicConfig struct {
	Enabled        bool   `json:"enabled"`
	PublishableKey string `json:"publishableKey,omitempty"`
	Environment    string `json:"environment,omitempty"`
}

// PaymentConfig is the PUBLIC half of an org's processor configuration — the ids
// a browser needs to tokenize a card, and the environment it must tokenize
// against. It carries no secret: an application id is published to every
// checkout page by design.
type PaymentConfig struct {
	Provider      string `json:"provider"`
	ApplicationID string `json:"applicationId"`
	LocationID    string `json:"locationId"`
	Environment   string `json:"environment"`
	Live          bool   `json:"live"`
	// PayPal is the public PayPal REST v2 posture alongside Square (or in place
	// of Square in countries Square does not serve). Appended last for ZAP wire.
	PayPal *PayPalPublicConfig `json:"paypal,omitempty"`
	// Stripe is the public Stripe.js posture, and Cards says per currency whose
	// card form takes it ("square" or "stripe"). A browser reads these two to pick
	// the form; without them it cannot tell Stripe is the processor. Appended last
	// for ZAP wire.
	Stripe *StripePublicConfig `json:"stripe,omitempty"`
	// Cards is the per-currency card form, as a slice because a map has no layout
	// on the plane. MarshalJSON renders the object the browser reads —
	// {"usd":"stripe"} — once, here, as CreditBreakdown renders its tags.
	Cards []PaymentCard `json:"-"`
}

// PaymentCard says whose card form takes one currency: Processor is "square" or
// "stripe". A currency no card processor takes has no entry.
type PaymentCard struct {
	Currency  string `json:"currency"`
	Processor string `json:"processor"`
}

// MarshalJSON renders Cards as the object checkout reads on
// GET /v1/billing/settings: {"cards": {"usd": "stripe"}}. Every other field keeps
// its tag.
func (c PaymentConfig) MarshalJSON() ([]byte, error) {
	type plain PaymentConfig
	var by map[string]string
	if len(c.Cards) > 0 {
		by = make(map[string]string, len(c.Cards))
		for _, e := range c.Cards {
			by[e.Currency] = e.Processor
		}
	}
	return json.Marshal(struct {
		plain
		Cards map[string]string `json:"cards,omitempty"`
	}{plain: plain(c), Cards: by})
}

// ModeIn moves an org between test and live money.
type ModeIn struct {
	TestMode bool `json:"testMode"`
}

// Mode is which money an org is transacting in. Live and TestMode are one fact
// stated twice, in the two vocabularies its readers use; they are always
// opposites, and the store is the single authority for both.
type Mode struct {
	OrgID    string `json:"orgId"`
	OrgName  string `json:"orgName"`
	Live     bool   `json:"live"`
	TestMode bool   `json:"testMode"`
}

// ---- billing.methods and billing.plans — two rendered documents -------------

const (
	BillingMethods       = "billing_methods"
	BillingMethodSave    = "billing_method_save"
	BillingMethodUpdate  = "billing_method_update"
	BillingMethodDefault = "billing_method_default"
	BillingMethodDetach  = "billing_method_detach"

	BillingPlans = "billing_plans"
)

// Rendered is a document the store produced and the endpoint serves without
// reading.
//
// It exists for the two families whose wire is a deep tree of the STORE'S OWN
// models — a saved payment method carries five kinds of processor detail plus a
// billing address, and a plan carries its limits and its licensing — where a
// mirror in this package would be sixty fields that only ever have to agree with
// something else. Carrying the rendered bytes is byte-exact by construction,
// which is the property a hand-kept mirror can only approximate.
//
// It is the same distinction the money endpoint already draws between its typed
// ledger read and its raw balance read, and it is deliberately narrow: a value
// the endpoint DECIDES on is typed, and only a document it forwards is bytes.
type Rendered struct {
	Body []byte `json:"body"`
	// Created reports that the act made a NEW row rather than answering with one
	// that already existed. It rides here because only the store can tell the two
	// apart — saving a card already on file answers with the row that holds it —
	// and the endpoint has to know which happened to answer 201 or 200. Absent on a
	// read, where nothing was created and the zero value is the truth.
	Created bool `json:"created,omitempty"`
}

// MethodsIn lists one subject's saved payment methods, optionally of one kind.
type MethodsIn struct {
	Subject string `json:"subject"`
	Kind    string `json:"kind,omitempty"`
}

// MethodSaveIn saves a payment method for a subject.
//
// Email is the caller's own address, resolved at the endpoint from its
// credential: it names the processor's customer profile, and the store must
// never read an identity it was not handed. Body is the method to save, exactly
// as the caller sent it, so the vaulting path sees what the browser produced.
type MethodSaveIn struct {
	Subject string `json:"subject"`
	Email   string `json:"email,omitempty"`
	Body    []byte `json:"body"`
	// Name is the caller's own name, off the identity record IAM holds for them: the
	// name on the processor's customer. It is never the body's, and never the billing key.
	Name string `json:"name,omitempty"`
}

// MethodRef names one saved method to remove.
//
// Privileged marks the service or admin caller that may act on any subject
// inside the org. It is the ENDPOINT'S determination and travels as one, because
// authority decided twice is authority that eventually disagrees with itself.
type MethodRef struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Privileged bool   `json:"privileged,omitempty"`
}

// MethodUpdateIn updates billing address or metadata on one saved payment method.
type MethodUpdateIn struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Privileged bool   `json:"privileged,omitempty"`
	Body       []byte `json:"body"`
}

// Detachment is what a removal answers with.
type Detachment struct {
	// Deleted is whether the method was actually removed. False with no error
	// means it was already gone, which is a successful detach rather than a
	// failure — a retry must not be an error.
	Deleted bool `json:"deleted"`
	// ID is the method that was detached, echoed so a caller batching several can
	// tell the answers apart.
	ID string `json:"id"`
}

// PlansIn names whose public plan catalog to read, optionally narrowed to one
// category.
type PlansIn struct {
	// Host is the customer-facing host the catalog is read on. The brand it
	// resolves to decides which catalog is on sale; empty is the deployment's
	// default brand.
	Host     string `json:"host,omitempty"`
	Category string `json:"category,omitempty"`
	// Coupon is a plan coupon code to quote the catalog under. Empty reads the
	// catalog exactly as it is; a code annotates each row with what each month it
	// discounts charges under the coupon and how many months that is, or why the
	// coupon does not apply.
	Coupon string `json:"coupon,omitempty"`
}

// ---- billing.plan — the plan authority's rows, as a SuperAdmin keeps them ---

const (
	// BillingPlanRows lists every row of the plan authority, whatever its status;
	// BillingPlanCreate adds one; BillingPlanSet restates one. The list answers a
	// SuperAdmin and the platform; the two writes answer a SuperAdmin only.
	BillingPlanRows   = "billing_plan_rows"
	BillingPlanCreate = "billing_plan_create"
	BillingPlanSet    = "billing_plan_set"
)

// PlanCapacity is how many of each thing a plan may hold: agents, bots, workspace
// guests, org members, the seat floor of a per-seat plan, and the DNS quotas. Nil is
// unstated, -1 is unlimited and 0 is none. A restatement keeps the seat bounds and
// the four order figures it leaves nil, and a 0 removes one.
type PlanCapacity struct {
	// Agents is how many agents the plan may run.
	Agents *int `json:"agents,omitempty"`
	// Bots is how many resident bots the plan may run.
	Bots *int `json:"bots,omitempty"`
	// TeamGuests is how many guests the holder may invite to its team workspace.
	TeamGuests *int `json:"teamGuests,omitempty"`
	// MaxMembers is the most members an org on the plan may hold, and the most
	// seats a card buys of a per-seat plan: 1 or more, or -1 for no ceiling.
	MaxMembers *int `json:"maxMembers,omitempty"`
	// MinSeats is the fewest seats a per-seat plan bills: 1 or more, and no more
	// than MaxMembers.
	MinSeats *int `json:"minSeats,omitempty"`
	// Zones is how many DNS zones the plan may hold.
	Zones *int `json:"zones,omitempty"`
	// RecordsPerZone is how many records each DNS zone may hold.
	RecordsPerZone *int `json:"recordsPerZone,omitempty"`
	// QueriesPerDay is how many DNS queries the plan answers in a day.
	QueriesPerDay *int `json:"queriesPerDay,omitempty"`
	// SpendLimits are the monthly usage limits per member an organization's order
	// offers, in whole US dollars from 1 to 1000000, ascending. A plan stating any of
	// the four order figures is sold per seat, to an organization's own account only.
	SpendLimits []int `json:"spendLimits,omitempty"`
	// SpendLimit is the per-member monthly limit an order starts on, in whole US
	// dollars: one of SpendLimits.
	SpendLimit *int `json:"spendLimit,omitempty"`
	// BalanceMonths is the starting usage balance an order starts with unless the
	// buyer says otherwise, in months of its seats' price: 1 or more, on a plan that
	// states a BalanceCeiling.
	BalanceMonths *int `json:"balanceMonths,omitempty"`
	// BalanceCeiling is the most a starting balance may be, in whole US dollars from
	// 1 to 1000000; a card sale above it is refused, and a plan stating none sells no
	// balance.
	BalanceCeiling *int `json:"balanceCeiling,omitempty"`
	// InvoiceDays is how many days an invoice for the plan gives to pay it, 1 to
	// 365; a plan stating none is not sold by invoice. After BalanceCeiling, for
	// the ZAP wire.
	InvoiceDays *int `json:"invoiceDays,omitempty"`
	// GraceDays is how many days past due an unpaid invoice stands before it is
	// voided, 1 to 365.
	GraceDays *int `json:"graceDays,omitempty"`
}

// PlanLicensing is what a plan licenses: the products, the engine app builds and
// capability tokens, the licensed seats (-1 unlimited, nil unstated), and whether
// its holders open every product in beta without applying.
type PlanLicensing struct {
	// Products are the proprietary products the plan entitles, such as engine.
	Products []string `json:"products,omitempty"`
	// Apps are the engine app builds it licenses, such as hanzo.
	Apps []string `json:"apps,omitempty"`
	// Features are the engine capability tokens it grants, such as inference.
	Features []string `json:"features,omitempty"`
	// Seats is the named users or devices the licence covers; -1 unlimited.
	Seats *int `json:"seats,omitempty"`
	// EarlyAccess opens every product in beta to a holder's org without applying.
	EarlyAccess bool `json:"earlyAccess,omitempty"`
}

// PlanReload is the automatic reload buying a plan switches on for the payer: its
// balance is topped up by AmountCents, charged to its card on file, whenever it falls
// below ThresholdCents.
type PlanReload struct {
	// ThresholdCents is the balance, in cents, the reload fires below.
	ThresholdCents int64 `json:"thresholdCents"`
	// AmountCents is what one reload charges and adds, in cents.
	AmountCents int64 `json:"amountCents"`
}

// PlanRow is one row of the plan authority: what a card is charged, what the pricing
// page prints, and whether it is on sale.
type PlanRow struct {
	// Slug is the plan's id, which every subscription on it records.
	Slug string `json:"slug"`
	// Name is what the pricing page and checkout call the plan.
	Name string `json:"name"`
	// Description is the plan's summary on its card and at checkout.
	Description string `json:"description"`
	// Category is the ladder the plan sits on, such as personal, team or dns.
	Category string `json:"category"`
	// Price is what a card is charged each interval, in cents; per seat on a
	// per-seat plan.
	Price int64 `json:"price"`
	// AnnualTotal is what a card is charged once for a year, in cents; 0 when the
	// plan is not sold by the year.
	AnnualTotal int64 `json:"annualTotal"`
	// Currency is the ISO code the prices are in, lower-cased.
	Currency string `json:"currency"`
	// Interval is the billing period: month or year.
	Interval string `json:"interval"`
	// PerSeat says Price is per seat.
	PerSeat bool `json:"perSeat"`
	// ContactSales says the plan is sold by quote, not at checkout.
	ContactSales bool `json:"contactSales"`
	// Popular marks the card the pricing page draws forward.
	Popular bool `json:"popular"`
	// Status is active (on sale), draft, private or archived; only active is sold.
	Status string `json:"status"`
	// Features are the card's lines, in order.
	Features []string `json:"features"`
	// Limits are the plan's capacities; absent when it states none.
	Limits *PlanCapacity `json:"limits,omitempty"`
	// Licensing is what the plan licenses; absent when it licenses nothing.
	Licensing *PlanLicensing `json:"licensing,omitempty"`
	// ReloadsPerDay is the most automatic reloads the plan allows a payer in one UTC
	// day, 0 for no daily limit; absent when the row states none (3).
	ReloadsPerDay *int `json:"reloadsPerDay,omitempty"`
	// Reload is the automatic reload buying the plan switches on; absent when none.
	Reload *PlanReload `json:"reload,omitempty"`
	// AdminEdited says a SuperAdmin stated the row.
	AdminEdited bool `json:"adminEdited"`
	// Entitlements is the canonical entitlement block the plan grants, a JSON
	// object of namespaced keys ("ai.priority_inference", "team.guests") to the
	// value granted, -1 for unlimited. Empty when no one has recorded it.
	Entitlements string `json:"entitlements,omitempty"`
	// Replaces are the slugs the plan was sold under before it was renamed.
	Replaces []string `json:"replaces,omitempty"`
}

// PlanRows is every row of the plan authority, ordered by slug.
type PlanRows struct {
	// Rows are the plan authority's rows.
	Rows []PlanRow `json:"rows"`
}

// PlanSet is a SuperAdmin's statement of one plan: every field here replaces the
// row's, the reload, seat-pricing and contact-sales fields only when present, and
// every field it does not carry (currency, the credit a plan grants) is kept as
// stored. Price and AnnualTotal are cents, as
// on [PlanRow]; Status is active, draft, private or archived.
type PlanSet struct {
	// Slug is the plan's id; it never changes once added.
	Slug string `json:"slug"`
	// Name is what the pricing page and checkout call the plan.
	Name string `json:"name"`
	// Description is the plan's summary on its card and at checkout.
	Description string `json:"description"`
	// Category is the ladder the plan sits on, such as personal, team or dns.
	Category string `json:"category"`
	// Price is what a card is charged each interval, in cents.
	Price int64 `json:"price"`
	// AnnualTotal is what a card is charged once for a year, in cents; 0 sells no
	// year.
	AnnualTotal int64 `json:"annualTotal"`
	// Interval is the billing period: month or year.
	Interval string `json:"interval"`
	// Status is active, draft, private or archived.
	Status string `json:"status"`
	// Popular marks the card the pricing page draws forward.
	Popular bool `json:"popular"`
	// Features are the card's lines, in order.
	Features []string `json:"features"`
	// Limits are the plan's capacities.
	Limits *PlanCapacity `json:"limits,omitempty"`
	// Licensing is what the plan licenses.
	Licensing *PlanLicensing `json:"licensing,omitempty"`
	// ReloadsPerDay is the most automatic reloads a day, 0 for no daily limit; absent
	// keeps the row's.
	ReloadsPerDay *int `json:"reloadsPerDay,omitempty"`
	// Reload is the automatic reload buying the plan switches on; absent keeps the
	// row's, and an AmountCents of 0 removes it.
	Reload *PlanReload `json:"reload,omitempty"`
	// PerSeat says Price is per seat; absent keeps the row's. After Reload, for
	// the ZAP wire.
	PerSeat *bool `json:"perSeat,omitempty"`
	// ContactSales says the plan is sold by quote and never by card; absent keeps
	// the row's.
	ContactSales *bool `json:"contactSales,omitempty"`
	// Entitlements is the canonical entitlement block the plan grants, a JSON
	// object of namespaced keys to the value granted; empty keeps the row's, and
	// "{}" states that the plan grants nothing.
	Entitlements string `json:"entitlements,omitempty"`
	// Replaces are the slugs the plan was sold under before it was renamed; absent
	// keeps the row's, and an empty list removes them.
	Replaces []string `json:"replaces,omitempty"`
}

// ---- billing.coupon — the plan coupons a SuperAdmin keeps ------------------

const (
	// BillingCoupon reads one plan coupon; BillingCouponSet creates or replaces
	// one; BillingCouponMint draws a new single-use one under a fresh code;
	// BillingCouponExtend grants a coupon's months to the subscriptions already
	// bought with it. All four are platform authority only.
	BillingCoupon       = "billing_coupon"
	BillingCouponSet    = "billing_coupon_set"
	BillingCouponMint   = "billing_coupon_mint"
	BillingCouponExtend = "billing_coupon_extend"
)

// CouponRef names one plan coupon by its code (case-insensitive).
type CouponRef struct {
	Code string `json:"code"`
}

// CouponSet is a SuperAdmin's whole statement of one plan coupon: a percent
// off each of the first Months monthly periods of a plan purchase, the window it
// is redeemable in, how many uses it has in all (0 is no limit), the plan slugs
// it covers and whether it is on.
//
// Start and End are RFC3339 text, empty for an open end, for the reason every
// timestamp on this plane is text (see [BillingInvoice]).
type CouponSet struct {
	Code    string   `json:"code"`
	Percent int      `json:"percent"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Limit   int      `json:"limit"`
	Plans   []string `json:"plans"`
	Enabled bool     `json:"enabled"`
	// Campaign names the offer the code is one of: an org and a card redeem one
	// code of a campaign. Empty is a code on its own. Last, so the fields before
	// it keep their places on the ZAP wire.
	Campaign string `json:"campaign,omitempty"`
	// Months is how many monthly periods the coupon discounts, from the first
	// paid one: 1 to 36, and 0 is 1, a first-month coupon. After Campaign, for the
	// same reason.
	Months int `json:"months,omitempty"`
}

// CouponMint asks for one new single-use plan coupon under a fresh code,
// Prefix-XXXXXXXXXX: Percent off the first Months months of every paid plan on
// sale (none is one), redeemable until End (RFC3339; empty never closes), one of
// Campaign's codes.
type CouponMint struct {
	Prefix   string `json:"prefix"`
	Percent  int    `json:"percent"`
	Months   int    `json:"months,omitempty"`
	End      string `json:"end,omitempty"`
	Campaign string `json:"campaign,omitempty"`
}

// Coupon is a plan coupon as stored, with how many uses it has had.
type Coupon struct {
	Code    string   `json:"code"`
	Percent int      `json:"percent"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Limit   int      `json:"limit"`
	Plans   []string `json:"plans"`
	Enabled bool     `json:"enabled"`
	Used    int      `json:"used"`
	// Campaign is the offer the code is one of; absent for a code on its own.
	Campaign string `json:"campaign,omitempty"`
	// Months is how many monthly periods the coupon discounts, from the first
	// paid one; 1 is a first-month coupon.
	Months int `json:"months"`
}

// CouponExtend asks for a plan coupon's months to be granted to every live
// subscription already bought with it. Apply false answers what it would do and
// changes nothing.
type CouponExtend struct {
	Code  string `json:"code"`
	Apply bool   `json:"apply"`
}

// CouponExtension is what extending one coupon found and did: every org holding
// a use of it, and each subscription of those orgs bought with it.
type CouponExtension struct {
	// Code is the coupon, upper-cased as stored.
	Code string `json:"code"`
	// Months is how many monthly periods the coupon discounts now.
	Months int `json:"months"`
	// Applied is false on a dry run.
	Applied bool `json:"applied"`
	// Orgs is every org holding a use of the coupon.
	Orgs []string `json:"orgs"`
	// Subscriptions is each subscription of those orgs bought with the coupon.
	Subscriptions []CouponExtended `json:"subscriptions"`
}

// CouponExtended is one subscription bought with the coupon and the grant it
// holds after the extension (or, on a dry run, would hold).
type CouponExtended struct {
	// Org is the org that holds the subscription.
	Org string `json:"org"`
	// SubscriptionID is the subscription; absent when the org could not be read.
	SubscriptionID string `json:"subscriptionId,omitempty"`
	// UserID is the subject that pays for it.
	UserID string `json:"userId,omitempty"`
	// Status is the subscription's state: active, past_due, trialing, canceled.
	Status string `json:"status,omitempty"`
	// Test marks a sandbox subscription.
	Test bool `json:"test,omitempty"`
	// Percent is what the coupon takes off each month it discounts, as sold.
	Percent int `json:"percent,omitempty"`
	// Months is how many billing cycles the coupon discounts, from From.
	Months int `json:"months,omitempty"`
	// From is the start of the first paid period, where the cycles are counted
	// from, RFC3339.
	From string `json:"from,omitempty"`
	// Billed is how many cycles have been invoiced since From, discounted or not.
	Billed int `json:"billed"`
	// Changed is whether the grant moved (or, on a dry run, would).
	Changed bool `json:"changed"`
	// Missed counts the cycles among the first Months invoiced without the coupon,
	// paid or still owed: renewals that ran before it was extended, for a person
	// to settle, since an extension moves no money.
	Missed int `json:"missed,omitempty"`
	// Error is why the subscription was left as it was.
	Error string `json:"error,omitempty"`
}

// ---- billing.promo — the platform plan promo a SuperAdmin keeps -------------

const (
	// BillingPromo reads the platform plan promo; BillingPromoSet replaces it.
	// Both are platform authority only.
	BillingPromo    = "billing_promo"
	BillingPromoSet = "billing_promo_set"
)

// PromoIn asks for the platform plan promo. It names nothing: there is one.
type PromoIn struct{}

// Promo is the platform plan promo: PercentOff (0-100) off every plan it covers
// (Plans; empty is every paid plan) between Start and End, while Active. Start
// and End are RFC3339 text, empty for an open bound, for the reason every
// timestamp on this plane is text (see [BillingInvoice]).
type Promo struct {
	PercentOff int      `json:"percentOff"`
	Start      string   `json:"start,omitempty"`
	End        string   `json:"end,omitempty"`
	Plans      []string `json:"plans"`
	Active     bool     `json:"active"`
}

// ---- billing.subscriptions — the customer's own plan ------------------------

const (
	BillingSubscriptionCancel     = "billing_subscription_cancel"
	BillingSubscriptionReactivate = "billing_subscription_reactivate"
	BillingSubscriptionSeats      = "billing_subscription_seats"
	BillingSubscriptionResize     = "billing_subscription_resize"
)

// ResizeIn sets the seats a team opens its next period at: Quantity of its
// plan's own seat type (0 keeps them) and each seat type Lines names, one of the
// team's group it does not hold yet among them. ID, Subject and Privileged name
// the subscription and whose it may be, exactly as on SubscriptionRef. The
// renewal that opens the period charges the seats; the period in hand keeps the
// ones it was paid for.
type ResizeIn struct {
	ID         string     `json:"id"`
	Subject    string     `json:"subject,omitempty"`
	Privileged bool       `json:"privileged,omitempty"`
	Quantity   int        `json:"quantity,omitempty"`
	Lines      []SaleLine `json:"lines,omitempty"`
}

// SeatsIn sets which member holds which of a team's seat types, replacing what
// was assigned: ID, Subject and Privileged name the subscription and whose it may
// be, exactly as on SubscriptionRef. Commerce gives no seat type more members
// than the seats of it paid for.
type SeatsIn struct {
	ID         string             `json:"id"`
	Subject    string             `json:"subject,omitempty"`
	Privileged bool               `json:"privileged,omitempty"`
	Assigned   []SubscriptionSeat `json:"assigned"`
}

// SubscriptionRef names one subscription to act on, and whose it may be.
//
// Subject is the wallet the caller pays from, and Privileged marks a caller with
// the org's own authority — an admin of the org, or the platform — who may act on
// every subscription in it. Both are the ENDPOINT'S determination and travel as
// one, as on MethodRef: authority decided twice is authority that eventually
// disagrees with itself. Neither set holds nothing, so a caller that states no
// authority reaches no row.
type SubscriptionRef struct {
	ID string `json:"id"`
	// AtPeriodEnd cancels at the end of the paid period rather than at once. It
	// defaults TRUE on the endpoint, because a customer who cancels has already
	// paid for the period they are in.
	AtPeriodEnd bool   `json:"atPeriodEnd,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Privileged  bool   `json:"privileged,omitempty"`
}

// Subscriptions is the org's own plan rows, rendered the way the customer's
// billing page has always read them.
//
// It is a SECOND view of the rows FinanceSubs already carries, and the two are
// not a duplication: FinanceSubs answers "who is subscribed and what is it
// worth" for an operator's revenue board, and this answers "what am I on" for
// the holder. One store, one core, two audiences — and folding them would make
// the customer's page inherit the board's fields or the board inherit the
// customer's.
type Subscriptions struct {
	Rows []Subscription `json:"subscriptions"`
	// Count is the row count beside the rows, which is the shape this address
	// has always answered with.
	Count int `json:"count"`
}

// FleetSubscriptions is every org commerce holds and that org's subscriptions, read
// in one call by the operator's subscription board. Each row is what
// BillingSubscriptions answers that org.
//
// An org commerce does not hold is ABSENT, and that is an answer: commerce holds an
// org from the first call that names it, so an org it has never seen has bought
// nothing. An org it holds and could not read is PRESENT with its error, never
// absent and never empty.
type FleetSubscriptions struct {
	// Orgs is every org commerce holds, by name, with its subscriptions or why they did
	// not read.
	Orgs []OrgSubscriptions `json:"orgs"`
}

// OrgSubscriptions is one org's subscriptions in a fleet read.
type OrgSubscriptions struct {
	// Org is the org's name, the IAM slug commerce holds it under.
	Org string `json:"org"`
	// Rows is the org's subscriptions; empty when it holds none or did not read.
	Rows []Subscription `json:"subscriptions"`
	// Error is why the org did not read; empty when it did.
	Error string `json:"error,omitempty"`
}

// SubscriptionPlan is the plan a subscription is on, as the subscription
// reports it: enough to name and price it, and no more.
type SubscriptionPlan struct {
	// ID is the plan row the subscription was opened on.
	ID string `json:"id"`
	// Slug is the catalog plan it was opened on, e.g. "max-20x": what a reader
	// matches against GET /v1/billing/plans. Empty on a row that predates it.
	Slug string `json:"slug,omitempty"`
	// Name is the plan's display name, e.g. "Max 20x".
	Name string `json:"name"`
	// Price is what one period costs per seat, in cents, as the subscription
	// snapshotted it: a later catalog price change does not move it.
	Price int64 `json:"price"`
	// Currency is the ISO code the price is in, lower-cased.
	Currency string `json:"currency"`
	// Interval is the billing period: "month" or "year".
	Interval string `json:"interval"`
}

// Subscription is one plan a subject holds.
//
// The four dates a subscription may not have are POINTERS because the key is
// absent on a CONDITION — a trial was opened, a cancel happened, the row ended —
// and not because the value is zero: omitempty omits no time, so a plain field
// would report every subscription as trialing since year one.
type Subscription struct {
	// ID is the subscription's own id, the one cancel and reactivate take.
	ID string `json:"id"`
	// UserID is the billing account that holds it: the org slug for an org's own
	// plan, "<org>/<name>" for a member's.
	UserID string `json:"userId"`
	// PlanID is the plan slug, e.g. "max-20x".
	PlanID string `json:"planId"`
	// Status is trialing, active, past_due, canceled or unpaid. Only active and
	// trialing confer the plan.
	Status string `json:"status"`
	// Quantity is the seat count; a flat plan holds 1.
	Quantity int `json:"quantity"`

	// CurrentPeriodStart is when the period in hand began, RFC 3339.
	CurrentPeriodStart string `json:"currentPeriodStart"`
	// CurrentPeriodEnd is when it ends, RFC 3339. The plan is paid through it.
	CurrentPeriodEnd string `json:"currentPeriodEnd"`
	// CancelAtPeriodEnd reports that the plan ends at CurrentPeriodEnd.
	CancelAtPeriodEnd bool `json:"cancelAtPeriodEnd"`

	// MRRCents is what this subscription contributes per month — commerce's own
	// figure, interval-normalized and multiplied by its seats, so no reader
	// re-derives it from price and interval.
	MRRCents int64 `json:"mrrCents"`
	// ProviderType is who collects it: a processor ("square"), "credit" for a
	// plan bought with credits, "internal" or "bundle" for a row nobody paid for.
	ProviderType string `json:"providerType"`
	// DefaultPaymentMethod is the saved method a renewal would use. Empty means
	// there is none, so nothing can charge the plan.
	DefaultPaymentMethod string `json:"defaultPaymentMethod"`
	// Settled is how the period in hand was paid: "card" (a card paid all of it),
	// "external:<processor>" (a payment recorded as collected outside commerce),
	// "balance", "credit" or "mixed" (prepaid money). Empty when unpaid or unknown.
	Settled string `json:"settled,omitempty"`
	// Seats is the seat count the period in hand was paid for — its paid invoice's
	// quantity, or the quantity recorded with an external period — never the row's
	// live Quantity, which the holder may change. 0 when the period is unpaid.
	Seats int `json:"seats,omitempty"`
	// ChargedCents is what the period in hand was actually paid, after any discount
	// and net of refunds. 0 when the period is unpaid.
	ChargedCents int64 `json:"chargedCents,omitempty"`

	// Plan is the plan as this subscription snapshotted it.
	Plan SubscriptionPlan `json:"plan"`

	// CreatedAt is when the row was written, RFC 3339.
	CreatedAt string `json:"createdAt"`
	// UpdatedAt is when the row last changed, RFC 3339.
	UpdatedAt string `json:"updatedAt"`

	// TrialStart is when a trial began; absent when there was none.
	TrialStart string `json:"trialStart,omitempty"`
	// TrialEnd is when that trial ends; absent when there was none.
	TrialEnd string `json:"trialEnd,omitempty"`
	// CanceledAt is when it was canceled; absent when it was not.
	CanceledAt string `json:"canceledAt,omitempty"`
	// EndedAt is when it ended; absent while it runs.
	EndedAt string `json:"endedAt,omitempty"`

	// Test marks a sandbox subscription: sold while its org was in test mode, paid
	// with sandbox money. It is listed and never revenue.
	Test bool `json:"test,omitempty"`
	// Coupon is the plan coupon code the first period was bought with, as commerce
	// stores it (upper case); absent when none was.
	Coupon string `json:"coupon,omitempty"`
	// Held reports that this row is the subject's one paid plan: while it stands,
	// commerce refuses a second paid sale (409).
	Held bool `json:"held"`
	// Lines are a team's other seat types beside Plan (a team of standard and
	// premium seats is Plan "team" and one line of "team-premium"), each with the
	// seats of it the period in hand was paid for. Seats above counts them too.
	// After Held, so the fields before them keep their places on the ZAP wire.
	Lines []SubscriptionLine `json:"lines,omitempty"`
	// Assigned is which member holds which seat type, as the org's admin set it.
	Assigned []SubscriptionSeat `json:"assigned,omitempty"`
	// LastPaid is when the latest payment for this subscription arrived, RFC 3339:
	// its latest paid invoice's paid time, or, for a period recorded as collected
	// outside commerce, which keeps no time of its own record, the start of the latest
	// period recorded — whichever is later. Any period, not only the one in hand: a
	// plan past due still says when it was last paid. Empty when nothing was, or when
	// the invoices did not read. Last, so every field before it keeps its place on the
	// ZAP wire.
	LastPaid string `json:"lastPaid,omitempty"`
	// CustomerCents is the price staff agreed this subscriber pays for one seat each
	// period, below the plan's list price; absent when none was. Every invoice bills
	// the plan at list and the difference as the customer's discount. After
	// LastPaid, for the ZAP wire.
	CustomerCents int64 `json:"customerCents,omitempty"`
}

// SubscriptionLine is one of a team's other seat types on its subscription.
type SubscriptionLine struct {
	// Plan is the seat type's catalog plan, e.g. "team-premium".
	Plan string `json:"plan"`
	// Name is the seat type's display name, e.g. "Team Premium".
	Name string `json:"name"`
	// Price is one seat of it for the row's period, in cents.
	Price int64 `json:"price"`
	// Quantity is the seats of it the row holds.
	Quantity int `json:"quantity"`
	// PendingQuantity is the seats of it the next period opens at, when a change
	// waits for the renewal that charges it; absent when none does.
	PendingQuantity int `json:"pendingQuantity,omitempty"`
	// Seats is the seats of it the period in hand was paid for; 0 when unpaid.
	Seats int `json:"seats,omitempty"`
}

// SubscriptionSeat is one member's seat on a team: the member, and the seat type
// (a plan the subscription holds seats of) they hold.
type SubscriptionSeat struct {
	// Member is the IAM user who holds the seat, as the org's members list names them.
	Member string `json:"member"`
	// Plan is the seat type held: "team" (standard) or "team-premium".
	Plan string `json:"plan"`
}

// ---- billing.subscription.record — a plan paid outside checkout --------------
//
// Some customers pay an invoice raised by hand in the processor's own dashboard,
// or wire the money. RecordPlanIn is that payment's facts, and recording it opens
// the plan it bought: collected externally, so nothing here ever charges it or
// renews it, and it grants no credit. It is the platform's act, refused to anyone
// but a SuperAdmin on both sides of the plane.

const BillingSubscriptionRecord = "billing_subscription_record"

// RecordProcessors is every processor a payment collected outside checkout is
// recorded from, and "comp" last. It is commerce's list (commercebilling.
// RecordProcessors), held equal by apps/commerce TestRecordProcessorsAreCommerces,
// so admin.hanzo.ai offers exactly the names a record accepts.
var RecordProcessors = []string{"square", "paypal", "wire", "ach", "check", "crypto", "comp"}

// ---- billing.subscription.period — staff moving a period's end ----------------

const BillingSubscriptionPeriod = "billing_subscription_period"

// PeriodIn moves the end of the period subscription ID holds — or the start of a
// card plan scheduled to begin — to End (RFC 3339), which is in the future.
type PeriodIn struct {
	// ID is the subscription.
	ID string `json:"id"`
	// End is the new end, RFC 3339, in the future.
	End string `json:"end"`
	// Note is why, kept on the row.
	Note string `json:"note,omitempty"`
}

// Adjusted is what moving a period did: the row as it now stands, the end it had
// (From, RFC 3339), and the card plan that was to start at From and now starts at
// the new end (absent when none waited).
type Adjusted struct {
	// Subscription is the row as it now stands.
	Subscription Subscription `json:"subscription"`
	// From is the end the period had, RFC 3339.
	From string `json:"from"`
	// Moved is the card plan that now starts at the new end; absent when none
	// waited.
	Moved *Subscription `json:"moved,omitempty"`
}

// ---- billing.customer.price — a customer's own price for a plan ---------------

const (
	BillingCustomerPrice  = "billing_customer_price"
	BillingCustomerPrices = "billing_customer_prices"
)

// CustomerPriceIn is what Subject pays for one seat of Plan each Interval ("month" or
// "year"): Cents, below the plan's list price, or 0 to remove the price.
type CustomerPriceIn struct {
	// Subject is the billing subject: the org's account or a member's wallet.
	Subject string `json:"subject"`
	// Plan is the plan's slug.
	Plan string `json:"plan"`
	// Interval is "month" (the default) or "year".
	Interval string `json:"interval,omitempty"`
	// Cents is one seat's price for one period; 0 removes the price.
	Cents int64 `json:"cents"`
	// Note is why, kept on the price.
	Note string `json:"note,omitempty"`
}

// CustomerPrice is a customer's own price as it stands, beside the plan's list
// price for the same period.
type CustomerPrice struct {
	// Subject is the billing subject the price is for.
	Subject string `json:"subject"`
	// Plan is the plan's slug.
	Plan string `json:"plan"`
	// Interval is the period the price is for: "month" or "year".
	Interval string `json:"interval"`
	// Cents is the agreed price of one seat for one period.
	Cents int64 `json:"cents"`
	// ListCents is the plan's own price for the same period.
	ListCents int64 `json:"listCents"`
	// Note is why, as staff wrote it.
	Note string `json:"note,omitempty"`
}

// PriceSet is what setting a price did: the price (Cents 0 when removed) and every
// live subscription of the plan the subject holds, as each now bills.
type PriceSet struct {
	// Price is the price as it now stands; Cents 0 when it was removed.
	Price CustomerPrice `json:"price"`
	// Subscriptions are the live rows of the plan the subject holds, as each now
	// bills.
	Subscriptions []Subscription `json:"subscriptions"`
}

// CustomerPrices is every price staff agreed with the org's subjects.
type CustomerPrices struct {
	// Rows are the prices, one per subject, plan and period.
	Rows []CustomerPrice `json:"prices"`
}

// PaymentRef is the processor's own ids for one payment, so it can be found again
// in the processor's dashboard. Each is optional; at least one is required.
type PaymentRef struct {
	// Invoice is the processor's invoice id.
	Invoice string `json:"invoice,omitempty"`
	// Order is the processor's order id.
	Order string `json:"order,omitempty"`
	// Payment is the processor's payment id: the money that moved.
	Payment string `json:"payment,omitempty"`
	// Customer is the processor's customer id for the payer.
	Customer string `json:"customer,omitempty"`
	// Receipt is the receipt number the customer was sent.
	Receipt string `json:"receipt,omitempty"`
}

// RecordPlanIn is one payment collected outside checkout and the plan it bought.
type RecordPlanIn struct {
	// Subject is the billing account the plan is for. The org is the caller's.
	Subject string `json:"subject"`
	// PlanID is the plan slug, one the catalog still sells.
	PlanID string `json:"planId"`
	// Interval is "month" (the default) or "year".
	Interval string `json:"interval,omitempty"`
	// Quantity is the seat count of a per-seat plan. 0 reads as 1.
	Quantity int `json:"quantity,omitempty"`
	// PriceCents is what the customer paid for the period, all seats. It is a
	// CHECK against the plan's catalog price, never a price of its own.
	PriceCents int64 `json:"priceCents"`
	// PeriodStart is when the period the payment covers begins, RFC 3339.
	PeriodStart string `json:"periodStart"`
	// PeriodEnd is when it ends, RFC 3339.
	PeriodEnd string `json:"periodEnd"`
	// Processor is who collected the money, one of RecordProcessors ("comp" is a
	// plan given away). Prepaid money is no processor: a plan is never paid from
	// the balance or credit, and "balance" is refused.
	Processor string `json:"processor"`
	// Reference is the processor's own ids for the payment.
	Reference PaymentRef `json:"reference"`
	// Terms is anything agreed alongside the plan, kept verbatim on it.
	Terms string `json:"terms,omitempty"`
	// Replaces is the id of the plan the account holds that this one takes over.
	// It ends at once, and only after the new plan is recorded and paid.
	Replaces string `json:"replaces,omitempty"`
	// Lines are a team's other seat types the payment bought beside PlanID's
	// seats; PriceCents covers every seat. Last, so the fields before them keep
	// their places on the ZAP wire.
	Lines []SaleLine `json:"lines,omitempty"`
}

// RecordedPlan is what recording a payment did.
type RecordedPlan struct {
	// Outcome is "created" (the account had no plan), "extended" (its external
	// plan moved on to the newly paid period) or "unchanged" (this period was
	// already recorded, so a retry changes nothing).
	Outcome string `json:"outcome"`
	// Subscription is the plan as it now stands.
	Subscription Subscription `json:"subscription"`
	// Replaced is the plan this record took over, as it now stands; absent when
	// it took over none.
	Replaced *Subscription `json:"replaced,omitempty"`
}

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

// ---- billing.invoice — an organization's order by invoice, paid by wire ----

const (
	// BillingSubscribeInvoice orders a plan by invoice: it raises the open
	// invoice, sends it to the buyer, and opens nothing until it is paid.
	BillingSubscribeInvoice = "billing_subscribe_invoice"
	// BillingOrderHosted and BillingOrderHostedPDF read the order invoice a link
	// shows, as anyone holding the link does.
	BillingOrderHosted    = "billing_order_hosted"
	BillingOrderHostedPDF = "billing_order_hosted_pdf"
	// BillingOrderSettle marks an order invoice paid by a wire, BillingOrderVoid
	// voids an unpaid one and BillingOrderRefund refunds a paid one: the
	// platform's SuperAdmin alone.
	BillingOrderSettle = "billing_order_settle"
	BillingOrderVoid   = "billing_order_void"
	BillingOrderRefund = "billing_order_refund"
	// BillingOrderRead reads one order invoice in the caller's org whole, as staff
	// read it: the platform's SuperAdmin alone.
	BillingOrderRead = "billing_order_read"
)

// InvoiceSaleIn is an organization's order by invoice: a card sale's order less
// the card and the coupon. Subject is the organization; Email where the invoice is
// sent. Chosen says the buyer selected the organization (X-Org-Id).
type InvoiceSaleIn struct {
	Host           string     `json:"host,omitempty"`
	PlanID         string     `json:"planId"`
	Subject        string     `json:"subject"`
	StoreID        string     `json:"storeId,omitempty"`
	Quantity       int        `json:"quantity,omitempty"`
	Level          int        `json:"level,omitempty"`
	Interval       string     `json:"interval,omitempty"`
	Email          string     `json:"email,omitempty"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	Lines          []SaleLine `json:"lines,omitempty"`
	BalanceCents   int64      `json:"balanceCents,omitempty"`
	Chosen         bool       `json:"chosen,omitempty"`
	Consent        *string    `json:"consent,omitempty"`
}

// InvoiceOrder is an order by invoice as its buyer reads it: the invoice, what it
// is for, when it is due and when it is voided if unpaid, how to pay it while it
// is open (the receiving bank with the reference, and the same as steps one field
// to a line), and where it is read (HostedURL, PDFURL). Active says the plan is
// open: once paid, or at once under net terms. Replaced is the paid plan the org
// held that this one ended when it opened; Payment the wire that paid it, on a
// staff read. Notice is "Your plan starts when we confirm your payment."; Unpaid
// says what happens if it is not paid.
//
// It is commerce's api/billing InvoiceOrder field for field, less what cloud
// keeps to itself, plus where it is read (apps/commerce TestTheInvoiceRailIsOneShape).
type InvoiceOrder struct {
	InvoiceID      string            `json:"invoiceId"`
	Number         string            `json:"number"`
	Reference      string            `json:"reference"`
	Status         string            `json:"status"`
	PlanID         string            `json:"planId"`
	Category       string            `json:"category,omitempty"`
	Seats          int64             `json:"seats"`
	AmountCents    int64             `json:"amountCents"`
	BalanceCents   int64             `json:"balanceCents,omitempty"`
	Currency       string            `json:"currency"`
	IssuedAt       time.Time         `json:"issuedAt"`
	DueAt          time.Time         `json:"dueAt"`
	VoidAt         time.Time         `json:"voidAt"`
	PaidAt         *time.Time        `json:"paidAt,omitempty"`
	VoidedAt       *time.Time        `json:"voidedAt,omitempty"`
	Lines          []OrderLine       `json:"lines"`
	Wire           *WireInstructions `json:"wire,omitempty"`
	HowToPay       []string          `json:"howToPay,omitempty"`
	HostedURL      string            `json:"hostedUrl,omitempty"`
	PDFURL         string            `json:"pdfUrl,omitempty"`
	Active         bool              `json:"active"`
	SubscriptionID string            `json:"subscriptionId,omitempty"`
	Replaced       string            `json:"replaced,omitempty"`
	Payment        string            `json:"payment,omitempty"`
	Notice         string            `json:"notice"`
	Unpaid         string            `json:"unpaid"`
}

// OrderLine is one line of an order's invoice: what it is, how many at what each,
// and its amount, in cents.
type OrderLine struct {
	Description string `json:"description"`
	Quantity    int64  `json:"quantity,omitempty"`
	UnitCents   int64  `json:"unitCents,omitempty"`
	AmountCents int64  `json:"amountCents"`
}

// InvoiceOrdered is what an order by invoice answered, keeping its shapes apart
// as Sold does: a fresh order, or an identical earlier one answered again
// (Replayed, its bytes), or the invoice already open for this org and plan the
// order was refused for (Open, with the refusal's sentence in Refusal). The last
// is a value rather than an error because the plane carries a refusal's code and
// sentence, not its data; the route answers it 409 invoice_open.
type InvoiceOrdered struct {
	Order    *InvoiceOrder `json:"order,omitempty"`
	Replayed []byte        `json:"replayed,omitempty"`
	Open     *InvoiceOrder `json:"open,omitempty"`
	Refusal  string        `json:"refusal,omitempty"`
}

// InvoiceLinkIn names an order invoice by the link it is shared by.
type InvoiceLinkIn struct {
	Link string `json:"link"`
}

// HostedInvoice is an order invoice as anyone holding its link reads it: the
// order, who bills whom, and how to pay it while it is open.
type HostedInvoice struct {
	Order         *InvoiceOrder `json:"order"`
	Issuer        string        `json:"issuer"`
	IssuerAddress string        `json:"issuerAddress,omitempty"`
	BillTo        string        `json:"billTo"`
}

// InvoiceSettleIn is a wire that paid an order invoice in the caller's org, as the bank
// states it: its receipt (the bank's reference), the amount received and when. An
// amount that is not the invoice's is taken only when Difference says which way it
// misses: "short" or "over".
type InvoiceSettleIn struct {
	InvoiceID   string    `json:"invoiceId"`
	Receipt     string    `json:"receipt"`
	AmountCents int64     `json:"amountCents"`
	ReceivedAt  time.Time `json:"receivedAt"`
	Difference  string    `json:"difference,omitempty"`
}

// InvoiceSettled is a paid order: the invoice, the plan it opened, what was
// received, and the starting balance credited. Reopened says a wire the bank
// received before the invoice was voided re-opened it; Replayed says this answers
// a settlement already made in full, so nothing moved and nobody is told again.
type InvoiceSettled struct {
	Order          *InvoiceOrder `json:"order"`
	Payment        string        `json:"payment"`
	SubscriptionID string        `json:"subscriptionId"`
	ReceivedCents  int64         `json:"receivedCents"`
	ShortCents     int64         `json:"shortCents,omitempty"`
	OverCents      int64         `json:"overCents,omitempty"`
	BalanceCents   int64         `json:"balanceCents,omitempty"`
	BalanceID      string        `json:"balanceId,omitempty"`
	Reopened       bool          `json:"reopened,omitempty"`
	Replayed       bool          `json:"replayed,omitempty"`
}

// InvoiceVoidIn voids an order invoice in the caller's org, and says why.
type InvoiceVoidIn struct {
	InvoiceID string `json:"invoiceId"`
	Reason    string `json:"reason"`
}

// InvoiceRefundIn refunds AmountCents of a paid order's wire in the caller's org under
// Reference, the bank's reference for the refund.
type InvoiceRefundIn struct {
	InvoiceID   string `json:"invoiceId"`
	AmountCents int64  `json:"amountCents"`
	Reference   string `json:"reference"`
}

// Recharged is one org's outcome in an auto-recharge sweep.
type Recharged struct {
	OrgName       string `json:"orgName"`
	UserID        string `json:"userId"`
	Charged       bool   `json:"charged"`
	AmountCents   int64  `json:"amountCents,omitempty"`
	BalanceCents  int64  `json:"balanceCents,omitempty"`
	TransactionID string `json:"transactionId,omitempty"`
	Error         string `json:"error,omitempty"`
}

// Renewed is one due subscription's outcome in a renewal sweep. Success is
// whether its period was paid; Error says why not.
type Renewed struct {
	OrgName        string `json:"orgName"`
	UserID         string `json:"userId"`
	SubscriptionID string `json:"subscriptionId"`
	InvoiceID      string `json:"invoiceId,omitempty"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
	// Lapsed says the row was not renewed but ended: a plan collected outside
	// commerce whose period ran out with no later payment recorded.
	Lapsed bool `json:"lapsed,omitempty"`
}

// Renewal is one whole renewal sweep. Orgs is the POPULATION considered and Due
// the subscriptions that had fallen due, so a sweep that renewed nothing reads
// differently from one that never ran.
type Renewal struct {
	// Orgs is how many orgs the sweep considered.
	Orgs int `json:"orgs"`
	// Due is how many subscriptions had a period that had ended.
	Due int `json:"due"`
	// Renewed is how many of them were paid; the rest are past due.
	Renewed int `json:"renewed"`
	// Results is one row per due subscription, and per lapsed one. Never null.
	Results []Renewed `json:"results"`
	// Lapsed is how many plans collected outside commerce ended with their period.
	Lapsed int `json:"lapsed"`
	// Reminded is how many subscribers were told their plan ends without a charge
	// (a week, then a day, before), and RemindFailed how many of those sends
	// failed and will be tried again.
	Reminded     int `json:"reminded"`
	RemindFailed int `json:"remindFailed"`
}

// Recharge is one whole sweep. Orgs is the POPULATION considered, not the row
// count — that difference is how a reader tells "nobody was below threshold"
// from "the sweep never ran".
type Recharge struct {
	// Orgs is how many orgs the sweep considered — every org with auto-recharge
	// armed, whether or not it needed charging.
	Orgs int `json:"orgs"`
	// Charged is how many of them were actually charged. It is at most Orgs, and
	// the difference is orgs whose balance was already above their threshold.
	Charged int `json:"charged"`
	// Results is one row per org considered, so a sweep that charged nobody is
	// still explainable. Never null.
	Results []Recharged `json:"results"`
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
