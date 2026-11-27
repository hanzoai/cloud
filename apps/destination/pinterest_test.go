package destination

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPinterestBuild(t *testing.T) {
	body := pinterestBuild([]Conversion{
		{
			Standard: EventPurchase, Name: "order_completed", Value: 20, Currency: "USD", EventID: "evt-9",
			URL: "https://shop.example/checkout", Time: time.Unix(1700000000, 0),
			User: UserData{Email: "  Bob@Example.COM ", Phone: "+1 (555) 000-1111", ExternalID: "u-42",
				IP: "203.0.113.5", UserAgent: "UA", Clicks: map[string]string{"epik": "ep1"}},
		},
		{Standard: EventCustom, Name: "feature_used", User: UserData{Email: "a@b.com"}},
	})
	if len(body.Data) != 2 {
		t.Fatalf("want 2 events, got %d", len(body.Data))
	}
	e := body.Data[0]
	if e.EventName != "checkout" || e.EventID != "evt-9" || e.ActionSource != "web" {
		t.Errorf("event: %+v", e)
	}
	if e.EventTime != 1700000000 {
		t.Errorf("event_time = %d, want 1700000000", e.EventTime)
	}
	// Email/phone/external id hashed (arrays); the epik click id + ip/ua ride un-hashed.
	if em, _ := e.UserData["em"].([]string); len(em) != 1 || em[0] != sha("bob@example.com") {
		t.Errorf("em = %v, want %s", e.UserData["em"], sha("bob@example.com"))
	}
	if ph, _ := e.UserData["ph"].([]string); len(ph) != 1 || ph[0] != sha("15550001111") {
		t.Errorf("ph = %v", e.UserData["ph"])
	}
	if xid, _ := e.UserData["external_id"].([]string); len(xid) != 1 || xid[0] != sha("u-42") {
		t.Errorf("external_id = %v", e.UserData["external_id"])
	}
	if e.UserData["client_ip_address"] != "203.0.113.5" || e.UserData["client_user_agent"] != "UA" {
		t.Errorf("network signals: %+v", e.UserData)
	}
	if e.UserData["click_id"] != "ep1" {
		t.Errorf("click_id (epik) = %v, want ep1", e.UserData["click_id"])
	}
	// Pinterest wants value as a STRING; a purchase carries its native order id.
	if e.CustomData["value"] != "20" || e.CustomData["currency"] != "USD" {
		t.Errorf("custom_data value/currency: %+v", e.CustomData)
	}
	if e.CustomData["order_id"] != "evt-9" {
		t.Errorf("order_id = %v, want evt-9", e.CustomData["order_id"])
	}
	// A custom (unmapped) event collapses to Pinterest's "custom" enum value.
	if body.Data[1].EventName != "custom" {
		t.Errorf("custom event name = %q, want custom", body.Data[1].EventName)
	}
	// The raw email must NEVER appear in the marshalled payload.
	raw, _ := json.Marshal(body)
	if strings.Contains(strings.ToLower(string(raw)), "bob@example.com") {
		t.Fatal("raw email leaked into the Pinterest payload")
	}
}

func TestPinterestEcommerceContents(t *testing.T) {
	body := pinterestBuild([]Conversion{{
		Standard: EventPurchase, Name: "order_completed", Value: 59.98, Currency: "USD", EventID: "ord-9",
		User: UserData{Email: "a@b.com"},
		Items: []Item{
			{ID: "SKU1", Price: 24.99, Quantity: 2},
			{ID: "SKU2", Price: 10.0, Quantity: 1},
		},
	}})
	cd := body.Data[0].CustomData
	if cd["value"] != "59.98" || cd["currency"] != "USD" {
		t.Errorf("value/currency: %+v", cd)
	}
	if ids, _ := cd["content_ids"].([]string); len(ids) != 2 || ids[0] != "SKU1" || ids[1] != "SKU2" {
		t.Errorf("content_ids: %+v", cd["content_ids"])
	}
	if cd["num_items"] != 3 {
		t.Errorf("num_items = %v, want 3", cd["num_items"])
	}
	// item_price rides as a string per Pinterest's contents contract.
	contents, _ := cd["contents"].([]map[string]any)
	if len(contents) != 2 || contents[0]["item_price"] != "24.99" || contents[0]["quantity"] != 2.0 {
		t.Errorf("contents: %+v", cd["contents"])
	}
}

func TestPinterestSendEndToEnd(t *testing.T) {
	var gotAccount, gotAuth string
	var gotBody pinterestBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // /ad_accounts/{id}/events
		if len(parts) >= 2 {
			gotAccount = parts[1]
		}
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"num_events_received":1,"num_events_processed":1}`))
	}))
	defer srv.Close()
	old := pinterestAPI
	pinterestAPI = srv.URL
	defer func() { pinterestAPI = old }()

	res, err := pinterest{}.Send(context.Background(), Config{"adAccountId": "549755885123"}, "pina_tok",
		[]Conversion{{Standard: EventLead, Name: "plan_clicked", User: UserData{Email: "x@y.com"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotAccount != "549755885123" {
		t.Errorf("account = %q", gotAccount)
	}
	// The token MUST ride the Authorization header, never the URL.
	if gotAuth != "Bearer pina_tok" {
		t.Errorf("auth = %q, want Bearer pina_tok", gotAuth)
	}
	if len(gotBody.Data) != 1 || gotBody.Data[0].EventName != "lead" {
		t.Errorf("body: %+v", gotBody)
	}
}

func TestPinterestRequiresConfig(t *testing.T) {
	if _, err := (pinterest{}).Send(context.Background(), Config{}, "s", nil); err == nil {
		t.Fatal("missing adAccountId must error")
	}
	if _, err := (pinterest{}).Send(context.Background(), Config{"adAccountId": "1"}, "", nil); err == nil {
		t.Fatal("missing access_token must error")
	}
}
