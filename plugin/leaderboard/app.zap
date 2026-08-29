# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package leaderboard

struct ActivityView {
    Subject   text        @0
    ID        text        @8
    From      text        @16
    To        text        @24
    Days      list<bytes> @32
    Totals    bytes       @40
    Available bool        @48
    Source    text        @56
    Note      text        @64
}

struct LeaderboardView {
    Scope     text        @0
    Subject   text        @8
    Metric    text        @16
    Period    text        @24
    Start     text        @32
    End       text        @40
    Rows      list<bytes> @48
    Self      bytes       @56
    Total     i64         @64
    Available bool        @72
    Source    text        @80
}

struct activityQuery {
    Subject text @0
    ID      text @8
    From    text @16
    To      text @24
}

struct backfillQuery {
    Before text @0
    Force  text @8
}

struct backfillResult {
    Status       text @0
    SeededBefore text @8
    Forced       bool @16
}

struct boardQuery {
    Scope  text @0
    Metric text @8
    Period text @16
    Limit  i64  @24
}

struct optinView {
    User bytes @0
    Org  bytes @8
}

struct orgOptinReq {
    Listed  bool @0
    Display text @8
}

struct orgOptinView {
    Listed    bool @0
    Display   text @8
    CanManage bool @16
}

struct userOptinReq {
    Listed bool @0
    Handle text @8
}

struct userOptinView {
    Listed bool @0
    Handle text @8
    CanSet bool @16
}

interface leaderboard {
    # Leaderboard ranks AI usage over a window, either the users of the caller's own org
    # or organizations against each other, and always reports the caller's own standing
    # even when it falls outside the returned page. Identities are private by default: a
    # caller sees themselves, plus the peers or orgs that opted into public listing, and
    # only an admin sees their own org's members named. Cross-org spend is restricted to
    # platform admins. When the warehouse is not connected the board answers empty with
    # available=false rather than a fabricated rank.
    get_leaderboard(req: boardQuery) returns (rep: LeaderboardView)
    # Activity returns the per-day usage series for ONE authorized subject — the points a
    # contribution heatmap and a timeline are drawn from, gap-filled so every day in the
    # range is present. Authorization is resolved server-side from the validated
    # principal, so a caller can never widen the subject past what they are entitled to:
    # a non-admin reads only themselves and their own org. subject=project answers empty
    # with a note, because the usage ledger records no project column yet. When the
    # warehouse is not connected the series answers empty with available=false rather
    # than fabricated days.
    get_leaderboard_activity(req: activityQuery) returns (rep: ActivityView)
    # Returns the caller's own public-listing preference and their org's,
    # each with whether the caller may change it. Public listing is opt-in and private
    # by default, so a fresh caller reads listed=false for both.
    get_leaderboard_optin() returns (rep: optinView)
    # Backfill seeds the derived usage rollup from ledger history — the rows written
    # before the incremental view existed, which that view can never capture. SuperAdmin
    # only. Because the rollup accumulates, a second unguarded run would double every
    # day it re-reads, so it refuses with 409 when the rollup already holds rows unless
    # force=true is passed; forcing WILL double-count.
    post_admin_leaderboard_rollup(req: backfillQuery) returns (rep: backfillResult)
    # Sets the CALLER's own public-listing preference on the leaderboard.
    # Self only: the row written is keyed by the caller's validated ledger identity, so
    # this can never edit another member's visibility whatever the request says. A
    # caller opting in with no handle is given their username, so a listed row never
    # renders as "Anonymous" to its own owner.
    put_leaderboard_optin(req: userOptinReq) returns (rep: userOptinView)
    # Sets the ORG's listing on the cross-org global board. Only an admin of
    # the caller's own org — an org admin or a platform SuperAdmin — may change it, and
    # the org written is the caller's validated tenant, never a value from the request.
    # Listing consents to publishing the org's usage VOLUME; cross-org spend stays
    # restricted to platform admins regardless.
    put_leaderboard_optin_org(req: orgOptinReq) returns (rep: orgOptinView)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (6) — crosses, arrives without its name:
#   ActivityView.Days  leaderboard.ActivityPoint (list element)
#   ActivityView.Totals  leaderboard.ActivityTotals
#   LeaderboardView.Rows  leaderboard.LeaderboardRow (list element)
#   LeaderboardView.Self  leaderboard.SelfRank
#   optinView.Org  leaderboard.orgOptinView
#   optinView.User  leaderboard.userOptinView
