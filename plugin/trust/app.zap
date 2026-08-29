# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package trust

struct centre {
    Version       text        @0
    Generated     i64         @8
    Org           text        @16
    Profile       bytes       @24
    Controls      list<bytes> @32
    Coverage      list<bytes> @40
    Inventory     bytes       @48
    Frameworks    list<bytes> @56
    Documents     list<bytes> @64
    Subprocessors list<bytes> @72
    Policies      list<bytes> @80
    Faq           list<bytes> @88
    Updates       list<bytes> @96
    Risk          bytes       @104
}

struct clauseCoverage {
    Version   text        @0
    Generated i64         @8
    Framework text        @16
    Name      text        @24
    Publisher text        @32
    Edition   text        @40
    Unit      text        @48
    Units     text        @56
    Total     i64         @64
    Automated i64         @72
    Partial   i64         @80
    None      i64         @88
    Statement text        @96
    Note      text        @104
    Clauses   list<bytes> @112
}

struct controlList {
    Version    text        @0
    Total      i64         @8
    Automated  i64         @16
    Partial    i64         @24
    Absent     i64         @32
    Unverified i64         @40
    Statement  text        @48
    Controls   list<bytes> @56
}

struct controlRef {
    ID text @0
}

struct dropped {
    Kind    text @0
    ID      text @8
    Deleted bool @16
}

struct evidenceQuery {
    Control text @0
    From    text @8
    To      text @16
    Limit   text @24
}

struct faqList {
    Faq list<bytes> @0
}

struct frameworkList {
    Frameworks list<bytes> @0
}

struct frameworkRef {
    Framework text @0
}

struct orgRef {
    Org text @0
}

struct policyList {
    Policies list<bytes> @0
}

struct sectionRef {
    Kind text @0
    ID   text @8
}

struct sectionWrite {
    Kind text  @0
    ID   text  @8
    Ord  i64   @16
    Data bytes @24
}

struct subprocessorList {
    Subprocessors list<bytes> @0
}

struct trustCoverage {
    Version    text        @0
    Generated  i64         @8
    Controls   bytes       @16
    Frameworks list<bytes> @24
}

struct trustDocuments {
    Documents list<bytes> @0
}

struct updateList {
    Updates list<bytes> @0
}

struct written {
    Kind    text @0
    ID      text @8
    Updated i64  @16
}

interface trust {
    # Removes one record from a section of your organization's trust centre. A
    # record that is not there is a 404, never a silent success. A control that
    # belongs to the deployment's own inventory is removed by a commit, not by a
    # request.
    delete_trust_by_kind_by_id(req: sectionRef) returns (rep: dropped)
    # Reads YOUR organization's whole trust centre, including the addresses of your
    # own gated documents. Same shape as the published endpoint; the difference is that
    # this one is resolved from your validated bearer and shows you your own
    # artifacts.
    get_trust() returns (rep: centre)
    # Lists every control your organization publishes, with the counts.
    # A control names what it asserts, the mechanism behind it, the repository and
    # file where that mechanism is enforced, how it is verified, and the framework
    # clauses it maps to. Status is automated, partial or absent — and an absent one
    # still names the clause it would satisfy, which is a roadmap, while never
    # moving a coverage number.
    get_trust_controls() returns (rep: controlList)
    # Reads one control by id.
    get_trust_controls_by_id(req: controlRef)
    # Reads coverage: per framework, how many clauses have an automated control
    # behind them, how many are partial, and how many have none — each carrying the
    # unit it is counted in, because "12 of 20" is not a fact until you know what
    # the 20 are.
    # Nothing here is a verdict. There is no boolean, and a control that only a
    # person has read counts one rung weaker than it claims to be, because only a
    # check that can FAIL is evidence.
    get_trust_coverage() returns (rep: trustCoverage)
    # Reads one framework clause by clause: every clause the standard publishes,
    # what covers it, and which controls stand behind it — so a coverage number can
    # be checked line by line rather than taken on trust.
    get_trust_coverage_by_framework(req: frameworkRef) returns (rep: clauseCoverage)
    # Lists your organization's documents. Because this is your own centre, a gated
    # artifact carries its address here; through the published endpoint it does not.
    get_trust_documents() returns (rep: trustDocuments)
    # Reads the audit rows that stand behind one control, over a window.
    # The inventory decides what evidences what: a control names the audit actions
    # that are its trail, and this resolves the control id to those actions and
    # reads them. So evidence cannot drift from the inventory, and it is scoped to
    # your own organization — the query carries no organization field for a caller
    # to fill in.
    # A control that nothing in the trail evidences says so plainly rather than
    # answering an empty page, because an empty page reads like a clean quarter. A
    # deployment with no audit store answers 501 and says the trail was not read,
    # for the same reason.
    get_trust_evidence(req: evidenceQuery)
    # Lists your knowledge base — the questions a reviewer asks, answered once.
    get_trust_faq() returns (rep: faqList)
    # Lists the frameworks coverage is computed against, and how many clauses each
    # publishes. That count is the denominator of every coverage number, which is
    # what keeps an uncovered clause visible instead of dropping out of the
    # fraction.
    get_trust_frameworks() returns (rep: frameworkList)
    # Lists your organization's published policies.
    get_trust_policies() returns (rep: policyList)
    # Reads your organization's trust-centre profile — the name, tagline and
    # summary a visitor sees, whether the centre is published, and where to send
    # somebody who wants a gated document.
    get_trust_profile()
    # Reads a published trust centre — the whole thing in one answer: the
    # organization's profile, its control inventory, coverage computed against each
    # framework's whole published clause list, its documents, subprocessors,
    # policies, knowledge base, updates and risk profile.
    # This is the PUBLIC endpoint and needs no credential, because a published trust
    # centre is a public document. It answers only for an organization that has
    # published one — an organization that has not is not found rather than empty,
    # since an empty centre and a centre nobody meant to show read the same and are
    # not the same thing.
    # A gated document appears here with its title, its type and its date and NO
    # address: the listing says the artifact exists and that reading it takes a
    # grant. Nothing an independent auditor signed is ever released through this
    # endpoint.
    get_trust_published_by_org(req: orgRef) returns (rep: centre)
    # Reads your risk profile — the label and value pairs describing what your
    # organization handles and how.
    get_trust_risk()
    # Lists the third parties your organization sends data to, each naming what it
    # is for.
    get_trust_subprocessors() returns (rep: subprocessorList)
    # Lists your trust-centre updates, newest as you ordered them.
    get_trust_updates() returns (rep: updateList)
    # Writes one record into a section of YOUR organization's trust centre —
    # profile, control, document, subprocessor, policy, faq, update or risk.
    # A control written here is held to exactly the rule a control committed to the
    # deployment's own inventory is held to, by the same validator: its prose may
    # not claim a certificate and may not name a framework (a framework belongs in
    # the mappings, where it arrives attached to a number), anything short of
    # automated must say what is missing, and a mapping to a clause no framework
    # declares is refused rather than scored as nothing.
    # A document defaults to GATED. An artifact an independent auditor signed — a
    # SOC 2 report, an ISO certificate, a penetration test, an auditor letter —
    # cannot be made public at all; it is released through a grant. A
    # self-assessment can, because the organization is the one attesting it.
    # The deployment's OWN control inventory is governed in git and is not writable
    # here: naming one of its ids is a conflict, not an overwrite.
    put_trust_by_kind_by_id(req: sectionWrite) returns (rep: written)
}

# ---------------------------------------------------------------------
# 17 op(s) here. What follows is what this schema does not carry.
#
# opaque (9) — crosses, arrives without its name:
#   centre.Coverage  trust.coverRow (list element)
#   centre.Documents  trust.docRow (list element)
#   centre.Frameworks  trust.frameworkRow (list element)
#   centre.Inventory  trust.trustTally
#   clauseCoverage.Clauses  trust.clauseRow (list element)
#   frameworkList.Frameworks  trust.frameworkRow (list element)
#   trustCoverage.Controls  trust.trustTally
#   trustCoverage.Frameworks  trust.coverRow (list element)
#   trustDocuments.Documents  trust.docRow (list element)
