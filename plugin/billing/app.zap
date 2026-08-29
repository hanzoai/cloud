# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package billing

struct Alert {
    ID               text @0
    UserID           text @8
    Title            text @16
    Threshold        i64  @24
    Currency         text @32
    Project          text @40
    Service          text @48
    Enforce          bool @56
    SoftPct          i64  @64
    RateLimitRpm     i64  @72
    TriggeredAt      text @80
    Period           text @88
    ResetsAt         text @96
    CreatedAt        text @104
    UpdatedAt        text @112
    PeriodSpentCents i64  @120
    Over             bool @128
    Warn             bool @129
}

struct AlertPatch {
    Subject      text @0
    ID           text @8
    Title        text @16
    Threshold    i64  @24
    Project      text @32
    Service      text @40
    Enforce      bool @48
    SoftPct      i64  @56
    RateLimitRpm i64  @64
}

struct AlertSpec {
    Subject      text @0
    Title        text @8
    Threshold    i64  @16
    Currency     text @24
    Project      text @32
    Service      text @40
    Enforce      bool @48
    SoftPct      i64  @56
    RateLimitRpm i64  @64
}

struct AutoRecharge {
    Subject         text @0
    Enabled         bool @8
    ThresholdCents  i64  @16
    AmountCents     i64  @24
    Currency        text @32
    LastRechargedAt text @40
    Stored          bool @48
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
    ID             text @0
    Status         text @8
    Chain          text @16
    Token          text @24
    DepositAddress text @32
    AddressTag     text @40
    ExpiresAt      text @48
}

struct CryptoOptions {
    Chains list<text> @0
    Tokens list<text> @8
}

struct Detachment {
    Deleted bool @0
    ID      text @8
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
}

struct InvoiceRef {
    ID text @0
}

struct Invoices {
    Rows  list<bytes> @0
    Count i64         @8
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

struct PaymentConfig {
    Provider      text @0
    ApplicationID text @8
    LocationID    text @16
    Environment   text @24
    Live          bool @32
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
    User          text        @0
    Plan          text        @8
    Currency      text        @16
    Period        text        @24
    Windows       list<bytes> @32
    Included      bytes       @40
    ConsumedCents i64         @48
    OverageCents  i64         @56
    Balance       bytes       @64
}

struct Subscription {
    ID                   text  @0
    UserID               text  @8
    PlanID               text  @16
    Status               text  @24
    Quantity             i64   @32
    CurrentPeriodStart   text  @40
    CurrentPeriodEnd     text  @48
    CancelAtPeriodEnd    bool  @56
    MRRCents             i64   @64
    ProviderType         text  @72
    DefaultPaymentMethod text  @80
    Plan                 bytes @88
    CreatedAt            text  @96
    UpdatedAt            text  @104
    TrialStart           text  @112
    TrialEnd             text  @120
    CanceledAt           text  @128
    EndedAt              text  @136
}

struct SubscriptionRef {
    ID          text @0
    AtPeriodEnd bool @8
}

struct Subscriptions {
    Rows  list<bytes> @0
    Count i64         @8
}

struct Tier {
    User    text        @0
    Tier    bytes       @8
    Balance bytes       @16
    Windows list<bytes> @24
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

struct capQuery {
    Project text @0
    Service text @8
    Amount  text @16
    PV      text @24
}

struct cryptoAsset {
    Chain       text @0
    Token       text @8
    AmountCents i64  @16
}

struct depositRef {
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

struct topupIn {
    SourceID       text @0
    MethodID       text @8
    AmountCents    i64  @16
    Currency       text @24
    IdempotencyKey text @32
}

struct transactionRef {
    ID text @0
}

struct window {
    Range text @0
}

interface billing {
    # Ends a subscription.
    # It cancels at the END OF THE PAID PERIOD by default, because a customer who
    # cancels has already paid for the period they are in and taking it away is
    # taking money for nothing. `atPeriodEnd: false` ends it at once, which is the
    # caller asking for that.
    # A subscription from another org is not found rather than refused, so an id
    # cannot be probed for existence.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    cancelSubscription(req: SubscriptionRef) returns (rep: Subscription)
    # Collects an issued invoice: credit grants first, then prepaid balance, then the
    # card on file — the same waterfall the dunning workflow runs.
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
    # is not something this operation can reach. A platform or service caller
    # detaches on the subject's behalf, and that authority is decided HERE, where the
    # credential is, and travels as a value: authority decided twice is authority
    # that eventually disagrees with itself.
    # The card is vaulted at the processor, so what goes is our token for it.
    delete_billing_methods_by_id(req: methodRef) returns (rep: Detachment)
    # DetachPortalMethod is DetachMethod at the address a hosted checkout addresses
    # it by. One set of rows, two spellings: a card detached at either is gone from
    # both, because there is one store behind them.
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
    # call, and its caller is a SERVICE rather than a person: a service token plus
    # the gateway-pinned org, with no user behind it. So this admits that principal
    # where the CRUD beside it does not.
    # Every covering row is evaluated, most-restrictive-wins, and the tightest one
    # is what `capCents`, `spentCents` and `reason` describe. Soft rows never deny;
    # nor does a project-scoped enforcing row whose project axis the caller could
    # not establish — `pv=1` is how a caller states that it did, and an unproven
    # claim must not be able to refuse traffic.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_alerts_authorize(req: capQuery) returns (rep: CapVerdict)
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
    # An intent belonging to another payer answers 404, exactly as an id that names
    # nothing, so a guessed id cannot confirm that somebody else's deposit exists.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_crypto_deposit_by_id(req: depositRef) returns (rep: CryptoDeposit)
    # Answers which chains and tokens the crypto rail accepts — what an asset picker
    # renders.
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
    get_billing_invoices() returns (rep: Invoices)
    # Answers the org's own postings inside `range=`, each as a signed
    # entry: a DEPOSIT CREDITS the wallet (positive, account `credits:<org>`) and
    # every other posting DEBITS it (negative, account `usage:<org>`), described by
    # its notes or its tags. The sign is the posting's own meaning, read through ONE
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
    # Answers the org's outbound payouts, newest first — amount, destination,
    # status, and the failure reason where one applies.
    # A payout is ORG-scoped rather than subject-scoped, so there is nothing to pin
    # beyond the tenant the caller already is, and no query can widen it.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_billing_payouts()
    # Reads the caller's auto-reload rule: top the balance up by `amountCents`
    # whenever it falls below `thresholdCents`, charging the card on file
    # off-session. It is the same setting every prepaid AI account calls auto-reload.
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
    # Lists the plans the caller holds, with the count beside them.
    # It is scoped to the caller's own org, so a query cannot widen it to another
    # customer's. An org on nothing is an empty list, not a refusal — being on no
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
    # collectible.
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
    # Issues a deposit address the caller can send crypto to, on the asset they ask
    # for.
    # The address credits the CALLER'S own wallet and nobody else's: the payer is
    # the validated principal, never a body value. Asking again reuses the caller's
    # open intent rather than minting a second address, so a refresh cannot spray
    # key generations — and a payer who sent to the address they saw earlier is
    # still credited.
    # No balance moves here. The chain watcher credits on real confirmations, so
    # what comes back is an address and a status, not a receipt.
    # An asset this rail cannot mint on is 400 — ask for another. A rail that is
    # shut for that asset is 503 — nothing sent now can be credited.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_crypto_deposit(req: cryptoAsset) returns (rep: CryptoDeposit)
    # Moves this org between sandbox money and real money.
    # It decides whether a charge hits a real card, so it is the one posture change
    # that is not self-service: the platform bar, never an org owner, because an org
    # that could put itself in test mode could take priced work for free.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    post_billing_mode(req: ModeIn) returns (rep: Mode)
    # Sweeps every org's auto-recharge and answers what it did.
    # PLATFORM AUTHORITY ONLY. It charges saved cards across every tenant, so an org
    # owner reaching it could sweep-charge the estate; a caller without it is
    # refused before anything is charged.
    # The answer explains a sweep that charged nobody as readily as one that
    # charged: it names how many orgs were considered and how many needed charging,
    # with a row each.
    post_billing_recharge_run_all() returns (rep: Recharge)
    # Charges a card the caller already saved and credits the
    # balance. Same receipt and the same retry safety as the token endpoint; the only
    # difference is which card, so a caller topping up from a saved method never
    # re-enters one.
    post_billing_topup(req: topupIn) returns (rep: Charged)
    # Charges a single-use card token and credits the caller's
    # balance.
    # The token comes from the payment form and is vaulted as part of the charge, so
    # no card number reaches this service and none is stored here. The receipt names
    # the ledger entry, the new balance, and the PROCESSOR's own reference — which is
    # the only field that proves money moved at the gateway rather than only in our
    # ledger.
    # Retry-safe on X-Idempotency-Key: the same key settles one charge and returns
    # the first receipt.
    post_billing_topup_token(req: topupIn) returns (rep: Charged)
    # Sets the caller's auto-reload rule, and answers with the rule as stored.
    # ENABLING REQUIRES A CARD ON FILE (400), because the sweep charges off-session:
    # a rule naming no chargeable method is a promise the schedule cannot keep. A
    # non-positive amount and a negative threshold are refused the same way, each
    # naming the field that was wrong.
    # The rule is the caller's OWN. The org comes from the validated principal and
    # the body names none, so there is no field a write could be steered through onto
    # another tenant's schedule.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    put_billing_recharge(req: AutoRechargeEdit) returns (rep: AutoRecharge)
    # Raises a DRAFT invoice against a customer in the caller's own org.
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
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    reactivateSubscription(req: SubscriptionRef) returns (rep: Subscription)
    # Voids a draft or issued invoice — the cancel.
    # A paid invoice cannot be voided: money has moved, and the correction for that
    # is a refund, not an erasure. The state machine refuses it and that refusal is
    # the answer.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    voidInvoice(req: InvoiceRef) returns (rep: Invoice)
}

# ---------------------------------------------------------------------
# 39 op(s) here. What follows is what this schema does not carry.
#
# opaque (20) — crosses, arrives without its name:
#   Collected.Invoice  plane.Invoice
#   CreditBalance.Balances  plane.CreditEntry (list element)
#   CreditBreakdown.Tags  plane.CreditTag (list element)
#   CreditBreakdown.Total  plane.CreditTotal
#   CreditGrants.Rows  plane.CreditGrant (list element)
#   Invoice.Lines  plane.InvoiceLine (list element)
#   Invoices.Rows  plane.BillingInvoice (list element)
#   RaiseIn.Lines  plane.InvoiceLine (list element)
#   Recharge.Results  plane.Recharged (list element)
#   Rollup.Balance  plane.RollupBalance
#   Rollup.Included  plane.RollupAllotment
#   Rollup.Windows  plane.Window (list element)
#   Subscription.Plan  plane.SubscriptionPlan
#   Subscriptions.Rows  plane.Subscription (list element)
#   Tier.Balance  plane.TierBalance
#   Tier.Tier  plane.TierLimits
#   Tier.Windows  plane.Window (list element)
#   Transactions.Rows  plane.Transaction (list element)
#   accounts.Accounts  link.RoutedUsage (list element)
#   accounts.Total  link.AccountsTotal
#
# renamed (3) — spelled differently here than on every other surface:
#   get_billing_credit-balance  ->  get_billing_credit_balance
#   get_billing_credit-balance_breakdown  ->  get_billing_credit_balance_breakdown
#   post_billing_recharge_run-all  ->  post_billing_recharge_run_all
