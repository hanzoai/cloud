# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package social

struct accountFilter {
    Provider text @0
    Limit    text @8
}

struct postFilter {
    Status text @0
    Limit  text @8
}

struct rowRef {
    ID text @0
}

struct socialAccount {
    ID        text @0
    Org       text @8
    Provider  text @16
    Handle    text @24
    Status    text @32
    Token     text @40
    CreatedAt i64  @48
    UpdatedAt i64  @56
}

struct socialAccountBody {
    Handle   text @0
    Provider text @8
    Status   text @16
}

struct socialAccountWrite {
    ID       text @0
    Handle   text @8
    Provider text @16
    Status   text @24
}

struct socialAccounts {
    Data list<bytes> @0
}

struct socialPost {
    ID         text       @0
    Org        text       @8
    Content    text       @16
    Channel    text       @24
    Status     text       @32
    ScheduleAt i64        @40
    Media      list<text> @48
    AccountID  text       @56
    ExternalID text       @64
    Error      text       @72
    CreatedAt  i64        @80
    UpdatedAt  i64        @88
}

struct socialPostBody {
    Channel    text       @0
    Content    text       @8
    Media      list<text> @16
    ScheduleAt i64        @24
    Status     text       @32
}

struct socialPostWrite {
    ID         text       @0
    Channel    text       @8
    Content    text       @16
    Media      list<text> @24
    ScheduleAt i64        @32
    Status     text       @40
}

struct socialPosts {
    Data list<bytes> @0
}

struct socialProviders {
    Data list<bytes> @0
}

struct socialSummary {
    Accounts  i64 @0
    Posts     i64 @8
    Published i64 @16
    Scheduled i64 @24
}

interface social {
    # Removes one connected account from the org and answers 204 with no
    # body; an id that is not there is 404.
    # It removes the account record only. Posts that already published through it keep
    # their published state and their recorded external ids — this does not retract
    # anything from the network.
    delete_social_accounts_by_id(req: rowRef)
    # Removes one post from the org and answers 204 with no body; an id that
    # is not there is 404.
    # It deletes the record here only. A post that has already published is not
    # retracted from the network by deleting it.
    delete_social_posts_by_id(req: rowRef)
    # Returns the org's connected accounts — each one's id, network,
    # handle, status and timestamps, most-recently-updated first.
    # An account's provider access token is NEVER included in any response on this
    # surface. Only the publisher reads it.
    get_social_accounts(req: accountFilter) returns (rep: socialAccounts)
    # Returns one of the org's connected accounts by id — its network,
    # handle, status and timestamps — or 404. The provider access token is not part of
    # the response.
    get_social_accounts_by_id(req: rowRef) returns (rep: socialAccount)
    # Returns the org's posts — content, channel, status, scheduled time,
    # media and timestamps — most-recently-updated first.
    get_social_posts(req: postFilter) returns (rep: socialPosts)
    # Returns one of the org's posts by id, with its current status, scheduled
    # time, media and — once it has published — the account and external id it published
    # under. 404 when there is no such post for this org.
    get_social_posts_by_id(req: rowRef) returns (rep: socialPost)
    # Reports each supported network's publish-readiness: whether this
    # deployment holds the OAuth application credentials for it and, when it does not,
    # exactly which environment variables are missing.
    # This is a live read of the deployment's own configuration, not a static list of
    # networks — it answers "can I connect this today", which is what a connect
    # affordance and a pre-cutover checklist both need. It says nothing about whether
    # the caller has connected an account; that is the accounts listing.
    get_social_providers() returns (rep: socialProviders)
    # Returns four counts for the caller's org: total posts, how many are
    # scheduled, how many have published, and how many accounts are connected. It is
    # the dashboard roll-up, computed over the org's own rows in one read.
    get_social_summary() returns (rep: socialSummary)
    # Records a social account for the org and answers 201 with the
    # stored row, including the generated id later calls address it by.
    post_social_accounts(req: socialAccountBody) returns (rep: socialAccount)
    # Stores a post for the org and answers 201 with the stored row.
    # A post created as scheduled for a time that has already passed is published
    # IMMEDIATELY, and the row returned carries that outcome — this is the one behaviour
    # a reader would otherwise miss. A future-scheduled post is left for the scheduler,
    # and a draft is left alone. Publishing never fails the creation: the post is stored
    # either way, and a publish that could not run leaves the row for the scheduler to
    # retry.
    post_social_posts(req: socialPostBody) returns (rep: socialPost)
    # Publishes the post immediately to the connected accounts on its channel
    # and answers with the updated row, carrying the account and external id it
    # published under.
    # It is IDEMPOTENT: a post that has already published, or that another caller is
    # publishing right now, comes back unchanged rather than being posted twice. That
    # claim is taken before any network call, which is what makes a double submit safe.
    # The two failure shapes differ on purpose. Having no connected account for the
    # channel is the caller's to fix, so it is recorded ON the post as failed with the
    # reason and answers normally. A deployment that lacks the network's own credentials
    # cannot publish for anyone, so that is a 503 naming exactly what is missing.
    post_social_posts_by_id_publish(req: rowRef) returns (rep: socialPost)
    # Replaces the account's network, handle and status with what the
    # body carries, and answers with the stored row.
    # This is a REPLACEMENT, not a merge, which is the rule most easily got wrong: a
    # field the body omits is written as its default, so leaving out the handle blanks
    # it and leaving out the status resets it to connected. Send the whole record. The
    # same vocabularies as create apply, and an unknown network or status is refused
    # rather than coerced.
    put_social_accounts_by_id(req: socialAccountWrite) returns (rep: socialAccount)
    # Replaces the post's content, channel, status, scheduled time and media
    # with what the body carries, and answers with the stored row.
    # A REPLACEMENT, not a merge: an omitted field is written as its default, so
    # omitting media clears it and omitting the status resets the post to draft.
    # `content` is required on every update. Unlike create, this never triggers a
    # publish — moving a post's scheduled time into the past here leaves it for the
    # scheduler; publish now is its own operation.
    put_social_posts_by_id(req: socialPostWrite) returns (rep: socialPost)
}

# ---------------------------------------------------------------------
# 13 op(s) here. What follows is what this schema does not carry.
#
# opaque (3) — crosses, arrives without its name:
#   socialAccounts.Data  social.socialAccount (list element)
#   socialPosts.Data  social.socialPost (list element)
#   socialProviders.Data  social.socialProvider (list element)
