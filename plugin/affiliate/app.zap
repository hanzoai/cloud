# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package affiliate

struct accrualsOut {
    Data bytes @0
}

struct affiliateBoard {
    Leaders list<bytes> @0
    Total   i64         @8
    You     bytes       @16
}

struct affiliateEarnings {
    AccruedCents  i64         @0
    ByPeriod      list<bytes> @8
    ByReferredOrg list<bytes> @16
    IsAffiliate   bool        @24
    MarginBps     i64         @32
    PaidCents     i64         @40
    PendingCents  i64         @48
}

struct affiliateLinks {
    IsAffiliate bool        @0
    Links       list<bytes> @8
    MaxLinks    i64         @16
    Status      text        @24
}

struct affiliateOut {
    Data bytes @0
}

struct affiliateRef {
    ID text @0
}

struct affiliateSelf {
    AccruedCents   i64         @0
    Code           text        @8
    DefaultRateBps i64         @16
    DownlineTotal  i64         @24
    Handle         text        @32
    ID             text        @40
    IsAffiliate    bool        @48
    Levels         list<bytes> @56
    Link           text        @64
    MarginBps      i64         @72
    PaidCents      i64         @80
    Payouts        list<bytes> @88
    PendingCents   i64         @96
    RateBps        i64         @104
    Schedule       list<bytes> @112
    Status         text        @120
}

struct affiliateStanding {
    AccruedCents   i64         @0
    Code           text        @8
    DefaultRateBps i64         @16
    Handle         text        @24
    ID             text        @32
    IsAffiliate    bool        @40
    Link           text        @48
    MarginBps      i64         @56
    PaidCents      i64         @64
    Payouts        list<bytes> @72
    PendingCents   i64         @80
    RateBps        i64         @88
    ReferredCount  i64         @96
    RequestedCode  text        @104
    Status         text        @112
}

struct application {
    Code          text @0
    Created       bool @8
    ID            text @16
    RateBps       i64  @24
    RequestedCode text @32
    Status        text @40
}

struct applyRequest {
    RequestedCode text @0
}

struct approval {
    Code text @0
    ID   text @8
}

struct attributeRequest {
    Code text @0
}

struct attribution {
    Code      text @0
    Created   bool @8
    CreatedAt i64  @16
    ID        text @24
}

struct clickCount {
    Counted bool @0
}

struct clickRequest {
    Code text @0
}

struct createLinkRequest {
    Label text @0
    Code  text @8
}

struct directoryOut {
    Data bytes @0
}

struct disbursal {
    AmountCents i64  @0
    ID          text @8
    Method      text @16
    Reference   text @24
}

struct handleRequest {
    Handle text @0
}

struct handleSet {
    Handle text @0
}

struct linkMint {
    Link bytes @0
}

struct page {
    Limit i64 @0
}

struct payoutOut {
    Data bytes @0
}

struct rateSet {
    ID      text @0
    RateBps i64  @8
}

struct referralsOut {
    Data bytes @0
}

interface affiliate {
    # Lists every affiliate across the fleet with its ORG exposed, plus a
    # fleet summary of lifetime accrued, still-pending and paid commission in
    # integer cents.
    # PLATFORM SUDO ONLY, and a non-admin is refused outright. This is the
    # cross-tenant view and it names orgs — exactly what the partner-facing
    # leaderboard refuses to do. There is deliberately no org-scoped variant of this
    # read; a partner sees its own standing through its own dashboard. Bounded per
    # request.
    get_admin_affiliate(req: page) returns (rep: directoryOut)
    # Answers the referral board: the top referrers by lifetime
    # commission, the funnel conversion rate (referred orgs that have actually
    # produced commission, over all referred orgs), and the accrual LIABILITY the
    # platform owes, broken out by upline level.
    # Read the liability figure carefully — it is commission accrued and NOT yet
    # paid, so it is money owed, not money spent, and the per-level split says how
    # much of it comes from direct referrals versus the second and third levels.
    # PLATFORM SUDO ONLY, cross-tenant, and it names orgs. It reads the SAME single
    # attribution spine the accrual itself walks, so the board and the ledger cannot
    # disagree. Amounts are integer cents.
    get_admin_affiliate_referrals() returns (rep: referralsOut)
    # Answers the caller org's OWN affiliate standing: status, referral
    # code and share link, commission rate, how many orgs it has referred, and its
    # lifetime accrued, still-pending and already-paid commission in integer cents,
    # with its payout history.
    # An org that never applied gets an honest `isAffiliate:false` and the default
    # rate rather than a 404 — the console renders the apply form off that answer.
    # The affiliate is resolved from the VALIDATED org, never from a field, so this
    # can only ever read the caller's own row; without a principal it is refused. It
    # is a PURE READ: nothing accrues until the sweep runs. Commission is earned on
    # Hanzo's MARGIN, never on the referred customer's bill, so nothing here changes
    # what that customer pays.
    get_affiliate() returns (rep: affiliateStanding)
    # Answers the top affiliates by lifetime accrued commission, shown by
    # OPT-IN HANDLE with aggregate figures only, plus the caller's own exact rank.
    # It never discloses an org identity and never a referred org's usage. An
    # affiliate that has set no handle still OCCUPIES its rank but is not listed —
    # so opting out hides the name, not the position, and the visible board must not
    # be read as a complete roster.
    # The caller's own row carries its exact GLOBAL rank, computed over the whole
    # approved set rather than over the page, so it is right well outside the top of
    # the board. Only an approved affiliate has a rank. Requires a validated
    # principal; a signed-in non-affiliate may read the board but gets no personal
    # row.
    get_affiliate_leaderboard() returns (rep: affiliateBoard)
    # Answers the richer self-view: the same lifetime accrued, pending and paid
    # commission and payout history, plus the caller's downline broken out by upline
    # LEVEL — direct, second, third — each with the rate paid at that level and how
    # many orgs sit there.
    # Commission is MULTI-LEVEL: a referred org's spend pays up its referral chain,
    # three levels deep and no further. The direct level is the affiliate's own
    # negotiated rate; the second and third are platform-wide switches, read live,
    # so the schedule shown is the one actually in force rather than one compiled
    # in. A caller that has not applied still gets that schedule alongside
    # `isAffiliate:false`, so the console can show what it would earn.
    # Scoped to the validated org and nothing else, and refused without a
    # principal. A PURE READ — it reports the downline but accrues nothing.
    get_affiliate_me() returns (rep: affiliateSelf)
    # Answers the caller's own commission ledger: per period, the margin it
    # earned against and the commission taken from that margin; and per referred
    # org, that referral's aggregate contribution. Integer cents throughout.
    # The per-org view deliberately carries the affiliate's OWN earned share and NOT
    # the referred org's spend or margin. An affiliate is entitled to what it
    # earned, not to a restatement of its customer's usage — the period view is
    # where the margin base appears, aggregated across every referral.
    # Scoped server-side to the validated caller's affiliate; a caller that is not
    # one gets `isAffiliate:false`.
    get_affiliate_me_earnings() returns (rep: affiliateEarnings)
    # Answers the caller's share links, each with its URL and its funnel:
    # clicks tracked, signups — orgs attributed with that code — and conversions,
    # meaning how many of those signups have actually produced commission.
    # Signups and conversions are DERIVED from the commission ledger and never
    # stored, so they cannot drift from the money. Clicks are the one stored counter
    # and the one that is pure vanity.
    # Any pending public click pings are folded into the store before the read, in
    # one batch — which is how the counters stay current without a database write
    # per click. Scoped to the validated caller's own affiliate; a non-affiliate
    # gets `isAffiliate:false` and the link cap.
    get_affiliate_me_links() returns (rep: affiliateLinks)
    # Approves an affiliate and MINTS its referral code — the moment
    # the partner has a working share link and starts accruing.
    # The code is taken from the body if one is given, else the vanity code the
    # applicant requested, else a slug derived for them. Codes are ONE global
    # namespace, so a taken code is a 409 and nothing is approved. The minted code
    # is also mirrored as a link row so click tracking is uniform across every code
    # the affiliate holds; that mirror is best-effort and its failure never fails
    # the approval.
    # Approval is what makes an affiliate eligible: before it, attribution against
    # its code does not resolve and no sweep accrues to it. PLATFORM SUDO ONLY.
    # Audited.
    post_admin_affiliate_by_id_approve(req: approval) returns (rep: affiliateOut)
    # Pays out accrued commission and answers the payout row with the
    # affiliate's updated balances.
    # The amount is reserved atomically against the affiliate's PENDING commission —
    # accrued minus paid — so a payout can never exceed what is owed. The METHOD
    # decides whether money actually moves: `credits` issues a commerce grant into
    # the affiliate ORG's own wallet, tagged so the ledger can tell an affiliate
    # payout apart from an admin or referral grant; every other method — wire,
    # paypal and the rest — is RECORD-ONLY: the payout row and the balances move,
    # the cash is disbursed out of band.
    # The amount is integer cents and must be positive. PLATFORM SUDO ONLY.
    # Audited.
    post_admin_affiliate_by_id_payout(req: disbursal) returns (rep: payoutOut)
    # Sets one affiliate's DIRECT commission rate, in basis points of
    # Hanzo's margin.
    # The rate is CAPPED so that the direct rate plus the platform-wide second- and
    # third-level rates can never exceed the whole margin — the structural guarantee
    # that everything paid on one source event stays inside the margin actually
    # earned. The cap is resolved from the rates in force at the moment of the call
    # and quoted in the refusal, because those switches move; a hardcoded bound
    # would start lying the moment somebody edits the schedule.
    # Only the direct level is per-affiliate. The second and third levels are
    # platform switches and are not settable here. The change applies to FUTURE
    # accruals — commission already latched for a period is not recomputed. PLATFORM
    # SUDO ONLY. Audited.
    post_admin_affiliate_by_id_rate(req: rateSet) returns (rep: affiliateOut)
    # Suspends an affiliate: it stops accruing on the next sweep, and
    # its code stops resolving for new attributions.
    # It CLAWS NOTHING BACK. Commission already accrued stays accrued and stays
    # payable, and existing attribution edges are left standing — suspension ends
    # earning, it does not unwind history. PLATFORM SUDO ONLY. Audited.
    post_admin_affiliate_by_id_suspend(req: affiliateRef) returns (rep: affiliateOut)
    # Runs the accrual: for each referred org it reads that org's metered
    # spend for the current period and accrues commission to every affiliate up its
    # referral chain, then answers how many sources were swept and how many NEW
    # accruals landed.
    # This is the cron path, and it is LATCHED at most once per affiliate, source
    # org and period — so re-running it inside the same period accrues nothing
    # further. Safe to retry, and safe to run by hand beside the schedule.
    # Commission is a rate of Hanzo's MARGIN on that spend, never of the customer's
    # gross bill, so every level's share summed over one source event stays within
    # the margin actually earned and the customer's charge is untouched. Nothing
    # accrues past the third upline level, and only an APPROVED affiliate accrues at
    # all.
    # The same spend read drives the OSS author royalty — one read, both programs —
    # so the answer reports royalties accrued alongside. PLATFORM SUDO ONLY. Bounded
    # per run; a source whose spend cannot be read is skipped and picked up next
    # time, never half-accrued.
    post_admin_affiliate_sweep() returns (rep: accrualsOut)
    # Enrolls the caller's OWN org as an affiliate at status `applied`,
    # optionally requesting a vanity code, and answers the record — 201 on the first
    # apply, 200 with `created:false` afterwards.
    # IDEMPOTENT, first apply wins: one affiliate per org, so re-applying never
    # creates a second row and never resets an existing approval. Applying is not
    # joining — no code is minted and nothing accrues until staff approve, which is
    # where both the code and the commission rate come from.
    # The org is the validated caller's, never a field. A malformed vanity code is
    # refused up front; the code is only REQUESTED here, and approval may mint a
    # different one if the requested code is taken.
    post_affiliate_apply(req: applyRequest) returns (rep: application)
    # Records the first-touch edge every later commission is computed
    # from: the caller's org was referred by the affiliate that owns this code.
    # The REFERRED org is the validated caller, never a field. A caller that could
    # name the referred org could attach itself to somebody else's revenue. The
    # affiliate is resolved from the code, and only an APPROVED affiliate's code
    # resolves.
    # FIRST TOUCH WINS, set once: one affiliate per referred org, so a re-post
    # answers the existing edge with `created:false` rather than moving the
    # attribution. Self-attribution is refused, and so is a code that would make a
    # cycle in the upline chain. An unknown code is a 404, deliberately: an
    # affiliate code IS a public shareable link, so whether one is real is public by
    # design, and the caller legitimately needs to know its link resolved.
    # A user-level mirror of the edge is written best-effort; a conflict there never
    # fails the org attribution, which is the money-bearing one.
    post_affiliate_attribute(req: attributeRequest) returns (rep: attribution)
    # Counts a click on a share link. PUBLIC — it takes no principal, because
    # a visitor clicking a shareable link has no session yet.
    # The ping folds into an in-memory buffer and NEVER writes the money database
    # synchronously, so a click flood cannot contend with the accrual and payout
    # write path; tallies are flushed in one batch on the next authenticated links
    # read and at shutdown. Clicks are a vanity metric: no accrual and no payout
    # ever reads them — those key on real metered spend — so click inflation cannot
    # move money.
    # Any well-formed code is accepted WITHOUT checking that it exists,
    # deliberately: this is not a code-existence oracle. `counted` reports that the
    # buffer took the ping, not that the code is real; an unknown code simply no-ops
    # at flush time.
    post_affiliate_click(req: clickRequest) returns (rep: clickCount)
    # Sets the caller's public leaderboard display name, or clears it.
    # The handle IS the opt-in. An empty handle opts out: the affiliate keeps its
    # rank and can still see its own row, it simply stops being listed to anyone
    # else. That is the whole privacy control — there is no separate visibility
    # flag, and no way to be listed without choosing a name.
    # Requires a validated principal and an existing affiliate record; apply first.
    # The handle is bounded and restricted to letters, digits, space, hyphen,
    # underscore and dot.
    post_affiliate_me_handle(req: handleRequest) returns (rep: handleSet)
    # Mints a new share link for the caller's own affiliate and answers it
    # with its full URL, 201.
    # APPROVAL IS REQUIRED: an org that has applied but is not approved is refused,
    # because a link that cannot accrue is a link that quietly loses the referral. A
    # requested vanity code must be valid and free across the WHOLE directory —
    # codes are one global namespace, so a taken code is a 409 rather than a silent
    # alias. Omit the code and a random one is minted.
    # Bounded per affiliate. The label is cosmetic: it is trimmed, stripped of
    # control characters and capped, and it is never part of a code.
    post_affiliate_me_links(req: createLinkRequest) returns (rep: linkMint)
}

# ---------------------------------------------------------------------
# 17 op(s) here. What follows is what this schema does not carry.
#
# dropped (5) — the value does not cross, and nothing fails:
#   accrualsOut.envelope  affiliate.envelope  (promoted, not carried)
#   affiliateOut.envelope  affiliate.envelope  (promoted, not carried)
#   directoryOut.envelope  affiliate.envelope  (promoted, not carried)
#   payoutOut.envelope  affiliate.envelope  (promoted, not carried)
#   referralsOut.envelope  affiliate.envelope  (promoted, not carried)
#
# opaque (15) — crosses, arrives without its name:
#   accrualsOut.Data  affiliate.accruals
#   affiliateBoard.Leaders  affiliate.leaderboardRow (list element)
#   affiliateBoard.You  affiliate.leaderboardRow
#   affiliateEarnings.ByPeriod  affiliate.periodEarningView (list element)
#   affiliateEarnings.ByReferredOrg  affiliate.orgEarningView (list element)
#   affiliateLinks.Links  affiliate.codeView (list element)
#   affiliateOut.Data  affiliate.affiliateData
#   affiliateSelf.Levels  affiliate.levelView (list element)
#   affiliateSelf.Payouts  affiliate.remittance (list element)
#   affiliateSelf.Schedule  affiliate.levelView (list element)
#   affiliateStanding.Payouts  affiliate.remittance (list element)
#   directoryOut.Data  affiliate.directoryData
#   linkMint.Link  affiliate.codeView
#   payoutOut.Data  affiliate.settlement
#   referralsOut.Data  affiliate.referralBoard
