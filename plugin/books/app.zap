# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package books

struct AskRequest {
    Question text @0
    From     text @8
    To       text @16
}

struct AskResponse {
    Answer    text        @0
    Figures   list<bytes> @8
    Followups list<text>  @16
    Sources   list<text>  @24
}

struct BalanceSheet {
    AsOf             text        @0
    Assets           list<bytes> @8
    Liabilities      list<bytes> @16
    Equity           list<bytes> @24
    TotalAssets      i64         @32
    TotalLiabilities i64         @40
    TotalEquity      i64         @48
    Balanced         bool        @56
}

struct BankTally {
    Ingested   i64 @0
    Posted     i64 @8
    Reconciled i64 @16
    Questions  i64 @24
    Transfers  i64 @32
    Skipped    i64 @40
}

struct BookRequest {
    ScanID   text  @0
    Voucher  bytes @8
    Override bool  @16
}

struct BookResponse {
    ScanID text @0
    Posted bool @8
}

struct FinancialPackage {
    Org          text        @0
    From         text        @8
    To           text        @16
    GeneratedAt  text        @24
    TrialBalance bytes       @32
    PnL          bytes       @40
    BalanceSheet bytes       @48
    GL           list<bytes> @56
}

struct MetricsResponse {
    From            text        @0
    To              text        @8
    Period          text        @16
    Months          i64         @24
    MRR             i64         @32
    ARR             i64         @40
    Revenue         i64         @48
    COGS            i64         @56
    Burn            i64         @64
    GrossProfit     i64         @72
    GrossMarginBps  i64         @80
    NetIncome       i64         @88
    Cash            i64         @96
    DeferredRevenue i64         @104
    MonthlyBurn     i64         @112
    RunwayMonths    i64         @120
    Figures         list<bytes> @128
}

struct PnL {
    From         text        @0
    To           text        @8
    Income       list<bytes> @16
    Expense      list<bytes> @24
    TotalIncome  i64         @32
    TotalExpense i64         @40
    NetIncome    i64         @48
}

struct QuestionsResponse {
    Questions list<bytes> @0
}

struct Rule {
    Pattern  text @0
    Category text @8
    Priority i64  @16
}

struct TrialBalance {
    From        text        @0
    To          text        @8
    Rows        list<bytes> @16
    TotalDebit  i64         @24
    TotalCredit i64         @32
    Balanced    bool        @40
}

struct VendorRow {
    Canonical       text       @0
    Aliases         list<text> @8
    DefaultCategory text       @16
}

struct asOfIn {
    Sandbox text @0
    To      text @8
}

struct bankLimitIn {
    Sandbox text @0
    Limit   i64  @8
}

struct exportIn {
    Sandbox text @0
    From    text @8
    To      text @16
    Format  text @24
    Limit   i64  @32
}

struct glIn {
    Sandbox text @0
    Limit   i64  @8
}

struct inboxOut {
    Items list<bytes> @0
}

struct ledgerIn {
    Sandbox text @0
}

struct periodIn {
    Sandbox text @0
    From    text @8
    To      text @16
}

struct rulesOut {
    Rules list<bytes> @0
}

struct syncTally {
    Live    i64 @0
    Sandbox i64 @8
}

struct transactionsOut {
    Transactions list<bytes> @0
}

struct txnQuery {
    Sandbox  text @0
    From     text @8
    To       text @16
    Category text @24
    Vendor   text @32
    Limit    i64  @40
}

struct unreconciledOut {
    Questions    list<bytes> @0
    Transactions list<bytes> @8
}

struct vendorsOut {
    Vendors list<bytes> @0
}

interface books {
    # Returns the org's chart of accounts — the seeded fixed chart every
    # posting key in the ledger refers to.
    get_books_accounts(req: ledgerIn)
    # Returns the org's normalized bank transactions, newest first —
    # every row the import and connector paths have ingested, with its amount in exact cents,
    # its direction, and whether it has been matched to a voucher yet.
    get_books_bank_transactions(req: bankLimitIn)
    # Returns the org's unmatched bank inflows and their open clarifying
    # questions — the queue a human answers so an unexplained deposit is never guessed into
    # revenue.
    get_books_bank_unreconciled(req: ledgerIn) returns (rep: unreconciledOut)
    # Returns the complete financial package for the caller's org over
    # (from, to]: the trial balance, the P&L, the balance sheet, and the GL detail behind
    # them — the four statements a tax preparer or an investor asks for, assembled from the
    # one ledger in a single read so they cannot disagree with each other.
    get_books_export(req: exportIn) returns (rep: FinancialPackage)
    # ListGL returns the org's most recent GL Entry rows, newest first. This is the raw
    # double-entry detail behind every statement: one row per leg, with its debit, credit,
    # posting time and the source that booked it.
    get_books_gl(req: glIn)
    # Returns the org's open document queue — everything uploaded but not yet
    # booked, newest first, each with its extracted summary and the confidence the scanner
    # resolved its category at. A booked document drops out of the queue.
    get_books_inbox(req: ledgerIn) returns (rep: inboxOut)
    # Metrics returns the org's deterministic SaaS-metrics snapshot over an optional
    # (from, to] window — MRR, ARR, revenue, COGS, burn, gross margin, net income, cash,
    # deferred revenue, monthly burn and runway — as raw int64-cent figures AND the same
    # figures already formatted. Every number is the ledger, aggregated the one way the books
    # define it, never a guess; it is the grounded read the unified /v1/ask advisor replays.
    get_books_metrics(req: periodIn) returns (rep: MetricsResponse)
    # Returns the org's accrual-basis Profit & Loss over an optional (from, to]
    # window of RFC3339 posting times: recognized revenue, matched cost, and the net.
    get_books_pnl(req: periodIn) returns (rep: PnL)
    # Returns the org's Balance Sheet as of `to` (empty = all time), with the
    # Assets == Liabilities + Equity equation proof.
    get_books_position(req: asOfIn) returns (rep: BalanceSheet)
    # Returns the clarifying questions the caller's own recent GL raises — the
    # unusual postings a founder should look at (outliers, reversals, round-offs, uncosted
    # revenue, an overdrawn wallet), sharpest first. An empty list means the books look clean;
    # the detector is deterministic over the ledger and invents nothing.
    get_books_questions(req: ledgerIn) returns (rep: QuestionsResponse)
    # Returns the org's auto-categorization rules, highest priority first. A rule
    # is a standing instruction — "anything whose merchant contains X books to category Y" —
    # and it overrides a vendor's default category, so this is the list that decides how a
    # future bill classifies itself.
    get_books_rules(req: ledgerIn) returns (rep: rulesOut)
    # Returns the org's booked ledger as a single-line register, newest
    # first: one row per voucher, with its date, description, vendor, category, source and
    # amount in exact cents. It is the double-entry ledger projected to the register a human
    # reads, filterable by posting-time window, category and vendor. Strictly read-only — it
    # restates the books, it never moves them.
    get_books_transactions(req: txnQuery) returns (rep: transactionsOut)
    # Returns the org's trial balance over an optional [from, to] window of
    # RFC3339 posting times, including the opening/closing columns and the
    # TotalDebit == TotalCredit proof that the books balance.
    get_books_trial(req: periodIn) returns (rep: TrialBalance)
    # Returns the org's vendor book: each canonical vendor, the alias spellings a
    # receipt may print it under, and the expense account new bills from it default to. A
    # vendor here is what makes a scanned bill self-classify instead of asking again.
    get_books_vendors(req: ledgerIn) returns (rep: vendorsOut)
    # Answers a plain-language question about the caller's own books — "what is my
    # MRR?", "how long is my runway?" — with figures taken from their ledger, never a guessed
    # number. A deterministic keyword router picks the intent and reads the real metrics, and
    # those figures, followups and report sources are computed BEFORE any model call and are
    # never altered by one: the optional narration client only rephrases the sentence, and it
    # degrades silently to the templated answer when no AI plane is wired. It is strictly
    # read-only — it restates the books, it never posts to them.
    post_books_ask(req: AskRequest) returns (rep: AskResponse)
    # Pulls every connected bank (Plaid/Teller) for the caller's org, maps each
    # fetched transaction to a posting and books it idempotently, then advances that
    # connector's cursor so the next sync resumes where this one stopped. One connector's
    # outage is skipped rather than failing the whole sync. It reports the batch: how many
    # transactions were seen, how many vouchers posted, how many inflows reconciled against
    # the processor clearing account, how many raised a question, how many were own-account
    # transfers, and how many were already-processed no-ops. It is READ-ONLY against the
    # bank — it ingests, it never sends money.
    post_books_bank_sync() returns (rep: BankTally)
    # Creates or updates one auto-categorization rule, keyed by its pattern —
    # writing a pattern that already exists REPLACES that row's category and priority. The
    # category is normalized to a real COA expense account, and anything unrecognized becomes
    # 5900 Uncategorized rather than a guessed real account. It answers the row exactly as
    # stored, so the caller sees the normalization. A rule overrides a vendor's default
    # category, so this is the standing instruction that decides how a future bill classifies.
    post_books_rules(req: Rule) returns (rep: Rule)
    # Posts a reviewed scanned bill to the ledger. It is the scanner's ONLY write:
    # the voucher goes through the same post() choke point every other source uses, so it is
    # checked to balance (Σdebit == Σcredit) and is idempotent by (scan, scanId) — re-booking
    # the same scan answers posted=false and writes nothing. A bill whose economic identity
    # (vendor, total, issue date) already posted under a DIFFERENT scan is refused 409 unless
    # override is set, which is what stops the same receipt re-scanned into a new file hash
    # from double-booking. An unbalanced voucher is refused 400.
    post_books_scan_book(req: BookRequest) returns (rep: BookResponse)
    # Sync ingests the caller's OWN org from commerce into BOTH ledgers (live and sandbox)
    # and reports how many new vouchers posted to each. It is idempotent — money that has
    # already been booked posts nothing on a repeat — and it is read-only against commerce:
    # it never mints a deposit, a credit or a payout, only the accounting twin of money that
    # already moved.
    post_books_sync() returns (rep: syncTally)
    # Creates or updates one vendor in the org's vendor book, keyed by its
    # canonical name — writing a canonical name that already exists REPLACES that row's
    # aliases and default category. A category given as a slug ("software") is normalized to
    # its real COA expense account, and anything unrecognized becomes 5900 Uncategorized
    # rather than a guessed real account. It answers the row exactly as stored, so the caller
    # sees the normalization. Recording a vendor is what makes future bills from it
    # self-classify instead of asking again.
    post_books_vendors(req: VendorRow) returns (rep: VendorRow)
}

# ---------------------------------------------------------------------
# 20 op(s) here. What follows is what this schema does not carry.
#
# opaque (20) — crosses, arrives without its name:
#   AskResponse.Figures  books.Figure (list element)
#   BalanceSheet.Assets  books.BalanceLine (list element)
#   BalanceSheet.Equity  books.BalanceLine (list element)
#   BalanceSheet.Liabilities  books.BalanceLine (list element)
#   BookRequest.Voucher  books.Voucher
#   FinancialPackage.BalanceSheet  books.BalanceSheet
#   FinancialPackage.GL  books.GLRow (list element)
#   FinancialPackage.PnL  books.PnL
#   FinancialPackage.TrialBalance  books.TrialBalance
#   MetricsResponse.Figures  books.Figure (list element)
#   PnL.Expense  books.PnLLine (list element)
#   PnL.Income  books.PnLLine (list element)
#   QuestionsResponse.Questions  books.Question (list element)
#   TrialBalance.Rows  books.TrialBalanceRow (list element)
#   inboxOut.Items  books.InboxItem (list element)
#   rulesOut.Rules  books.Rule (list element)
#   transactionsOut.Transactions  books.Txn (list element)
#   unreconciledOut.Questions  books.BankQuestion (list element)
#   unreconciledOut.Transactions  books.BankTxnRow (list element)
#   vendorsOut.Vendors  books.VendorRow (list element)
