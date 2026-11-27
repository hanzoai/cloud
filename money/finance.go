package money

// finance.go — the finance ledger's contract: a subject's prepaid wallet on the
// double-entry ledger, the native writes it takes and who may have paid for a served
// call. It lives with the amount it is made of, so every app that holds a money value
// can name the ledger without reaching a host package.

import "context"

// DepositInput is a native credit write (grant / prefund / settlement) to a
// subject's prepaid ledger in an org's namespace — the typed twin of the
// /v1/billing/deposit body, no HTTP. Authorization is the CALLER's: only
// already-gated paths (the global-admin grant, the platform service float) invoke
// it; the native write marks the ledger sink authorized because the HTTP mint gate
// it bypasses is replaced by that caller gate.
type DepositInput struct {
	Org      string // X-Org-Id namespace, resolved to the commerce Organization
	Subject  string // billing subject / DestinationId (org slug or "owner/name")
	Amount   Amount // amount to credit, exact 18-decimal USD (> 0)
	Currency string // default "usd"
	Notes    string
	Tags     string
	// Ref, when non-empty, is the deposit's idempotency key: two deposits carrying the SAME
	// Ref credit the wallet AT MOST ONCE (the replay is a no-op returning the first entry's
	// id), so a fixed Ref makes a backfill/settlement exactly-once. Empty keeps the additive
	// default — each grant takes a fresh ref and stacks. Mirrors UsageInput.Ref.
	Ref  string
	Test bool // write to the sandbox (test-mode) ledger
	// Cash says a settled payment funded this deposit — money the customer paid. Ref
	// must then be the payment's own reference (the processor's payment id, or
	// wire:<bank reference>), so one payment credits once whichever path records it. Unset, the deposit is credit Hanzo minted: a grant, a
	// promo, a carry. Notes says who minted it and why.
	Cash bool
}

// UsageInput is a native usage (withdraw/debit) write to a subject's prepaid
// ledger — the typed twin of the /v1/billing/usage body, no HTTP.
type UsageInput struct {
	Org      string
	Subject  string // billing subject / SourceId
	Amount   Amount // amount to debit, exact 18-decimal USD (> 0)
	Currency string
	Model    string
	Provider string
	Project  string
	Service  string
	// Ref names the metered ACT this debit records, and it is the debit's idempotency
	// key WITHIN the subject's wallet: two debits carrying the same Ref for the same
	// subject and amount move the money AT MOST ONCE. Empty keeps the additive default —
	// the ledger mints the entry's own ref and the debit stands alone.
	//
	// IT IS THE SERVER'S WORD FOR THE ACT, never a value the payer chose. It used to be
	// the request's X-Request-Id, which the edge propagates verbatim from the client and
	// CORS-allows from a browser: a caller who pinned that header was billed once for
	// every call it made afterwards. Set it from an identity the server assigns — a
	// message row's id, a settlement id, a registration ref — or leave it empty and let
	// the ledger mint one. The correlation id belongs on the log line and on the
	// attribution wire (metering.Usage.RequestID), never here. Mirrors DepositInput.Ref.
	Ref string
	// Cash says the debit never draws granted credit: the usage policy refused credit
	// for what was bought. Cash and the org's credit line pay; what they cannot cover
	// is owed.
	Cash bool
	Test bool
	// Cost is what serving the call cost the platform — the provider's price, our COGS —
	// exact. Zero when unknown.
	Cost Amount
	// Class is the model's usage class: premium, ours or free.
	Class string
	// Units is the work served, in the unit the call is priced in (tokens for a model).
	Units int64
	// PaidBy is who pays: PaidByPlan or PaidByHanzo move no money and are counted;
	// empty (or any wallet source) is a wallet debit, and the ledger records which of
	// prepaid, line and credits paid it. A zero amount with no payer is PaidByHanzo.
	PaidBy string
	// Actor is the member who made the call, "<org>/<name>", when that is not the
	// wallet being billed, and Key the API key it arrived on, "<org>/<id>". Both are
	// attribution only: they decide no balance, and the usage log keeps them so every
	// charge names its person and its key.
	Actor string
	Key   string
}

// Who paid for one served call (money.UsageInput.PaidBy, the usage log's paid_by).
const (
	PaidByPlan    = "plan"    // the plan's included usage
	PaidByPrepaid = "prepaid" // the wallet's cash
	PaidByLine    = "line"    // the org's credit line: owed, invoiced
	PaidByCredits = "credits" // granted credit
	PaidByHanzo   = "hanzo"   // the platform absorbs it: a free model, a free allowance
)

// FinanceClient is the ONE money interface : a subject's prepaid wallet on the double-entry finance ledger
// (hanzoai/ledger). Balance is a ledger account balance; a deposit and a usage debit
// are balanced ledger postings (funding→wallet, wallet→revenue) — so every customer
// debit IS a platform-revenue credit in one entry. IAM multi-tenancy is the account
// namespace (org/project), scoped by the authenticated caller. Orthogonal to
// CommerceClient: the ai prepaid gate, the admin grant, and the edge meter all bill
// through THIS, and it composes the same finance ledger the treasury posts to — one
// ledger, two account layers.
type FinanceClient interface {
	// Balance returns subject's AVAILABLE prepaid balance as an exact 18-decimal USD money
	// value (settled ledger balance; transient holds are the caller's in-pod
	// reservation) within org's namespace. The ONE balance read the ai gate + the edge
	// meter share.
	Balance(ctx context.Context, org, subject, currency string, test bool) (Amount, error)
	// Deposit posts a credit (funding→wallet) to subject's ledger wallet and returns
	// the ledger entry id. Idempotent on in.Ref when set.
	Deposit(ctx context.Context, in DepositInput) (entryID string, err error)
	// RecordUsage posts a usage debit (wallet→revenue) from subject's ledger wallet.
	// Idempotent on in.Ref WITHIN subject's wallet.
	RecordUsage(ctx context.Context, in UsageInput) error
	// SumUsageSince returns org's metered usage in CENTS at/after the unix cutoff
	// `since` within one (project, service) scope (deposits excluded; test selects the
	// sandbox books). An empty project or service does not narrow that axis, so
	// ("", "") is the org total. The spend cap's window source — usage older than the
	// cutoff drops out of the sum, so a moving window needs no reset job.
	SumUsageSince(ctx context.Context, org string, test bool, project, service string, since int64) (int64, error)
}
