# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package ad

struct AdCampaign {
    ID         text @0
    Org        text @8
    Name       text @16
    Platform   text @24
    Account    text @32
    ExternalID text @40
    Status     text @48
    Objective  text @56
    Budget     i64  @64
    Spend      i64  @72
    CreatedAt  i64  @80
    UpdatedAt  i64  @88
}

struct adSummary {
    Active    i64 @0
    Budget    i64 @8
    Campaigns i64 @16
    Spend     i64 @24
}

struct campaignInput {
    Name      text @0
    Platform  text @8
    Account   text @16
    Status    text @24
    Objective text @32
    Budget    i64  @40
    Spend     i64  @48
}

struct campaignList {
    Data list<bytes> @0
}

struct campaignRef {
    ID text @0
}

struct listCampaignsIn {
    Status text @0
    Limit  i64  @8
}

struct updateCampaignIn {
    ID        text @0
    Name      text @8
    Platform  text @16
    Account   text @24
    Status    text @32
    Objective text @40
    Budget    i64  @48
    Spend     i64  @56
}

interface ad {
    # Removes one of the caller org's campaigns and answers 204 with
    # no body. It deletes the stored record only: a campaign already launched keeps
    # running on the ad network, which must be stopped there. An id another org owns
    # reads as not found.
    delete_ad_campaigns_by_id(req: campaignRef)
    # Returns the caller org's ad campaigns, most recently updated
    # first, optionally narrowed to one lifecycle status. The listing is bounded by
    # the org: another tenant's campaigns are not reachable from here at all.
    get_ad_campaigns(req: listCampaignsIn) returns (rep: campaignList)
    # Returns one of the caller org's campaigns. An id another org owns
    # reads as not found, so the response cannot confirm that it exists.
    get_ad_campaigns_by_id(req: campaignRef) returns (rep: AdCampaign)
    # Rolls the caller org's ad campaigns up into four numbers: how many
    # campaigns exist, how many are active, and the summed budget and spend across
    # all of them. Budget and spend are MINOR units (cents), the same units the
    # campaign rows carry. It counts only this org's campaigns.
    get_ad_summary() returns (rep: adSummary)
    # Registers a new ad campaign for the caller's org and answers
    # 201 with the stored row. It only records the campaign — nothing is sent to the
    # ad network until POST /v1/ad/campaigns/{id}/launch runs it. The org is
    # stamped by the server from the validated principal, so a body can never place
    # a campaign in another tenant.
    post_ad_campaigns(req: campaignInput) returns (rep: AdCampaign)
    # Replaces the user-owned fields of one of the caller org's
    # campaigns and answers the stored row. It is a full replace, not a patch: every
    # field is written from the request, so an omitted one is cleared. externalId is
    # launch-owned and is never touched here, so editing a campaign cannot break its
    # link to a live provider execution.
    put_ad_campaigns_by_id(req: updateCampaignIn) returns (rep: AdCampaign)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   campaignList.Data  ad.AdCampaign (list element)
