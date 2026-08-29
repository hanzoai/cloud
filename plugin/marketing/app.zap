# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package marketing

struct Audience {
    ID         text @0
    Org        text @8
    Name       text @16
    Event      text @24
    WindowDays i64  @32
    CreatedAt  i64  @40
    UpdatedAt  i64  @48
}

struct AudienceList {
    Data list<bytes> @0
}

struct AudiencePreview {
    Available   bool       @0
    Reason      text       @8
    Count       i64        @16
    Deliverable i64        @24
    Unmatched   i64        @32
    Sample      list<text> @40
    Source      text       @48
}

struct AudienceRef {
    ID text @0
}

struct CalendarPost {
    ID          text @0
    Org         text @8
    Title       text @16
    Body        text @24
    Channel     text @32
    ScheduledAt i64  @40
    Status      text @48
    PublishedAt i64  @56
    Error       text @64
    CreatedAt   i64  @72
    UpdatedAt   i64  @80
}

struct Campaign {
    ID          text @0
    Org         text @8
    Name        text @16
    Channel     text @24
    Status      text @32
    Objective   text @40
    Budget      i64  @48
    Spend       i64  @56
    ScheduledAt i64  @64
    CreatedAt   i64  @72
    UpdatedAt   i64  @80
}

struct CampaignList {
    Data list<bytes> @0
}

struct CampaignQuery {
    Status text @0
    Limit  i64  @8
}

struct CampaignRef {
    ID text @0
}

struct EnrollInput {
    ID         text @0
    Address    text @8
    AudienceID text @16
    Channel    text @24
}

struct EnrollResult {
    Resolved        i64  @0
    Enrolled        i64  @8
    AlreadyEnrolled i64  @16
    EnrollmentID    text @24
}

struct EnrollmentList {
    Data list<bytes> @0
}

struct EnrollmentQuery {
    ID    text @0
    Limit i64  @8
}

struct EnrollmentRef {
    ID  text @0
    EID text @8
}

struct Page {
    Limit i64 @0
}

struct PostList {
    Data list<bytes> @0
}

struct PostQuery {
    Status text @0
    Limit  i64  @8
}

struct PostRef {
    ID text @0
}

struct PromoList {
    Data list<bytes> @0
}

struct PromoRef {
    Code text @0
}

struct Quote {
    Code          text @0
    Plan          text @8
    Seats         i64  @16
    Eligible      bool @24
    Reason        text @32
    ListCents     i64  @40
    ChargeCents   i64  @48
    DiscountCents i64  @56
    Remaining     i64  @64
}

struct QuoteQuery {
    Code  text @0
    Plan  text @8
    Seats i64  @16
}

struct RedeemInput {
    Code       text @0
    Instrument text @8
}

struct RedeemResult {
    Redemption      bytes @0
    ChargeCents     i64   @8
    DiscountCents   i64   @16
    AlreadyRedeemed bool  @24
}

struct Redemption {
    Code          text @0
    Org           text @8
    Instrument    text @16
    Plan          text @24
    Seats         i64  @32
    DiscountCents i64  @40
    RedeemedAt    i64  @48
}

struct ScheduleInput {
    ID          text @0
    ScheduledAt i64  @8
}

struct Sequence {
    ID        text @0
    Org       text @8
    Name      text @16
    Status    text @24
    CreatedAt i64  @32
    UpdatedAt i64  @40
}

struct SequenceList {
    Data list<bytes> @0
}

struct SequenceRef {
    ID text @0
}

struct SequenceStatus {
    ID     text @0
    Status text @8
}

struct SequenceView {
    Sequence bytes       @0
    Steps    list<bytes> @8
}

struct Step {
    ID           text @0
    Org          text @8
    SequenceID   text @16
    Idx          i64  @24
    DelaySeconds i64  @32
    Subject      text @40
    Body         text @48
    CreatedAt    i64  @56
}

struct StepInput {
    SequenceID   text @0
    DelaySeconds i64  @8
    Subject      text @16
    Body         text @24
}

struct StepList {
    Data list<bytes> @0
}

struct Summary {
    Campaigns i64 @0
    Active    i64 @8
    Budget    i64 @16
    Spend     i64 @24
}

struct Suppression {
    Org       text @0
    Channel   text @8
    Address   text @16
    Reason    text @24
    CreatedAt i64  @32
}

struct SuppressionList {
    Data list<bytes> @0
}

struct UnsubscribeInput {
    Org     text @0
    Channel text @8
    Address text @16
    Token   text @24
}

struct Unsubscribed {
    Unsubscribed bool @0
    Address      text @8
    Channel      text @16
}

interface marketing {
    # Removes one of the caller org's audiences and answers 204. It
    # deletes the saved filter only — no customer, event or enrollment is touched.
    delete_marketing_audiences_by_id(req: AudienceRef)
    # Removes one of the caller org's posts and answers 204. A
    # post already published is deleted from the calendar only — nothing is
    # retracted from the network it went out on.
    delete_marketing_calendar_by_id(req: PostRef)
    # Removes one of the caller org's campaigns and answers 204. A
    # campaign belonging to another org reads as not found and is left untouched.
    delete_marketing_campaigns_by_id(req: CampaignRef)
    # Re-subscribes an address on one channel and answers 204. An
    # address that is not on the list reads as not found.
    delete_marketing_suppressions(req: Suppression)
    # Returns the org's saved audiences, most recently updated first.
    get_marketing_audiences(req: Page) returns (rep: AudienceList)
    # Returns one of the caller org's saved audiences. An audience
    # belonging to another org reads as not found.
    get_marketing_audiences_by_id(req: AudienceRef) returns (rep: Audience)
    # Evaluates the cohort LIVE — the same resolution an enrollment
    # would run — and reports how big it is and how many real mailboxes it reaches.
    # It is the honest answer to "is this send worth making": a cohort of 500 that
    # mails 3 says so, in deliverable and unmatched. Nothing is sent.
    get_marketing_audiences_by_id_preview(req: AudienceRef) returns (rep: AudiencePreview)
    # Returns the org's calendar, latest scheduled first,
    # optionally narrowed to one status.
    get_marketing_calendar(req: PostQuery) returns (rep: PostList)
    # Returns one of the caller org's posts, including the exact
    # error behind a failed publish. A post belonging to another org reads as not
    # found.
    get_marketing_calendar_by_id(req: PostRef) returns (rep: CalendarPost)
    # Returns the org's campaigns, most recently updated first,
    # optionally narrowed to one lifecycle status.
    get_marketing_campaigns(req: CampaignQuery) returns (rep: CampaignList)
    # Returns one of the caller org's campaigns. A campaign belonging to
    # another org reads as not found.
    get_marketing_campaigns_by_id(req: CampaignRef) returns (rep: Campaign)
    # Returns every promo the deployment offers with its live counters:
    # how many orgs have redeemed it and how many redemptions remain under the cap.
    # The promos are fleet-wide, not per-org — only the counters move.
    get_marketing_promos() returns (rep: PromoList)
    # Prices a promo against a plan and seat count. It is PURE: nothing
    # is redeemed, credited or counted, so it is safe to call from a pricing page on
    # every keystroke. An inactive promo or an exhausted cap quotes ineligible with
    # the reason rather than erroring.
    get_marketing_promos_by_code_eligibility(req: QuoteQuery) returns (rep: Quote)
    # Returns the caller org's OWN redemption of a promo — an
    # org-scoped read, so it can never surface another tenant's. Not found when this
    # org has not redeemed it.
    get_marketing_promos_by_code_redemption(req: PromoRef) returns (rep: Redemption)
    # Returns the org's drip sequences, most recently updated first.
    get_marketing_sequences(req: Page) returns (rep: SequenceList)
    # Returns one of the caller org's sequences together with its steps
    # in send order. A sequence belonging to another org reads as not found.
    get_marketing_sequences_by_id(req: SequenceRef) returns (rep: SequenceView)
    # Returns who is walking one sequence, most recently enrolled
    # first, with each walk's current step and next due time.
    get_marketing_sequences_by_id_enrollments(req: EnrollmentQuery) returns (rep: EnrollmentList)
    # Returns one sequence's steps in send order.
    get_marketing_sequences_by_id_steps(req: SequenceRef) returns (rep: StepList)
    # Rolls up the caller org's campaigns: how many there are, how many are
    # active, and the summed budget and spend in cents.
    get_marketing_summary() returns (rep: Summary)
    # Returns the org's opt-out list, newest first — everyone the
    # send gate will refuse to deliver to.
    get_marketing_suppressions(req: Page) returns (rep: SuppressionList)
    # Is the PUBLIC one-click endpoint (no principal): a recipient
    # clicks the signed link in an email footer. The token binds (org, channel,
    # address), so a caller can only opt OUT exactly the tuple it was minted for —
    # never another address and never another org. An invalid token is refused, and
    # a deployment with no KMS-sealed key refuses rather than accepting anything.
    get_marketing_unsubscribe(req: UnsubscribeInput) returns (rep: Unsubscribed)
    # Saves a cohort filter for the caller's org. Name is required.
    # Omitting event saves the WHOLE-ORG audience — every mailable customer — which
    # needs no analytics warehouse; naming one narrows that roster to the customers
    # who fired it within windowDays.
    post_marketing_audiences(req: Audience) returns (rep: Audience)
    # Adds a post to the content calendar. Channel and body are
    # required. A scheduledAt in the future makes the post "scheduled" and the
    # durable sweep publishes it when it comes due — claimed once, so a post
    # publishes at most once; without one it stays a draft.
    post_marketing_calendar(req: CalendarPost) returns (rep: CalendarPost)
    # Publishes a post NOW, synchronously, whatever its
    # schedule. No social connector is wired today, so every channel answers an
    # honest 501 naming the client a real one would plug into, and the post is
    # recorded failed with that exact reason — never a faked "published".
    post_marketing_calendar_by_id_publish(req: PostRef) returns (rep: CalendarPost)
    # Registers a campaign in the caller's org. Name is required;
    # channel defaults to email and status to draft, and a future scheduledAt with
    # no explicit status makes the campaign "scheduled". Budget and spend are cents
    # and are clamped to >= 0. The id, createdAt and updatedAt of the input are
    # ignored — the server assigns them.
    post_marketing_campaigns(req: Campaign) returns (rep: Campaign)
    # Sets a campaign's send time and moves it to "scheduled". A
    # scheduledAt of 0 clears the schedule and returns it to "draft".
    post_marketing_campaigns_by_id_schedule(req: ScheduleInput) returns (rep: Campaign)
    # Records the caller org's claim on a promo. NOTHING IS CREDITED:
    # the redemption is a row, and credit into an org is an admin decision made on
    # the admin surface against an auditable ledger.
    # The plan is DERIVED from the org's live ACTIVE/TRIALING paid subscription and
    # can never be named by the caller — an org with no qualifying subscription is
    # refused, and so is one whose subscription cannot be read. The seat count is
    # the single-seat floor (claimSeats), so the recorded figure has no input that
    # can inflate it.
    # Guards run under one lock so the cap cannot be raced past: the fleet-wide
    # redemption cap, one redemption per org, one per payment instrument (REQUIRED),
    # and the per-redemption ceiling.
    # It is IDEMPOTENT: an org that already redeemed gets its original redemption
    # back with alreadyRedeemed true.
    post_marketing_promos_by_code_redeem(req: RedeemInput) returns (rep: RedeemResult)
    # Registers a drip sequence in the caller's org. Name is
    # required; status defaults to draft, and a sequence must be ACTIVE before it
    # will accept enrollments. The id, createdAt and updatedAt of the input are
    # ignored — the server assigns them.
    post_marketing_sequences(req: Sequence) returns (rep: Sequence)
    # Adds one contact or a whole audience to a sequence and schedules the
    # first step for each. The sequence must be ACTIVE (a draft sends nothing), and
    # the request must name exactly one of address or audienceId.
    # Enrolling is ALL this does: the message itself is sent later by the drip
    # engine, through the suppression gate, so an opted-out customer can be enrolled
    # here and still never be mailed. Re-posting is safe — an address this sequence
    # already took is counted in alreadyEnrolled and never double-dripped — which is
    # what makes retrying a partially-applied announcement a resume rather than a
    # second send.
    post_marketing_sequences_by_id_enroll(req: EnrollInput) returns (rep: EnrollResult)
    # Stops one walk mid-sequence and answers 204: no further step
    # is sent, and steps already delivered are not recalled. Only an ACTIVE
    # enrollment can be canceled — one already completed or canceled reads as not
    # found.
    post_marketing_sequences_by_id_enrollments_by_eid_cancel(req: EnrollmentRef)
    # Flips draft/active/archived — the activation gate for
    # sending, since only an active sequence accepts enrollments. It does not touch
    # enrollments already walking: archiving stops new ones, not in-flight ones.
    post_marketing_sequences_by_id_status(req: SequenceStatus) returns (rep: SequenceStatus)
    # Appends a message to the END of a sequence: the new step's idx is one
    # past the last, so steps arrive in the order they are added. Body is required
    # and delaySeconds must be >= 0. Adding a step does not disturb enrollments
    # already walking — one that has passed this index simply never sees it.
    post_marketing_sequences_by_id_steps(req: StepInput) returns (rep: Step)
    # Records an opt-out for the org (admin / self-service
    # management). Address is required; channel defaults to email. It is idempotent:
    # re-suppressing the same tuple keeps the original record rather than erroring.
    # From here on the ONE send gate refuses that recipient on that channel.
    post_marketing_suppressions(req: Suppression) returns (rep: Suppression)
    # Replaces a post's editable fields. It is a full write, not
    # a patch, and it RESETS the lifecycle from the schedule: a scheduledAt makes
    # the post "scheduled" again and none makes it a draft — so editing a failed
    # post requeues it rather than leaving it stuck.
    put_marketing_calendar_by_id(req: CalendarPost) returns (rep: CalendarPost)
    # Replaces a campaign's editable fields. It is a full write, not
    # a patch: every field takes the value in the body, and an omitted one is
    # cleared. The id comes from the path — the body cannot retarget another
    # campaign — and createdAt is never rewritten.
    put_marketing_campaigns_by_id(req: Campaign) returns (rep: Campaign)
}

# ---------------------------------------------------------------------
# 35 op(s) here. What follows is what this schema does not carry.
#
# opaque (11) — crosses, arrives without its name:
#   AudienceList.Data  marketing.Audience (list element)
#   CampaignList.Data  marketing.Campaign (list element)
#   EnrollmentList.Data  marketing.Enrollment (list element)
#   PostList.Data  marketing.CalendarPost (list element)
#   PromoList.Data  marketing.PromoStatus (list element)
#   RedeemResult.Redemption  marketing.Redemption
#   SequenceList.Data  marketing.Sequence (list element)
#   SequenceView.Sequence  marketing.Sequence
#   SequenceView.Steps  marketing.Step (list element)
#   StepList.Data  marketing.Step (list element)
#   SuppressionList.Data  marketing.Suppression (list element)
