// Copyright © 2026 Hanzo AI. MIT License.

package commerce

// THE STORE'S ORDERS AND CARTS, listed for the team that sells.
//
// A shopper's cart becomes an order at checkout (/v1/commerce/store/:storeid/charge),
// and a cart that never did is a basket left behind. Both lived in the org's own store
// with no address that lists them, so the CRM could say who a customer was and nothing
// of what they bought or left. These two reads are that address: GET /v1/commerce/order
// and GET /v1/commerce/cart, each the org's own rows, newest first, narrowed to one
// buyer's by address.
//
// THE CALLER RUNS THE STORE. A list names every buyer of the org, so it is an admin of
// the org's, or SuperAdmin's: a member of a brand org, where everyone who signed up is
// one, is refused. The org is the validated principal's and never the query's, so
// another tenant's rows are not there to be found.
//
// MONEY IS CARRIED, NEVER DERIVED. Each amount is the stored row's, in cents, and the
// revenue is what the orders were paid less what was refunded, summed in each currency
// over EVERY order the query matches, not only the page, so a total does not move with
// the page size. These ops read and write no ledger.

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	commercedatastore "github.com/hanzoai/commerce/datastore"
	ordermodel "github.com/hanzoai/commerce/models/order"
	"github.com/hanzoai/commerce/models/organization"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
)

const (
	// pageDefault is how many rows a list answers when it is not told, and pageMax the
	// most it will: one response does not grow with the store.
	pageDefault = 50
	pageMax     = 200
)

// OrderQuery narrows the orders listed.
type OrderQuery struct {
	// Email is one buyer's address; empty lists every buyer's orders.
	Email string `json:"email,omitempty"`
	// Limit is how many orders to answer, newest first; absent is 50, and at most 200.
	Limit int `json:"limit,omitempty"`
}

// Order is one sale the org's store made.
type Order struct {
	// ID is the order's id.
	ID string `json:"id"`
	// Number is the order's number in the org's store.
	Number int `json:"number,omitempty"`
	// Email is the buyer's address.
	Email string `json:"email,omitempty"`
	// User is the signed-in buyer, empty for a guest.
	User string `json:"user,omitempty"`
	// Store is the storefront it was sold from.
	Store string `json:"store,omitempty"`
	// Status is where the order stands: its fulfilment.
	Status string `json:"status"`
	// PaymentStatus is where its payment stands: paid, refunded and the rest.
	PaymentStatus string `json:"paymentStatus"`
	// Currency is the ISO 4217 code every amount is in, lower-cased.
	Currency string `json:"currency"`
	// Items are what was bought, as the order's lines hold them.
	Items []CartItem `json:"items"`
	// TotalCents is what the buyer owed, in whole cents.
	TotalCents int64 `json:"totalCents"`
	// PaidCents is what has been paid of it, in whole cents.
	PaidCents int64 `json:"paidCents"`
	// RefundedCents is what has been paid back, in whole cents.
	RefundedCents int64 `json:"refundedCents"`
	// CreatedAt is when it was placed, RFC 3339.
	CreatedAt string `json:"createdAt,omitempty"`
}

// Sum is an amount in one currency.
type Sum struct {
	// Currency is the ISO 4217 code, lower-cased.
	Currency string `json:"currency"`
	// Cents is the amount in whole cents.
	Cents int64 `json:"cents"`
}

// Orders is a page of orders and what every order the query matched came to.
type Orders struct {
	// Orders is the page, newest first.
	Orders []Order `json:"orders"`
	// Count is how many orders the query matched, whatever the page holds.
	Count int `json:"count"`
	// Revenue is what the matched orders were paid less what was refunded, by currency.
	Revenue []Sum `json:"revenue"`
}

// orderOps binds the order ops to the subsystem, for the reason cartOps is.
type orderOps struct{}

// exposeOrder publishes the order list. Mount calls it.
func exposeOrder(app *zip.App) {
	o := orderOps{}
	app.Get("/v1/commerce/order", o.list,
		zip.WithOperationID("listOrders"),
		zip.WithSummary("List the org's orders, newest first, with what they came to"),
		zip.WithTags("order"))
}

// Lists the org's orders, newest first, with the count and the revenue of every order
// the query matched.
//
// Name one buyer by address to read what they bought; name none to read the org's
// sales. The address is matched as commerce stores it, lower-cased. The page holds at
// most 200 orders, and the count and the revenue are taken over all of them, so a
// total is the same at any page size.
//
// Only an admin of the org, or SuperAdmin, reads: the list names every buyer. The org
// is the caller's, taken from the validated principal, so another tenant's orders
// are not there to be found.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (orderOps) list(ctx context.Context, in *OrderQuery) (*Orders, error) {
	org, found, err := stewarded(ctx, "list orders")
	if err != nil {
		return nil, err
	}
	out := &Orders{Orders: []Order{}, Revenue: []Sum{}}
	if !found {
		return out, nil
	}
	q := ordermodel.Query(commercedatastore.New(org.Namespaced(ctx))).Order("-CreatedAt")
	if email := strings.ToLower(strings.TrimSpace(in.Email)); email != "" {
		q = q.Filter("Email=", email)
	}
	var rows []*ordermodel.Order
	if _, err := q.GetAll(&rows); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list orders: %v", err)
	}
	limit := pageOf(in.Limit)
	revenue := map[string]int64{}
	for i, r := range rows {
		revenue[string(r.Currency)] += int64(r.Paid - r.Refunded)
		if i < limit {
			out.Orders = append(out.Orders, orderAnswer(r))
		}
	}
	out.Count = len(rows)
	for currency, cents := range revenue {
		out.Revenue = append(out.Revenue, Sum{Currency: currency, Cents: cents})
	}
	slices.SortFunc(out.Revenue, func(a, b Sum) int { return cmp.Compare(a.Currency, b.Currency) })
	return out, nil
}

// stewarded is the caller's org, for a caller who runs its store: an admin of it or
// SuperAdmin. found is false when commerce holds no org of that name, which a read
// answers as empty rather than creating one.
func stewarded(ctx context.Context, op string) (*organization.Organization, bool, error) {
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return nil, false, cloud.Admin.Refusal()
	}
	return existingOrgOf(ctx, op)
}

// pageOf bounds a caller's page size: absent or non-positive is the default, and
// nothing above the maximum is honoured.
func pageOf(n int) int {
	if n <= 0 {
		return pageDefault
	}
	return min(n, pageMax)
}

// orderAnswer projects the stored order onto the answer shape, its lines through the
// one projection a cart's lines answer through.
func orderAnswer(o *ordermodel.Order) Order {
	out := Order{
		ID:            o.Id(),
		Number:        o.Number,
		Email:         o.Email,
		User:          o.UserId,
		Store:         o.StoreId,
		Status:        string(o.Status),
		PaymentStatus: string(o.PaymentStatus),
		Currency:      string(o.Currency),
		Items:         make([]CartItem, 0, len(o.Items)),
		TotalCents:    int64(o.Total),
		PaidCents:     int64(o.Paid),
		RefundedCents: int64(o.Refunded),
	}
	for _, li := range o.Items {
		out.Items = append(out.Items, cartLine(li))
	}
	if t := o.GetCreatedAt(); !t.IsZero() {
		out.CreatedAt = t.UTC().Format(time.RFC3339)
	}
	return out
}
