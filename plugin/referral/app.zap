# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package referral

struct adminBonusesEnvelope {
    Data   bytes @0
    Msg    text  @8
    Status text  @16
}

struct adminListIn {
    Limit text @0
}

struct claimRequest {
    Code text @0
}

struct claimView {
    Code      text @0
    Created   bool @8
    CreatedAt i64  @16
    ID        text @24
    Status    text @32
}

struct myReferrals {
    Code      text        @0
    Counts    bytes       @8
    Link      text        @16
    Referrals list<bytes> @24
}

struct sweepEnvelope {
    Data   bytes @0
    Msg    text  @8
    Status text  @16
}

interface referral {
    # Returns every referral edge in the directory with a fleet summary.
    # SuperAdmin only, fail-closed. This is the ATTRIBUTION directory — who referred
    # whom and whether that referee became a customer. It carries no amounts because
    # this package issues none. The cross-tenant referral ANALYTICS board (top
    # referrers, conversion) is a different surface, GET /v1/admin/affiliate/referrals,
    # owned by the affiliates subsystem over the shared attribution spine.
    get_admin_referral_bonuses(req: adminListIn) returns (rep: adminBonusesEnvelope)
    # Returns the caller's referral code, share link and the referrals they have made.
    # The code is a stable, deterministic function of the org, so the link in this
    # response is the same one every time. Each row carries the referee and the status
    # of that attribution.
    # IT IS A PURE READ. It advances no referral, grants nothing and deposits nothing
    # — a GET reports state, it never changes it. Qualification is the admin sweep's
    # job (POST /v1/admin/referral/sweep). The one row this handler can write is the
    # caller's OWN code-directory entry (EnsureCode), which materialises a value
    # deriveCode already computes deterministically from the org id so the code has an
    # O(1) reverse lookup; it carries no money, no referral state and no other tenant.
    get_referral() returns (rep: myReferrals)
    # Qualify-checks every pending referral and advances the ones that now qualify.
    # SuperAdmin only, fail-closed. This is the cron path, and the ONLY path that
    # advances a referral: a referee QUALIFIES once they have made metered spend — the
    # honest signal that they actually used the product rather than merely signing up.
    # Qualifying moves NO money. It records that an attribution became a real customer;
    # what is owed for that is an affiliate payable in commerce, settled by wire or to a
    # connected wallet. One pass is bounded, so a large backlog drains over several runs
    # instead of wedging one request, and the latch makes the transition at-most-once
    # under a concurrent sweep.
    # It reads nothing from the caller — the counters it returns are the whole result.
    post_admin_referral_sweep() returns (rep: sweepEnvelope)
    # Records that the caller's org signed up through a referral code.
    # The REFEREE is the validated caller, never a client field, and the referrer is
    # resolved from the code — so a caller can only ever attach THEMSELVES to someone
    # else's code. Referring yourself is 400 and an unknown code is 404.
    # It is idempotent and first-touch: an org can be referred once, ever. A repeat
    # call returns the referral already on file with created=false and 200, where the
    # first call answers 201.
    # Recording a referral grants nothing, and neither does anything downstream of it:
    # the edge later advances to qualified when the referee makes metered spend
    # (POST /v1/admin/referral/sweep), and that is the end of it. No credit is ever
    # issued from this package.
    post_referral_claim(req: claimRequest) returns (rep: claimView)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# opaque (4) — crosses, arrives without its name:
#   adminBonusesEnvelope.Data  referral.adminBonusDirectory
#   myReferrals.Counts  referral.statusCounts
#   myReferrals.Referrals  referral.myReferralView (list element)
#   sweepEnvelope.Data  referral.sweepResult
