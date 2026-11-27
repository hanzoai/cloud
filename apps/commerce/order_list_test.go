// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commercedatastore "github.com/hanzoai/commerce/datastore"
	cartmodel "github.com/hanzoai/commerce/models/cart"
	"github.com/hanzoai/commerce/models/lineitem"
	ordermodel "github.com/hanzoai/commerce/models/order"
	"github.com/hanzoai/commerce/models/types/currency"
	commerceorg "github.com/hanzoai/commerce/pkg/org"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// shop is a store with two orgs' sales in it, served as the routes serve them: each
// request is the person its headers name, acting in X-Org-Id. It answers the app and
// the org whose store is read; a name no other test has used, because commerce holds
// the store an org name resolves to for the life of the process.
func shop(t *testing.T) (*zip.App, string) {
	t.Helper()
	boot(t)
	ctx := context.Background()
	mine := fresh("shop")
	for _, name := range []string{mine, fresh("other")} {
		org, err := commerceorg.Resolve(ctx, name)
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		db := commercedatastore.New(org.Namespaced(ctx))
		sale := func(email string, paid, refunded currency.Cents) {
			r := ordermodel.New(db)
			r.Email, r.Currency, r.Status = email, currency.USD, ordermodel.Completed
			r.Items = []lineitem.LineItem{{ProductName: "Mug", Quantity: 2}}
			r.Total, r.Paid, r.Refunded = paid, paid, refunded
			if err := r.Create(); err != nil {
				t.Fatalf("order: %v", err)
			}
		}
		left := func(email string, status cartmodel.Status) {
			c := cartmodel.New(db)
			c.Email, c.Status, c.Currency = email, status, currency.USD
			if err := c.Create(); err != nil {
				t.Fatalf("cart: %v", err)
			}
		}
		sale("ada@example.com", 5000, 1000)
		sale("ada@example.com", 2500, 0)
		sale("grace@example.com", 700, 0)
		left("ada@example.com", cartmodel.Active)
		left("ada@example.com", cartmodel.Ordered)
		left("grace@example.com", cartmodel.Discarded)
	}
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), DisableStartupMessage: true})
	app.Use(zip.H(func(c *zip.Ctx) error {
		if org := c.Header("X-Org-Id"); org != "" {
			principal.Mint(c, principal.Principal{Org: org, User: "u", Subject: "u", Kind: principal.Person, Stable: true, Holder: org + "/u"})
		}
		return c.Continue()
	}))
	app.Use(zip.H(cloud.Bridge()))
	exposeCart(app)
	exposeOrder(app)
	return app, mine
}

// read GETs path as an admin of org, a member of it, or nobody, and decodes the answer.
func read(t *testing.T, app *zip.App, org, who, path string, out any) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	switch who {
	case "admin":
		r.Header.Set("X-User-Id", "u")
		r.Header.Set("X-Org-Id", org)
		r.Header.Set("X-User-IsOrgAdmin", "true")
	case "member":
		r.Header.Set("X-User-Id", "u")
		r.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("GET %s: %v: %s", path, err, body)
		}
	}
	return resp.StatusCode
}

// An org's orders are listed newest first with what they came to, and one buyer's are
// told by address: the count and the revenue (paid less refunded) cover every order the
// query matches, not the page, and another org's sales are not there.
func TestTheOrgsOrdersAreListedWithWhatTheyCameTo(t *testing.T) {
	app, org := shop(t)

	var all Orders
	if code := read(t, app, org, "admin", "/v1/commerce/order?limit=1", &all); code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	if all.Count != 3 || len(all.Orders) != 1 || len(all.Revenue) != 1 || all.Revenue[0] != (Sum{Currency: "usd", Cents: 4000 + 2500 + 700}) {
		t.Fatalf("the org's orders = %+v, want 3 of them, a page of 1, revenue 7200 usd", all)
	}

	var ada Orders
	if code := read(t, app, org, "admin", "/v1/commerce/order?email=Ada@Example.com", &ada); code != http.StatusOK {
		t.Fatalf("one buyer: %d", code)
	}
	if ada.Count != 2 || len(ada.Orders) != 2 || ada.Revenue[0].Cents != 6500 {
		t.Fatalf("ada's orders = %+v, want 2 worth 6500", ada)
	}
	o := ada.Orders[0]
	if o.Email != "ada@example.com" || o.Currency != "usd" || len(o.Items) != 1 || o.Items[0].Name != "Mug" || o.Items[0].Quantity != 2 || o.CreatedAt == "" || o.PaidCents != o.TotalCents {
		t.Errorf("an order = %+v, want what was bought and what it came to", o)
	}
	if ada.Orders[0].CreatedAt < ada.Orders[1].CreatedAt {
		t.Errorf("orders are not newest first: %s before %s", ada.Orders[0].CreatedAt, ada.Orders[1].CreatedAt)
	}
}

// Carts are listed the same way, and a cart is left when it never became an order.
func TestTheOrgsCartsAreListedAndCountedWhenLeft(t *testing.T) {
	app, org := shop(t)

	var all Carts
	if code := read(t, app, org, "admin", "/v1/commerce/cart", &all); code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	if all.Count != 3 || all.Left != 2 || len(all.Carts) != 3 {
		t.Fatalf("the org's carts = count %d left %d of %d, want 3 and 2", all.Count, all.Left, len(all.Carts))
	}
	var ada Carts
	read(t, app, org, "admin", "/v1/commerce/cart?email=ada@example.com", &ada)
	if ada.Count != 2 || ada.Left != 1 {
		t.Fatalf("ada's carts = count %d left %d, want 2 and 1", ada.Count, ada.Left)
	}
}

// A list names every buyer of the org, so a member of it and a request with no principal
// are refused; and an org commerce holds nothing of lists nothing, creating nothing.
func TestOnlyAnAdminReadsTheOrgsSales(t *testing.T) {
	app, org := shop(t)
	for _, path := range []string{"/v1/commerce/order", "/v1/commerce/cart"} {
		if code := read(t, app, org, "member", path, nil); code != http.StatusForbidden {
			t.Errorf("a member read %s: %d", path, code)
		}
		if code := read(t, app, org, "", path, nil); code < 400 {
			t.Errorf("a request with no principal read %s: %d", path, code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/commerce/order", nil)
	r.Header.Set("X-User-Id", "u")
	r.Header.Set("X-Org-Id", fresh("nobody"))
	r.Header.Set("X-User-IsOrgAdmin", "true")
	resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var none Orders
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &none) != nil || none.Count != 0 || len(none.Orders) != 0 {
		t.Fatalf("an org with no store: %d %s", resp.StatusCode, body)
	}
}
