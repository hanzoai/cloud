# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package company

struct EIN {
    Status      text        @0
    Number      text        @8
    Expedited   bool        @16
    Responsible bytes       @24
    NAICS       text        @32
    Forms       list<bytes> @40
    Online      bool        @48
}

struct RoundInput {
    Name              text @0
    RoundType         text @8
    TargetAmount      f64  @16
    PreMoneyValuation f64  @24
    PricePerShare     f64  @32
    ShareClassID      text @40
}

struct Tariff {
    Structure      text        @0
    Jurisdiction   text        @8
    Lines          list<bytes> @16
    DueNowCents    i64         @24
    RecurringCents i64         @32
    Recurring      text        @40
    Currency       text        @48
}

struct advanceIn {
    To text @0
}

struct beginIn {
    Structure           text @0
    Jurisdiction        text @8
    Name                text @16
    AlreadyIncorporated bool @24
}

struct decisionIn {
    Email  text @0
    Status text @8
}

struct einIn {
    Responsible bytes @0
    NAICS       text  @8
    Expedited   bool  @16
}

struct esignCompleteIn {
    Signed bool @0
}

struct esignOut {
    Provider  text  @0
    EsignRef  text  @8
    Formation bytes @16
}

struct formationView {
    Formation  bytes      @0
    NextStages list<text> @8
}

struct foundersIn {
    Founders list<bytes> @0
}

struct importCapTableIn {
    SpreadsheetID text @0
    Range         text @8
}

struct importCapTableOut {
    StakeholdersImported i64   @0
    Rows                 i64   @8
    Formation            bytes @16
}

struct importDocumentsIn {
    FolderID text @0
}

struct importDocumentsOut {
    Ingested  i64   @0
    Formation bytes @8
}

struct kycRefreshOut {
    Provider  text  @0
    Formation bytes @8
}

struct kycStartOut {
    Provider  text        @0
    Sessions  list<bytes> @8
    Formation bytes       @16
}

struct registerFilter {
    Stage     text @0
    Structure text @8
    Limit     i64  @16
    Offset    i64  @24
}

struct registerPage {
    Formations list<bytes> @0
    Count      i64         @8
    Limit      i64         @16
    Offset     i64         @24
}

struct reviewFilter {
    Limit i64 @0
}

struct reviewQueue {
    Queue list<bytes> @0
    Count i64         @8
}

struct roundOut {
    RoundID text @0
}

struct safeIn {
    DocumentIDs list<text>  @0
    Signers     list<bytes> @8
}

struct safeOut {
    EsignRef text @0
    Provider text @8
}

struct structureIn {
    Structure    text @0
    Jurisdiction text @8
    Name         text @16
}

struct tariffIn {
    Structure     text @0
    Jurisdiction  text @8
    ExpeditedEIN  bool @16
    AgentOfRecord bool @17
}

interface company {
    # Get returns the caller org's formation and the stages reachable from it, or 404
    # when the org has not begun one.
    get_company() returns (rep: formationView)
    # Returns the platform's whole formation register, newest activity
    # first — every org's formation, not the caller's. It is a Hanzo platform
    # operation: a caller who is not a platform reviewer gets 403.
    # Filter by stage and structure, page with limit and offset. An unknown stage is
    # refused with 400 rather than returning a silently empty page.
    get_company_register(req: registerFilter) returns (rep: registerPage)
    # Reports the founders whose KYC is not yet settled, oldest formation
    # first, so the queue drains in the order founders have been waiting. A Hanzo
    # platform operation: a caller who is not a platform reviewer gets 403.
    # It only says who is waiting; the decision itself is POST
    # /v1/company/kyc/decision.
    get_company_review(req: reviewFilter) returns (rep: reviewQueue)
    # Begin starts the org's one formation and returns it with the stages reachable
    # from it. It is idempotent: an org that already has a formation gets that one
    # back with 200, while a first call creates it and answers 201.
    post_company(req: beginIn) returns (rep: formationView)
    # Advance runs the ONE guarded transition of the formation machine. It is the
    # only endpoint between stages: the actions populate data, this decides ordering.
    # An edge the machine does not define answers 409; an edge whose guard is not yet
    # satisfied answers 422 naming what is missing. Reaching the terminal `company`
    # stage also records the incorporation on the canonical cap table, and that must
    # succeed before the transition is persisted.
    post_company_advance(req: advanceIn) returns (rep: formationView)
    # Renders the formation documents for the chosen structure and
    # jurisdiction, ingests each into the org's data room, and submits the state
    # filing through the filing client.
    # With no filing partner wired the filing is recorded honestly as "manual" — no
    # filing id is fabricated. Available only at the documents stage.
    post_company_documents() returns (rep: formationView)
    # Opens the EIN application and answers what it owes.
    # The answer states whether it can be filed ONLINE, because that is the fact
    # deciding whether the customer waits a sitting or several weeks — and it names
    # each form with what that form is for, so nobody has to already know what an
    # SS-4 is to understand why they are signing one.
    post_company_ein(req: einIn) returns (rep: EIN)
    # Sends the generated formation documents for signature by every
    # founder and records the provider's reference on the formation. Available only
    # at the esign stage.
    post_company_esign() returns (rep: esignOut)
    # Records whether the formation documents have been signed. It
    # consults the e-signature provider, which a real provider's webhook drives; the
    # signal is idempotent.
    # An explicit `signed` in the request overrides the provider's answer, which is
    # the manual path for the stub provider that never self-completes.
    post_company_esign_complete(req: esignCompleteIn) returns (rep: formationView)
    # Replaces the formation's founders. Each founder needs a name, an
    # email and an equity share in basis points; every founder is (re)set to pending
    # KYC, so a previously settled decision does not survive a change of the list.
    post_company_founders(req: foundersIn) returns (rep: formationView)
    # Records a fundraising round on the org's canonical cap table.
    # Available only after incorporation (stage company); roundType defaults to
    # PRICED.
    post_company_fundraise_round(req: RoundInput) returns (rep: roundOut)
    # Raises an e-signature request over documents already in the org's
    # data room — a SAFE, a convertible note, or any other fundraising paper.
    # Available only after incorporation (stage company).
    post_company_fundraise_safe(req: safeIn) returns (rep: safeOut)
    # Seeds the canonical cap table with the founding allocation
    # (stakeholders, a common share class, issued shares) and anchors the
    # deterministic equity-genesis root on-chain.
    # It is idempotent: once a root is recorded the cap table is NOT re-seeded, which
    # would double-issue founder share certificates. The root is persisted even when
    # the on-chain submit fails, because the root is the tamper-evident witness and
    # must not be recomputed on retry. Available only at the genesis stage.
    post_company_genesis() returns (rep: formationView)
    # Reads an existing company's cap table from a Google Sheet and
    # adds its stakeholders to the canonical cap table.
    # The first row is a header and columns are matched by name (case-insensitive):
    # name and email are required, type/relationship/institution optional. A sheet
    # without name and email columns, or with no usable data rows, is refused with
    # 400. Available only at the import stage.
    post_company_import_captable(req: importCapTableIn) returns (rep: importCapTableOut)
    # Ingests an existing company's corporate documents from a Google
    # Drive folder into the org's data room. The import is shallow — sub-folders are
    # skipped, not walked — and available only at the import stage.
    post_company_import_documents(req: importDocumentsIn) returns (rep: importDocumentsOut)
    # StartKYC opens an identity-verification session for every founder with the
    # wired provider and records each session's reference on the formation.
    # A start is never a decision: any terminal status the provider reports at
    # inquiry time is clamped back to pending, so the payment gate can never open
    # here. A terminal status arrives only from POST /v1/company/kyc/refresh (the
    # provider) or POST /v1/company/kyc/decision (a Hanzo platform reviewer).
    post_company_kyc() returns (rep: kycStartOut)
    # DecideKYC records a privileged reviewer's MANUAL decision on a founder's KYC —
    # the human-in-the-loop path, and the ONLY route to a pass when no real provider
    # is wired. It produces a DISTINCT reviewer_confirmed, never a provider
    # "verified".
    # Because Hanzo forms the entity and carries the formation KYC/AML obligation,
    # the reviewer is a HANZO platform reviewer (SuperAdmin), and the decision is
    # ATTRIBUTED to them.
    post_company_kyc_decision(req: decisionIn) returns (rep: formationView)
    # RefreshKYC reconciles each pending founder's KYC with the WIRED provider — the
    # PULL path to a provider-reported terminal status. For the manual provider the
    # check stays pending; for a real provider it reflects the settled decision,
    # ATTRIBUTED to the provider.
    # It NEVER trusts a client-asserted status — the status comes from the PROVIDER —
    # so a client cannot force a pass here, and an already-passing founder (e.g. a
    # reviewer confirmation) is left untouched.
    post_company_kyc_refresh() returns (rep: kycRefreshOut)
    # Charges the caller's own org the one-time Hanzo Company formation fee.
    # It is $999 unless the deployment sets another, and the answer is the formation
    # record carrying its paid flag and the charge reference. It takes no body: the org is the validated tenant and the amount is the
    # platform's, never the caller's to assert.
    # IDEMPOTENT on the formation rather than on the request: an already-paid
    # formation answers 200 with the same record and is not charged again, so a
    # retry or a double-clicked button costs nothing. Available only at the
    # `payment` stage (409 anywhere else) and only for an org that has begun a
    # formation (404 otherwise).
    # A denial answers the fleet-wide billing contract — 402 insufficient_balance,
    # 402 spend_cap_exceeded, 503 balance_unavailable — carried by cloud.Denied,
    # which is the money wire's own {"error":{"code","message"}} body rather than a
    # second vocabulary invented for this surface.
    # The gate is the LAST thing it does, after the stage check and the paid
    # short-circuit, so a caller the machine is about to refuse is never charged.
    # That ordering is why the gate cannot lift into middleware, where it would run
    # first. Both facts are pinned: TestPaymentDenialWire, TestPaymentChargesLast.
    post_company_payment() returns (rep: formationView)
    # Skip marks the org as already incorporated and moves it onto the import path,
    # so an existing company brings its documents and cap table in instead of forming
    # a new entity. Available only at the structure stage.
    post_company_skip() returns (rep: formationView)
    # Itemises what a formation costs before anyone commits to it.
    # It answers what is due now and what recurs, as separate figures, and marks the
    # state's filing fee as money we collect and remit rather than keep. A caller can
    # therefore show a payer the whole bill — which is the point of quoting at all,
    # and was impossible while the fee was one number in an error string.
    # A jurisdiction whose filing fee this deployment has not been told REFUSES,
    # naming the setting that fixes it. Quoting our half as though it were the total
    # is the one answer that would be worse than no answer.
    post_company_tariff(req: tariffIn) returns (rep: Tariff)
    # Records the entity kind, the state of formation and the proposed
    # name. Available only at the structure stage; an unknown structure or
    # jurisdiction, or an empty name, is refused with 400.
    put_company_structure(req: structureIn) returns (rep: formationView)
}

# ---------------------------------------------------------------------
# 22 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_company_register_summary  registerCounts.ByStage  map[string]int  (map)
#
# opaque (15) — crosses, arrives without its name:
#   EIN.Forms  company.Form (list element)
#   EIN.Responsible  company.Responsible
#   Tariff.Lines  company.Charge (list element)
#   einIn.Responsible  company.Responsible
#   esignOut.Formation  company.Formation
#   formationView.Formation  company.Formation
#   foundersIn.Founders  company.Founder (list element)
#   importCapTableOut.Formation  company.Formation
#   importDocumentsOut.Formation  company.Formation
#   kycRefreshOut.Formation  company.Formation
#   kycStartOut.Formation  company.Formation
#   kycStartOut.Sessions  company.kycSession (list element)
#   registerPage.Formations  company.Registration (list element)
#   reviewQueue.Queue  company.waiting (list element)
#   safeIn.Signers  company.Signer (list element)
