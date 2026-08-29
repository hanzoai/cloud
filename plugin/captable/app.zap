# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package captable

struct captableCompany {
    CreatedAt            i64  @0
    ID                   text @8
    IncorporationCountry text @16
    IncorporationState   text @24
    IncorporationType    text @32
    Name                 text @40
    PublicID             text @48
    UpdatedAt            i64  @56
}

struct captableCompanyUpdate {
    SizedIn              bytes @0
    IncorporationCountry text  @8
    IncorporationState   text  @16
    IncorporationType    text  @24
    Name                 text  @32
}

struct captableConvertibleIn {
    SizedIn           bytes @0
    PublicID          text  @8
    StakeholderID     text  @16
    Capital           text  @24
    Type              text  @32
    Status            text  @40
    IssueDate         text  @48
    BoardApprovalDate text  @56
    ConversionCap     text  @64
    DiscountRate      text  @72
    InterestRate      text  @80
    AdditionalTerms   text  @88
}

struct captableCreated {
    ID      text @0
    Message text @8
    Success bool @16
}

struct captableDeleted {
    Success bool @0
}

struct captableEquityPlanIn {
    SizedIn                    bytes @0
    Name                       text  @8
    BoardApprovalDate          text  @16
    InitialSharesReserved      text  @24
    ShareClassID               text  @32
    DefaultCancellatonBehavior text  @40
    PlanEffectiveDate          text  @48
    Comments                   text  @56
}

struct captableEquityPlans {
    Data list<bytes> @0
}

struct captableInvested {
    ID         text @0
    Message    text @8
    NewShareID text @16
    Success    bool @24
}

struct captableInvestmentIn {
    SizedIn       bytes @0
    ID            text  @8
    StakeholderID text  @16
    Amount        text  @24
    Date          text  @32
    Comments      text  @40
}

struct captableInvestments {
    Data list<bytes> @0
}

struct captableNotes {
    Data list<bytes> @0
}

struct captableOptionIn {
    SizedIn           bytes @0
    GrantID           text  @8
    StakeholderID     text  @16
    EquityPlanID      text  @24
    Quantity          text  @32
    ExercisePrice     text  @40
    Type              text  @48
    Status            text  @56
    CliffYears        text  @64
    VestingYears      text  @72
    IssueDate         text  @80
    ExpirationDate    text  @88
    VestingStartDate  text  @96
    BoardApprovalDate text  @104
    Rule144Date       text  @112
    Notes             text  @120
}

struct captableOptions {
    Data list<bytes> @0
}

struct captableRoundCloseRequest {
    SizedIn   bytes @0
    CloseDate text  @8
    ID        text  @16
}

struct captableRoundDetail {
    Investments list<bytes> @0
    Round       bytes       @8
}

struct captableRoundIn {
    SizedIn           bytes @0
    Name              text  @8
    RoundType         text  @16
    TargetAmount      text  @24
    ShareClassID      text  @32
    PricePerShare     text  @40
    PreMoneyValuation text  @48
}

struct captableRounds {
    Data list<bytes> @0
}

struct captableSafeIn {
    SizedIn           bytes @0
    PublicID          text  @8
    StakeholderID     text  @16
    Capital           text  @24
    Type              text  @32
    Status            text  @40
    IssueDate         text  @48
    BoardApprovalDate text  @56
    ValuationCap      text  @64
    DiscountRate      text  @72
    AdditionalTerms   text  @80
}

struct captableSafes {
    Data list<bytes> @0
}

struct captableShareClassAmend {
    SizedIn                       bytes @0
    ID                            text  @8
    ClassType                     text  @16
    Name                          text  @24
    InitialSharesAuthorized       text  @32
    BoardApprovalDate             text  @40
    StockholderApprovalDate       text  @48
    VotesPerShare                 text  @56
    ParValue                      text  @64
    PricePerShare                 text  @72
    Seniority                     text  @80
    ConversionRights              text  @88
    ConvertsToShareClassID        text  @96
    LiquidationPreferenceMultiple text  @104
    ParticipationCapMultiple      text  @112
}

struct captableShareClassIn {
    SizedIn                       bytes @0
    ClassType                     text  @8
    Name                          text  @16
    InitialSharesAuthorized       text  @24
    BoardApprovalDate             text  @32
    StockholderApprovalDate       text  @40
    VotesPerShare                 text  @48
    ParValue                      text  @56
    PricePerShare                 text  @64
    Seniority                     text  @72
    ConversionRights              text  @80
    ConvertsToShareClassID        text  @88
    LiquidationPreferenceMultiple text  @96
    ParticipationCapMultiple      text  @104
}

struct captableShareIn {
    SizedIn             bytes      @0
    StakeholderID       text       @8
    ShareClassID        text       @16
    CertificateID       text       @24
    Quantity            text       @32
    Status              text       @40
    PricePerShare       text       @48
    CapitalContribution text       @56
    IPContribution      text       @64
    DebtCancelled       text       @72
    OtherContributions  text       @80
    CliffYears          text       @88
    VestingYears        text       @96
    CompanyLegends      list<text> @104
    IssueDate           text       @112
    Rule144Date         text       @120
    VestingStartDate    text       @128
    BoardApprovalDate   text       @136
}

struct captableShareTransfer {
    SizedIn         bytes @0
    ShareID         text  @8
    ToStakeholderID text  @16
    Quantity        text  @24
    CertificateID   text  @32
}

struct captableShares {
    Data list<bytes> @0
}

struct captableStakeholderPatch {
    SizedIn             bytes @0
    City                text  @8
    CurrentRelationship text  @16
    Email               text  @24
    ID                  text  @32
    InstitutionName     text  @40
    Name                text  @48
    StakeholderType     text  @56
    State               text  @64
    StreetAddress       text  @72
    TaxID               text  @80
    Zipcode             text  @88
}

struct captableSummary {
    ByShareClass  list<bytes> @0
    ByStakeholder list<bytes> @8
    Company       bytes       @16
    Convertibles  bytes       @24
    Rounds        bytes       @32
    Totals        bytes       @40
}

struct captableTransferred {
    Message     text @0
    NewShareID  text @8
    Success     bool @16
    Transferred i64  @24
}

struct captableUpdated {
    Message text @0
    Success bool @8
}

struct noteRef {
    ID text @0
}

struct optionRef {
    ID text @0
}

struct roundRef {
    ID text @0
}

struct safeRef {
    ID text @0
}

struct shareRef {
    ID text @0
}

struct stakeholderRef {
    ID text @0
}

interface captable {
    # Removes one of the caller org's convertible notes, taking its
    # principal out of the cap table's unconverted-instrument totals. An id this org
    # does not hold is not found.
    delete_captable_convertibles_by_id(req: noteRef) returns (rep: captableDeleted)
    # Removes one of the caller org's option grants, taking its shares
    # out of the cap table's granted-options and fully-diluted counts. An id this org
    # does not hold is not found.
    delete_captable_options_by_id(req: optionRef) returns (rep: captableDeleted)
    # Removes one of the caller org's SAFEs, taking its capital out of the
    # cap table's unconverted-instrument totals. An id this org does not hold is not
    # found.
    delete_captable_safes_by_id(req: safeRef) returns (rep: captableDeleted)
    # Removes one of the caller org's share certificates, taking its
    # shares out of the cap table's outstanding and fully-diluted counts. An id this
    # org does not hold is not found.
    delete_captable_shares_by_id(req: shareRef) returns (rep: captableDeleted)
    # Removes one of the caller org's stakeholders. It REFUSES to
    # orphan issued equity: a holder that still holds share certificates or option
    # grants cannot be deleted, and answers 400 saying so — release or transfer the
    # holdings first. An id this org does not hold is not found.
    delete_captable_stakeholders_by_id(req: stakeholderRef) returns (rep: captableDeleted)
    # Returns the caller org's share classes, in creation order. A
    # share class is what a certificate is issued in, and every class the company
    # has authorized appears. The response is a bare JSON array, not an envelope.
    get_captable_classes()
    # Returns the caller org's cap-table company record. The row is
    # seeded when the tenant's store first opens, so it always exists; its name and
    # incorporation details are set with PUT /v1/captable/company.
    get_captable_company() returns (rep: captableCompany)
    # Returns the caller org's convertible notes, newest first. A
    # note's principal sits OUTSIDE issued equity until it converts, so it is not
    # part of the share counts.
    get_captable_convertibles() returns (rep: captableNotes)
    # Returns the caller org's investments, newest first. It spans
    # every round, so it is the flat ledger of cheques written into the company,
    # each naming its investor and the round it went into.
    get_captable_investments() returns (rep: captableInvestments)
    # Returns the caller org's option grants, newest first. Each row is
    # joined to its grantee and its equity plan. Grants that are EXERCISED, EXPIRED
    # or CANCELLED are listed here but do not dilute the cap table.
    get_captable_options() returns (rep: captableOptions)
    # Returns the caller org's equity plans, newest first. An equity
    # plan is an option pool: a reserve of shares, drawn from one share class, that
    # option grants are written against.
    get_captable_plans() returns (rep: captableEquityPlans)
    # Returns the caller org's fundraising rounds, newest first. A round
    # groups a fundraising event; a PRICED round also carries the share class and
    # price per share it issues at.
    get_captable_rounds() returns (rep: captableRounds)
    # Returns one of the caller org's fundraising rounds together with every
    # investment written into it, oldest first. A round id that does not exist in the
    # caller's org is not found — including one that exists in another tenant, since
    # the org comes from the caller's principal and is part of the lookup.
    get_captable_rounds_by_id(req: roundRef) returns (rep: captableRoundDetail)
    # Returns the caller org's SAFEs, newest first. A SAFE is a simple
    # agreement for future equity: its capital sits OUTSIDE issued equity until it
    # converts, so it is not part of the share counts.
    get_captable_safes() returns (rep: captableSafes)
    # Returns the caller org's share certificates, newest first. Each row
    # is joined to its holder and its share class, so a certificate names who holds
    # it and what class it is in without a second call.
    get_captable_shares() returns (rep: captableShares)
    # Returns the caller org's stakeholders, newest first. The
    # response is a bare JSON array, not an envelope. Each row carries the holder's
    # contact and address fields alongside the company's name.
    get_captable_stakeholders()
    # Computes the caller org's cap table. It answers who owns what on a
    # fully-diluted basis: outstanding shares, granted options, per-stakeholder
    # ownership percentages, each share class's authorized versus issued position,
    # and the capital sitting on SAFEs and convertible notes that have not yet
    # converted. Only non-terminal option grants dilute — EXERCISED, EXPIRED and
    # CANCELLED grants are excluded, so equity issued through an exercised option is
    # never counted twice.
    get_captable_summary() returns (rep: captableSummary)
    # Replaces one share class's terms.
    # It is a full REPLACE and not a merge, despite the PATCH: every field is written
    # as sent, so a field omitted is written empty rather than left alone. Send the
    # whole class. The method is PATCH because the resource is addressed by id, not
    # because the body is partial — and getting that backwards silently blanks terms
    # every later issuance prices against.
    patch_captable_classes_by_id(req: captableShareClassAmend) returns (rep: captableUpdated)
    # Changes one of the caller org's stakeholders. It is a
    # PARTIAL update: only the fields the request names are written, and a field
    # sent as null clears that column. A request that names no updatable field is
    # refused, and an id this org does not hold is not found.
    # The values are stored as sent. Unlike adding a stakeholder, this route does
    # not check the email's shape or the type and relationship vocabularies, so it
    # can record a value that adding one would have rejected.
    patch_captable_stakeholders_by_id(req: captableStakeholderPatch) returns (rep: captableUpdated)
    # Defines a new class of shares.
    # Every field but convertsToShareClassId is required — a class is the instrument
    # every later issuance prices against, so a partially-specified one would silently
    # mis-value every share issued into it. `seniority` orders liquidation preference
    # with LOWER first.
    post_captable_classes(req: captableShareClassIn) returns (rep: captableCreated)
    # Records a convertible note.
    post_captable_convertibles(req: captableConvertibleIn) returns (rep: captableCreated)
    # Grants options to a stakeholder from an equity plan.
    post_captable_options(req: captableOptionIn) returns (rep: captableCreated)
    # Opens an equity plan that options are granted from.
    post_captable_plans(req: captableEquityPlanIn) returns (rep: captableCreated)
    # Opens a priced round that investments can be added to.
    # The round opens OPEN; investing into a closed one is refused.
    post_captable_rounds(req: captableRoundIn) returns (rep: captableCreated)
    # Closes one of the caller org's fundraising rounds, recording the
    # close date and moving its status to CLOSED. Only an OPEN round can be closed:
    # a round that is already closed — like an id this org does not hold — is not
    # found. Closing a round does not change what was invested in it.
    post_captable_rounds_by_id_close(req: captableRoundCloseRequest) returns (rep: captableUpdated)
    # Records one investor's money into an open round.
    # The round must be OPEN; investing into a closed one is refused. Where the round
    # carries a price per share, the investment also issues the shares it buys and the
    # answer names them.
    post_captable_rounds_by_id_investments(req: captableInvestmentIn) returns (rep: captableInvested)
    # Records a SAFE — a simple agreement for future equity.
    post_captable_safes(req: captableSafeIn) returns (rep: captableCreated)
    # Issues a share certificate to a stakeholder.
    # The certificate id must be UNIQUE within the company — a duplicate is refused
    # 409, not silently merged — and both the stakeholder and the share class must
    # belong to this company, so an id from another tenant is a 400 rather than a
    # cross-company issuance.
    post_captable_shares(req: captableShareIn) returns (rep: captableCreated)
    # Moves shares from one stakeholder to another.
    # Omit `quantity` to transfer the whole certificate, which REASSIGNS it and mints
    # no new share. Send a quantity below the amount held to SPLIT it — the source
    # certificate keeps the remainder, and a split additionally requires
    # `certificateId` for the new certificate, which must be unique in the company.
    # A quantity outside 1..held is refused, so a transfer can never over-issue.
    # Both outcomes answer 200: a transfer records a movement between holders and
    # mints no security of its own, which is why this is not a 201 the way an
    # investment is.
    post_captable_shares_transfer(req: captableShareTransfer) returns (rep: captableTransferred)
    # Sets the caller org's company name and incorporation details.
    # The name is required; the three incorporation fields are optional and each is
    # stored as empty when omitted, so a call that sends only a name CLEARS them.
    # The company row itself is seeded when the tenant's store first opens, so this
    # never creates one.
    put_captable_company(req: captableCompanyUpdate) returns (rep: captableUpdated)
}

# ---------------------------------------------------------------------
# 30 op(s) here. What follows is what this schema does not carry.
#
# dropped (13) — the value does not cross, and nothing fails:
#   captableCompanyUpdate.SizedIn  goja.SizedIn  (empty message)
#   captableConvertibleIn.SizedIn  goja.SizedIn  (empty message)
#   captableEquityPlanIn.SizedIn  goja.SizedIn  (empty message)
#   captableInvestmentIn.SizedIn  goja.SizedIn  (empty message)
#   captableOptionIn.SizedIn  goja.SizedIn  (empty message)
#   captableRoundCloseRequest.SizedIn  goja.SizedIn  (empty message)
#   captableRoundIn.SizedIn  goja.SizedIn  (empty message)
#   captableSafeIn.SizedIn  goja.SizedIn  (empty message)
#   captableShareClassAmend.SizedIn  goja.SizedIn  (empty message)
#   captableShareClassIn.SizedIn  goja.SizedIn  (empty message)
#   captableShareIn.SizedIn  goja.SizedIn  (empty message)
#   captableShareTransfer.SizedIn  goja.SizedIn  (empty message)
#   captableStakeholderPatch.SizedIn  goja.SizedIn  (empty message)
#
# opaque (28) — crosses, arrives without its name:
#   captableCompanyUpdate.SizedIn  goja.SizedIn
#   captableConvertibleIn.SizedIn  goja.SizedIn
#   captableEquityPlanIn.SizedIn  goja.SizedIn
#   captableEquityPlans.Data  captable.captableEquityPlan (list element)
#   captableInvestmentIn.SizedIn  goja.SizedIn
#   captableInvestments.Data  captable.captableInvestment (list element)
#   captableNotes.Data  captable.captableNote (list element)
#   captableOptionIn.SizedIn  goja.SizedIn
#   captableOptions.Data  captable.captableOption (list element)
#   captableRoundCloseRequest.SizedIn  goja.SizedIn
#   captableRoundDetail.Investments  captable.captableRoundInvestment (list element)
#   captableRoundDetail.Round  captable.captableRound
#   captableRoundIn.SizedIn  goja.SizedIn
#   captableRounds.Data  captable.captableRound (list element)
#   captableSafeIn.SizedIn  goja.SizedIn
#   captableSafes.Data  captable.captableSafe (list element)
#   captableShareClassAmend.SizedIn  goja.SizedIn
#   captableShareClassIn.SizedIn  goja.SizedIn
#   captableShareIn.SizedIn  goja.SizedIn
#   captableShareTransfer.SizedIn  goja.SizedIn
#   captableShares.Data  captable.captableShare (list element)
#   captableStakeholderPatch.SizedIn  goja.SizedIn
#   captableSummary.ByShareClass  captable.captableClassHolding (list element)
#   captableSummary.ByStakeholder  captable.captableHolding (list element)
#   captableSummary.Company  captable.captableSummaryCompany
#   captableSummary.Convertibles  captable.captableConvertibles
#   captableSummary.Rounds  captable.captableRoundTotals
#   captableSummary.Totals  captable.captableTotals
