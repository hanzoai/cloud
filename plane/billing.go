// billing.go is the billing capability's half of the contract: the questions a
// customer's money door asks, answered by the process that holds the merchant
// store.
//
// The two are not the same app and cannot be. `/v1/billing` belongs to the
// billing capability (HIP-0018 carries `capability: billing`), and the rows
// behind it — invoices, subscriptions, saved cards, spend caps, credit grants —
// live in commerce's per-tenant datastore, which has ONE owner. So billing serves
// the address and commerce answers the question, and these are the questions.
//
// Every rule in plane.go's package doc applies here without exception. There is
// no Org field anywhere below: the tenant rides the caller. A SUBJECT does
// travel on the inputs that need one, and that is not a loophole — a billing
// subject is a wallet INSIDE the ledger the caller's identity already pinned, so
// naming one can widen a read to another account of the caller's own org and to
// nothing else. The door resolves it server-side (apps/billing's payer) and a
// client-supplied one never reaches here.
//
// The outputs carry the json tags the HTTP surface publishes, because they ARE
// that surface: billing answers with the value it was handed rather than
// re-rendering it. One shape, declared once, so the wire a customer reads cannot
// drift from the wire commerce produced.

package plane

import "encoding/json"

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
	BillingInvoiceVoid    = "billing_invoice_void"

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
// query parameter has always had on the door this shares its query with.
//
// Subject is the billing subject the door resolved for the caller — a wallet
// inside the caller's own org, never an org.
type InvoicesIn struct {
	Subject        string `json:"subject,omitempty"`
	Status         string `json:"status,omitempty"`
	SubscriptionID string `json:"subscriptionId,omitempty"`
}

// Invoices is the listing the customer's invoice page renders. It carries the
// count beside the rows because that is the wire commerce publishes and a reader
// deriving it would be deriving a number the store already has.
type Invoices struct {
	Rows  []BillingInvoice `json:"invoices"`
	Count int              `json:"count"`
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

	// BillingTransactions is one page of a subject's ledger.
	BillingTransactions = "billing_transactions"
)

// CallerIn carries the identity the door already validated: who is asking, what
// they are called, and what standing they hold in the org.
//
// It is not a tenant claim and cannot become one — there is no org field, and
// the subject names a wallet inside the org the caller's own principal pinned.
// It travels because commerce cannot resolve it: the roster is IAM's, the
// request is the door's, and a callee that guessed would be inventing a member.
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
// plane as a root value, and the door unwraps it back to the array its wire has
// always been.
type Accounts struct {
	Rows []BillingAccount `json:"rows"`
}

// Holder is one person on a billing account. It is not [Member], which answers
// whether a subject holds a workspace row at all — a verdict rather than a
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

// Transactions is one page of that ledger. Count is the size of the WHOLE
// history rather than of the page — that difference is how a reader knows there
// is more to ask for — and User echoes the subject the page was read for, so a
// caller can see which wallet answered.
type Transactions struct {
	Rows  []Transaction `json:"transactions"`
	Count int           `json:"count"`
	User  string        `json:"user"`
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
// page reads, carried whole rather than rebuilt at the door.
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
// against it, rather than once per door.
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
// the caller's own, resolved at the door.
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
// ProjectValidated says whether the project axis was ESTABLISHED by the door
// rather than merely claimed. It travels because only the door knows: a
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

// CryptoOptions is the menu of assets the rail accepts: the chains a payer may
// send on and the tokens they may send.
type CryptoOptions struct {
	Chains []string `json:"chains"`
	Tokens []string `json:"tokens"`
}

// CryptoMintIn asks for a deposit address for one payer and one asset.
type CryptoMintIn struct {
	Payer       string `json:"payer"`
	Chain       string `json:"chain"`
	Token       string `json:"token"`
	AmountCents int64  `json:"amountCents,omitempty"`
}

// CryptoDepositIn reads one intent back. A foreign id is a miss, not a refusal,
// so a guessed id cannot confirm that somebody else's deposit exists.
type CryptoDepositIn struct {
	Payer string `json:"payer"`
	ID    string `json:"id"`
}

// CryptoDeposit is one deposit intent.
//
// AddressTag is the routing tag a payer MUST send with the payment on a chain
// where the address is shared. omitempty is load-bearing and safe: the tag is
// decimal text, so the first tag ever issued is "0" — not empty, and therefore
// sent. A payment to a pooled address with no tag names nobody and is credited
// to nobody.
type CryptoDeposit struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Chain          string `json:"chain"`
	Token          string `json:"token"`
	DepositAddress string `json:"depositAddress"`
	AddressTag     string `json:"addressTag,omitempty"`
	ExpiresAt      string `json:"expiresAt,omitempty"`
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

// ---- billing.tier and billing.rollup — what a plan allows, and what is left --

const (
	BillingTier   = "billing_tier"
	BillingRollup = "billing_rollup"
)

// Window is one of a plan's four nested request bounds and how much of it is
// left. Limit 0 declares NO bound at that span rather than a bound of zero, so a
// reader skips the window instead of reporting it exhausted.
type Window struct {
	Span      string `json:"span"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
	Resets    string `json:"resets"`
}

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

// TierIn asks for one subject's tier.
//
// Tier is the DOOR'S minted override and is empty in the ordinary case, where
// the answer is derived from the subject's own subscriptions. It is a field here
// rather than something this side reads because naming a tier is a MINT: the
// header and the query string that carry one are request facts, and only a
// caller entitled to mint may have one honoured — a decision the door makes,
// holding the credential, and states as a value.
type TierIn struct {
	Subject string `json:"subject"`
	Tier    string `json:"tier,omitempty"`
}

// Tier is a subject's tier: which one they are on, what it allows, what they can
// spend, and how much of each plan window is left.
type Tier struct {
	User    string      `json:"user"`
	Tier    TierLimits  `json:"tier"`
	Balance TierBalance `json:"balance"`
	Windows []Window    `json:"windows"`
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
	Windows       []Window        `json:"windows"`
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
	BillingMethods      = "billing_methods"
	BillingMethodSave   = "billing_method_save"
	BillingMethodDetach = "billing_method_detach"

	BillingPlans = "billing_plans"
)

// Rendered is a document the store produced and the door serves without reading.
//
// It exists for the two families whose wire is a deep tree of the STORE'S OWN
// models — a saved payment method carries five kinds of processor detail plus a
// billing address, and a plan carries its limits and its licensing — where a
// mirror in this package would be sixty fields that only ever have to agree with
// something else. Carrying the rendered bytes is byte-exact by construction,
// which is the property a hand-kept mirror can only approximate.
//
// It is the same distinction the money door already draws between its typed
// ledger read and its raw balance read, and it is deliberately narrow: a value
// the door DECIDES on is typed, and only a document it forwards is bytes.
type Rendered struct {
	Body []byte `json:"body"`
	// Created reports that the act made a NEW row rather than answering with one
	// that already existed. It rides here because only the store can tell the two
	// apart — saving a card already on file answers with the row that holds it —
	// and the door has to know which happened to answer 201 or 200. Absent on a
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
// Email is the caller's own address, resolved at the door from its credential:
// it names the processor's customer profile, and the store must never read an
// identity it was not handed. Body is the method to save, exactly as the caller
// sent it, so the vaulting path sees what the browser produced.
type MethodSaveIn struct {
	Subject string `json:"subject"`
	Email   string `json:"email,omitempty"`
	Body    []byte `json:"body"`
}

// MethodRef names one saved method to remove.
//
// Privileged marks the service or admin caller that may act on any subject
// inside the org. It is the DOOR'S determination and travels as one, because
// authority decided twice is authority that eventually disagrees with itself.
type MethodRef struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	Privileged bool   `json:"privileged,omitempty"`
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

// PlansIn narrows the public plan catalog to one category.
type PlansIn struct {
	Category string `json:"category,omitempty"`
}

// ---- billing.subscriptions — the customer's own plan ------------------------

const (
	BillingSubscriptionCancel     = "billing_subscription_cancel"
	BillingSubscriptionReactivate = "billing_subscription_reactivate"
)

// SubscriptionRef names one subscription to act on.
type SubscriptionRef struct {
	ID string `json:"id"`
	// AtPeriodEnd cancels at the end of the paid period rather than at once. It
	// defaults TRUE on the door, because a customer who cancels has already paid
	// for the period they are in.
	AtPeriodEnd bool `json:"atPeriodEnd,omitempty"`
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

// SubscriptionPlan is the plan a subscription is on, as the subscription
// reports it: enough to name and price it, and no more.
type SubscriptionPlan struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Price    int64  `json:"price"`
	Currency string `json:"currency"`
	Interval string `json:"interval"`
}

// Subscription is one plan a subject holds.
//
// The four dates a subscription may not have are POINTERS because the key is
// absent on a CONDITION — a trial was opened, a cancel happened, the row ended —
// and not because the value is zero: omitempty omits no time, so a plain field
// would report every subscription as trialing since year one.
type Subscription struct {
	ID       string `json:"id"`
	UserID   string `json:"userId"`
	PlanID   string `json:"planId"`
	Status   string `json:"status"`
	Quantity int    `json:"quantity"`

	CurrentPeriodStart string `json:"currentPeriodStart"`
	CurrentPeriodEnd   string `json:"currentPeriodEnd"`
	CancelAtPeriodEnd  bool   `json:"cancelAtPeriodEnd"`

	// MRRCents is what this subscription contributes per month — commerce's own
	// figure, interval-normalized and multiplied by its seats, so no reader
	// re-derives it from price and interval.
	MRRCents             int64  `json:"mrrCents"`
	ProviderType         string `json:"providerType"`
	DefaultPaymentMethod string `json:"defaultPaymentMethod"`

	Plan SubscriptionPlan `json:"plan"`

	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`

	TrialStart string `json:"trialStart,omitempty"`
	TrialEnd   string `json:"trialEnd,omitempty"`
	CanceledAt string `json:"canceledAt,omitempty"`
	EndedAt    string `json:"endedAt,omitempty"`
}

// ---- the card doors: top up a wallet, or buy a plan --------------------------
//
// All three MOVE MONEY, so all three carry the same two properties. The SUBJECT
// is resolved at the door from the caller's own credential and is a field here
// only so the store is never asked to re-derive an identity nobody proved. And
// none of them carries an AMOUNT the caller chose for a plan: a subscription is
// priced from the catalog, so there is no field an underpayment could be written
// in — it is a request that cannot be expressed rather than a check that can be
// forgotten.

const (
	BillingSubscriptions = "billing_subscriptions"

	// BillingTopupCard charges a single-use card token; BillingTopup charges a
	// card the subject already saved. Two ops rather than one with a mode,
	// because they take different things and fail in different places: one
	// vaults nothing and one must find a saved row first.
	BillingTopupCard = "billing_topup_card"
	BillingTopup     = "billing_topup"

	BillingSubscribe = "billing_subscribe"
	BillingRecharge  = "billing_recharge"
)

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
	// BalanceCents is the subject's balance AFTER the charge settled, in cents, so
	// a caller does not have to re-read to show the new number.
	BalanceCents int64 `json:"balanceCents"`
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
	SourceID       string `json:"sourceId,omitempty"`
	MethodID       string `json:"methodId,omitempty"`
	PlanID         string `json:"planId"`
	Subject        string `json:"subject"`
	StoreID        string `json:"storeId,omitempty"`
	Quantity       int    `json:"quantity,omitempty"`
	Level          int    `json:"level,omitempty"`
	Currency       string `json:"currency,omitempty"`
	Email          string `json:"email,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

// Sale is the receipt for a card subscription: what was opened, what it cost,
// and which card renewals will charge.
type Sale struct {
	SubscriptionID  string `json:"subscriptionId"`
	InvoiceID       string `json:"invoiceId"`
	PlanID          string `json:"planId"`
	Level           int    `json:"level"`
	PaymentMethodID string `json:"paymentMethodId"`
	AmountCents     int64  `json:"amountCents"`
	Currency        string `json:"currency"`
	Status          string `json:"status"`
}

// Sold is what a sale answered with, and it keeps the two shapes APART: a fresh
// receipt, or the sealed body of an identical earlier sale replayed verbatim.
//
// Exactly one field is ever set, and the door owes a different status for each —
// 201 for the first, 200 for the second. A retry that got 201 back would read as
// a second subscription having been opened. Replayed is RAW so the retry gets
// the bytes the first answer was rather than a second rendering of them.
type Sold struct {
	Sale     *Sale  `json:"sale,omitempty"`
	Replayed []byte `json:"replayed,omitempty"`
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
