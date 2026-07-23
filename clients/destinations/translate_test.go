package destinations

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/analytics"
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
