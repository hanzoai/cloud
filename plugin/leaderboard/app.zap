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

struct BetaBoard {
    Rows      list<bytes> @0
    You       bytes       @8
    Of        i64         @16
    Tokens    bool        @24
    Referrals bool        @25
}

struct BountyAwardIn {
    ID           text @0
    Org          text @8
    User         text @16
    Handle       text @24
    Email        text @32
    Kind         text @40
    Target       text @48
    Summary      text @56
    Points       i64  @64
    CreditsCents i64  @72
}

struct BountyAwardOut {
    Award     bytes @0
    Hunter    bytes @8
    EmailSent bool  @16
}

struct BountyBoard {
    Hunters      list<bytes> @0
    RecentAwards list<bytes> @8
    Self         bytes       @16
}

struct GalaxyView {
    Available bool       @0
    Center    bytes      @8
    Bands     list<text> @16
    Stars     bytes      @24
    Self      text       @32
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

struct PulseView {
    Available bool        @0
    Days      list<text>  @8
    Hours     list<text>  @16
    Tokens    bytes       @24
    Orgs      bytes       @32
    Signups   bytes       @40
    Referred  bytes       @48
    Models    bytes       @56
    Variety   bytes       @64
    Tries     list<bytes> @72
    Missing   list<text>  @80
}

struct StandingView {
    Available bool        @0
    Total     i64         @8
    Scored    i64         @16
    Locked    i64         @24
    Rows      list<bytes> @32
    Self      bytes       @40
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

struct betaQuery {
    Limit i64 @0
}

struct boardQuery {
    Scope  text @0
    Metric text @8
    Period text @16
    Limit  i64  @24
}

struct bountyQuery {
    Limit i64 @0
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
    # Ranks AI usage over a window, either the users of the caller's own org
    # or organizations against each other, and always reports the caller's own standing
    # even when it falls outside the returned page. Identities are private by default: a
    # caller sees themselves, plus the peers or orgs that opted into public listing, and
    # only an admin sees their own org's members named. Cross-org spend is restricted to
    # platform admins. When the warehouse is not connected the board answers empty with
    # available=false rather than a fabricated rank.
    get_leaderboard(req: boardQuery) returns (rep: LeaderboardView)
    # Returns the per-day usage series for ONE authorized subject — the points a
    # contribution heatmap and a timeline are drawn from, gap-filled so every day in the
    # range is present. Authorization is resolved server-side from the validated
    # principal, so a caller can never widen the subject past what they are entitled to:
    # a non-admin reads only themselves and their own org. subject=project answers empty
    # with a note, because the usage ledger records no project column yet. When the
    # warehouse is not connected the series answers empty with available=false rather
    # than fabricated days.
    get_leaderboard_activity(req: activityQuery) returns (rep: ActivityView)
    # Ranks everyone on the hanzo.ai/beta waitlist by one score: 1 per star
    # on GitHub verified for the record and 1 per founder link followed, plus 1 per org
    # that signed up with its referral code, plus 20 per such org that has made metered
    # spend, plus 1 per 100,000 tokens the person used, all time. Ties go to the earlier
    # application. Share clicks count for nothing.
    # Everyone who ever applied is on it. A form filed before the person had an
    # account is a row of its own (no name, score 0, the day it was filed) until their
    # account's record claims it, and the record then begins when the form did.
    # A row is named only for a person who opted into the public board (PUT
    # /v1/leaderboard/optin); the caller always sees their own row named. Tokens are a
    # range on every row and exact on a named row and the caller's. `tokens` and
    # `referrals` say whether the warehouse and the referral service answered; a
    # term that did not is left out of every score alike.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_leaderboard_beta(req: betaQuery) returns (rep: BetaBoard)
    # Returns the ranked bug hunters and bounty contributors, and the recent bug and bounty awards.
    get_leaderboard_bounty(req: bountyQuery) returns (rep: BountyBoard)
    # Returns every ranked org as a star, for a page to draw the community as a galaxy: each
    # star holds its band in the reward program's level table, a coarse size (a factor of
    # two), the star of the org that referred it, if any, and, only for the ranks the reader
    # has earned a view of, its exact points and the name the standing shows for it. The
    # window is the standing's: a signed-in reader sees their own star, five ranks below it
    # and as many above as their points have earned; a reader who is not signed in sees the
    # last ranks, where a newcomer starts. Every other star has `points` of -1 and no name,
    # and is still drawn. The stars are columns of equal length, best first, and `center` is
    # the tokens used and the orgs active in the last 24 hours. A star's id is opaque: it is
    # the same wherever this answer mentions that star, names no org, and is replaced on the
    # next read, about a minute later. A signed-in reader gets `self`, the id of their own
    # star. Names are the standing's: masked in the middle, whole where the org listed
    # itself, empty where it withdrew. No org id, handle or email is sent. No credential is
    # needed to read it.
    get_leaderboard_galaxy() returns (rep: GalaxyView)
    # Returns the caller's own public-listing preference and their org's,
    # each with whether the caller may change it. Public listing is opt-in and private
    # by default, so a fresh caller reads listed=false for both.
    get_leaderboard_optin() returns (rep: optinView)
    # Returns how fast the platform is being used and is spreading, as counts by day for
    # thirty days and by hour for the last day: tokens used, orgs active, orgs signed up,
    # orgs that came through a referral link, orgs using a model for the first time (with
    # the models most tried), and how many different models were in use (variety). Every
    # series is read from where cloud records it; one that cannot be read is left out and
    # named in `missing`, and the ones nothing records today (orgs trying a product,
    # shares) are always named there. Counts only: no org, no name. No credential is
    # needed to read it.
    get_leaderboard_pulse() returns (rep: PulseView)
    # Returns the public board of who has used and built the most on Hanzo, shown as a
    # window around the reader. Every org IAM holds is ranked, by points: tokens used,
    # referrals, verified stars, follows and bounty awards. A signed-in reader sees their own rank, the ranks
    # just below it and as many above it as their points have earned; the rest are
    # counted in `locked` and never sent. A reader who is not signed in sees the last
    # ranks, where a newcomer starts. Names are masked in the middle; the reader's own is
    # whole, an org that listed itself is whole, and one that withdrew has none. No
    # credential is needed to read it.
    get_leaderboard_standing() returns (rep: StandingView)
    # Records a verified bug report or bounty PR award on the leaderboard,
    # opts the recipient's handle into public listing, and sends a celebratory email
    # announcing their points, AI credits, and updated leaderboard rank. SuperAdmin only.
    post_admin_leaderboard_bounty(req: BountyAwardIn) returns (rep: BountyAwardOut)
    # Seeds the derived usage rollup from ledger history — the rows written
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
# 12 op(s) here. What follows is what this schema does not carry.
#
# opaque (24) — crosses, arrives without its name:
#   ActivityView.Days  leaderboard.ActivityPoint (list element)
#   ActivityView.Totals  leaderboard.ActivityTotals
#   BetaBoard.Rows  leaderboard.BetaRow (list element)
#   BetaBoard.You  leaderboard.BetaRow
#   BountyAwardOut.Award  leaderboard.BountyAwardView
#   BountyAwardOut.Hunter  leaderboard.BountyHunter
#   BountyBoard.Hunters  leaderboard.BountyHunter (list element)
#   BountyBoard.RecentAwards  leaderboard.BountyAwardView (list element)
#   BountyBoard.Self  leaderboard.BountyHunter
#   GalaxyView.Center  leaderboard.GalaxyCenter
#   GalaxyView.Stars  leaderboard.GalaxyStars
#   LeaderboardView.Rows  leaderboard.LeaderboardRow (list element)
#   LeaderboardView.Self  leaderboard.SelfRank
#   PulseView.Models  leaderboard.PulseSeries
#   PulseView.Orgs  leaderboard.PulseSeries
#   PulseView.Referred  leaderboard.PulseSeries
#   PulseView.Signups  leaderboard.PulseSeries
#   PulseView.Tokens  leaderboard.PulseSeries
#   PulseView.Tries  leaderboard.PulseTally (list element)
#   PulseView.Variety  leaderboard.PulseSeries
#   StandingView.Rows  leaderboard.StandingRow (list element)
#   StandingView.Self  leaderboard.StandingSelf
#   optinView.Org  leaderboard.orgOptinView
#   optinView.User  leaderboard.userOptinView
