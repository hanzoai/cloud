# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package patent

struct PatentProof {
    Project      text        @0
    Root         text        @8
    Records      list<bytes> @16
    Order        list<bytes> @24
    Verdict      text        @32
    PatentAnchor bytes       @40
    Coverage     bytes       @48
    Gaps         list<bytes> @56
    VerifiedAt   text        @64
}

struct PatentTimeline {
    Kind     text        @0
    ID       text        @8
    Project  text        @16
    Versions list<bytes> @24
    Retained i64         @32
    Found    bool        @40
}

struct approvalBatch {
    Approvals list<bytes> @0
}

struct approvalsIn {
    Disclosure text @0
    Project    text @8
}

struct approvalsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct disclosureBatch {
    Disclosures list<bytes> @0
}

struct discsIn {
    ID      text @0
    Status  text @8
    Project text @16
}

struct discsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct grantIn {
    Project    text       @0
    ID         text       @8
    Kind       text       @16
    Visibility list<text> @24
}

struct ideaBatch {
    Ideas list<bytes> @0
}

struct ideasIn {
    ID      text @0
    Kind    text @8
    Project text @16
}

struct ideasOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct implBatch {
    Implementations list<bytes> @0
}

struct implsIn {
    ID      text @0
    Idea    text @8
    Project text @16
}

struct implsOut {
    Data  list<bytes> @0
    Total i64         @8
}

struct patentGrantOut {
    Updated i64 @0
}

struct patentHealth {
    Durable               bool @0
    Anchored              bool @1
    SignatureVerification bool @2
    ServerClockSays       text @8
    RecordedAt            text @16
}

struct patentWriteOut {
    Project   text @0
    Recorded  i64  @8
    Canonical i64  @16
}

struct proofIn {
    Disclosure text @0
    Idea       text @8
    Project    text @16
}

struct timelineIn {
    ID      text @0
    Kind    text @8
    Project text @16
}

interface patent {
    # Returns the decision trail — each person's current word on
    # each disclosure. ?disclosure= narrows to one document.
    get_patent_approvals(req: approvalsIn) returns (rep: approvalsOut)
    # Returns the canonical disclosures, each carrying its
    # current approvals.
    get_patent_disclosures(req: discsIn) returns (rep: discsOut)
    # Reports what this deployment actually guarantees about the
    # records it keeps, in honest states. Anchored and durable are the two that
    # change what a record is worth, and an unwired one reads as unwired rather than
    # as a green light.
    get_patent_health() returns (rep: patentHealth)
    # Returns the caller org's canonical conception entries, oldest
    # appended first, which is the order the log says they were thought. ?id= narrows
    # to one, ?kind= to one kind, ?project= to one project.
    get_patent_ideas(req: ideasIn) returns (rep: ideasOut)
    # Returns the canonical implementation records with
    # their citations. ?idea= is the query the record exists to answer: which
    # artifacts rest on this one conception entry.
    get_patent_implementations(req: implsIn) returns (rep: implsOut)
    # Folds the caller's record set and answers the ordering question
    # over it.
    # ?disclosure= proves one document's set, ?idea= proves one entry and everything
    # citing it, and neither proves the whole org. The answer is computed per request
    # and writes nothing.
    get_patent_proof(req: proofIn) returns (rep: PatentProof)
    # Returns every retained version of one entry, in append
    # order. ?id= names it and ?kind= says which log; a retraction is shown as the
    # withdrawal it is rather than hidden.
    get_patent_timeline(req: timelineIn) returns (rep: PatentTimeline)
    # Appends one person's decision about one disclosure. The
    # approver is the validated principal and nothing else, so no caller records a
    # sign-off on someone else's behalf — which is the only way this field would be
    # worth reading.
    post_patent_approvals(req: approvalBatch) returns (rep: patentWriteOut)
    # Appends the assembled document and the record ids it
    # rests on. The document is a rendering of what the log already holds, and its
    # citations are what let a reader check it instead of taking it.
    post_patent_disclosures(req: disclosureBatch) returns (rep: patentWriteOut)
    # Makes the decision a recording never implied: whether an
    # idea, implementation or disclosure stays private to its author's project, opens
    # to the org, or goes public. Publication of a conception entry is a
    # disclosure under 35 U.S.C. §102 in every jurisdiction that reads it that way,
    # so it is always a separate call and never a side effect of writing one down.
    post_patent_grants(req: grantIn) returns (rep: patentGrantOut)
    # Appends conception entries. A write states the idea and
    # nothing else: the project, the author, the clock, the append order and the
    # content digest are all the server's, and a correction appends a version rather
    # than editing the entry — which is the property that makes any of it usable as
    # evidence at all.
    post_patent_ideas(req: ideaBatch) returns (rep: patentWriteOut)
    # Appends the machine's half of the record, and the
    # idea citation is the load-bearing field: it is what lets a reader see that the
    # conception came first. An entry citing no idea is refused rather than filed.
    post_patent_implementations(req: implBatch) returns (rep: patentWriteOut)
}

# ---------------------------------------------------------------------
# 12 op(s) here. What follows is what this schema does not carry.
#
# opaque (14) — crosses, arrives without its name:
#   PatentProof.Coverage  patent.PatentCoverage
#   PatentProof.Gaps  patent.PatentGap (list element)
#   PatentProof.Order  patent.PatentOrderCheck (list element)
#   PatentProof.PatentAnchor  patent.PatentAnchor
#   PatentProof.Records  patent.PatentProofEntry (list element)
#   PatentTimeline.Versions  patent.PatentTimelineEntry (list element)
#   approvalBatch.Approvals  patent.PatentApproval (list element)
#   approvalsOut.Data  patent.PatentApproval (list element)
#   disclosureBatch.Disclosures  patent.PatentDisclosure (list element)
#   discsOut.Data  patent.PatentDisclosure (list element)
#   ideaBatch.Ideas  patent.PatentIdea (list element)
#   ideasOut.Data  patent.PatentIdea (list element)
#   implBatch.Implementations  patent.PatentImplementation (list element)
#   implsOut.Data  patent.PatentImplementation (list element)
