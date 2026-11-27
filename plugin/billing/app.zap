# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package billing

struct AgentOrder {
    ID             text  @0
    CheckoutID     text  @8
    Status         text  @16
    Currency       text  @24
    TotalCents     i64   @32
    CreditedCents  i64   @40
    PaymentID      text  @48
    SubscriptionID text  @56
    Plan           text  @64
    Token          bytes @72
    CompletedAt    text  @80
}

struct AgentPayment {
    Status  text  @0
    Error   bytes @8
    Invoice bytes @16
}

struct Alert {
    ID               text       @0
    UserID           text       @8
    Title            text       @16
    Threshold        i64        @24
    Currency         text       @32
    Project          text       @40
    Service          text       @48
    Enforce          bool       @56
    SoftPct          i64        @64
    RateLimitRpm     i64        @72
    TriggeredAt      text       @80
    Period           text       @88
    ResetsAt         text       @96
    CreatedAt        text       @104
    UpdatedAt        text       @112
    PeriodSpentCents list<i64>  @120
    Over             list<bool> @128
    Warn             list<bool> @136
}

struct AlertPatch {
    Subject      text       @0
    ID           text       @8
    Title        list<text> @16
    Threshold    list<i64>  @24
    Project      list<text> @32
    Service      list<text> @40
    Enforce      list<bool> @48
    SoftPct      list<i64>  @56
    RateLimitRpm list<i64>  @64
}

struct AlertSpec {
    Subject      text       @0
    Title        text       @8
    Threshold    i64        @16
    Currency     text       @24
    Project      text       @32
    Service      text       @40
    Enforce      list<bool> @48
    SoftPct      i64        @56
    RateLimitRpm i64        @64
}

struct AutoRecharge {
    Subject         text @0
    Enabled         bool @8
    ThresholdCents  i64  @16
    AmountCents     i64  @24
    Currency        text @32
    LastRechargedAt text @40
    Stored          bool @48
    PerDay          i64  @56
}

struct AutoRechargeEdit {
    Enabled        bool @0
    ThresholdCents i64  @8
    AmountCents    i64  @16
    Currency       text @24
}

struct CapVerdict {
    Allow      bool @0
    Reason     text @8
    CapCents   i64  @16
    SpentCents i64  @24
    WarnPct    i64  @32
}

struct Charged {
    TransactionID text @0
    BalanceCents  i64  @8
    Status        text @16
    ProcessorRef  text @24
    Test          bool @32
}

struct Collected {
    Invoice          bytes @0
    Paid             bool  @8
    CreditUsedCents  i64   @16
    BalanceUsedCents i64   @24
    CardChargedCents i64   @32
    ProcessorRef     text  @40
    Reason           text  @48
}

struct CreditBalance {
    UserID   text        @0
    Balances list<bytes> @8
}

struct CreditBreakdown {
    UserID text        @0
    Tags   list<bytes> @8
    Total  bytes       @16
}

struct CreditGrants {
    Rows  list<bytes> @0
    Count i64         @8
}

struct CryptoDeposit {
    ID                    text @0
    Status                text @8
    Chain                 text @16
    Token                 text @24
    DepositAddress        text @32
    AddressTag            text @40
    ExpiresAt             text @48
    AmountCents           i64  @56
    MinimumCents          i64  @64
    FeeCents              i64  @72
    RequiredConfirmations i64  @80
    Confirmations         i64  @88
    CreditedCents         i64  @96
    Instructions          text @104
    PlanID                text @112
    SubscriptionID        text @120
}

struct CryptoOptions {
    Chains  list<text>  @0
    Tokens  list<text>  @8
    Assets  list<bytes> @16
    Gateway list<text>  @24
}

struct CryptoTopup {
    IntentID    text        @0
    AmountCents i64         @8
    Currency    text        @16
    UsdOnly     bool        @24
    Addresses   list<bytes> @32
    Status      text        @40
}

struct Detachment {
    Deleted bool @0
    ID      text @8
}

struct GatewayPayment {
    ID          text @0
    Status      text @8
    Network     text @16
    From        text @24
    Value       text @32
    AmountCents i64  @40
    Credited    bool @48
    Deferred    bool @49
    Reason      text @56
    Payment     text @64
    TxHash      text @72
    CreatedAt   text @80
    UpdatedAt   text @88
}

struct GatewayTerms {
    Version     i64         @0
    Scheme      text        @8
    PayTo       text        @16
    Nonce       text        @24
    Salt        text        @32
    MinCents    i64         @40
    MaxCents    i64         @48
    MaxValidity i64         @56
    Primary     text        @64
    Types       bytes       @72
    Accepts     list<bytes> @80
}

struct Invoice {
    ID              text        @0
    Number          text        @8
    UserID          text        @16
    CustomerEmail   text        @24
    Status          text        @32
    Currency        text        @40
    SubtotalCents   i64         @48
    AmountDueCents  i64         @56
    AmountPaidCents i64         @64
    Lines           list<bytes> @72
    PaymentRef      text        @80
    CreatedAt       text        @88
    Quote           bytes       @96
}

struct InvoicePayment {
    Status         text  @0
    Invoice        bytes @8
    Authentication bytes @16
    Reason         text  @24
}

struct InvoiceRef {
    ID    text @0
    Brand text @8
}

struct Invoices {
    Rows   list<bytes> @0
    Count  i64         @8
    Cursor text        @16
}

struct InvoicesIn {
    Subject        text @0
    Status         text @8
    SubscriptionID text @16
    Limit          i64  @24
    Cursor         text @32
}

struct Mode {
    OrgID    text @0
    OrgName  text @8
    Live     bool @16
    TestMode bool @17
}

struct ModeIn {
    TestMode bool @0
}

struct PayPalOrderOut {
    OrderID     text @0
    AmountCents i64  @8
    Currency    text @16
    Intent      text @24
    Status      text @32
    Environment text @40
    Test        bool @48
}

struct PaymentConfig {
    Provider      text        @0
    ApplicationID text        @8
    LocationID    text        @16
    Environment   text        @24
    Live          bool        @32
    PayPal        bytes       @40
    Stripe        bytes       @48
    Cards         list<bytes> @56
}

struct PriceQuote {
    Amount            i64  @0
    Decimals          i64  @8
    Currency          text @16
    UsdCents          i64  @24
    Rate              f64  @32
    Source            text @40
    FXFee             text @48
    ExchangeRate      f64  @56
    LockExpiresAt     i64  @64
    QuoteID           text @72
    ReferenceRate     f64  @80
    ReferenceProvider text @88
    BaseRate          f64  @96
    FeeRate           f64  @104
    Premium           f64  @112
    Increment         i64  @120
    Disclosure        text @128
}

struct RaiseIn {
    UserID        text        @0
    CustomerEmail text        @8
    Currency      text        @16
    Lines         list<bytes> @24
}

struct Recharge {
    Orgs    i64         @0
    Charged i64         @8
    Results list<bytes> @16
}

struct Rollup {
    User          text  @0
    Plan          text  @8
    Currency      text  @16
    Period        text  @24
    Included      bytes @32
    ConsumedCents i64   @40
    OverageCents  i64   @48
    Balance       bytes @56
}

struct Route {
    Processor     text        @0
    Reason        text        @8
    Methods       list<text>  @16
    Country       text        @24
    Rule          text        @32
    Chain         list<bytes> @40
    LocalCurrency text        @48
    Version       i64         @56
    Quote         bytes       @64
    Crypto        list<bytes> @72
    Alternatives  list<text>  @80
}

struct Subscription {
    ID                   text        @0
    UserID               text        @8
    PlanID               text        @16
    Status               text        @24
    Quantity             i64         @32
    CurrentPeriodStart   text        @40
    CurrentPeriodEnd     text        @48
    CancelAtPeriodEnd    bool        @56
    MRRCents             i64         @64
    ProviderType         text        @72
    DefaultPaymentMethod text        @80
    Settled              text        @88
    Seats                i64         @96
    ChargedCents         i64         @104
    Plan                 bytes       @112
    CreatedAt            text        @120
    UpdatedAt            text        @128
    TrialStart           text        @136
    TrialEnd             text        @144
    CanceledAt           text        @152
    EndedAt              text        @160
    Test                 bool        @168
    Coupon               text        @176
    Held                 bool        @184
    Lines                list<bytes> @192
    Assigned             list<bytes> @200
    LastPaid             text        @208
    CustomerCents        i64         @216
}

struct Subscriptions {
    Rows  list<bytes> @0
    Count i64         @8
}

struct Tier {
    User         text  @0
    Plan         text  @8
    Subscription text  @16
    Tier         bytes @24
    Balance      bytes @32
    PlanName     text  @40
}

struct TokenSupplied {
    Status text  @0
    Error  bytes @8
    Token  bytes @16
}

struct TopupIntentCancelled {
    IntentID text @0
    State    text @8
}

struct TopupIntentOut {
    IntentID       text  @0
    ClientSecret   text  @8
    PublishableKey text  @16
    Currency       text  @24
    Amount         i64   @32
    UsdCents       i64   @40
    Rate           f64   @48
    Quote          bytes @56
    PolicyVersion  i64   @64
}

struct TopupIntentState {
    State          text @0
    Credited       bool @8
    PaymentID      text @16
    SubscriptionID text @24
}

struct Transaction {
    ID        text  @0
    Type      text  @8
    Amount    i64   @16
    Currency  text  @24
    Tags      text  @32
    Notes     text  @40
    Metadata  bytes @48
    CreatedAt text  @56
    ExpiresAt text  @64
}

struct Transactions {
    Rows  list<bytes> @0
    Count i64         @8
    User  text        @16
}

struct WireCredit {
    Reference   text @0
    Subject     text @8
    AmountCents i64  @16
    Credited    bool @24
}

struct WireInstructions {
    BankName      text @0
    BankAddress   text @8
    AccountNumber text @16
    RoutingNumber text @24
    SwiftCode     text @32
    IBAN          text @40
    AccountName   text @48
    Memo          text @56
    Reference     text @64
}

struct accountRef {
    ID text @0
}

struct accounts {
    Scope    text        @0
    Source   text        @8
    Total    bytes       @16
    Accounts list<bytes> @24
}

struct alertRef {
    ID text @0
}

struct assignment {
    Assigned list<bytes> @0
}

struct capQuery {
    Project text @0
    Service text @8
    Amount  text @16
    PV      text @24
}

struct checkoutChange {
    ID    text        @0
    Items list<bytes> @8
}

struct checkoutIn {
    Items list<bytes> @0
}

struct checkoutRef {
    ID text @0
}

struct commerceBalance {
    Balance     i64       @0
    Holds       i64       @8
    Available   i64       @16
    Account     text      @24
    Cash        i64       @32
    Credit      i64       @40
    Prepaid     i64       @48
    CreditLimit list<i64> @56
    Due         list<i64> @64
    Line        list<i64> @72
}

struct completeIn {
    ID      text  @0
    Key     text  @8
    Payment bytes @16
}

struct cryptoAsset {
    Chain       text @0
    Token       text @8
    AmountCents i64  @16
    PlanID      text @24
    Quantity    i64  @32
    Level       i64  @40
    Interval    text @48
    Coupon      text @56
}

struct cryptoTopupIn {
    AmountCents i64  @0
    Network     text @8
}

struct depositRef {
    ID text @0
}

struct gatewayRef {
    ID text @0
}

struct intentRef {
    ID text @0
}

struct ledgerPage {
    Currency text @0
    Limit    text @8
    Offset   text @16
}

struct methodRef {
    ID text @0
}

struct paypalOrderIn {
    AmountCents    i64         @0
    PlanID         text        @8
    StoreID        text        @16
    Quantity       i64         @24
    Lines          list<bytes> @32
    Level          i64         @40
    Interval       text        @48
    Currency       text        @56
    Coupon         text        @64
    BalanceCents   i64         @72
    IdempotencyKey text        @80
}

struct quoteQuery {
    Plan     text @0
    Level    i64  @8
    Interval text @16
    UsdCents i64  @24
    Country  text @32
    Currency text @40
    Method   text @48
}

struct resize {
    Quantity i64         @0
    Lines    list<bytes> @8
}

struct routeIn {
    Country     text @0
    Currency    text @8
    AmountCents i64  @16
    Method      text @24
}

struct signedPayment {
    Network       text  @0
    Salt          text  @8
    Authorization bytes @16
    Signature     text  @24
}

struct subscriptionAct {
    ID          text @0
    AtPeriodEnd bool @8
}

struct tokenIn {
    ID    text @0
    Token text @8
}

struct topupIn {
    SourceID       text  @0
    MethodID       text  @8
    AmountCents    i64   @16
    Currency       text  @24
    Provider       text  @32
    IdempotencyKey text  @40
    Phone          text  @48
    Billing        bytes @56
}

struct topupIntentIn {
    AmountCents i64   @0
    Country     text  @8
    Currency    text  @16
    Method      text  @24
    Phone       text  @32
    Billing     bytes @40
    PlanID      text  @48
    Quantity    i64   @56
    Level       i64   @64
    Interval    text  @72
    Coupon      text  @80
}

struct transactionRef {
    ID text @0
}

struct usageReceipt {
    ID      text  @0
    Org     text  @8
    Account text  @16
    Amount  bytes @24
}

struct usageReport {
    ID      text  @0
    Org     text  @8
    Amount  bytes @16
    Service text  @24
    Model   text  @32
    Project text  @40
}

struct window {
    Range text @0
}

struct wireReconcileIn {
    Reference     text @0
    AmountCents   i64  @8
    BankReference text @16
}

interface billing {
    # Assigns a team's seats: which member holds a standard seat and which a premium
    # one, replacing what was assigned.
    # A seat type is given no more members than the seats of it paid for — the fewer
    # of the period in hand's and the next one's — and a member holds one seat; past
    # either the request is 400 and nothing changes. Which member holds a seat moves
    # no money and bounds no usage: a team's usage is all of its seats' together.
    # The team's seats are its admins' to assign, and the platform's. A member is
    # refused with 403 and told to ask an admin. A subscription from another org, or
    # held by a wallet the caller may not change, is not found.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    assignSeats(req: assignment) returns (rep: Subscription)
    # Ends a subscription.
    # It cancels at the END OF THE PAID PERIOD by default, because a customer who
    # cancels has already paid for the period they are in and taking it away is
    # taking money for nothing. `atPeriodEnd: false` ends it at once, which is the
    # caller asking for that.
    # The org's plan is its admins' to end, and the platform's in support mode. A
    # member is refused with 403 and told to ask an admin; a member billed from a
    # wallet of their own may end that wallet's plan. Every one is on the audit
    # trail, the operator named when it is the platform acting inside the org.
    # A subscription from another org, or held by a wallet the caller may not
    # change, is not found rather than refused, so an id cannot be probed for
    # existence.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    cancelSubscription(req: subscriptionAct) returns (rep: Subscription)
    # Collects an issued invoice: credit grants first, then prepaid balance, then the
    # card on file — the same waterfall the dunning workflow runs. It spends the
    # named customer's money, so it is the org admin's act; a member is refused 403.
    # A DECLINE IS NOT AN ERROR. It answers with paid=false, a reason, and the
    # invoice still open, because a declined collection is a normal business outcome
    # that must remain retryable — and because sealing it as a failure would wedge
    # dunning behind a replayed decline. Only a successful collection is sealed, so a
    # retry of a paid invoice replays the receipt instead of charging again.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    collectInvoice(req: InvoiceRef) returns (rep: Collected)
    # Removes one of the caller's spend caps and answers 204.
    # Removing a cap RAISES what the org may spend, so it takes the same authority
    # setting one does. The caps that remain still bind: this drops one, never the
    # whole policy.
    delete_billing_alerts_by_id(req: alertRef)
    # Removes one card or account the caller has saved.
    # It detaches only the CALLER'S own — the wallet this request bills from,
    # resolved server-side — so an id belonging to another customer of the same org
    # is not something this operation can reach. The org's cards are removed only
    # by an admin of the org or the platform; a member is refused with 403 and told
    # to ask an admin. A platform or service caller
    # detaches on the subject's behalf, and that authority is decided HERE, where the
    # credential is, and travels as a value: authority decided twice is authority
    # that eventually disagrees with itself.
    # The card is vaulted at the processor, so what goes is our token for it.
    delete_billing_methods_by_id(req: methodRef) returns (rep: Detachment)
    # Removes one card or account the caller has saved, at the
    # address a hosted checkout reaches it by. One set of rows, two spellings: a card
    # detached at either is gone from both, because there is one store behind them.
    delete_billing_portal_methods_by_id(req: methodRef) returns (rep: Detachment)
    # Reads one invoice out of the caller's org.
    # The org scopes the read by construction — the store is namespaced to it — so an
    # id belonging to another tenant is not found rather than found and then filtered.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    getInvoice(req: InvoiceRef) returns (rep: Invoice)
    # Answers the caller's billing accounts: the org itself, its currency, when it
    # was opened, and the caller's own standing in it.
    # The standing is the caller's, resolved from the validated principal here and
    # sent to the store rather than looked up there — the membership roster is IAM's
    # and commerce keeps none, so a callee that answered "what role is this" would
    # be inventing it. An anonymous read gets the account with no role rather than
    # an implied membership.
    # Scoped to the caller's own org, which is the whole tenancy story: there is no
    # org field on the wire and none on the input.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_accounts()
    # Answers one billing account's roster.
    # commerce stores no roster — that is IAM's — so the only member it can name is
    # the caller, and that is what comes back. What it does enforce is that the
    # account named in the path is the caller's own: a foreign id is 403, not an
    # empty list, because "no members" and "not your account" are different answers.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_accounts_by_id_members(req: accountRef)
    # Lists this org's spend caps: the ceiling, its scope, whether it enforces, and
    # how much of it has been spent this period.
    # `periodSpentCents`, `over` and `warn` are ABSENT rather than zero when the
    # spend could not be read, because "nothing spent" and "spend unknown" are
    # different answers and a customer acting on the first when the second is true
    # would be reading a ceiling that is not there. The policy row is reported
    # either way.
    # The period is the UTC calendar month and `resetsAt` is when the count starts
    # again, so a surface can say "resets on" without a second call.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_alerts()
    # Answers whether one proposed spend fits inside this org's caps.
    # It is the per-request verdict the metering edge consumes before every priced
    # call, and its caller is a SERVICE rather than a person: the platform, stating
    # the org over the plane, with no user behind it. So this admits that principal
    # where the CRUD beside it does not.
    # Every covering row is evaluated, most-restrictive-wins, and the tightest one
    # is what `capCents`, `spentCents` and `reason` describe. Soft rows never deny;
    # nor does a project-scoped enforcing row whose project axis the caller could
    # not establish — `pv=1` is how a caller states that it did, and an unproven
    # claim must not be able to refuse traffic.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_alerts_authorize(req: capQuery) returns (rep: CapVerdict)
    # Answers the spendable prepaid balance of the wallet this caller bills from — the same
    # wallet the AI prepaid gate reads before admitting a paid request, the edge meter
    # debits, and a top-up or admin grant credits.
    # The wallet is an ADDRESS, not an org: `account` echoes the key resolved within the
    # ledger — the org's shared pool for a tenant org, a personal account for a member of
    # the shared signup org.
    # `balance`, `holds` and `available` are whole USD cents, floored from the ledger's
    # exact 18-decimal value. `cash`, `line` and `credit` say where `available` came from,
    # and add up to it; `prepaid` is `cash` plus `line`.
    # Scoped to the caller's own org from the validated IAM principal (401 without one).
    # A balance that cannot be read is 502, never 0: unknown is not broke.
    get_billing_balance() returns (rep: commerceBalance)
    # Answers what the caller can spend right now, one entry per currency.
    # Only ACTIVE grants count: a voided, exhausted or lapsed grant contributes
    # nothing, which is why this number can be smaller than the grant list suggests
    # and why the two reads exist separately. It is credit, not prepaid balance —
    # /v1/billing/balance is the wallet, and the two are added by the gate, never by
    # a reader.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_credit_balance() returns (rep: CreditBalance)
    # Answers that same spendable credit split by grant tag, with the earliest
    # expiry under each and the total across all of them.
    # The split is the point: it is how trial credit is told apart from bought
    # credit, which is what a surface asks before it decides whether to spend any.
    # An unregistered address answers 404 and a caller reads that as "no credit", so
    # this being served is the difference between a customer with a trial grant
    # being offered their trial and being told they have none.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_credit_balance_breakdown() returns (rep: CreditBreakdown)
    # Lists the caller's credit grants — every one of them, spent and lapsed and
    # voided included.
    # That is deliberate and it is what makes the list useful: a grant list is a
    # LEDGER, and one that hid its spent rows could not be reconciled against a
    # burn-down. What is spendable right now is the sibling read, /v1/billing/
    # credit-balance, and the two are different questions.
    # Scoped to the caller's own wallet, resolved server-side.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_credits() returns (rep: CreditGrants)
    # Reads one of the caller's own deposit intents back — pending, confirming, or
    # succeeded.
    # status is pending until a transfer to the address is seen, confirming while
    # the chain buries it (confirmations counts up to requiredConfirmations), and
    # succeeded once it is credited: creditedCents is then in the caller's balance
    # as cash (GET /v1/billing/balance, cash). The answer is the same shape the mint
    # returned, terms and instructions included, so polling it is all an agent needs.
    # An intent belonging to another payer answers 404, exactly as an id that names
    # nothing, so a guessed id cannot confirm that somebody else's deposit exists.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_crypto_deposit_by_id(req: depositRef) returns (rep: CryptoDeposit)
    # Answers the terms to sign a USDC top-up against, for an agent or a person
    # paying from a wallet: the treasury to pay, the networks and USDC contracts,
    # the EIP-712 domain and types, the limits, and a nonce issued to the caller.
    # Sign TransferWithAuthorization {from, to: payTo, value, validAfter,
    # validBefore, nonce} under one network's domain, then POST it here with the
    # salt. The nonce is the caller's own: a payment signed against it credits the
    # caller and cannot be presented by anyone else.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_crypto_gateway() returns (rep: GatewayTerms)
    # Reads one of the caller's own USDC top-ups back: pending, accepted (credited
    # or deferred), settled, failed or refused.
    # A payment belonging to another payer answers 404, exactly as an id that names
    # nothing, so a guessed id cannot confirm somebody else's payment exists.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_crypto_gateway_by_id(req: gatewayRef) returns (rep: GatewayPayment)
    # Answers which chains and tokens the crypto rail accepts — what an asset picker
    # renders — and the pairs themselves in assets: a chain and a token each on offer
    # are not a pair on offer.
    # It is the intersection of two live facts rather than a configured list: an
    # asset appears only if something is WATCHING it and the custody processor
    # supports it. An address nobody watches credits nobody, so offering one would
    # take a customer's money and lose it. A rail with nothing armed answers 503,
    # not an empty menu — "no rail" and "no assets" are different, and only one of
    # them means try again later.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_crypto_options() returns (rep: CryptoOptions)
    # Lists the caller's invoices, newest first, with the count beside them.
    # It is scoped to the caller's own billing subject — the wallet this request
    # bills from, resolved server-side — so a query cannot widen it to another
    # customer of the same org. An org with no invoices is an empty list, not a
    # refusal.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_invoices(req: InvoicesIn) returns (rep: Invoices)
    # Answers the org's own postings inside `range=`, each as a signed
    # entry: a DEPOSIT CREDITS the wallet (positive, account `credits:<org>`), a
    # HOLD sets money aside for a payment agreed and not yet made (negative, account
    # `held:<org>`) and its return gives it back (positive), and every other posting
    # DEBITS it (negative, account `usage:<org>`), described by its notes or its
    # tags. The sign is the posting's own meaning, read through ONE
    # vocabulary shared with the ledger that wrote it — a reader with its own
    # spelling for `deposit` rendered a customer's grant as a charge.
    # This is the closest projection of the truth. The org's double-entry postings
    # are the source of record — balanced, only ever appended, one file per org —
    # and this lane is that list, wider than either half of it: the deposits are the
    # grants /v1/billing/credits lists and the debits are the spend /v1/billing/usage
    # rolls up. It answers 503 where this deployment runs no ledger, rather than
    # reporting an empty wallet.
    # A row whose timestamp will not parse is KEPT rather than dropped — a malformed
    # date must show up in a money list, not vanish from it. `balanceCents` is
    # omitted: these are MOVEMENTS, and the standing balance is /v1/billing/balance.
    # Cents are ROUNDED from the ledger's exact 18-decimal USD. Scoped to the
    # caller's own org, where the org's ledger file is the tenant boundary; 401
    # without a validated principal.
    get_billing_ledger(req: window)
    # Confirms a completed checkout: the order, what it cost, what the wallet was credited or
    # the subscription it opened, and for a subscription what is left on the token it renews on.
    # A checkout that is not complete has no order yet (404), and one that is not the caller's
    # is 404.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_orders_by_id(req: checkoutRef) returns (rep: AgentOrder)
    # Answers the org's outbound payouts, newest first — amount, destination,
    # status, and the failure reason where one applies.
    # A payout is ORG-scoped rather than subject-scoped, so there is nothing to pin
    # beyond the tenant the caller already is, and no query can widen it.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_payouts()
    # Answers the price of what the caller is about to buy.
    get_billing_quote(req: quoteQuery) returns (rep: PriceQuote)
    # Reads the auto-reload rule of the wallet the caller pays from — the org's own
    # account, or a person's wallet in the signup org: top that balance up by
    # `amountCents` whenever it falls below `thresholdCents`, charging that payer's card
    # on file off-session, at most `perDay` times in a UTC day (0 is no daily limit,
    # which the payer's plan sets). It is the same setting every prepaid AI account
    # calls auto-reload.
    # An org that has never set one reads as disabled with zeroes rather than as an
    # error — "no rule" answers the question — and `stored` is how a caller tells
    # never-configured from deliberately-off.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_recharge() returns (rep: AutoRecharge)
    # Answers the PUBLIC half of this org's processor configuration — the ids a
    # browser needs to tokenize a card, and the environment it must tokenize
    # against.
    # It carries no secret: an application id is published to every checkout page by
    # design. What matters is that it names the SAME processor account the charge
    # will be made on, because a card vaulted against one account and charged
    # against another is a card that saves and then cannot be used.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_settings() returns (rep: PaymentConfig)
    # Lists the plans the caller may see, with the count beside them.
    # An admin of the org, and the platform, see every plan the org holds, and may
    # narrow to one wallet with `userId`. A member sees the plans of the wallet they
    # pay from — the org's own for a member of a pooled org, their own in the shared
    # signup org — and never another member's; the card a plan renews on is left off
    # a row the member cannot change. A query cannot widen any of it to another
    # customer's org. An org on nothing is an empty list, not a refusal — being on no
    # plan is an answer.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_subscriptions() returns (rep: Subscriptions)
    # Answers which tier the caller is on, what it allows, and what is left to spend.
    # `effectiveAvailable` is the ONLY figure to compare against zero. The others are
    # its parts — prepaid money, granted credits and the daily term are three sources
    # of one spend, not three balances to add up a second time.
    # A tier that cannot be READ is an error, never Free. The router in front of the
    # models maps any non-2xx to Free, so answering Free from a question nobody could
    # answer would pin every paying customer to the most restrictive row with nothing
    # anywhere to find.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_tier() returns (rep: Tier)
    # Says where one of the caller's own payments stands.
    get_billing_topup_intent_by_id(req: intentRef) returns (rep: TopupIntentState)
    # Answers one page of the caller's own ledger, newest first: what moved, how
    # much, when, and what it was tagged with.
    # `count` is the size of the WHOLE history rather than of the page, which is how
    # a reader knows there is more to ask for, and `user` echoes the wallet the page
    # was read for — the same subject the spend gate debits, so a customer can see
    # which account answered rather than guessing from their own token.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_transactions(req: ledgerPage) returns (rep: Transactions)
    # Reads one ledger entry by its id.
    # It is the MEMBER of the collection beside it rather than a second way to ask —
    # the same rows GET /v1/billing/transactions lists, addressed one at a time. A
    # top-up receipt is read here, because a receipt IS a ledger entry: the id this
    # takes is the `transactionId` a top-up hands back.
    # The read is narrower than the list: commerce's core loads the row and refuses
    # anything that is not a deposit, so a row that exists but is not a top-up
    # answers 404. That asymmetry is stated rather than closed, because widening a
    # money read to make two shapes match is not a change worth making for symmetry.
    # The books are the caller's own and cannot be named, so a guessed id misses
    # rather than reaching another tenant's ledger.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_transactions_by_id(req: transactionRef) returns (rep: Transaction)
    # Answers per-account totals for the linked provider accounts the
    # gateway ROUTED this caller's traffic through — requests, prompt and completion
    # tokens, recorded cost — plus their honest sum.
    # This is the one read in the billing namespace scoped to the PERSON, not the
    # org. Rows are keyed on (validated org, validated user), so a caller sees the
    # accounts THEY linked and never a colleague's, even inside one org — everything
    # else under /v1/billing is org-wide. Neither key is ever read from the request
    # body or the query.
    # It is a ROUTING counter, not the money ledger. `costCents` is 0 for an account
    # billed by its own subscription, where the plan pays the provider directly, so
    # these totals do not reconcile against what the org was charged.
    # /v1/billing/usage is the charged ledger.
    # 401 without a validated principal. Where the linked-account plane is not
    # resident the answer is an honest 501 — never an empty breakdown, which would
    # read as no usage.
    get_billing_usage_accounts() returns (rep: accounts)
    # Answers the caller's month: what their plan includes, what has been consumed
    # against it, and the wallet beside it.
    # The two blocks are SEPARATE monies and are never added. One is usage a plan
    # granted; the other is prepaid credit bought with a card. Their sum is not a
    # number anyone holds, and a reader that formed it would be inventing a balance.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_usage_rollup() returns (rep: Rollup)
    # Answers where to send a wire top-up: the receiving bank details, with the
    # caller's own payment reference.
    # The account is the SERVING BRAND'S — resolved from the host the customer is
    # paying on, so paying on one brand never shows another's bank — and the
    # reference carries the caller's billing key, which is how an arriving wire
    # names who it credits. Nothing mints here; a receipt is settled by an operator
    # once the bank confirms it.
    # It is all-or-nothing: no configured account is 503 rather than a partial form,
    # because nobody can wire to three fields out of five.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_wire() returns (rep: WireInstructions)
    # Issues a draft invoice: moves it to OPEN, assigns its number, and makes it
    # collectible. It is the org admin's act; a member is refused 403.
    # Only a draft can be issued. An invoice already open, paid or void is refused
    # with the state machine's own reason rather than being silently re-issued, which
    # would mint a second number for one debt.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    issueInvoice(req: InvoiceRef) returns (rep: Invoice)
    # Changes one spend cap: raise or lower the ceiling, flip enforcement, retune
    # the rate limit.
    # Only the fields the body carries move. Every mutable field is optional, and an
    # absent one is PRESERVED rather than reset — so a change that flips enforcement
    # cannot silently wipe the threshold it enforces.
    # A cap belonging to another org is a 404, not a 403: a guessed id must not
    # become an oracle for what anyone else holds.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    patch_billing_alerts_by_id(req: AlertPatch) returns (rep: Alert)
    # Pays an open invoice with the buyer present. A paid or declined payment answers 200
    # with the invoice and, for a decline, the reason. A card the bank asks to
    # authenticate answers 402 with what the buyer finishes (code, id, clientSecret,
    # publishableKey, quote, message). Paying twice is one payment.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    payInvoice(req: InvoiceRef) returns (rep: InvoicePayment)
    # Opens a spend cap on the caller's own org.
    # At least one limit must mean something: a threshold above zero (a spend cap)
    # or a requests-per-minute above zero (a rate limit). A row that bounds neither
    # is refused rather than stored, because a ceiling nothing measures against is a
    # ceiling a customer believes in and does not have.
    # The cap is keyed on the caller's own billing subject, resolved server-side —
    # the SAME key the verdict looks it up under, which is what makes enforcement
    # bind rather than merely record.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_alerts(req: AlertSpec) returns (rep: Alert)
    # Pays one of the caller's checkouts with a shared payment token the buyer granted the
    # agent (`{"payment":{"type":"shared_payment_token","token":"spt_..."}}`) or with a
    # stablecoin deposit (`{"payment":{"type":"stablecoin","network":"base"}}`), and answers
    # the checkout under the status the attempt earned.
    # 200 is complete, with the order. 202 waits on the buyer, and `next` says what to do: send
    # the deposit, or take the bank's step. A refusal is 402 for a payment that was declined and
    # 400, 404, 409, 422 or 429 for the rest, always with `error.code` to branch on.
    # The `Idempotency-Key` header is required. It binds the charge to this checkout, the buyer
    # and the key, so completing twice is one purchase; the same key answers the same order, and
    # another key on a checkout already paid is 409.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_checkouts_by_id_complete(req: completeIn)
    # Issues a deposit address the caller can send crypto to, on the asset they ask
    # for.
    # The address credits the CALLER'S own wallet and nobody else's: the payer is
    # the validated principal, never a body value. Asking again reuses the caller's
    # open intent rather than minting a second address, so a refresh cannot spray
    # key generations — and a payer who sent to the address they saw earlier is
    # still credited.
    # No balance moves here. The chain watcher credits on real confirmations, so
    # what comes back is an address, a status and the terms: the minimum that
    # credits, any fee, the confirmations required, and one line of instructions.
    # Once the transfer is that deep the deposit is credited as cash to the caller's
    # balance (GET /v1/billing/balance, cash) and its status is succeeded.
    # The asset is checked against GET /v1/billing/crypto/options before anything is
    # minted. A chain, a token or a pair the rail does not accept is 400, and the
    # answer lists the accepted chains, tokens and assets. A deposit rail with
    # nothing watched is 503 — nothing sent now can be credited, and the Gateway
    # rail or a card is the way to pay until it reopens.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_crypto_deposit(req: cryptoAsset) returns (rep: CryptoDeposit)
    # Takes a signed USDC payment from the caller's Circle Gateway balance to the
    # treasury, and answers what became of it.
    # accepted: Circle Gateway LOCKED the USDC and the caller is credited — or,
    # once the caller holds $1,000 of credit Gateway has not confirmed, credited
    # when Gateway confirms this payment (deferred). pending: the outcome is not
    # known yet; read it back or present the same payment again — it resolves to
    # one credit either way. A refusal is 4xx with Gateway's reason
    # (insufficient_balance, invalid_signature, …) or ours (nonce_not_issued,
    # recipient_mismatch, amount_below_minimum, …); 503 means the rail is shut.
    # Who is credited is the caller, never a body value: the payer is resolved
    # server-side, and the payment's own nonce must be the one issued to it.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_crypto_gateway(req: signedPayment) returns (rep: GatewayPayment)
    # Pays one of the caller's past-due renewals with a fresh shared payment token. The token
    # becomes the subscription's payment method when it carries a recurring interval that
    # covers the renewal, and the invoice is charged on it now.
    # 200 is paid. 402 is declined, with `error.code` to branch on, and the subscription keeps
    # the method it had. An invoice that is not the caller's is 404.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_invoices_by_id_agent_token(req: tokenIn) returns (rep: AgentPayment)
    # Moves this org between sandbox money and real money.
    # It decides whether a charge hits a real card, so it is the one posture change
    # that is not self-service: the platform bar, never an org owner, because an org
    # that could put itself in test mode could take priced work for free.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_mode(req: ModeIn) returns (rep: Mode)
    # Opens a server-priced PayPal REST v2 order for either a
    # bounded credit top-up (intent CAPTURE) or a plan purchase's first period
    # (intent AUTHORIZE).
    # No balance or subscription row is minted here: the browser hands the returned
    # `orderId` to the PayPal Smart Buttons SDK for buyer approval, then settles
    # the payment through POST /v1/billing/topup/token or
    # POST /v1/billing/subscribe/card with `provider: "paypal"`.
    post_billing_paypal_order(req: paypalOrderIn) returns (rep: PayPalOrderOut)
    # Sweeps every org's auto-recharge and answers what it did.
    # PLATFORM AUTHORITY ONLY. It charges saved cards across every tenant, so an org
    # owner reaching it could sweep-charge the estate; a caller without it is
    # refused before anything is charged.
    # The answer explains a sweep that charged nobody as readily as one that
    # charged: it names how many orgs were considered and how many needed charging,
    # with a row each.
    post_billing_recharge_run_all() returns (rep: Recharge)
    # Answers which processor takes the caller's new checkout.
    post_billing_route(req: routeIn) returns (rep: Route)
    # Replaces the token one of the caller's subscriptions renews on with a fresh one that
    # covers the next renewal, before the renewal is attempted, and answers what is left on it.
    # 200 is accepted. 402 is declined, with `error.code` to branch on, and the subscription
    # keeps the token it had. A subscription that is not the caller's is 404.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_subscriptions_by_id_agent_token(req: tokenIn) returns (rep: TokenSupplied)
    # Charges a card the caller already saved and credits the
    # balance. Same receipt and the same retry safety as the token endpoint; the only
    # difference is which card, so a caller topping up from a saved method never
    # re-enters one.
    # The org's card on file is charged only by an admin of the org or the platform;
    # a member is refused with 403 and told to ask an admin. A member billed from a
    # wallet of their own charges that wallet's card. An org selected with X-Org-Id that
    # the sign-in does not list is 409, and nothing is charged.
    post_billing_topup(req: topupIn) returns (rep: Charged)
    # Opens a stablecoin deposit for a top-up of the caller's own wallet and answers where to
    # send it, per network. Nothing is credited by this call: the wallet gets the dollars only
    # when the deposit is confirmed on chain and the payment reported succeeded, once. Refused
    # unless the account can take stablecoins now.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_topup_crypto(req: cryptoTopupIn) returns (rep: CryptoTopup)
    # Opens the caller's Stripe checkout for a top-up.
    post_billing_topup_intent(req: topupIntentIn) returns (rep: TopupIntentOut)
    # Ends one of the caller's own payments that is not yet confirmed.
    post_billing_topup_intent_by_id_cancel(req: intentRef) returns (rep: TopupIntentCancelled)
    # Charges a single-use card token and credits the caller's
    # balance.
    # The token comes from the payment form and is vaulted as part of the charge, so
    # no card number reaches this service and none is stored here. The receipt names
    # the ledger entry, the new balance, and the PROCESSOR's own reference — which is
    # the only field that proves money moved at the gateway rather than only in our
    # ledger.
    # Retry-safe on X-Idempotency-Key: the same key settles one charge and returns
    # the first receipt. An org selected with X-Org-Id that the sign-in does not list
    # is 409, and nothing is charged.
    post_billing_topup_token(req: topupIn) returns (rep: Charged)
    # Debits one act an application metered to the org it acts for, and
    # answers the receipt.
    # The caller is an application acting as itself — an IAM client_credentials
    # token — and the org is the one that token acts in: the application's own, or
    # one that granted it membership, selected with X-Org-Id and named again in
    # `org`. A person, an API key, an unauthenticated caller, and a body naming an
    # org the token does not act in are all refused before anything is debited.
    # The debit lands in the same ledger every other meter writes, in the wallet GET
    # /v1/billing/balance reports for the same caller. It is exactly-once on `id`: a
    # retry answers the same receipt, and the same id for a different amount is 409.
    # Recording does not gate — the work already happened — so a caller that must
    # refuse unfunded work asks GET /v1/billing/balance and GET
    # /v1/billing/alerts/authorize first.
    post_billing_usage(req: usageReport) returns (rep: usageReceipt)
    # Credits the wallet that holds a wire's reference with the cents the bank received, once.
    # A reference nobody was given credits nothing (404), a repeat posts nothing and answers
    # `credited: false`, and a repeat for another amount is 409.
    # SuperAdmin only. Every reconcile is on the audit trail as wire.reconciled, naming who
    # attested the receipt, and recorded by commerce as a billing event.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_wire_reconcile(req: wireReconcileIn) returns (rep: WireCredit)
    # Sets the auto-reload rule of the wallet the caller pays from, and answers with the
    # rule as stored.
    # The org's own account's rule is its admins' to change, and the platform's; a
    # member billed from a wallet of their own (a person in the signup org) changes that
    # wallet's rule and no other (403 otherwise, with the plain sentence "Only an admin
    # of <org> can change its auto-reload. Ask an admin to do it."): the sweep charges
    # the payer's default card with nobody there.
    # ENABLING REQUIRES A CARD ON FILE (400), because the sweep charges off-session:
    # a rule naming no chargeable method is a promise the schedule cannot keep. The
    # amount is a top-up's own range, $5 to $5,000, and the balance it reloads below
    # is $0 to $5,000; a value outside either is refused the same way, the range in
    # the sentence. The sweep reloads at most `perDay` times in a UTC day.
    # The rule is the payer's OWN. The org and the wallet come from the validated
    # principal and the body names neither, so there is no field a write could be
    # steered through onto another payer's schedule.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    put_billing_recharge(req: AutoRechargeEdit) returns (rep: AutoRecharge)
    # Raises a DRAFT invoice against a customer in the caller's own org. It is the
    # org admin's act; a member is refused 403.
    # The invoice is not collectible yet: a draft exists so it can be read and
    # corrected, and issueInvoice is the separate act that turns it into a demand for
    # payment. The subtotal and amount due are computed from the lines, so there is
    # no total to send and none to get wrong.
    # The billing org is the caller's, taken from the validated principal, so an
    # invoice can only ever be raised on the caller's own books.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    raiseInvoice(req: RaiseIn) returns (rep: Invoice)
    # Puts a canceled subscription back on its plan.
    # What asks for this is usually a recovered payment method or a support tool
    # rather than a browser, which is most of the argument for it having an address
    # at all. The engine decides whether the move is legal; a row it will not
    # reactivate comes back with its own reason.
    # Resuming the org's plan commits its card to the next renewal, so it takes the
    # authority ending it does: an admin of the org or the platform, or a member for
    # a wallet of their own. A member is refused with 403 and told to ask an admin.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    reactivateSubscription(req: subscriptionAct) returns (rep: Subscription)
    # Changes a team's seats: how many standard and premium seats its next period
    # opens at.
    # `quantity` is the subscription's own seat type, 0 to keep it, and `lines` each
    # other seat type: `{"lines":[{"planId":"team-premium","quantity":1}]}` adds a
    # premium seat to a team bought with standard ones. The renewal that opens the
    # next period charges the seats asked for, and the period in hand keeps the ones
    # it was paid for, so nothing is charged now. A team is at least 2 seats and at
    # most 100 across every seat type; past either, or naming a plan that is not a
    # seat type of the team, is 400 and nothing changes.
    # The team's seats are its admins' to change, and the platform's. A member is
    # refused with 403 and told to ask an admin. A subscription from another org, or
    # held by a wallet the caller may not change, is not found.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    resizeSubscription(req: resize) returns (rep: Subscription)
    # Voids a draft or issued invoice — the cancel. It is the org admin's act; a
    # member is refused 403.
    # A paid invoice cannot be voided: money has moved, and the correction for that
    # is a refund, not an erasure. The state machine refuses it and that refusal is
    # the answer.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    voidInvoice(req: InvoiceRef) returns (rep: Invoice)
}

# ---------------------------------------------------------------------
# 59 op(s) here. What follows is what this schema does not carry.
#
# blocked (3) — the op is absent; the field has no wire form:
#   get_billing_checkouts_by_id  checkoutView.Links  map[string]string  (map)
#   patch_billing_checkouts_by_id  checkoutView  billing.checkoutView  (reaches one)
#   post_billing_checkouts  checkoutView  billing.checkoutView  (reaches one)
#
# opaque (49) — crosses, arrives without its name:
#   AgentOrder.Token  client.TokenRoom
#   AgentPayment.Error  client.AgentError
#   AgentPayment.Invoice  client.Invoice
#   Collected.Invoice  client.Invoice
#   CreditBalance.Balances  client.CreditEntry (list element)
#   CreditBreakdown.Tags  client.CreditTag (list element)
#   CreditBreakdown.Total  client.CreditTotal
#   CreditGrants.Rows  client.CreditGrant (list element)
#   CryptoOptions.Assets  client.CryptoPair (list element)
#   CryptoTopup.Addresses  client.DepositAddress (list element)
#   GatewayTerms.Accepts  client.GatewayAccept (list element)
#   Invoice.Lines  client.InvoiceLine (list element)
#   Invoice.Quote  client.PriceQuote
#   InvoicePayment.Authentication  client.AuthenticationStep
#   InvoicePayment.Invoice  client.Invoice
#   Invoices.Rows  client.BillingInvoice (list element)
#   PaymentConfig.Cards  client.PaymentCard (list element)
#   PaymentConfig.PayPal  client.PayPalPublicConfig
#   PaymentConfig.Stripe  client.StripePublicConfig
#   RaiseIn.Lines  client.InvoiceLine (list element)
#   Recharge.Results  client.Recharged (list element)
#   Rollup.Balance  client.RollupBalance
#   Rollup.Included  client.RollupAllotment
#   Route.Chain  client.RouteStep (list element)
#   Route.Crypto  client.RoutingCryptoOffer (list element)
#   Route.Quote  client.PriceQuote
#   Subscription.Assigned  client.SubscriptionSeat (list element)
#   Subscription.Lines  client.SubscriptionLine (list element)
#   Subscription.Plan  client.SubscriptionPlan
#   Subscriptions.Rows  client.Subscription (list element)
#   Tier.Balance  client.TierBalance
#   Tier.Tier  client.TierLimits
#   TokenSupplied.Error  client.AgentError
#   TokenSupplied.Token  client.TokenRoom
#   TopupIntentOut.Quote  client.PriceQuote
#   Transactions.Rows  client.Transaction (list element)
#   accounts.Accounts  link.RoutedUsage (list element)
#   accounts.Total  link.AccountsTotal
#   assignment.Assigned  client.SubscriptionSeat (list element)
#   checkoutChange.Items  client.AgentItem (list element)
#   checkoutIn.Items  client.AgentItem (list element)
#   completeIn.Payment  client.AgentPay
#   paypalOrderIn.Lines  client.SaleLine (list element)
#   resize.Lines  client.SaleLine (list element)
#   signedPayment.Authorization  client.GatewayAuthorization
#   topupIn.Billing  client.BillingAddress
#   topupIntentIn.Billing  client.BillingAddress
#   usageReceipt.Amount  client.Money
#   usageReport.Amount  client.Money
#
# renamed (5) — spelled differently here than on every other surface:
#   get_billing_credit-balance  ->  get_billing_credit_balance
#   get_billing_credit-balance_breakdown  ->  get_billing_credit_balance_breakdown
#   post_billing_invoices_by_id_agent-token  ->  post_billing_invoices_by_id_agent_token
#   post_billing_recharge_run-all  ->  post_billing_recharge_run_all
#   post_billing_subscriptions_by_id_agent-token  ->  post_billing_subscriptions_by_id_agent_token
