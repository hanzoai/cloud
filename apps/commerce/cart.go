// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// THE CART, as typed ops — the shopper's basket, finally addressable.
//
// A cart is where a sale begins: items accumulate, totals are tallied, and the
// checkout family (/v1/commerce/store/:storeid/authorize, /capture, /charge) turns one into
// an order. Every one of those checkout addresses has been served by this binary
// for as long as it has existed. The cart they operate on could not be created,
// read or amended through it — hanzoai/commerce implements the whole noun
// (models/cart, api/cart) and cloud simply never mounted the route table that
// carries it, so the first step of the flow had no endpoint while the last three did.
//
// THIS IS THE MISSING STEP, not a new one. The rules live where they already
// lived: cart.Cart.SetItem resolves a product or a variant into a line item,
// updates a quantity in place, and removes the line when the quantity is zero.
// These ops call THAT method — they do not restate it. What is added here is the
// only thing that was actually missing: an address, a declared input, a declared
// answer and the prose that makes all three legible to an SDK, an MCP tool and a
// CLI command.
//
// ONE WAY TO CHANGE A LINE. There is no DELETE beside the set: quantity zero
// removes the line, because that is what the module's SetItem does and a second
// spelling of one act is a second set of edge cases. Setting a quantity on an item
// that is not in the cart adds it; setting zero on one that is not there is a
// no-op, not an error.
//
// IDENTITY IS NOT AN INPUT. The org is read from the validated principal parked on
// the context by cloud.Bridge, exactly as the payment ops read it, so a cart is
// always created, found and amended inside the caller's OWN namespaced store. A
// cart id belonging to another tenant is not found there — 404, never 403, so the
// id space cannot be probed.
//
// WHAT THIS SURFACE DOES NOT DO, stated because the standalone one does: commerce's
// own api/cart handlers mirror every write into Mailchimp's abandoned-cart feed
// when the org has that integration configured. That is a storefront-marketing
// side effect of the standalone deployment, not part of what a cart IS, and it is
// deliberately absent here rather than reimplemented — a second copy of an
// integration is a second thing to drift.

import (
	"context"
	"net/http"
	"strings"
	"time"

	commercedatastore "github.com/hanzoai/commerce/datastore"
	cartmodel "github.com/hanzoai/commerce/models/cart"
	"github.com/hanzoai/commerce/models/lineitem"
	"github.com/hanzoai/commerce/models/types/currency"
	"github.com/zap-proto/zip"
)

// CartOpen is a new cart to open. Every field is optional: a cart with nothing on
// it is a legitimate empty basket, and the fields below only pre-fill what the
// caller already knows about the shopper.
type CartOpen struct {
	// Email is the shopper's address, for a cart that belongs to someone who has
	// not signed in. It is what a guest checkout and an abandoned-cart follow-up
	// key on. Empty is fine.
	Email string `json:"email,omitempty" url:"-"`
	// User is the id of the signed-in shopper this cart belongs to, when there is
	// one. Empty means a guest cart identified only by its own id.
	User string `json:"user,omitempty" url:"-"`
	// Store is the storefront this cart is being filled on. Empty uses the org's
	// default store, which is what a single-storefront merchant always wants.
	Store string `json:"store,omitempty" url:"-"`
	// Currency is the ISO 4217 code the cart is priced in, lower-cased. Empty
	// means usd.
	Currency string `json:"currency,omitempty" url:"-"`
}

// CartRef names one cart to read or discard. The id is the path segment: the URL
// is the addressing authority, so it binds from there whatever else arrives.
type CartRef struct {
	// ID is the cart's id, as the open call answered it.
	ID string `json:"id"`
}

// CartItemSet sets one line's quantity. It is the whole vocabulary for changing a
// cart's contents — add, change and remove are the same act at three quantities.
type CartItemSet struct {
	// ID is the cart to amend, from the path.
	ID string `json:"id"`
	// Product names the catalog product to set, by its id or its URL slug. Give
	// this or Variant, never both; a request naming neither is refused.
	Product string `json:"product,omitempty" url:"-"`
	// Variant names the specific sellable variant to set, by its id or its SKU.
	// Prefer it over Product for anything sold in sizes, colours or tiers — the
	// price and the stock are the variant's, not the product's.
	Variant string `json:"variant,omitempty" url:"-"`
	// Quantity is how many of that item the cart should hold AFTER this call — it
	// is the resulting count, not a delta, so sending 3 twice leaves 3 and not 6.
	// ZERO REMOVES the line, which is the only way to take an item out.
	Quantity int `json:"quantity" url:"-"`
}

// CartItem is one line of a cart, as the cart holds it.
type CartItem struct {
	// ID is the line's identity — the variant id when the line is a variant,
	// otherwise the product id. It is what a subsequent set call addresses.
	ID string `json:"id"`
	// Kind is "variant" when this line is a specific sellable variant and
	// "product" when it is the product itself.
	Kind string `json:"kind"`
	// Name is the item's display name, cached onto the line when it was added so
	// a cart renders without a second read.
	Name string `json:"name,omitempty"`
	// SKU is the line's stock-keeping unit — the variant's when it has one,
	// otherwise the product's. Empty when neither carries one.
	SKU string `json:"sku,omitempty"`
	// Quantity is how many units of this item the cart holds.
	Quantity int `json:"quantity"`
	// PriceCents is the unit price in whole cents, cached at the moment the line
	// was added. The line's contribution to the cart is this times Quantity.
	PriceCents int64 `json:"priceCents"`
	// Free reports a line that costs nothing because a coupon or a promotion made
	// it so, rather than because its price is zero.
	Free bool `json:"free,omitempty"`
}

// Cart is a shopper's basket: what is in it, and what it comes to.
type Cart struct {
	// ID is the cart's id — what every other cart op addresses it by, and what a
	// storefront persists against the browser session.
	ID string `json:"id"`
	// Status is "active" for a cart still being filled, "ordered" once checkout
	// turned it into an order, and "discarded" when the shopper abandoned it.
	Status string `json:"status"`
	// Currency is the ISO 4217 code every amount below is denominated in.
	Currency string `json:"currency"`
	// Email is the shopper's address, when the cart carries one.
	Email string `json:"email,omitempty"`
	// User is the signed-in shopper this cart belongs to, empty for a guest cart.
	User string `json:"user,omitempty"`
	// Store is the storefront the cart is being filled on.
	Store string `json:"store,omitempty"`
	// Order is the order this cart became, once checkout completed it. Empty
	// until then, and its presence is what makes a cart final.
	Order string `json:"order,omitempty"`
	// Items are the cart's lines, in the order they were added.
	Items []CartItem `json:"items"`
	// LineTotalCents is the sum of the lines before any discount, in whole cents.
	LineTotalCents int64 `json:"lineTotalCents"`
	// DiscountCents is what coupons and promotions took off, in whole cents.
	DiscountCents int64 `json:"discountCents"`
	// SubtotalCents is LineTotalCents less DiscountCents, in whole cents.
	SubtotalCents int64 `json:"subtotalCents"`
	// ShippingCents is the shipping charge, in whole cents. It stays zero until a
	// shipping option is priced at checkout.
	ShippingCents int64 `json:"shippingCents"`
	// TaxCents is the sales tax, in whole cents. It stays zero until checkout
	// resolves the shopper's tax region.
	TaxCents int64 `json:"taxCents"`
	// TotalCents is what the shopper pays: subtotal plus shipping plus tax, in
	// whole cents.
	TotalCents int64 `json:"totalCents"`
	// CreatedAt is when the cart was opened, RFC3339.
	CreatedAt string `json:"createdAt,omitempty"`
	// UpdatedAt is when the cart was last amended, RFC3339.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// cartOps binds the cart ops to the subsystem. A zip.TypedHandler is
// func(context.Context, *In) (*Out, error) — there is no parameter for the
// subsystem — so every op is a method value, which is also the only bound form
// cmd/zipdoc can lift prose from.
type cartOps struct{}

// exposeCart publishes the cart surface. Mount calls it.
//
// Registered on the APP with whole paths rather than on a group: the surface root
// IS /v1/commerce/cart, and Group("/v1/commerce/cart") composed with an empty leaf yields
// "/v1/commerce/cart/" — a different address from the one a storefront will POST to. One
// registrar for all four keeps every op's published path exactly the path the
// router matches.
//
// No middleware rides these registrations. The app-wide cloud.Bridge (serve.go)
// parks the validated org each handler reads, and a cart moves no money, so there
// is no credit screen to compose here — the risk gate belongs on the acts that
// mint, which is why it is on the payment endpoint and not on this one.
func exposeCart(app *zip.App) {
	o := cartOps{}
	zip.Post(app, "/v1/commerce/cart", o.open,
		zip.WithOperationID("openCart"),
		zip.WithSummary("Open a cart for a shopper to fill"),
		zip.WithTags("cart"),
		zip.WithStatus(http.StatusCreated))
	zip.Get(app, "/v1/commerce/cart/:id", o.read,
		zip.WithOperationID("getCart"),
		zip.WithSummary("Read one cart with its lines and totals"),
		zip.WithTags("cart"))
	zip.Post(app, "/v1/commerce/cart/:id/item", o.set,
		zip.WithOperationID("setCartItem"),
		zip.WithSummary("Set one item's quantity in a cart; zero removes it"),
		zip.WithTags("cart"))
	zip.Post(app, "/v1/commerce/cart/:id/discard", o.discard,
		zip.WithOperationID("discardCart"),
		zip.WithSummary("Discard a cart the shopper abandoned"),
		zip.WithTags("cart"))
}

// Opens an empty cart for a shopper to fill, and answers it with its new id.
//
// This is the first step of a sale: hold the id, add items to it with
// setCartItem, then hand it to checkout. Every field of the request is optional —
// an empty body opens a perfectly good anonymous cart — and the fields exist only
// to pre-fill what is already known about the shopper.
//
// The STORE defaults to the org's own default storefront, so a merchant selling
// through one storefront never has to name it. The CURRENCY defaults to usd; note
// that checkout overrides it with the store's own currency when the sale is
// authorized, so a currency set here is a hint rather than a commitment.
//
// The cart is created in the CALLER'S OWN org namespace, taken from the validated
// principal and never from the body, so a cart can never be opened on another
// tenant's books.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (cartOps) open(ctx context.Context, in *CartOpen) (*Cart, error) {
	org, err := payingOrg(ctx, "open cart")
	if err != nil {
		return nil, err
	}
	db := commercedatastore.New(org.Namespaced(ctx))
	c := cartmodel.New(db)
	c.Status = cartmodel.Active
	c.Email = strings.TrimSpace(in.Email)
	c.UserId = strings.TrimSpace(in.User)
	c.Currency = normalizeCartCurrency(in.Currency)
	c.StoreId = strings.TrimSpace(in.Store)
	if c.StoreId == "" {
		c.StoreId = org.DefaultStore
	}
	if err := c.Create(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "open cart: %v", err)
	}
	return cartAnswer(c), nil
}

// Reads one cart: its lines, its status and what it comes to.
//
// This is what a storefront calls to render the basket, and what a support agent
// calls to see what a shopper is looking at. The totals are the cart's STORED
// tally — shipping and tax stay zero until checkout resolves a shipping option
// and a tax region, so a cart total before checkout is the merchandise total and
// is meant to be.
//
// The org scopes the read by construction: the store is namespaced to it, so a
// cart id belonging to another tenant is simply not found rather than found and
// then filtered, and answers 404 rather than 403 so the id space cannot be probed.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (cartOps) read(ctx context.Context, in *CartRef) (*Cart, error) {
	c, err := loadCart(ctx, in.ID, "read cart")
	if err != nil {
		return nil, err
	}
	return cartAnswer(c), nil
}

// Sets how many of one item a cart holds, and answers the whole updated cart.
//
// This is the ONE way a cart's contents change. The quantity is the RESULT, not a
// delta: sending 3 leaves 3 however many were there before, so a retry is safe and
// a double-submit cannot double an order. ZERO REMOVES the line — there is
// deliberately no separate delete, because removal is the same act at the boundary
// value and a second spelling would be a second set of edge cases.
//
// Name the item with EITHER product OR variant, never both. Prefer variant for
// anything sold in sizes, colours or tiers: the price and the stock belong to the
// variant, so a product-level line on a varianted product prices the wrong thing.
// Either may be given as an id or as the human key — a product's URL slug, a
// variant's SKU — which is what lets a storefront add to cart straight from a
// product page URL without a lookup first.
//
// The item's price and name are CACHED onto the line as it is added, so the cart
// keeps the price the shopper was shown even if the catalog moves underneath it.
//
// An item that resolves to nothing in the catalog is refused 400 and the cart is
// left exactly as it was; nothing is partially applied.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (cartOps) set(ctx context.Context, in *CartItemSet) (*Cart, error) {
	c, err := loadCart(ctx, in.ID, "set cart item")
	if err != nil {
		return nil, err
	}
	product := strings.TrimSpace(in.Product)
	variant := strings.TrimSpace(in.Variant)
	switch {
	case product == "" && variant == "":
		return nil, zip.ErrBadRequest("name the item to set: give product or variant")
	case product != "" && variant != "":
		return nil, zip.ErrBadRequest("name product or variant, not both")
	}
	if in.Quantity < 0 {
		return nil, zip.ErrBadRequest("quantity must be zero or more; zero removes the line")
	}
	// The module's OWN method, not a reimplementation of it: SetItem resolves the
	// catalog item, caches its price and name onto a new line, updates an existing
	// line's quantity in place, and removes the line when the quantity is zero.
	kind, id := "product", product
	if variant != "" {
		kind, id = "variant", variant
	}
	if err := c.SetItem(c.Datastore(), id, kind, in.Quantity); err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "set cart item: %v", err)
	}
	if err := c.Update(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "set cart item: %v", err)
	}
	return cartAnswer(c), nil
}

// Discards a cart the shopper abandoned, and answers it in its final state.
//
// A discarded cart is CLOSED, not deleted: the row stays, so abandoned-basket
// reporting and any follow-up that keys on it still have something to read. It
// stops being a cart anything will check out, which is the point — it is how a
// storefront says "this basket is over" without destroying the evidence that it
// existed.
//
// Discarding is idempotent: a cart already discarded answers its stored state
// rather than failing, so a retry is safe.
//
// The cart is resolved inside the caller's own org namespace, so another tenant's
// id answers 404.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (cartOps) discard(ctx context.Context, in *CartRef) (*Cart, error) {
	c, err := loadCart(ctx, in.ID, "discard cart")
	if err != nil {
		return nil, err
	}
	if c.Status == cartmodel.Discarded {
		return cartAnswer(c), nil
	}
	c.Status = cartmodel.Discarded
	if err := c.Update(); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "discard cart: %v", err)
	}
	return cartAnswer(c), nil
}

// loadCart resolves the caller's org and loads one cart inside it. It is the ONE
// place a cart id becomes a cart, so every op scopes its read the same way and no
// handler can be the one that forgets the namespace.
func loadCart(ctx context.Context, id, op string) (*cartmodel.Cart, error) {
	org, err := payingOrg(ctx, op)
	if err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, zip.ErrBadRequest(op + ": cart id is required")
	}
	db := commercedatastore.New(org.Namespaced(ctx))
	c := cartmodel.New(db)
	if err := c.GetById(id); err != nil {
		return nil, zip.ErrNotFound("cart not found")
	}
	return c, nil
}

// normalizeCartCurrency is the ONE currency default on this surface: empty means
// usd, and a code is always lower-cased, so two carts opened with "USD" and "usd"
// are denominated identically rather than in two currencies that render the same.
func normalizeCartCurrency(code string) currency.Type {
	cur := currency.Type(strings.ToLower(strings.TrimSpace(code)))
	if cur == "" {
		return currency.USD
	}
	return cur
}

// cartAnswer projects the stored cart onto the answer shape. It is the SINGLE
// projection every cart op answers through, so the four addresses cannot disagree
// about what a cart looks like.
func cartAnswer(c *cartmodel.Cart) *Cart {
	out := &Cart{
		ID:             c.Id(),
		Status:         string(c.Status),
		Currency:       string(c.Currency),
		Email:          c.Email,
		User:           c.UserId,
		Store:          c.StoreId,
		Order:          c.OrderId,
		Items:          make([]CartItem, 0, len(c.Items)),
		LineTotalCents: int64(c.LineTotal),
		DiscountCents:  int64(c.Discount),
		SubtotalCents:  int64(c.Subtotal),
		ShippingCents:  int64(c.Shipping),
		TaxCents:       int64(c.Tax),
		TotalCents:     int64(c.Total),
	}
	for _, li := range c.Items {
		out.Items = append(out.Items, cartLine(li))
	}
	if t := c.GetCreatedAt(); !t.IsZero() {
		out.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	if t := c.GetUpdatedAt(); !t.IsZero() {
		out.UpdatedAt = t.UTC().Format(time.RFC3339)
	}
	return out
}

// cartLine projects one stored line item. The line's identity and SKU follow the
// model's own rule — the variant's when it is a variant line, the product's
// otherwise — rather than restating it, so an id answered here is an id setCartItem
// accepts back.
func cartLine(li lineitem.LineItem) CartItem {
	item := CartItem{
		ID:         li.Id(),
		Kind:       "product",
		Name:       li.ProductName,
		SKU:        li.SKU(),
		Quantity:   li.Quantity,
		PriceCents: int64(li.Price),
		Free:       li.Free,
	}
	if li.VariantId != "" {
		item.Kind = "variant"
		if li.VariantName != "" {
			item.Name = li.VariantName
		}
	}
	return item
}

// cartPrefix is the subtree the cart ops own. It is declared in Prefixes
// (mount.go) and, by hand, in manifest/apps.go — the light host must not import an
// app package, so the two copies are kept equal deliberately.
const cartPrefix = "/v1/commerce/cart"
