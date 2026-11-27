# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package social

struct accountFilter {
    Provider text @0
    Limit    text @8
}

struct channelAnalyticsIn {
    ID   text @0
    Days text @8
}

struct channelAnalyticsOut {
    ChannelID text        @0
    Provider  text        @8
    Handle    text        @16
    GroupName text        @24
    Posts     i64         @32
    Published i64         @40
    Scheduled i64         @48
    Failed    i64         @56
    Metrics   list<bytes> @64
}

struct channelConnectOut {
    AuthorizeURL text  @0
    Connected    bool  @8
    Provider     text  @16
    Group        text  @24
    Channel      bytes @32
}

struct channelGroupList {
    Data list<bytes> @0
}

struct channelItem {
    ID            text @0
    Provider      text @8
    Name          text @16
    Handle        text @24
    Profile       text @32
    Picture       text @40
    Status        text @48
    ExternalID    text @56
    GroupID       text @64
    GroupName     text @72
    RefreshNeeded bool @80
    Disabled      bool @81
    CreatedAt     i64  @88
    UpdatedAt     i64  @96
}

struct channelList {
    Data list<bytes> @0
}

struct channelListFilter {
    Provider text @0
    Group    text @8
    Limit    text @16
}

struct channelProviderList {
    Data list<bytes> @0
}

struct channelRef {
    ID text @0
}

struct channelSetGroupIn {
    ID    text @0
    Group text @8
}

struct channelUpdateIn {
    ID       text @0
    Provider text @8
    Name     text @16
    Handle   text @24
    Picture  text @32
    Status   text @40
    Group    text @48
    Disabled bool @56
}

struct orgApps {
    Apps list<bytes> @0
    Data list<bytes> @8
}

struct orgAppsWrite {
    Apps list<bytes> @0
}

struct postAnalyticsOut {
    PostID     text        @0
    Channel    text        @8
    AccountID  text        @16
    Status     text        @24
    ReleaseURL text        @32
    ExternalID text        @40
    Metrics    list<bytes> @48
}

struct postCreateIn {
    Content    text       @0
    Channel    text       @8
    Channels   list<text> @16
    AccountID  text       @24
    AccountIDs list<text> @32
    Group      text       @40
    Status     text       @48
    ScheduleAt i64        @56
    Media      list<text> @64
}

struct postCreateOut {
    ID            text        @0
    Content       text        @8
    Channel       text        @16
    Status        text        @24
    ScheduleAt    i64         @32
    Media         list<text>  @40
    AccountID     text        @48
    ExternalID    text        @56
    Error         text        @64
    ExternalGroup text        @72
    ReleaseURL    text        @80
    CreatedAt     i64         @88
    UpdatedAt     i64         @96
    Posts         list<bytes> @104
}

struct postFilter {
    Status text @0
    Limit  text @8
}

struct postItem {
    ID                    text       @0
    Content               text       @8
    Channel               text       @16
    Status                text       @24
    ScheduleAt            i64        @32
    Media                 list<text> @40
    AccountID             text       @48
    ExternalID            text       @56
    Error                 text       @64
    Origin                text       @72
    ExternalIDPostiz      text       @80
    ExternalIntegrationId text       @88
    ExternalGroup         text       @96
    ReleaseURL            text       @104
    CreatedAt             i64        @112
    UpdatedAt             i64        @120
}

struct postList {
    Data list<bytes> @0
}

struct postListFilter {
    Status    text @0
    Channel   text @8
    AccountID text @16
    Group     text @24
    Limit     text @32
}

struct postRef {
    ID text @0
}

struct postUpdateIn {
    ID         text       @0
    Content    text       @8
    Channel    text       @16
    AccountID  text       @24
    Status     text       @32
    ScheduleAt i64        @40
    Media      list<text> @48
}

struct rowRef {
    ID text @0
}

struct socialAccount {
    ID            text @0
    Org           text @8
    Provider      text @16
    Handle        text @24
    Status        text @32
    Token         text @40
    CreatedAt     i64  @48
    UpdatedAt     i64  @56
    ExternalID    text @64
    Name          text @72
    Picture       text @80
    Profile       text @88
    GroupID       text @96
    GroupName     text @104
    RefreshNeeded bool @112
    Disabled      bool @113
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
    ID                    text       @0
    Org                   text       @8
    Content               text       @16
    Channel               text       @24
    Status                text       @32
    ScheduleAt            i64        @40
    Media                 list<text> @48
    AccountID             text       @56
    ExternalID            text       @64
    Error                 text       @72
    Origin                text       @80
    ExternalIDPostiz      text       @88
    ExternalIntegrationId text       @96
    ExternalGroup         text       @104
    ReleaseURL            text       @112
    CreatedAt             i64        @120
    UpdatedAt             i64        @128
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
    # Disconnects a social channel from the caller's organization and purges its sealed token from KMS.
    delete_channel_by_id(req: channelRef)
    # Removes one social post record from the caller's organization.
    delete_post_by_id(req: postRef)
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
    # Returns the caller organization's connected and registered social channels,
    # optionally filtered by provider (`?provider=x`) or employee/brand group (`?group=Antje%20Worring`).
    get_channel(req: channelListFilter) returns (rep: channelList)
    # Returns one connected social channel by id for the caller's organization.
    get_channel_by_id(req: channelRef) returns (rep: channelItem)
    # Returns post delivery counts and upstream engagement metrics for one social channel.
    get_channel_by_id_analytics(req: channelAnalyticsIn) returns (rep: channelAnalyticsOut)
    # Lists the employee and brand channel groups configured across the caller
    # organization's channels (such as executive profiles and brand accounts) with channel counts.
    get_channel_groups() returns (rep: channelGroupList)
    # Lists every supported social and publishing network along with whether
    # OAuth application credentials are ready on this deployment or sealed for the org.
    get_channel_providers() returns (rep: channelProviderList)
    # Returns the caller organization's social posts, optionally filtered by
    # status, network channel, specific channel ID (`accountId`), or employee/brand `group`.
    get_post(req: postListFilter) returns (rep: postList)
    # Returns one social post by id for the caller's organization.
    get_post_by_id(req: postRef) returns (rep: postItem)
    # Returns delivery status, release URL, and upstream engagement metrics for one post.
    get_post_by_id_analytics(req: postRef) returns (rep: postAnalyticsOut)
    # Returns the org's connected accounts — each one's id, network,
    # handle, status and timestamps, most-recently-updated first.
    # An account's provider access token is NEVER included in any response on this
    # surface. Only the publisher reads it.
    get_social_accounts(req: accountFilter) returns (rep: socialAccounts)
    # Returns one of the org's connected accounts by id — its network,
    # handle, status and timestamps — or 404. The provider access token is not part of
    # the response.
    get_social_accounts_by_id(req: rowRef) returns (rep: socialAccount)
    # Lists the apps this org has sealed. The secret is not included.
    get_social_credentials() returns (rep: orgApps)
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
    # Stores a new draft, scheduled post, or immediate publish (`status: "now"`),
    # supporting single-network, multi-network (`channels`), specific channel ID (`accountId` / `accountIds`),
    # and employee/brand group (`group`) fanout.
    post_post(req: postCreateIn) returns (rep: postCreateOut)
    # Immediately publishes a draft, scheduled, or failed post to its target channel(s)
    # and returns the updated post record with its external post ID or failure reason.
    post_post_by_id_publish(req: postRef) returns (rep: postItem)
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
    # Modifies a social channel's display metadata, status, group, or disabled flag.
    put_channel_by_id(req: channelUpdateIn) returns (rep: channelItem)
    # Assigns a channel to an employee or brand group (or clears the group when empty),
    # keeping /v1/channel and post.hanzo.ai sidebar groups synchronized.
    put_channel_by_id_group(req: channelSetGroupIn) returns (rep: channelItem)
    # Replaces one social post's content, target channel, schedule, status, and media.
    put_post_by_id(req: postUpdateIn) returns (rep: postItem)
    # Replaces the account's network, handle and status with what the
    # body carries, and answers with the stored row.
    # This is a REPLACEMENT, not a merge, which is the rule most easily got wrong: a
    # field the body omits is written as its default, so leaving out the handle blanks
    # it and leaving out the status resets it to connected. Send the whole record. The
    # same vocabularies as create apply, and an unknown network or status is refused
    # rather than coerced.
    put_social_accounts_by_id(req: socialAccountWrite) returns (rep: socialAccount)
    # Seals the apps in the body. A network left out is unchanged.
    put_social_credentials(req: orgAppsWrite) returns (rep: orgApps)
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
# 30 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   post_channel_connect  channelConnectIn.Fields  map[string]string  (map)
#
# opaque (14) — crosses, arrives without its name:
#   channelAnalyticsOut.Metrics  social.channelAnalyticsMetric (list element)
#   channelConnectOut.Channel  social.channelItem
#   channelGroupList.Data  social.channelGroupItem (list element)
#   channelList.Data  social.channelItem (list element)
#   channelProviderList.Data  social.channelProviderItem (list element)
#   orgApps.Apps  social.orgApp (list element)
#   orgApps.Data  social.orgApp (list element)
#   orgAppsWrite.Apps  social.orgAppWrite (list element)
#   postAnalyticsOut.Metrics  social.postAnalyticsMetric (list element)
#   postCreateOut.Posts  social.postItem (list element)
#   postList.Data  social.postItem (list element)
#   socialAccounts.Data  social.socialAccount (list element)
#   socialPosts.Data  social.socialPost (list element)
#   socialProviders.Data  social.socialProvider (list element)
