# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package campaign

struct campaignFilter {
    Status text @0
    Limit  i64  @8
}

struct campaignPage {
    Data list<bytes> @0
}

struct campaignRecord {
    ID         text        @0
    Org        text        @8
    Name       text        @16
    Audience   text        @24
    Content    list<text>  @32
    Channels   list<bytes> @40
    ScheduleAt i64         @48
    Budget     i64         @56
    Status     text        @64
    CreatedAt  i64         @72
    UpdatedAt  i64         @80
}

struct campaignRef {
    ID text @0
}

struct campaignResults {
    CampaignID  text        @0
    Name        text        @8
    Status      text        @16
    Range       text        @24
    Start       text        @32
    End         text        @40
    Available   bool        @48
    Impressions i64         @56
    Clicks      i64         @64
    Conversions i64         @72
    Revenue     f64         @80
    Visitors    i64         @88
    SpendCents  i64         @96
    CTR         f64         @104
    CVR         f64         @112
    CAC         f64         @120
    ROAS        f64         @128
    Channels    list<bytes> @136
    Source      text        @144
    ABTest      bytes       @152
}

struct campaignSummary {
    Campaigns i64        @0
    Live      i64        @8
    Budget    i64        @16
    Channels  list<text> @24
}

struct campaignUpdate {
    ID text @0
}

struct campaignWrite {
    Name       text        @0
    Audience   text        @8
    Content    list<text>  @16
    Channels   list<bytes> @24
    ScheduleAt i64         @32
    Budget     i64         @40
}

struct channelAdd {
    ID       text @0
    Kind     text @8
    Platform text @16
    Account  text @24
}

struct channelRef {
    ID   text @0
    Kind text @8
}

struct metricsQuery {
    ID    text @0
    Range text @8
    Start text @16
    End   text @24
}

interface campaign {
    # Removes one campaign of the caller's org and answers 204 with no
    # body. 404 when the org has no campaign with that id.
    # It deletes the RECORD, not the executions: a campaign whose channels are live
    # on a provider should be paused first, or those executions keep running with
    # nothing here to report them.
    delete_campaign_by_id(req: campaignRef)
    # Drops one channel from a campaign and returns the updated
    # campaign. 404 when the campaign carries no channel of that kind.
    # It removes the channel from the PLAN. A channel that is live at its provider
    # should be paused first — dropping the row here leaves nothing to pause it with
    # afterwards.
    delete_campaign_by_id_channels_by_kind(req: channelRef) returns (rep: campaignRecord)
    # Returns the org's campaigns, newest first, optionally narrowed to
    # one status.
    # A campaign is the top-level go-to-market object: a value that SPANS channels
    # (paid, organic, email) and fans out to the executor for each. The listing is
    # org-scoped server-side, so one org can never see another's campaigns.
    get_campaign(req: campaignFilter) returns (rep: campaignPage)
    # Returns one campaign of the caller's org — its name, audience,
    # creatives, channels with their per-channel launch state, schedule, budget and
    # status. 404 when the org has no campaign with that id.
    get_campaign_by_id(req: campaignRef) returns (rep: campaignRecord)
    # Returns a campaign's results over a window: the analytics
    # funnel (impressions, clicks, conversions, revenue, visitors), the spend each
    # channel's connector reports, and the derived growth KPIs — CTR, CVR, CAC and
    # ROAS.
    # There is exactly ONE metrics plane and nothing is stored here: the funnel is an
    # analytics query over the campaign's utm_campaign-tagged events, and the spend is
    # each provider's own number read through the org's connector. A warehouse that is
    # not emitting yet degrades to available:false with zeroes — honest-empty, never a
    # 500 and never a fabricated number. When the campaign runs more than one creative
    # and an experiment is wired, abTest carries the A/B analysis.
    get_campaign_by_id_metrics(req: metricsQuery) returns (rep: campaignResults)
    # Returns the org's go-to-market roll-up: how many campaigns
    # exist, how many are live, their total budget in cents, and which channel
    # executors this deployment can actually reach.
    # The channel list is the deployment's honest capability, not a wish: a kind
    # missing from it is one a launch will record as "unavailable" rather than fail
    # on.
    get_campaign_summary() returns (rep: campaignSummary)
    # Creates a campaign as a DRAFT and returns it.
    # A draft is inert: nothing is sent, no connector is touched and no budget is
    # committed until the campaign is launched. The channels named here are validated
    # and de-duplicated by kind (one executor per kind), and every channel starts
    # "pending" whatever the caller claims — a client can never assert a launched
    # state.
    post_campaign(req: campaignWrite) returns (rep: campaignRecord)
    # Adds a channel to a campaign, or REPLACES the one it already
    # has of that kind, and returns the updated campaign.
    # A campaign carries at most one channel per kind, because the kind IS the
    # executor: adding a second "paid" channel would mean two ad accounts running one
    # campaign with no way to tell their results apart. The new channel starts
    # "pending" — adding it does not launch it.
    post_campaign_by_id_channels(req: channelAdd) returns (rep: campaignRecord)
    # Rewrites a campaign's core fields — name, audience, creatives,
    # schedule and budget — and returns the updated campaign.
    # Channels are replaced ONLY while the campaign is still a draft. Once it is
    # launched its channels carry provider state (an external id, a live status), so
    # they are added and removed explicitly through the channels sub-resource
    # instead; a whole-object write would silently orphan a running execution.
    put_campaign_by_id(req: campaignUpdate) returns (rep: campaignRecord)
}

# ---------------------------------------------------------------------
# 9 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   campaignUpdate.campaignWrite  campaign.campaignWrite  (promoted, not carried)
#
# opaque (4) — crosses, arrives without its name:
#   campaignPage.Data  campaign.campaignRecord (list element)
#   campaignRecord.Channels  campaign.ChannelSpec (list element)
#   campaignResults.Channels  campaign.ChannelMetric (list element)
#   campaignWrite.Channels  campaign.ChannelSpec (list element)
