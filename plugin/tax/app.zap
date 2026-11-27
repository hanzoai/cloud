# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package tax

struct Filing {
    ID         text        @0
    Year       i64         @8
    Kind       text        @16
    Status     text        @24
    Forms      list<text>  @32
    Parts      list<bytes> @40
    Layout     text        @48
    Due        text        @56
    MissingTIN list<text>  @64
    TCC        text        @72
    ReceiptID  text        @80
    Steps      list<text>  @88
    CreatedAt  i64         @96
    UpdatedAt  i64         @104
}

struct Form {
    ID           text        @0
    Kind         text        @8
    Year         i64         @16
    Status       text        @24
    Corrected    bool        @32
    Supersedes   text        @40
    SupersededBy text        @48
    Account      text        @56
    Payer        bytes       @64
    Recipient    bytes       @72
    Boxes        list<bytes> @80
    Backup       bytes       @88
    Certified    bool        @96
    W9           text        @104
    Due          bytes       @112
    Payments     list<text>  @120
    Delivery     text        @128
    FurnishedAt  i64         @136
    MailedAt     i64         @144
    Reason       text        @152
    Notes        list<text>  @160
    CreatedAt    i64         @168
    UpdatedAt    i64         @176
}

struct Ledger {
    Year           i64         @0
    ThresholdCents i64         @8
    Source         text        @16
    Payees         list<bytes> @24
}

struct Prepared {
    Year    i64         @0
    Forms   list<bytes> @8
    Skipped list<text>  @16
}

struct Profile {
    Name           text  @0
    BusinessName   text  @8
    Classification text  @16
    ForeignOwners  bool  @24
    ExemptPayee    text  @32
    FATCA          text  @40
    Address        bytes @48
    Phone          text  @56
    TIN            text  @64
    TINType        text  @72
    Consent        bytes @80
    Certification  bytes @88
    Version        i64   @96
    UpdatedAt      i64   @104
    Disclosure     text  @112
    Form           text  @120
    ForeignTIN     text  @128
    W8             bytes @136
    Expires        i64   @144
    Valid          bool  @152
}

struct Statement {
    ID           text        @0
    Payer        bytes       @8
    Recipient    bytes       @16
    Kind         text        @24
    Year         i64         @32
    Corrected    bool        @40
    Supersedes   text        @48
    SupersededBy text        @56
    Account      text        @64
    Boxes        list<bytes> @72
    Backup       bytes       @80
    FurnishedAt  i64         @88
}

struct W9 {
    ID          text  @0
    Role        text  @8
    Payer       text  @16
    Payee       text  @24
    Status      text  @32
    Category    text  @40
    Match       bytes @48
    Profile     bytes @56
    RequestedAt i64   @64
    DecidedAt   i64   @72
}

struct correctIn {
    ID     text        @0
    Reason text        @8
    Boxes  list<bytes> @16
}

struct exportIn {
    Year i64  @0
    Kind text @8
}

struct filingList {
    Data list<bytes> @0
}

struct filingRef {
    ID text @0
}

struct formList {
    Data list<bytes> @0
}

struct formRef {
    ID text @0
}

struct formsIn {
    Year i64 @0
}

struct matchIn {
    ID     text @0
    Result text @8
}

struct prepareIn {
    Year i64 @0
}

struct profileIn {
    Name              text       @0
    BusinessName      text       @8
    Classification    text       @16
    ForeignOwners     bool       @24
    ExemptPayee       text       @32
    FATCA             text       @40
    Address           bytes      @48
    Phone             text       @56
    TIN               text       @64
    TINType           text       @72
    ElectronicConsent list<bool> @80
    Form              text       @88
    ForeignTIN        text       @96
    W8                bytes      @104
}

struct receiptIn {
    ID        text @0
    Status    text @8
    ReceiptID text @16
    TCC       text @24
}

struct statementList {
    Data list<bytes> @0
}

struct tinOut {
    TIN        text @0
    TINType    text @8
    ForeignTIN text @16
}

struct w9List {
    AsPayer list<bytes> @0
    AsPayee list<bytes> @8
}

struct w9Ref {
    ID text @0
}

struct w9Request {
    Payee    text @0
    Category text @8
}

struct yearIn {
    Year i64 @0
}

interface tax {
    # Lists the caller org's IRS return exports and where each stands, without their
    # files. Org admins only.
    get_tax_filings() returns (rep: filingList)
    # Reads one IRS return export WITH its files, rebuilt from the sealed forms and
    # proven identical to what was exported. The files carry full TINs: org admins of
    # the payer only — a SuperAdmin is refused — and every read is on the audit trail.
    get_tax_filings_by_id(req: filingRef) returns (rep: Filing)
    # Lists the 1099s the caller org prepared as payer, oldest first, voided and
    # superseded ones included — a return that was furnished stays on the record.
    # Org admins only.
    get_tax_forms(req: formsIn) returns (rep: formList)
    # Reads one 1099 the caller org prepared as payer, every box and both parties,
    # TINs masked. Org admins only; another org's form is not found.
    get_tax_forms_by_id(req: formRef) returns (rep: Form)
    # Lists the Copy B statements furnished to the caller org — every 1099 a payer on
    # Hanzo delivered to it electronically, corrected ones beside the ones they
    # supersede. Any member may read it; nothing here can be changed or removed.
    get_tax_inbox(req: formsIn) returns (rep: statementList)
    # Reads one Copy B furnished to the caller org. Any member may read it; another
    # org's statement is not found.
    get_tax_inbox_by_id(req: formRef) returns (rep: Statement)
    # Returns the caller org's tax year as a PAYER: every payment it made to another
    # org in that calendar year, on any rail, as the event plane records it, each
    # classified — reportable or not, in which 1099 box, and the rule that decided it,
    # with its source — and each payee's totals tested against the year's threshold.
    # The rules applied: a payment settled by a card or a third-party settlement
    # network is reported on Form 1099-K by the settlement entity, not here; a payment
    # for merchandise is not reported; a payment to a corporation is generally not
    # reported, except for legal services; a payment in a digital asset is reported at
    # its fair market value on the date paid; a box is reported only at or above the
    # year's threshold — $600 through 2025, $2,000 for 2026 under P.L. 119-21, and not
    # guessed for a year the IRS has not yet published. A payee's classification comes
    # only from a W-9 it granted the caller; without one, nothing establishes an
    # exemption.
    # Org admins only. It reads; it prepares nothing and files nothing.
    get_tax_payments(req: yearIn) returns (rep: Ledger)
    # Returns the caller org's own tax profile — every line of its Form W-9, W-8BEN or
    # W-8BEN-E, every number masked to its last four characters, where the
    # certification stands, whether a W-8 is valid and when it expires, and whether
    # the org consents to receive its 1099s electronically.
    # Any member may read it. 404 until an admin writes one. No full number is ever in
    # this answer.
    get_tax_profile() returns (rep: Profile)
    # Lists the caller org's W-9 relationships on both sides: the W-9s it asked its
    # payees for, with where each stands, and the requests other orgs made for its
    # own W-9. Org admins only. No W-9 content is in the list; read one to see it.
    get_tax_w9() returns (rep: w9List)
    # Reads one W-9 relationship from the caller's side. Org admins only.
    # As the PAYER, the answer carries the payee's W-9 — every line, the TIN masked
    # to its last four digits, and whether it is certified — but only while the
    # payee's grant is live; before it is granted, and after it is declined or
    # revoked, there is no W-9 in the answer. As the PAYEE, it is the request and
    # where it stands.
    get_tax_w9_by_id(req: w9Ref) returns (rep: W9)
    # Reads a payee's TIN in full — and, for a payee on a W-8, its foreign TIN, which
    # the payer's Form 1042-S carries. Only an org admin of the PAYER, only while the
    # payee's grant is live, and every read is on the audit trail before a number is
    # answered — a read the trail cannot record is refused.
    # This is the one place a TIN leaves the platform in full, apart from the IRS
    # filing export. SuperAdmin is not admitted: platform authority is not a reason
    # to read a company's TIN.
    get_tax_w9_by_id_tin(req: w9Ref) returns (rep: tinOut)
    # Exports the caller org's return for a tax year and form, as the files the IRIS
    # Taxpayer Portal takes, and records the export. Org admins of the payer only: the
    # files carry TINs in full, so a SuperAdmin acting inside the org is refused here
    # exactly as at the TIN read.
    # It carries every furnished (or owed) form of that year and kind not already in
    # an earlier export that IRIS did not reject — a rejected submission filed
    # nothing, so its forms go out again as originals. Originals are in one set of
    # files and corrections of forms already exported in another, at most 250 records
    # a file, each written to every rule Pub 5717 §3.5 sets and checked against them
    # before it is answered. The files carry full TINs: the export is on the audit
    # trail and the files are not stored, only rebuilt from the sealed forms when read
    # again.
    # It DOES NOT FILE WITH THE IRS. Submitting through IRIS needs the payer's own
    # Transmitter Control Code and a person at the portal; the answer lists those
    # steps, and POST /v1/tax/filings/{id}/receipt records what IRIS answered.
    post_tax_filings(req: exportIn) returns (rep: Filing)
    # Records what IRIS answered for an exported return — the Receipt ID on
    # submission, then the acknowledgment. Org admins only.
    # This is the payer's report of its own act at the IRIS portal, made under its
    # own Transmitter Control Code; nothing here reaches IRIS, and a status is only
    # ever what the payer recorded. Moves: exported → submitted (with the Receipt ID
    # and TCC) → accepted, accepted_with_errors or rejected.
    post_tax_filings_by_id_receipt(req: receiptIn) returns (rep: Filing)
    # Prepares the caller org's 1099 drafts for a tax year, as the PAYER: one
    # 1099-NEC and/or 1099-MISC per payee whose reportable payments on Hanzo's rails
    # reach the year's threshold, from the same derivation GET /v1/tax/payments
    # answers. Org admins only.
    # Each draft carries the payee as its W-9 names it — under a live grant, and
    # otherwise only its org id — its boxes, box 4, the due dates, the payments it
    # sums, and backup withholding at 24% when the payer holds no TIN for the payee
    # (IRC §3406(a)(1)(A)) — an uncertified W-9 is stated as a warning, not a
    # withholding, because these payments do not require the certification. Preparing
    # again refreshes a draft or reviewed form in place (a reviewed one goes back to
    # draft, to be reviewed again); one payee has at most one live form per kind and
    # year, so a second preparation racing the first is refused, never doubled. A form
    # already furnished is left alone and named in skipped — change it by correcting
    # it. Nothing is furnished to anyone and nothing is filed with the IRS.
    post_tax_forms(req: prepareIn) returns (rep: Prepared)
    # Corrects a furnished (or owed) 1099 with a NEW form that supersedes it. Org
    # admins only.
    # The furnished form is never changed: the correction is a second form, marked
    # CORRECTED, carrying the original's account number and naming the form it
    # supersedes, and it goes through review and furnishing like any other. State the
    # corrected boxes, or omit them to derive the year again from the payments and the
    # payee's current W-9. A form already superseded is corrected by correcting its
    # successor. It files nothing with the IRS.
    post_tax_forms_by_id_correct(req: correctIn) returns (rep: Form)
    # Furnishes Copy B of a reviewed 1099 to its payee. Org admins only.
    # When the payee's tax profile consents to electronic delivery, Copy B is
    # delivered into the payee's /v1/tax/inbox and the form is furnished. Otherwise it
    # is OWED on paper at the payee's address: mailing it is the payer's, and
    # POST /v1/tax/forms/{id}/mailed records it. Either way the statement is final
    # from this call — the form can no longer change, only be corrected. Calling it
    # again on a form furnished electronically re-delivers Copy B if the inbox lacks
    # it, and changes nothing else.
    # It does not file anything with the IRS, and it does not send the notice of
    # posting Pub 1099 Part M asks a furnisher to give an electronic recipient; the
    # statement is in the recipient's inbox, and telling them so is the payer's.
    post_tax_forms_by_id_furnish(req: formRef) returns (rep: Form)
    # Records that the payer mailed a paper Copy B it owed. Org admins only.
    # Mailing is the payer's act, done outside Hanzo; this is the record of it, made
    # by the person who did it and kept on the audit trail. Nothing here mails
    # anything.
    post_tax_forms_by_id_mailed(req: formRef) returns (rep: Form)
    # Marks a draft 1099 reviewed — a person has checked it. Only a reviewed form can
    # be furnished. Org admins only.
    post_tax_forms_by_id_review(req: formRef) returns (rep: Form)
    # Voids a draft or reviewed 1099, which is then never furnished or filed. A form
    # already furnished cannot be voided: it is corrected, and a correction to zero
    # is how a furnished return is withdrawn (Pub 1099 Part H). Org admins only.
    post_tax_forms_by_id_void(req: formRef) returns (rep: Form)
    # Signs the caller org's certification — Form W-9 Part II, Form W-8BEN Part III or
    # Form W-8BEN-E Part XXX — through /v1/legal's e-signature. The org's own admins
    # only, and the admin is the signer: a SuperAdmin acting inside the org is not a
    # person who can certify for it.
    # The first call renders the certification — the IRS's own certification text over
    # this profile's facts, every number truncated — as a /v1/legal document and opens
    # its signature request with the calling admin as signer; the answer is `pending` with
    # the document id. The signer completes it where every /v1/legal signature is
    # completed (POST /v1/legal/documents/{id}/sign/complete). Calling this again then
    # asks legal whether the document is signed and by whom: signed by the signer, the
    # profile is recorded certified; not yet signed, it answers `pending` again; signed
    # by anyone else — another member, a key — or voided, it is not the signer's
    # certification, and a fresh request is opened in its place.
    # A certification covers one profile version: writing any fact on the form
    # afterwards voids it. A certified W-8 is valid through the last day of the third
    # calendar year after the year it was signed (Instructions for Form W-8BEN and
    # W-8BEN-E, Rev. 10-2021); certifying it again after that renews it. It is not a
    # statement that a TIN matches IRS records — that is IRS TIN Matching, which only
    # a payer enrolled in IRS e-Services can run.
    post_tax_profile_certify() returns (rep: Profile)
    # Asks another org for its W-9, as the org that pays it. Org admins only.
    # A request is refused unless the caller has paid that org on Hanzo's rails, or
    # has asked /v1/principal whether it may — a W-9 is asked for by a payer, of a payee,
    # and the payments, or a payment the caller is about to make, are what say which
    # companies those are. The form is needed BEFORE the first payment: withholding
    # is decided when a payment is made, not when it is reported. The request lands with the payee, whose admin grants or
    # declines it; until it is granted the caller sees its status and nothing of the
    # payee's W-9. Asking again updates the category, and re-opens a request the
    # payee declined or revoked.
    # Category is the caller's statement of what it pays this payee for, and decides
    # the 1099 box: services and attorney go to 1099-NEC box 1a, rents, royalties
    # and other to 1099-MISC boxes 1, 2 and 3; merchandise is not reported.
    post_tax_w9(req: w9Request) returns (rep: W9)
    # Declines a request for this org's W-9. The payer keeps seeing only the status.
    # Org admins of the payee only.
    post_tax_w9_by_id_decline(req: w9Ref) returns (rep: W9)
    # Grants the payer that asked a live read of this org's W-9 — every line, the TIN
    # masked, and the full TIN to that payer's org admins alone, each read audited.
    # Org admins of the payee only. Needs a tax profile to grant.
    post_tax_w9_by_id_grant(req: w9Ref) returns (rep: W9)
    # Records the result of IRS TIN Matching for a payee, as the payer. Org admins of
    # the payer only, on a live grant.
    # It does not run TIN Matching. That is an IRS e-Services program a payer enrolls
    # in and runs itself, on the name and TIN it holds; this records what the IRS
    # answered so it sits beside the W-9 and on the audit trail. Nothing here ever
    # records a match the payer did not report.
    post_tax_w9_by_id_match(req: matchIn) returns (rep: W9)
    # Revokes a W-9 this org granted. From the moment it is revoked the payer reads
    # nothing of the W-9 and no full TIN. A 1099 the payer already prepared keeps the
    # TIN it was prepared with — the payer must keep it to file — and that TIN is
    # never answered in full outside the payer's IRS filing export. Org admins of the
    # payee only.
    post_tax_w9_by_id_revoke(req: w9Ref) returns (rep: W9)
    # Writes the caller org's tax profile — its Form W-9, or its Form W-8BEN or
    # W-8BEN-E when it is a foreign person — and answers it, every number masked. Org
    # admins only.
    # A U.S. TIN is validated before it is kept — nine digits; an EIN's prefix must be
    # one the IRS assigns; an SSN never begins 000, 666 or 9xx and has no zero group
    # or serial; an ITIN's middle digits are in the IRS ranges. A foreign TIN is the
    # issuing jurisdiction's and is checked only for its characters. Every number is
    # sealed under this org's own data key before it is written. Validating the
    # FORMAT is all this does: whether the IRS has the number on file under this name
    # is IRS TIN Matching, which a payer runs through IRS e-Services.
    # A W-8 is refused where the form itself says it is the wrong one: a U.S. citizen,
    # a U.S. permanent residence or an entity organized in the United States is a U.S.
    # person and files a W-9. A treaty claim needs a U.S. or foreign TIN on the form
    # (Instructions for Form W-8BEN, line 5; for Form W-8BEN-E, line 8), and names the
    # country of the permanent residence address.
    # Changing any fact on the form makes a new version, and the certification no
    # longer covers it: certify again. For a W-8 that is the "change in circumstances"
    # the instructions end its validity on. Changing only the consent does not.
    put_tax_profile(req: profileIn) returns (rep: Profile)
}

# ---------------------------------------------------------------------
# 26 op(s) here. What follows is what this schema does not carry.
#
# opaque (26) — crosses, arrives without its name:
#   Filing.Parts  tax.Part (list element)
#   Form.Backup  tax.Withholding
#   Form.Boxes  tax.Amount (list element)
#   Form.Due  tax.Deadline
#   Form.Payer  tax.Party
#   Form.Recipient  tax.Party
#   Ledger.Payees  tax.PayeeYear (list element)
#   Prepared.Forms  tax.Form (list element)
#   Profile.Address  tax.Address
#   Profile.Certification  tax.Certification
#   Profile.Consent  tax.Consent
#   Profile.W8  tax.W8
#   Statement.Backup  tax.Withholding
#   Statement.Boxes  tax.Amount (list element)
#   Statement.Payer  tax.Party
#   Statement.Recipient  tax.Party
#   W9.Match  tax.Match
#   W9.Profile  tax.Profile
#   correctIn.Boxes  tax.Amount (list element)
#   filingList.Data  tax.Filing (list element)
#   formList.Data  tax.Form (list element)
#   profileIn.Address  tax.Address
#   profileIn.W8  tax.W8
#   statementList.Data  tax.Statement (list element)
#   w9List.AsPayee  tax.W9 (list element)
#   w9List.AsPayer  tax.W9 (list element)
