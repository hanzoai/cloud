package books

// coa.go — the FIXED Hanzo chart of accounts. This is the accounting VOCABULARY the
// whole domain posts against: a small, closed set of accounts seeded into every org's
// books on first open, never edited by a request. Porting ERPNext's Accounts SEMANTICS
// (not its dynamic tree) to Go means the chart is a value, not a table an admin mutates
// — one and only one chart, identical across orgs, so a rule map (ingest.go) and a
// report (trial-balance) can reference an account by its stable number.
//
// THE ONE MONEY-MODEL DECISION. Prepaid credits a customer buys are a LIABILITY, not
// income: the money is the customer's until it is SPENT. So a top-up CREDITS the
// Customer Wallet liability (2000, deferred revenue) — recognizing revenue at deposit
// would book money we may still owe back. Revenue is RECOGNIZED only when usage
// consumes the wallet: Dr 2000 Customer Wallet / Cr 4000 Usage revenue. That single
// deferral is the reason this domain exists.

// AccountType is the fundamental accounting class of an account. It records the account's
// NORMAL balance side (asset/expense are debit-normal; liability/income/equity are
// credit-normal) for classification and presentation; the trial balance places a signed
// net by its SIGN, so a faithfully-kept ledger never depends on the normal side to balance.
type AccountType string

const (
	Asset     AccountType = "asset"
	Liability AccountType = "liability"
	Income    AccountType = "income"
	Expense   AccountType = "expense"
	Equity    AccountType = "equity"
)

// PartyType marks an account that carries a SUBLEDGER (the Payment Ledger Entry twin):
// a receivable (money owed TO us) or a payable (money we owe). A leg posted to such an
// account also writes a payment_ledger_entry row, mirroring ERPNext's party-account
// posting. NoParty accounts (bank, wallet, revenue, COGS) carry no subledger.
type PartyType string

const (
	NoParty    PartyType = ""
	Receivable PartyType = "receivable"
	Payable    PartyType = "payable"
)

// Account is one line of the chart: a stable number (the posting key), a human name, its
// fundamental type, and — for AR/AP — its party subledger class.
type Account struct {
	Number string      `json:"number"`
	Name   string      `json:"name"`
	Type   AccountType `json:"type"`
	Party  PartyType   `json:"party,omitempty"`
}

// Account numbers — the ONE set of posting keys the rule map and reports reference.
const (
	Bank            = "1000" // operating cash
	SquareClearing  = "1010" // funds captured by Square, pre-settlement
	AR              = "1200" // accounts receivable (party)
	OwnerEquity     = "3000" // owner equity
	RoundOff        = "3900" // round-off difference sink (see Post)
	CustomerWallet  = "2000" // PREPAID CREDITS — a liability / deferred revenue
	OSSPayout       = "2100" // owed to OSS maintainers (party)
	SalesTaxPayable = "2200" // collected sales tax owed to authorities (party)
	AccruedInfra    = "2300" // accrued cloud/GPU infra cost owed (COGS accrual sink)
	UsageRevenue    = "4000" // AI usage revenue — RECOGNIZED on consumption
	MRR             = "4100" // recurring subscription revenue
	ProductRevenue  = "4200" // one-off product revenue
	CloudCOGS       = "5000" // cloud / GPU cost of goods sold
	ProcessorFees   = "5100" // payment-processor fees
	PromoCredit     = "5200" // promotional credit given away
	// UncategorizedExpense is the DEFAULT debit sink for a bank OUTFLOW the bank
	// engine could not categorize (bank_map.go). It exists so an outflow always
	// books against a real expense account — never a guessed one — and surfaces as
	// a bucket a human recategorizes, rather than silently landing in COGS.
	UncategorizedExpense = "5900" // outflow pending human categorization
)

// chartOfAccounts is the FIXED chart seeded into every org's books. Order is
// number-ascending within class so the seed and the accounts report read top-to-bottom.
var chartOfAccounts = []Account{
	// Assets (debit-normal)
	{Number: Bank, Name: "Bank", Type: Asset},
	{Number: SquareClearing, Name: "Square clearing", Type: Asset},
	{Number: AR, Name: "Accounts receivable", Type: Asset, Party: Receivable},
	// Liabilities (credit-normal)
	{Number: CustomerWallet, Name: "Customer wallet (prepaid credits)", Type: Liability},
	{Number: OSSPayout, Name: "OSS payout payable", Type: Liability, Party: Payable},
	{Number: SalesTaxPayable, Name: "Sales tax payable", Type: Liability, Party: Payable},
	// Accrued infra is an AGGREGATE cost accrual (matching COGS to recognized revenue),
	// not a vendor-specific invoice — so it carries NO party subledger. It is drawn down
	// when the real cloud/GPU bill settles (a future cash voucher), never party-matched here.
	{Number: AccruedInfra, Name: "Accrued infrastructure payable", Type: Liability},
	// Equity (credit-normal)
	{Number: OwnerEquity, Name: "Equity", Type: Equity},
	{Number: RoundOff, Name: "Round-off", Type: Equity},
	// Income (credit-normal)
	{Number: UsageRevenue, Name: "AI usage revenue", Type: Income},
	{Number: MRR, Name: "Recurring revenue (MRR)", Type: Income},
	{Number: ProductRevenue, Name: "Product revenue", Type: Income},
	// Expenses (debit-normal)
	{Number: CloudCOGS, Name: "Cloud / GPU COGS", Type: Expense},
	{Number: ProcessorFees, Name: "Processor fees", Type: Expense},
	{Number: PromoCredit, Name: "Promotional credit", Type: Expense},
	{Number: UncategorizedExpense, Name: "Uncategorized expense", Type: Expense},
}

// accountByNumber indexes the chart for O(1) party/type lookup on the posting path.
var accountByNumber = func() map[string]Account {
	m := make(map[string]Account, len(chartOfAccounts))
	for _, a := range chartOfAccounts {
		m[a.Number] = a
	}
	return m
}()
