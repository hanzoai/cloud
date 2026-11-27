// billing_ledger.go holds statement, recharge, credit grant and spend alert types.

package client

import "encoding/json"

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

