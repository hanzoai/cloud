package destinations

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/analytics"
)

// TestTranslateStandardMapping verifies the canonical EVENTS vocabulary maps onto the
// normalized StandardEvent taxonomy the adapters render — the ONE interlingua.
func TestTranslateStandardMapping(t *testing.T) {
	cases := map[string]StandardEvent{
		"$pageview":        EventPageView,
		"pricing_viewed":   EventViewContent,
		"plan_clicked":     EventLead,
		"signup_completed": EventSignUp,
		"checkout_started": EventStartCheckout,
		"order_completed":  EventPurchase,
		"feature_used":     EventCustom, // unmapped ⇒ custom, forwarded raw
		"chat_started":     EventCustom,
	}
	for name, want := range cases {
		got := Translate(analytics.SinkEvent{Name: name}).Standard
		if got != want {
			t.Errorf("Translate(%q).Standard = %q, want %q", name, got, want)
		}
	}
}

// TestTranslateCommerce verifies revenue/currency lift from first-class fields and
// from properties, and the USD default.
func TestTranslateCommerce(t *testing.T) {
	// First-class fields win.
	cv := Translate(analytics.SinkEvent{Name: "order_completed", Revenue: 49, Currency: "usd"})
	if cv.Value != 49 || cv.Currency != "USD" {
		t.Fatalf("first-class commerce: got value=%v currency=%q", cv.Value, cv.Currency)
	}
	// Fall back to properties.
	cv = Translate(analytics.SinkEvent{Name: "order_completed", Properties: map[string]any{"value": 12.5, "currency": "eur"}})
	if cv.Value != 12.5 || cv.Currency != "EUR" {
		t.Fatalf("property commerce: got value=%v currency=%q", cv.Value, cv.Currency)
	}
	// No currency ⇒ USD default; no value ⇒ 0.
	cv = Translate(analytics.SinkEvent{Name: "$pageview"})
	if cv.Value != 0 || cv.Currency != "USD" {
		t.Fatalf("default commerce: got value=%v currency=%q", cv.Value, cv.Currency)
	}
}

// TestTranslateEcommerce verifies the ecommerce vocabulary (both the @hanzo/event
// canonical names and the schema.org/GA4 action names) maps onto the commerce standard
// events, and that line items lift from a properties array or a first-class product id.
func TestTranslateEcommerce(t *testing.T) {
	names := map[string]StandardEvent{
		"product_viewed":   EventViewContent,
		"product_view":     EventViewContent,
		"product_added":    EventAddToCart,
		"add_to_cart":      EventAddToCart,
		"checkout_started": EventStartCheckout,
		"begin_checkout":   EventStartCheckout,
		"order_completed":  EventPurchase,
		"purchase":         EventPurchase,
	}
	for name, want := range names {
		if got := Translate(analytics.SinkEvent{Name: name}).Standard; got != want {
			t.Errorf("Translate(%q).Standard = %q, want %q", name, got, want)
		}
	}
	// Items lift from a properties array, tolerating GA4/Segment/schema.org keys.
	cv := Translate(analytics.SinkEvent{
		Name: "order_completed",
		Properties: map[string]any{
			"items": []any{
				map[string]any{"item_id": "SKU1", "item_name": "Widget", "price": 24.99, "quantity": float64(2)},
				map[string]any{"sku": "SKU2", "name": "Gadget", "item_price": 10.0},
			},
		},
	})
	if len(cv.Items) != 2 {
		t.Fatalf("want 2 items, got %d (%+v)", len(cv.Items), cv.Items)
	}
	if cv.Items[0].ID != "SKU1" || cv.Items[0].Name != "Widget" || cv.Items[0].Price != 24.99 || cv.Items[0].Quantity != 2 {
		t.Errorf("item0: %+v", cv.Items[0])
	}
	if cv.Items[1].ID != "SKU2" || cv.Items[1].Name != "Gadget" || cv.Items[1].Price != 10.0 {
		t.Errorf("item1: %+v", cv.Items[1])
	}
	// A first-class product id synthesizes a single item when no array is present.
	cv2 := Translate(analytics.SinkEvent{Name: "product_added", ProductID: "SKU9", Quantity: 3})
	if len(cv2.Items) != 1 || cv2.Items[0].ID != "SKU9" || cv2.Items[0].Quantity != 3 {
		t.Fatalf("synthesized item: %+v", cv2.Items)
	}
	// No commerce data ⇒ no items (a non-commerce event carries none).
	if items := Translate(analytics.SinkEvent{Name: "$pageview"}).Items; items != nil {
		t.Errorf("pageview items = %+v, want nil", items)
	}
}

// TestTranslateLiftUser verifies the match-key set is lifted from the raw properties
// and the distinct id — the keys the adapters hash before send.
func TestTranslateLiftUser(t *testing.T) {
	cv := Translate(analytics.SinkEvent{
		Name:       "signup_completed",
		DistinctID: "user-123",
		Time:       time.Unix(1700000000, 0),
		MessageID:  "msg-1",
		Properties: map[string]any{
			"email":     "Alice@Example.com",
			"phone":     "+1 (555) 123-4567",
			"ip":        "203.0.113.9",
			"userAgent": "Mozilla/5.0",
			"fbclid":    "abc123",
			"gclid":     "g999",
			"unrelated": "kept-in-properties",
		},
	})
	u := cv.User
	if u.ExternalID != "user-123" {
		t.Errorf("externalID = %q", u.ExternalID)
	}
	if u.Email != "Alice@Example.com" {
		t.Errorf("email = %q (should be lifted RAW; adapters hash)", u.Email)
	}
	if u.Phone != "+1 (555) 123-4567" {
		t.Errorf("phone = %q", u.Phone)
	}
	if u.IP != "203.0.113.9" || u.UserAgent != "Mozilla/5.0" {
		t.Errorf("ip/ua = %q / %q", u.IP, u.UserAgent)
	}
	if u.click("fbclid") != "abc123" || u.click("gclid") != "g999" {
		t.Errorf("clicks = %+v", u.Clicks)
	}
	if cv.EventID != "msg-1" {
		t.Errorf("eventID = %q", cv.EventID)
	}
	// Raw properties are carried through for custom_data.
	if cv.Properties["unrelated"] != "kept-in-properties" {
		t.Errorf("properties not carried through: %+v", cv.Properties)
	}
}

// TestTranslateAServerSidePurchase is the last link of the commerce bridge, read
// where the conversion actually leaves: a sale apps/commerce stated when a card
// SETTLED, carried over the event plane and handed here by the fan-out.
//
// The browser pixel was the only Purchase these platforms ever received — fired from
// a page, blocked by every content blocker, lost on every navigation away. This one
// is the payment itself, so all three facts a platform bids on have to survive the
// trip: WHAT it was worth, in WHICH money, and WHICH sale it was.
//
// The last of those is the dedup key. Every adapter renders EventID as the platform's
// own order identity (Meta's order_id, GA4's transaction_id, Pinterest's order_id),
// and apps/commerce states the SETTLEMENT's reference there — so the browser pixel
// and this server event collapse onto one conversion, and a settlement replayed by a
// webhook does not report the sale twice.
func TestTranslateAServerSidePurchase(t *testing.T) {
	cv := Translate(analytics.SinkEvent{
		Name:       "order_completed",
		MessageID:  "minted-per-call",
		DistinctID: "person_42",
		Revenue:    49.5,
		Currency:   "eur",
		ProductID:  "plan_pro",
		Quantity:   2,
		Properties: map[string]any{"event_id": "sq_pay_9Xk2"},
	})
	switch {
	case cv.Standard != EventPurchase:
		t.Errorf("standard %q, want %q — a settled payment is a Purchase to every platform",
			cv.Standard, EventPurchase)
	case cv.Value != 49.5:
		t.Errorf("value %v, want 49.5 — the money the card was charged", cv.Value)
	case cv.Currency != "EUR":
		t.Errorf("currency %q, want EUR", cv.Currency)
	case cv.EventID != "sq_pay_9Xk2":
		t.Errorf("eventID = %q, want the settlement reference — the minted message id would "+
			"give the browser pixel and this event two identities for one sale", cv.EventID)
	case cv.User.ExternalID != "person_42":
		t.Errorf("externalID = %q, want person_42", cv.User.ExternalID)
	}
	if len(cv.Items) != 1 || cv.Items[0].ID != "plan_pro" || cv.Items[0].Quantity != 2 {
		t.Errorf("contents = %+v, want one plan_pro x2 — what the platforms report the sale against", cv.Items)
	}
}
