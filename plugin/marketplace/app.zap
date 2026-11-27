# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package marketplace

struct Feedback {
    Job     text @0
    From    text @8
    About   text @16
    Listing text @24
    Rating  i64  @32
    Comment text @40
    At      i64  @48
}

struct Job {
    ID        text        @0
    Listing   text        @8
    Title     text        @16
    Attempt   text        @24
    BuyerOrg  text        @32
    SellerOrg text        @40
    Amount    bytes       @48
    Currency  text        @56
    Category  text        @64
    Status    text        @72
    Ending    text        @80
    Brief     text        @88
    Performed text        @96
    Deadline  i64         @104
    Review    i64         @112
    Clearance text        @120
    Delivery  bytes       @128
    Dispute   bytes       @136
    Escrow    bytes       @144
    History   list<bytes> @152
    CreatedAt i64         @160
    UpdatedAt i64         @168
}

struct Listing {
    ID           text  @0
    PublisherOrg text  @8
    Kind         text  @16
    Tool         text  @24
    Ref          text  @32
    Title        text  @40
    Description  text  @48
    Category     text  @56
    Price        bytes @64
    Currency     text  @72
    Recipient    text  @80
    Public       bool  @88
    Docs         text  @96
    Expert       bytes @104
    Capability   bytes @112
    CreatedAt    i64   @120
    UpdatedAt    i64   @128
}

struct Payout {
    Wallet  text @0
    Address text @8
    Bound   bool @16
    BoundAt i64  @24
}

struct PayoutChallenge {
    Wallet  text @0
    Address text @8
    Message text @16
    Digest  text @24
    Expires i64  @32
}

struct ShopCapabilityWeights {
    Name           text @0
    Version        i64  @8
    SHA256         text @16
    BaseSHA256     text @24
    Representation text @32
    Form           text @40
    Bytes          i64  @48
    Tar            text @56
}

struct ShopListing {
    ID           text  @0
    PublisherOrg text  @8
    Kind         text  @16
    Tool         text  @24
    Title        text  @32
    Description  text  @40
    Category     text  @48
    Price        bytes @56
    Currency     text  @64
    Public       bool  @72
    Docs         text  @80
    Expert       bytes @88
    Capability   bytes @96
    CreatedAt    i64   @104
    UpdatedAt    i64   @112
    Seller       bytes @120
    Reputation   bytes @128
    Links        bytes @136
}

struct declineIn {
    ID     text @0
    Reason text @8
}

struct deliverIn {
    ID   text @0
    Note text @8
    URL  text @16
    Hash text @24
}

struct disputeIn {
    ID     text @0
    Reason text @8
}

struct feedbackIn {
    ID      text @0
    Rating  i64  @8
    Comment text @16
}

struct hireIn {
    Listing   text @0
    Seller    text @8
    Title     text @16
    Brief     text @24
    Amount    text @32
    Category  text @40
    Deadline  i64  @48
    Review    i64  @56
    Performed text @64
    Wallet    text @72
    Rail      text @80
    Payment   text @88
    Attempt   text @96
}

struct installReq {
    Tool text @0
}

struct installState {
    Tool      text @0
    Installed bool @8
}

struct jobPage {
    Jobs list<bytes> @0
}

struct jobRef {
    ID text @0
}

struct jobsQuery {
    Role   text @0
    Status text @8
}

struct listQuery {
    Kind text @0
}

struct listingPage {
    Listings list<bytes> @0
}

struct listingRef {
    ID text @0
}

struct marketCatalog {
    Items list<bytes> @0
}

struct patchReq {
    ID          text       @0
    Title       list<text> @8
    Description list<text> @16
    Category    list<text> @24
    Price       list<text> @32
    Currency    list<text> @40
    Recipient   list<text> @48
    Public      list<bool> @56
    Docs        list<text> @64
    Expert      bytes      @72
    Capability  bytes      @80
}

struct payoutIn {
    Wallet text @0
}

struct publishReq {
    Kind        text  @0
    Tool        text  @8
    Title       text  @16
    Description text  @24
    Category    text  @32
    Price       text  @40
    Currency    text  @48
    Recipient   text  @56
    Public      bool  @64
    Docs        text  @72
    Expert      bytes @80
    Capability  bytes @88
}

struct resolveIn {
    ID      text @0
    Outcome text @8
}

struct sellerQuery {
    Year i64 @0
}

struct shopQuery {
    Q         text @0
    Kind      text @8
    Category  text @16
    Price     text @24
    Rating    i64  @32
    Seller    text @40
    Area      text @48
    Limit     i64  @56
    Offset    i64  @64
    Sort      text @72
    Dir       text @80
    Language  text @88
    Verified  bool @96
    MinLevel  i64  @104
    Badge     text @112
    TokenTier text @120
    RateMin   text @128
    RateMax   text @136
    Product   text @144
}

struct shopRef {
    ID text @0
}

struct verifyIn {
    Signature text @0
}

struct verifyReq {
    ID       text @0
    Verified bool @8
}

interface marketplace {
    # Withdraws one of the caller org's listings from the marketplace and
    # answers 204. Only the publishing org can remove its own listing; an id that is
    # unknown, or belongs to another org, is the same 404, so a probe learns nothing
    # about what exists. Removing a listing removes its price from per-call enforcement;
    # it does not uninstall the tool for anyone who already installed it, and a job
    # already opened through it runs to its end. An org admin unpublishes, and an
    # expert unpublishes their own listing.
    delete_marketplace_listings_by_id(req: listingRef)
    # Lists every tool and agent the caller can reach in their own org and
    # project, enriched with any public listing's title, category and price, and with
    # installed=true on the ones already activated for that scope. It is the shop
    # window: one read that answers what exists, what it costs and what is already on.
    get_marketplace() returns (rep: marketCatalog)
    # Lists the jobs the caller's org is a party to, newest first: as buyer, the work
    # it hired — with the quotes it has not paid and the jobs being funded, each with
    # the attempt that made it, so a hire whose answer was lost is found here — and as
    # seller, the work it was hired for, which never includes another org's quote.
    # Any member of the org.
    get_marketplace_jobs(req: jobsQuery) returns (rep: jobPage)
    # Reads one job the caller's org is a party to. Another org's job, and one that
    # does not exist, are the same 404.
    get_marketplace_jobs_by_id(req: jobRef) returns (rep: Job)
    # Returns the listings the caller's own org has published — what this
    # org is offering, not what it can buy — optionally of one kind. A publisher only
    # ever sees its own rows.
    get_marketplace_listings(req: listQuery) returns (rep: listingPage)
    # Reads one public listing as the shop shows it — its seller's public face, its
    # reputation and the ways to buy it. A private listing and one that does not
    # exist are the same 404. It needs no credential.
    get_marketplace_shop_by_id(req: shopRef) returns (rep: ShopListing)
    # Downloads an open-source Kai capability listing's .tar weights (base64) and
    # shared representation metadata. Only public capability listings with price 0
    # allow weight downloads; monetized capability listings keep their weights
    # sealed and answer 403. It needs no credential.
    get_marketplace_shop_by_id_weights(req: shopRef) returns (rep: ShopCapabilityWeights)
    # Edits one of the caller org's listings — its copy, price, payout wallet,
    # visibility and documentation, and an expert's profile — under the same rules
    # publish applies, and answers the listing as it now stands. Another org's
    # listing and one that does not exist are the same 404. An org admin edits, and
    # an expert edits their own listing.
    patch_marketplace_listings_by_id(req: patchReq) returns (rep: Listing)
    # Activates one tool for the caller's own org and project. A marketplace
    # install IS the tool plane's activation write — one store, one truth — so an
    # installed capability is immediately dispatchable and a monetized one is priced
    # from its listing at every call. The tool must resolve in the caller's scope, so
    # installing something that does not exist is refused rather than recorded.
    post_marketplace_install(req: installReq) returns (rep: installState)
    # Hires another org for a piece of work, through a public listing or by a direct
    # offer, and answers 201 with the job — open, waiting for the seller.
    # It is paid over x402, in two steps on this one call. Sent without payment, the
    # terms are cleared first — principal decides whether the buyer may pay the seller
    # this amount, and a blocked payment, one that waits on the seller's tax form, and
    # one that clears only with tax withheld, which this rail does not withhold, are
    # refused and nothing is opened — and it answers 402 with the terms to sign, for
    # the job resource job:<id>: on PAYMENT-REQUIRED, in the body, and in the message.
    # Sent again with the signed authorization — on PAYMENT-SIGNATURE, or as
    # payment — echoing that resource, the terms are cleared once more, the job is
    # funding, and the rail checks the payment against exactly those terms, checks
    # it stays acceptable until a dispute could last be ruled on, holds it for this
    # job, and sets the amount aside in the buyer's wallet: a wallet that cannot
    # cover it answers 402 insufficient_funds and opens nothing. Nothing is paid
    # until the buyer releases the job, or its review window passes after delivery;
    # a job that ends unpaid returns the amount to the wallet. Any member of the
    # buying org; a platform SuperAdmin inspecting another org cannot spend it.
    # A hire sent with an attempt — the buyer's own key for it, in the body — is one
    # hire however often it is sent: the same attempt with the same request answers
    # the same job, so a request whose answer was lost is sent again as it was, and
    # opens one job and sets its amount aside once. It answers the quote's terms
    # again while the job is quoted or funding (a quote over its hour is given up
    # and quoted anew), and the job, 201, once it opened. Another request under an
    # attempt already used is refused, 422. The buyer's own jobs list its quotes
    # and those being funded, with their attempts, so an attempt in flight is found
    # there too. Without an attempt, the same terms asked again within the hour
    # answer the same quote, and the same payment sent again answers the job it
    # opened.
    post_marketplace_jobs(req: hireIn) returns (rep: Job)
    # Accepts a job the caller's org was hired for: the seller takes the work on and
    # the clock toward its deadline is the seller's. Only an open job, and only before
    # its deadline.
    post_marketplace_jobs_by_id_accept(req: jobRef) returns (rep: Job)
    # Takes back a job the caller's org opened, before the seller accepts it. Nothing
    # was paid: the amount set aside returns to the wallet.
    post_marketplace_jobs_by_id_cancel(req: jobRef) returns (rep: Job)
    # Declines a job the caller's org was hired for, before any work. Nothing was
    # paid: the buyer's authorization is given up, never settled, and the amount it
    # set aside returns to the buyer's wallet.
    post_marketplace_jobs_by_id_decline(req: declineIn) returns (rep: Job)
    # Records delivery of a job the caller's org accepted, before its deadline, and
    # starts the review window: the buyer releases or disputes within it, or it
    # releases itself when it closes.
    post_marketplace_jobs_by_id_deliver(req: deliverIn) returns (rep: Job)
    # Stops a job for a ruling, as either party: before delivery (and before the
    # deadline), or within the review window after it. Nothing moves while it is
    # disputed. It ends when the buyer releases it, the seller refunds it, the
    # platform's arbiter rules, or the arbiter's time lapses and it is refunded.
    post_marketplace_jobs_by_id_dispute(req: disputeIn) returns (rep: Job)
    # Rates the other party of a settled job — the seller when the caller's org
    # bought, the buyer when it sold — once per party per job, and never edited. A
    # job counts as settled once it was released, or refunded after the seller took
    # it on; one cancelled or declined before any work earns nothing. What buyers say
    # of a seller is its listing's and its own reputation in the shop. It answers 201
    # with the feedback recorded.
    post_marketplace_jobs_by_id_feedback(req: feedbackIn) returns (rep: Feedback)
    # Refunds a job the caller's org accepted, as the seller, at any point before it
    # is paid: nothing was moved, so the buyer's authorization is given up, never
    # settled, and the amount it set aside returns to the buyer's wallet.
    post_marketplace_jobs_by_id_refund(req: jobRef) returns (rep: Job)
    # Releases a job the caller's org is paying for, paying the seller the whole
    # amount: the buyer's authorization is settled on the rail, once, and the rail
    # states the payment. The buyer may release any time after acceptance, including
    # to end a dispute.
    post_marketplace_jobs_by_id_release(req: jobRef) returns (rep: Job)
    # Rules on a disputed job, as the platform's arbiter: release pays the seller the
    # whole amount from the buyer's held authorization, if the payment still clears;
    # refund ends it with nothing paid, and the amount set aside returns to the
    # buyer's wallet. A platform SuperAdmin only.
    post_marketplace_jobs_by_id_resolve(req: resolveIn) returns (rep: Job)
    # Offers one thing the caller's org owns on the marketplace — an agent,
    # persona, app, skill, Kai capability or MCP server, or, for the platform, a tool
    # sold per call — optionally monetized. An expert is the exception to the org's
    # admin: any member lists themself, once, as a person (kind expert), and is the
    # listing's owner beside the org's admins. The owning app is asked, as the org,
    # whether the thing is the org's, so a listing never sells something that does
    # not exist or belongs to someone else: a tool every org reaches is the
    # platform's, and a server enabled off the public shelf is the shelf's. A
    # monetized listing names a payout wallet of the org's own, which wallets
    # confirms, so a price is never unpayable. The price is exact to 18 decimal
    # places, so a per-call price below a cent is a real price and not a rounded-away
    # zero. An org admin publishes, at most 1000 listings an org; it answers 201 with
    # the created row.
    post_marketplace_listings(req: publishReq) returns (rep: Listing)
    # Marks an expert listing verified, or takes the mark away, whichever org
    # published it. Verified is staff's word that the person is who the listing says
    # and does what it says: only a SuperAdmin (an owner of the reserved admin org)
    # gives it, never an org admin and never the expert, and each change is on the
    # audit trail with who made it. It answers the listing as it now stands. A
    # listing that is not an expert's is a 404.
    post_marketplace_listings_by_id_verify(req: verifyReq) returns (rep: Listing)
    # Starts binding one of the caller org's wallets as the wallet it is paid into:
    # answers a challenge naming the org, the wallet and its address, to be signed
    # with that wallet within fifteen minutes and sent to POST
    # /v1/marketplace/seller/payout/verify. A newer challenge replaces an older one.
    # An org admin binds.
    post_marketplace_seller_payout(req: payoutIn) returns (rep: PayoutChallenge)
    # Binds the caller org's payout wallet: the signature over its outstanding
    # challenge is recovered, and must recover to the wallet's address, before the
    # challenge expires. A challenge binds once. It answers the payout wallet as it
    # now stands; a job offered directly to the org is paid into it. An org admin
    # binds.
    post_marketplace_seller_payout_verify(req: verifyIn) returns (rep: Payout)
    # Deactivates one tool for the caller's own org and project, so it stops
    # being dispatchable there. It is the exact inverse of install and touches the same
    # activation record; deactivating something that was never active is not an error.
    # The listing itself is untouched — this withdraws the caller's use of a capability,
    # not anyone's offer of it.
    post_marketplace_uninstall(req: installReq) returns (rep: installState)
}

# ---------------------------------------------------------------------
# 24 op(s) here. What follows is what this schema does not carry.
#
# dropped (3) — the value does not cross, and nothing fails:
#   Job.Amount  money.Amount  (empty message)
#   Listing.Price  money.Amount  (empty message)
#   ShopListing.Price  money.Amount  (empty message)
#
# blocked (2) — the op is absent; the field has no wire form:
#   get_marketplace_seller  Onboarding.Earnings  marketplace.Earnings  (reaches one)
#   get_marketplace_shop  Shop.Facets  marketplace.Facets  (reaches one)
#
# opaque (21) — crosses, arrives without its name:
#   Job.Amount  money.Amount
#   Job.Delivery  marketplace.Delivery
#   Job.Dispute  marketplace.Dispute
#   Job.Escrow  marketplace.Escrow
#   Job.History  marketplace.Step (list element)
#   Listing.Capability  marketplace.CapabilitySpec
#   Listing.Expert  marketplace.Expert
#   Listing.Price  money.Amount
#   ShopListing.Capability  marketplace.CapabilitySpec
#   ShopListing.Expert  marketplace.Expert
#   ShopListing.Links  marketplace.Links
#   ShopListing.Price  money.Amount
#   ShopListing.Reputation  marketplace.Reputation
#   ShopListing.Seller  marketplace.Seller
#   jobPage.Jobs  marketplace.Job (list element)
#   listingPage.Listings  marketplace.Listing (list element)
#   marketCatalog.Items  marketplace.marketItem (list element)
#   patchReq.Capability  marketplace.capabilityIn
#   patchReq.Expert  marketplace.expertIn
#   publishReq.Capability  marketplace.capabilityIn
#   publishReq.Expert  marketplace.expertIn
