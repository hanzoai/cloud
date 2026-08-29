# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package marketplace

struct Listing {
    ID           text  @0
    PublisherOrg text  @8
    Tool         text  @16
    Title        text  @24
    Description  text  @32
    Category     text  @40
    Price        bytes @48
    Currency     text  @56
    Recipient    text  @64
    Public       bool  @72
    CreatedAt    i64   @80
}

struct installReq {
    Tool text @0
}

struct installState {
    Tool      text @0
    Installed bool @8
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

struct publishReq {
    Tool        text @0
    Title       text @8
    Description text @16
    Category    text @24
    Price       text @32
    Currency    text @40
    Recipient   text @48
    Public      bool @56
}

interface marketplace {
    # Unpublish withdraws one of the caller org's listings from the marketplace and
    # answers 204. Only the publishing org can remove its own listing; an id that is
    # unknown, or belongs to another org, is the same 404, so a probe learns nothing
    # about what exists. Removing a listing removes its price from per-call enforcement;
    # it does not uninstall the tool for anyone who already installed it.
    delete_marketplace_listings_by_id(req: listingRef)
    # Discover lists every tool and agent the caller can reach in their own org and
    # project, enriched with any public listing's title, category and price, and with
    # installed=true on the ones already activated for that scope. It is the shop
    # window: one read that answers what exists, what it costs and what is already on.
    get_marketplace() returns (rep: marketCatalog)
    # Returns the listings the caller's own org has published — what this
    # org is offering, not what it can buy. A publisher only ever sees its own rows.
    get_marketplace_listings() returns (rep: listingPage)
    # Install activates one tool for the caller's own org and project. A marketplace
    # install IS the tool plane's activation write — one store, one truth — so an
    # installed capability is immediately dispatchable and a monetized one is priced
    # from its listing at every call. The tool must resolve in the caller's scope, so
    # installing something that does not exist is refused rather than recorded.
    post_marketplace_install(req: installReq) returns (rep: installState)
    # Publish offers one tool on the marketplace, optionally monetized. The tool must
    # already resolve in the publisher's own scope, so a listing can never advertise a
    # capability that does not exist; a listing with a price must name the payout wallet
    # the x402 client settles to, so a monetized offer is never unpayable. The price is
    # exact to 18 decimal places, so a per-call price below a cent is a real price and
    # not a rounded-away zero. The listing is owned by the publishing org, paid into a
    # wallet of that same org, and answers 201 with the created row.
    post_marketplace_listings(req: publishReq) returns (rep: Listing)
    # Uninstall deactivates one tool for the caller's own org and project, so it stops
    # being dispatchable there. It is the exact inverse of install and touches the same
    # activation record; deactivating something that was never active is not an error.
    # The listing itself is untouched — this withdraws the caller's use of a capability,
    # not anyone's offer of it.
    post_marketplace_uninstall(req: installReq) returns (rep: installState)
}

# ---------------------------------------------------------------------
# 6 op(s) here. What follows is what this schema does not carry.
#
# dropped (1) — the value does not cross, and nothing fails:
#   Listing.Price  money.Amount  (empty message)
#
# opaque (3) — crosses, arrives without its name:
#   Listing.Price  money.Amount
#   listingPage.Listings  marketplace.Listing (list element)
#   marketCatalog.Items  marketplace.marketItem (list element)
