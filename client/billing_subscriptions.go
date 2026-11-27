// billing_subscriptions.go holds customer subscriptions, external plan records, invoice orders and renewal/recharge sweeps.

package client

import "time"

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

