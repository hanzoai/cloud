# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package compliance

struct Subject {
    ID        text @0
    Org       text @8
    Kind      text @16
    Ref       text @24
    Email     text @32
    Name      text @40
    CreatedAt i64  @48
    UpdatedAt i64  @56
}

struct accList {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct accView {
    ID            text @0
    SubjectID     text @8
    Method        text @16
    Basis         text @24
    Status        text @32
    EvidenceDocID text @40
    ReviewerSub   text @48
    Note          text @56
    ExpiresAt     i64  @64
    CreatedAt     i64  @72
    UpdatedAt     i64  @80
}

struct accreditationDecision {
    ID     text @0
    Status text @8
}

struct accreditationRef {
    ID text @0
}

struct accreditationReq {
    SubjectID     text @0
    Method        text @8
    Basis         text @16
    Status        text @24
    EvidenceDocID text @32
    Note          text @40
    ExpiresAt     i64  @48
}

struct auditIn {
    Result text @0
}

struct auditList {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct checkList {
    Data       list<bytes> @0
    Disclaimer text        @8
}

struct checkView {
    ID        text @0
    SubjectID text @8
    Kind      text @16
    Provider  text @24
    Status    text @32
    VerifyURL text @40
    DecidedBy text @48
    DecidedAt i64  @56
    CreatedAt i64  @64
    UpdatedAt i64  @72
}

struct healthView {
    Status   text @0
    Provider text @8
}

struct listIn {
    Limit i64 @0
}

struct recordList {
    Verifications list<bytes> @0
    Accreditation list<bytes> @8
    Disclaimer    text        @16
}

struct subjectList {
    Data list<bytes> @0
}

struct subjectRef {
    ID text @0
}

struct subjectReq {
    Kind  text @0
    Ref   text @8
    Email text @16
    Name  text @24
}

struct verificationDecision {
    ID     text @0
    Status text @8
}

struct verificationRef {
    ID text @0
}

struct verificationReq {
    SubjectID text @0
    Kind      text @8
    Ref       text @16
    Email     text @24
    Name      text @32
}

interface compliance {
    # Returns the org's tracked accreditation-state records, newest
    # first — evidence entries the org keeps, never a platform certification.
    get_compliance_accreditation(req: listIn) returns (rep: accList)
    # Returns one tracked accreditation record.
    get_compliance_accreditation_by_id(req: accreditationRef) returns (rep: accView)
    # AuditRead is the compliance read of the SHARED tamper-evident audit plane —
    # the SOC 2 posture surface (privileged actions: who started/decided what, when). The
    # org is PINNED to the caller's validated org and the rows are narrowed to
    # compliance.* actions. Fail-closed: no principal is a 403, no configured audit
    # store a 501.
    get_compliance_audit(req: auditIn) returns (rep: auditList)
    # Health reports subsystem liveness and the wired verification provider. Fail-open
    # on purpose: it never probes the external provider, so a provider outage cannot
    # fail liveness.
    get_compliance_health() returns (rep: healthView)
    # ListRecords is the unified compliance-record view for the org: its verifications
    # and accreditation records together, each provider-reported or tracked, never
    # platform-asserted. PII stays in the subject store; records carry only opaque ids
    # and statuses.
    get_compliance_records(req: listIn) returns (rep: recordList)
    # Returns the org's subjects as PII-MINIMIZED summaries — no name or
    # email, only whether an email is on file. The full record is returned only by the
    # explicit single-subject read.
    get_compliance_subjects(req: listIn) returns (rep: subjectList)
    # Returns one subject WITH its contact PII — the only surface that
    # returns it, and only to the owning org. The response is never cached by any
    # intermediary.
    get_compliance_subjects_by_id(req: subjectRef) returns (rep: Subject)
    # Returns the org's KYC/KYB verifications, newest first — opaque
    # subject references and provider-reported statuses only, no subject PII.
    get_compliance_verifications(req: listIn) returns (rep: checkList)
    # Returns one verification — its opaque subject reference and
    # provider-reported status, no subject PII.
    get_compliance_verifications_by_id(req: verificationRef) returns (rep: checkView)
    # Records an ASSERTED accreditation state for a subject — the
    # subject's own assertion, with no verifier. Every CONFIRMED state
    # (provider_verified, reviewer_confirmed) and every rejected/expired state is a
    # DECISION recorded via the decision endpoint, attributed to the reviewer — a
    # create can never stamp a confirmation. The underlying figures (income, net
    # worth) are never stored; only the method, category, and state.
    post_compliance_accreditation(req: accreditationReq) returns (rep: accView)
    # Records an org reviewer's decision on an accreditation
    # record — a reviewer confirmation, a provider verification the reviewer has
    # evidence of (a CPA/attorney letter, a verifier report), a rejection, or an
    # expiry. ROLE-GATED (an org admin or platform reviewer) and ATTRIBUTED: the
    # reviewer's identity is recorded as ReviewerSub and audited. Human-in-the-loop:
    # the platform never confirms on its own, and even a provider_verified state
    # carries the reviewer who recorded it.
    post_compliance_accreditation_by_id_decision(req: accreditationDecision) returns (rep: accView)
    # Records a party the org is verifying as part of its own
    # onboarding/compliance — a team member, vendor, customer, or counterparty. The
    # subject's contact PII (name/email) is sealed at rest and returned only to the
    # owning org; downstream records reference the subject by opaque id.
    post_compliance_subjects(req: subjectReq) returns (rep: Subject)
    # Begins a KYC/KYB verification of a subject through the wired
    # provider — an existing subject by id, or one created inline from the request.
    # The returned status is provider-reported and never terminal on a fresh start:
    # starting a verification can never yield a verified record, and a provider error
    # is a 502, never a verification.
    post_compliance_verifications(req: verificationReq) returns (rep: checkView)
    # Records a privileged reviewer's MANUAL decision on a
    # verification — the human-in-the-loop path, and the ONLY route to a passing status
    # when no real provider is wired. It produces a DISTINCT reviewer_confirmed, never
    # a provider_verified (a provider decision is the provider's to report, via the
    # webhook or a reconcile), and it is ROLE-GATED (an org admin or platform reviewer)
    # AND ATTRIBUTED (the reviewer's user id is DecidedBy), so a manual pass is always
    # accountable.
    post_compliance_verifications_by_id_decision(req: verificationDecision) returns (rep: checkView)
    # Polls the wired provider for its current decision and
    # records it, ATTRIBUTED to the provider — the internal PULL reconcile. For the
    # Manual provider the check stays pending; for a hosted provider it reflects the
    # provider's settled status. A poll error is a 502, never a verification.
    post_compliance_verifications_by_id_refresh(req: verificationRef) returns (rep: checkView)
}

# ---------------------------------------------------------------------
# 15 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_compliance_status  statusView.Verifications  compliance.verificationTally  (reaches one)
#
# opaque (6) — crosses, arrives without its name:
#   accList.Data  compliance.accView (list element)
#   auditList.Data  audit.Wire (list element)
#   checkList.Data  compliance.checkView (list element)
#   recordList.Accreditation  compliance.accView (list element)
#   recordList.Verifications  compliance.checkView (list element)
#   subjectList.Data  compliance.subjectSummary (list element)
