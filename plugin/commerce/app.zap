# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package commerce

struct Cart {
    ID             text        @0
    Status         text        @8
    Currency       text        @16
    Email          text        @24
    User           text        @32
    Store          text        @40
    Order          text        @48
    Items          list<bytes> @56
    LineTotalCents i64         @64
    DiscountCents  i64         @72
    SubtotalCents  i64         @80
    ShippingCents  i64         @88
    TaxCents       i64         @96
    TotalCents     i64         @104
    CreatedAt      text        @112
    UpdatedAt      text        @120
}

struct CartItemSet {
    ID       text @0
    Product  text @8
    Variant  text @16
    Quantity i64  @24
}

struct CartOpen {
    Email    text @0
    User     text @8
    Store    text @16
    Currency text @24
}

struct CartRef {
    ID text @0
}

struct liveness {
    Service text @0
    Status  text @8
}

interface commerce {
    # Discards a cart the shopper abandoned, and answers it in its final state.
    # A discarded cart is CLOSED, not deleted: the row stays, so abandoned-basket
    # reporting and any follow-up that keys on it still have something to read. It
    # stops being a cart anything will check out, which is the point — it is how a
    # storefront says "this basket is over" without destroying the evidence that it
    # existed.
    # Discarding is idempotent: a cart already discarded answers its stored state
    # rather than failing, so a retry is safe.
    # The cart is resolved inside the caller's own org namespace, so another tenant's
    # id answers 404.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    discardCart(req: CartRef) returns (rep: Cart)
    # Reads one cart: its lines, its status and what it comes to.
    # This is what a storefront calls to render the basket, and what a support agent
    # calls to see what a shopper is looking at. The totals are the cart's STORED
    # tally — shipping and tax stay zero until checkout resolves a shipping option
    # and a tax region, so a cart total before checkout is the merchandise total and
    # is meant to be.
    # The org scopes the read by construction: the store is namespaced to it, so a
    # cart id belonging to another tenant is simply not found rather than found and
    # then filtered, and answers 404 rather than 403 so the id space cannot be probed.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    getCart(req: CartRef) returns (rep: Cart)
    # Answers ok whenever the commerce subsystem is mounted. It is registered
    # before the module embed boots, so it keeps answering even when the embed
    # failed and every business route serves the fail-closed 503 — which is the
    # point: it reports that the process is reachable, never that the money plane
    # is healthy. Unauthenticated: a probe that needs a credential is a probe that
    # reports the credential.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    get_commerce_health() returns (rep: liveness)
    # Opens an empty cart for a shopper to fill, and answers it with its new id.
    # This is the first step of a sale: hold the id, add items to it with
    # setCartItem, then hand it to checkout. Every field of the request is optional —
    # an empty body opens a perfectly good anonymous cart — and the fields exist only
    # to pre-fill what is already known about the shopper.
    # The STORE defaults to the org's own default storefront, so a merchant selling
    # through one storefront never has to name it. The CURRENCY defaults to usd; note
    # that checkout overrides it with the store's own currency when the sale is
    # authorized, so a currency set here is a hint rather than a commitment.
    # The cart is created in the CALLER'S OWN org namespace, taken from the validated
    # principal and never from the body, so a cart can never be opened on another
    # tenant's books.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    openCart(req: CartOpen) returns (rep: Cart)
    # Sets how many of one item a cart holds, and answers the whole updated cart.
    # This is the ONE way a cart's contents change. The quantity is the RESULT, not a
    # delta: sending 3 leaves 3 however many were there before, so a retry is safe and
    # a double-submit cannot double an order. ZERO REMOVES the line — there is
    # deliberately no separate delete, because removal is the same act at the boundary
    # value and a second spelling would be a second set of edge cases.
    # Name the item with EITHER product OR variant, never both. Prefer variant for
    # anything sold in sizes, colours or tiers: the price and the stock belong to the
    # variant, so a product-level line on a varianted product prices the wrong thing.
    # Either may be given as an id or as the human key — a product's URL slug, a
    # variant's SKU — which is what lets a storefront add to cart straight from a
    # product page URL without a lookup first.
    # The item's price and name are CACHED onto the line as it is added, so the cart
    # keeps the price the shopper was shown even if the catalog moves underneath it.
    # An item that resolves to nothing in the catalog is refused 400 and the cart is
    # left exactly as it was; nothing is partially applied.
    # A named handler, not a closure, so zipdoc can lift this prose into the registry.
    setCartItem(req: CartItemSet) returns (rep: Cart)
}

# ---------------------------------------------------------------------
# 5 op(s) here. What follows is what this schema does not carry.
#
# opaque (1) — crosses, arrives without its name:
#   Cart.Items  commerce.CartItem (list element)
