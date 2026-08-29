# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package author

struct adminBook {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct adminLimit {
    Limit i64 @0
}

struct approveRequest {
    ID       text @0
    ShareBps i64  @8
}

struct authorRef {
    ID text @0
}

struct authorResult {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct authorSweepResult {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct basisQuery {
    ID     text @0
    Period text @8
}

struct claim {
    Repo    bytes @0
    Org     bytes @8
    Created bool  @16
}

struct connectRequest {
    Provider    text @0
    GithubLogin text @8
    Login       text @16
}

struct deployRecord {
    Recorded  bool @0
    Reason    text @8
    Created   bool @16
    Self      bool @17
    DeployID  text @24
    CreatedAt i64  @32
}

struct deployRequest {
    RepoURL text @0
    Project text @8
}

struct enrolment {
    ID            text @0
    Status        text @8
    GithubLogin   text @16
    Verified      bool @24
    VerifyCode    text @32
    VerifyFile    text @40
    VerifySnippet text @48
    ShareBps      i64  @56
    Created       bool @64
}

struct payoutRequest {
    ID          text @0
    AmountCents i64  @8
    Method      text @16
    Reference   text @24
}

struct payoutResult {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct periodQuery {
    Period text @0
}

struct verifyRequest {
    RepoURL text @0
}

interface author {
    # Returns the platform's whole author program — every org's author
    # record, not the caller's — with each one's repository and deploy counts and a
    # fleet roll-up of the money accrued, pending and paid.
    # It is a Hanzo platform operation: a caller who is not a SuperAdmin gets 403. It
    # exposes the owning org of each author, which no tenant-facing read ever does.
    get_admin_author(req: adminLimit) returns (rep: adminBook)
    # Returns the caller's author-program dashboard: enrolment status,
    # linked forge login, verified repositories and owner-wide claims, recorded deploys,
    # accrued / pending / paid royalty, and the payout history.
    # It answers ONE OF TWO SHAPES from this address. An org that has never connected
    # gets {"isAuthor": false, "defaultShareBps", "badgeBase"} — an honest "not enrolled"
    # rather than a 404, so the console can render the connect form. An enrolled org gets
    # the dashboard: isAuthor, id, status, githubLogin, verified, verifyCode, verifyFile,
    # verifySnippet, shareBps, badgeBase, repos, orgs, deploys, accruedCents,
    # pendingCents, paidCents, payouts and ledger.
    # For an APPROVED author this read ALSO runs the accrual sweep opportunistically, so
    # the dashboard is self-updating. That is why the royalty AUDIT lives at its own
    # address: an audit must not move the money it is auditing.
    get_author()
    # Returns the AUDIT TRAIL behind the caller's own royalty: every
    # ledger row with the spend it was computed from, the share applied at the time, the
    # platform's matching half, whether each row satisfies the formula, and the
    # attribution edges that already existed when the row was written.
    # It answers ONE OF TWO SHAPES. An org that has never connected gets
    # {"isAuthor": false, "defaultShareBps"} — never a 404, which would answer "is this
    # org an author?" for anyone who asked. An enrolled org gets the basis: isAuthor, id,
    # status, asOf, shareBps, platformShareBps, defaultShareBps, shareSource, settlesTo,
    # method (the formula, the rate card and the sizing), ledger, reconciliation, window,
    # and period when one was requested.
    # This read NEVER sweeps, and that is the point of it being a separate address from
    # the dashboard: an audit must not move the money it is auditing, so calling it N
    # times leaves the balances and the ledger byte-identical.
    get_author_basis(req: periodQuery)
    # Admits one author to EARNING, optionally on a negotiated royalty
    # share. Until this runs, a connected author accrues nothing however many verified
    # repositories they have.
    # A share override applies from here forward only — existing ledger rows keep the
    # share that was applied when they were written, because a rate change must never
    # rewrite what was already owed.
    # A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
    post_admin_author_by_id_approve(req: approveRequest) returns (rep: authorResult)
    # Records a payout of accrued royalty and settles it.
    # The amount is RESERVED against the author's pending royalty atomically before
    # anything is paid, so a payout can never exceed what is owed even under concurrent
    # calls. An external author's payout is then BACKED against the platform reserve
    # fund — a second, independent guard — and refused with 402 if the reserve cannot
    # cover it, with the reservation voided. A "credits" method issues the actual wallet
    # grant after both guards; a cash method is record-only. A first-party (treasury)
    # author's royalty is realized into Hanzo's own reserve instead of an external
    # wallet, and every payout row discloses which of the three it was.
    # A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
    post_admin_author_by_id_payout(req: payoutRequest) returns (rep: payoutResult)
    # Stops one author earning. Their record, verified claims and ledger
    # are untouched — suspension halts future accrual, it does not erase what was already
    # owed, and it does not delete the evidence behind it.
    # A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
    post_admin_author_by_id_suspend(req: authorRef) returns (rep: authorResult)
    # Runs the accrual sweep across every approved author: for each of
    # their deploying orgs it computes this period's royalty from that org's metered
    # spend and latches it at most once per period.
    # It is an OVERRIDE, not the mechanism: a background scheduler runs the same sweep on
    # its own, and every author's dashboard read sweeps their own accruals lazily. This
    # is the manual trigger for an operator who needs the numbers now. It is idempotent —
    # the per-period latch means running it twice accrues nothing the second time.
    # A Hanzo platform operation: a caller who is not a SuperAdmin gets 403.
    post_admin_author_sweep() returns (rep: authorSweepResult)
    # Enrols the caller's org in the author program at status "connected"
    # and returns its enrolment, including the verify code the file method needs. It is
    # IDEMPOTENT: a second call returns the same enrolment rather than a conflict.
    # The forge login is taken from IAM's LINKED account for the provider when there is
    # one — that is identity proof, not a claim — and only otherwise from the login in
    # the body, which then has to be proven per repository. Connecting does not admit an
    # org to earning: a platform reviewer approves that separately.
    # Answers 201 when it enrolled the org and 200 when it found an existing enrolment.
    post_author_connect(req: connectRequest) returns (rep: enrolment)
    # Records that the caller's org deployed a project built from a
    # source repository, which is the edge that makes an author's work earn royalty.
    # It is deliberately NOT an error for a deploy to attribute to nobody: a project
    # built from no repository, or from one no author has verified, answers
    # {"recorded": false, "reason"} so a deploy pipeline can fire this on every deploy
    # without branching. Attribution resolves per-repository first, then owner-wide, so a
    # repository with its own claim always earns for its own author.
    # A deploy of a Hanzo-maintained template attributes to the platform treasury, and a
    # self-deploy (the author's own org deploying its own repository) is recorded for
    # provenance but excluded from accrual. The edge is idempotent per
    # repository+project+org.
    # Answers 201 when it recorded a new edge and 200 otherwise.
    post_author_deploys_record(req: deployRequest) returns (rep: deployRecord)
    # Proves that the caller owns a repository — or a whole OWNER — and
    # records the claim, which is what makes deploys of that code earn royalty.
    # Ownership is proven the SAME two ways in both cases, tried in order: an IAM-linked
    # forge token with admin or push permission, or a hanzo.json on the default branch
    # carrying the author's verify code. Claiming an OWNER proves it against that
    # owner's ".github" control repository, and is exactly as strong as a per-repository
    # claim — an owner the caller cannot prove is refused with 422, never assumed.
    # A per-repository claim wins over an owner-wide one, so a specifically-claimed
    # repository always earns for its own author. A repository another author has
    # already verified is a 409. The org must have connected first.
    # Answers 201 when it recorded a new claim and 200 when the claim already existed.
    post_author_repos_verify(req: verifyRequest) returns (rep: claim)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   get_admin_author_by_id_basis  basisResult.Data  map[string]interface {}  (map)
#
# opaque (6) — crosses, arrives without its name:
#   adminBook.Data  author.adminBookData
#   authorResult.Data  author.authorData
#   authorSweepResult.Data  author.sweepCounts
#   claim.Org  author.orgView
#   claim.Repo  author.authorRepo
#   payoutResult.Data  author.payoutData
