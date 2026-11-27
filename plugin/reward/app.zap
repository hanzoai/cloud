# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package reward

struct adminPostsIn {
    Status text @0
}

struct badgeIn {
    Repo text @0
}

struct badgeOut {
    Repo    text       @0
    Badges  list<text> @8
    Claimed i64        @16
}

struct checkOut {
    Login   text @0
    Stars   i64  @8
    Follows i64  @16
    Forks   i64  @24
}

struct postIn {
    URL    text @0
    Handle text @8
}

struct postList {
    Posts   list<bytes> @0
    Pending i64         @8
}

struct postRef {
    ID text @0
}

struct postView {
    ID       text        @0
    Network  text        @8
    URL      text        @16
    Status   text        @24
    Note     text        @32
    Week     text        @40
    Since    bytes       @48
    CountsAt bytes       @56
    DaysLeft i64         @64
    Checks   list<bytes> @72
}

struct rulesOut {
    Pillars list<bytes> @0
}

struct scanIn {
    Network text @0
    Handle  text @8
}

struct scanOut {
    Looked  i64         @0
    Held    i64         @8
    Skipped i64         @16
    Posts   list<bytes> @24
}

interface reward {
    # Lists the posts of every member, newest first, optionally of one status, so staff
    # can find one to look at again. SuperAdmin only.
    get_admin_reward_posts(req: adminPostsIn) returns (rep: postList)
    # Lists your posts, newest first, each with its status and every look at it. Pending
    # posts are not points; the count of them is beside the list.
    get_reward_me_posts() returns (rep: postList)
    # Lists what the rewards program pays for: every active rule of every active
    # program, grouped by pillar (use, share, community, contribute, expert), each with
    # what has to happen, the points, the badge or credit it awards, how often one person
    # can earn it and whether staff verify each claim first. Public: it needs no
    # credential, and it is the program's own list, so nothing else promises a reward.
    get_reward_rules() returns (rep: rulesOut)
    # Looks at any member's post now, with the same checks as the daily look, and adds
    # the look to its history. A removed post that is back up and carries the member's
    # link is held again and its week starts over. SuperAdmin only.
    post_admin_reward_posts_by_id_recheck(req: postRef) returns (rep: postView)
    # Verifies against GitHub's API that a project of yours carries a Hanzo badge image
    # in its README, and puts one claim to staff for each kind it carries: your live
    # profile badge, your rank badge, or the Built with Hanzo badge. The project must be
    # public, not a fork, and owned by the GitHub account you linked at sign-in. Each kind
    # is counted once for the project, so asking again claims nothing twice. Asked at most
    # ten times an hour.
    post_reward_me_github_badge(req: badgeIn) returns (rep: badgeOut)
    # Reads the stars, follows and forks of the GitHub account you linked at sign-in,
    # from GitHub's API, and puts to staff each that is a repository or account of ours,
    # once each. Asked at most twice an hour.
    post_reward_me_github_check() returns (rep: checkOut)
    # Takes the address of a post of yours, reads it from its network, and holds it for
    # a week if it is public, is yours, and carries your own referral link. A held post
    # is not points: it is looked at every day, and one found gone, private or without
    # your link is removed and earns nothing. One still up after seven days is put to
    # staff, who decide it. Pasting a post again answers the first.
    post_reward_me_posts(req: postIn) returns (rep: postView)
    # Looks at one of your pending or removed posts now, with the same checks as the
    # daily look, and adds the look to its history. A removed post that is back up and
    # carries your link is held again and its week starts over. Asked at most ten times
    # an hour.
    post_reward_me_posts_by_id_recheck(req: postRef) returns (rep: postView)
    # Reads your recent public posts on Bluesky, Mastodon, Reddit, Hacker News or DEV by the
    # handle you give, and holds each that carries your own referral link, as pasting
    # it would. Every other network is pasted as an address: X, Medium and Hashnode are read when
    # pasted, and LinkedIn, TikTok, YouTube, Threads, Instagram and Facebook cannot be read
    # without your sign-in there, so staff decide those by hand. Asked at most three times an hour.
    post_reward_me_posts_scan(req: scanIn) returns (rep: scanOut)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# dropped (2) — the value does not cross, and nothing fails:
#   postView.CountsAt  time.Time  (empty message)
#   postView.Since  time.Time  (empty message)
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_admin_reward_legacy  legacyOut.Sources  map[string]reward.sourceOut  (map)
#
# opaque (6) — crosses, arrives without its name:
#   postList.Posts  reward.postView (list element)
#   postView.Checks  catalog.Check (list element)
#   postView.CountsAt  time.Time
#   postView.Since  time.Time
#   rulesOut.Pillars  reward.pillarRules (list element)
#   scanOut.Posts  reward.postView (list element)
