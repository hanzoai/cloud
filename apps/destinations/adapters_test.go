package destinations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func sha(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// ── GA4 (full, end-to-end against a mock MP endpoint) ────────────────────────

func TestGA4GroupByClient(t *testing.T) {
	batch := []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 49, Currency: "USD", EventID: "e1", User: UserData{ExternalID: "v1"}},
		{Standard: EventPageView, Name: "$pageview", User: UserData{ExternalID: "v1"}},
		{Standard: EventCustom, Name: "feature_used", User: UserData{ExternalID: "v2"}},
		{Standard: EventPageView, Name: "$pageview", User: UserData{ExternalID: ""}}, // no client id → dropped
	}
	groups := ga4Group(batch)
	if len(groups) != 2 {
		t.Fatalf("want 2 client groups, got %d", len(groups))
	}
	if groups[0].ClientID != "v1" || len(groups[0].Events) != 2 {
		t.Fatalf("v1 group wrong: %+v", groups[0])
	}
	if groups[0].Events[0].Name != "purchase" {
		t.Errorf("order_completed → GA4 %q, want purchase", groups[0].Events[0].Name)
	}
	if groups[0].Events[0].Params["value"] != 49.0 || groups[0].Events[0].Params["currency"] != "USD" {
		t.Errorf("purchase params: %+v", groups[0].Events[0].Params)
	}
	// custom event name is coerced to GA4's rules.
	if groups[1].Events[0].Name != "feature_used" {
		t.Errorf("custom name = %q", groups[1].Events[0].Name)
	}
}

func TestGA4SendEndToEnd(t *testing.T) {
	var gotBody ga4Payload
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusNoContent) // GA4 MP: 204 no body
	}))
	defer srv.Close()
	old := ga4Collect
	ga4Collect = srv.URL
	defer func() { ga4Collect = old }()

	res, err := ga4{}.Send(context.Background(), Config{"measurementId": "G-TEST"}, "sekret",
		[]Conversion{{Standard: EventPurchase, Name: "order_completed", Value: 10, Currency: "USD", User: UserData{ExternalID: "v9"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotQuery.Get("measurement_id") != "G-TEST" || gotQuery.Get("api_secret") != "sekret" {
		t.Fatalf("query creds wrong: %v", gotQuery)
	}
	if gotBody.ClientID != "v9" || len(gotBody.Events) != 1 || gotBody.Events[0].Name != "purchase" {
		t.Fatalf("body wrong: %+v", gotBody)
	}
}

func TestGA4RequiresConfig(t *testing.T) {
	if _, err := (ga4{}).Send(context.Background(), Config{}, "s", nil); err == nil {
		t.Fatal("missing measurementId must error")
	}
	if _, err := (ga4{}).Send(context.Background(), Config{"measurementId": "G-X"}, "", nil); err == nil {
		t.Fatal("missing api_secret must error")
	}
}

// ── Meta (full, end-to-end + PII hashing) ────────────────────────────────────

func TestMetaBuildHashesPII(t *testing.T) {
	cv := Conversion{
		Standard: EventPurchase, Name: "order_completed", Value: 20, Currency: "USD", EventID: "evt-9",
		URL: "https://shop.example/checkout", Time: time.Unix(1700000000, 0),
		User: UserData{Email: "  Bob@Example.COM ", Phone: "+1 (555) 000-1111", ExternalID: "u-42", IP: "203.0.113.5", UserAgent: "UA"},
	}
	body := metaBuild(Config{"pixelId": "PX", "testEventCode": "TEST9"}, "tok", []Conversion{cv})
	if body.AccessToken != "tok" || body.TestEventCode != "TEST9" {
		t.Fatalf("token/test-code: %+v", body)
	}
	if len(body.Data) != 1 {
		t.Fatalf("want 1 event, got %d", len(body.Data))
	}
	e := body.Data[0]
	if e.EventName != "Purchase" || e.EventID != "evt-9" || e.ActionSource != "website" {
		t.Errorf("event meta: %+v", e)
	}
	// Email hashed with trim+lowercase.
	em, _ := e.UserData["em"].([]string)
	if len(em) != 1 || em[0] != sha("bob@example.com") {
		t.Errorf("em hash = %v, want %s", em, sha("bob@example.com"))
	}
	// Phone hashed digits-only.
	ph, _ := e.UserData["ph"].([]string)
	if len(ph) != 1 || ph[0] != sha("15550001111") {
		t.Errorf("ph hash = %v", ph)
	}
	// External id hashed; ip/ua un-hashed.
	if xid, _ := e.UserData["external_id"].([]string); len(xid) != 1 || xid[0] != sha("u-42") {
		t.Errorf("external_id = %v", e.UserData["external_id"])
	}
	if e.UserData["client_ip_address"] != "203.0.113.5" || e.UserData["client_user_agent"] != "UA" {
		t.Errorf("network signals: %+v", e.UserData)
	}
	if e.CustomData["value"] != 20.0 || e.CustomData["currency"] != "USD" {
		t.Errorf("custom_data: %+v", e.CustomData)
	}
	// The raw email must NEVER appear in the marshalled payload.
	raw, _ := json.Marshal(body)
	if strings.Contains(strings.ToLower(string(raw)), "bob@example.com") {
		t.Fatal("raw email leaked into the CAPI payload")
	}
}

func TestMetaSendEndToEnd(t *testing.T) {
	var gotPixel string
	var gotBody metaBody
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPixel = strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/events"), "/")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"events_received":1,"fbtrace_id":"TR9"}`))
	}))
	defer srv.Close()
	old := metaGraph
	metaGraph = srv.URL
	defer func() { metaGraph = old }()

	res, err := meta{}.Send(context.Background(), Config{"pixelId": "999"}, "captoken",
		[]Conversion{{Standard: EventLead, Name: "plan_clicked", User: UserData{Email: "x@y.com"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 || res.Message != "TR9" {
		t.Fatalf("result: %+v", res)
	}
	if gotPixel != "999" || gotBody.AccessToken != "captoken" || gotBody.Data[0].EventName != "Lead" {
		t.Fatalf("meta request wrong: pixel=%q body=%+v", gotPixel, gotBody)
	}
}

// ── ecommerce mapping (GA4 items[] + Meta content signals) ───────────────────

func TestGA4EcommerceItems(t *testing.T) {
	groups := ga4Group([]Conversion{{
		Standard: EventPurchase, Name: "order_completed", Value: 49.98, Currency: "USD", EventID: "ord-1",
		User:  UserData{ExternalID: "v1"},
		Items: []Item{{ID: "SKU1", Name: "Widget", Category: "tools", Brand: "Acme", Price: 24.99, Quantity: 2}},
	}})
	if len(groups) != 1 || len(groups[0].Events) != 1 {
		t.Fatalf("groups: %+v", groups)
	}
	p := groups[0].Events[0].Params
	if groups[0].Events[0].Name != "purchase" {
		t.Errorf("event name = %q, want purchase", groups[0].Events[0].Name)
	}
	if p["value"] != 49.98 || p["currency"] != "USD" {
		t.Errorf("value/currency: %+v", p)
	}
	// Purchase carries GA4's native transaction id (from the shared dedup id).
	if p["transaction_id"] != "ord-1" {
		t.Errorf("transaction_id = %v, want ord-1", p["transaction_id"])
	}
	items, ok := p["items"].([]map[string]any)
	if !ok || len(items) != 1 {
		t.Fatalf("items = %v", p["items"])
	}
	it := items[0]
	if it["item_id"] != "SKU1" || it["item_name"] != "Widget" || it["item_category"] != "tools" || it["item_brand"] != "Acme" {
		t.Errorf("item fields: %+v", it)
	}
	if it["price"] != 24.99 || it["quantity"] != 2.0 {
		t.Errorf("item price/qty: %+v", it)
	}
	// A non-purchase ecommerce event still carries items but no transaction_id.
	vc := ga4EventOf(Conversion{Standard: EventViewContent, Name: "product_viewed", User: UserData{ExternalID: "v1"},
		Items: []Item{{ID: "SKU2"}}})
	if vc.Name != "view_item" {
		t.Errorf("view name = %q, want view_item", vc.Name)
	}
	if _, has := vc.Params["transaction_id"]; has {
		t.Error("non-purchase must not carry transaction_id")
	}
	if items, _ := vc.Params["items"].([]map[string]any); len(items) != 1 || items[0]["item_id"] != "SKU2" {
		t.Errorf("view_item items: %+v", vc.Params["items"])
	}
}

func TestMetaEcommerceContents(t *testing.T) {
	body := metaBuild(Config{"pixelId": "PX"}, "tok", []Conversion{{
		Standard: EventPurchase, Name: "order_completed", Value: 59.98, Currency: "USD", EventID: "ord-9",
		User: UserData{Email: "a@b.com"},
		Items: []Item{
			{ID: "SKU1", Price: 24.99, Quantity: 2},
			{ID: "SKU2", Price: 10.0, Quantity: 1},
		},
	}})
	if body.Data[0].EventName != "Purchase" {
		t.Fatalf("event name = %q, want Purchase", body.Data[0].EventName)
	}
	cd := body.Data[0].CustomData
	if cd["value"] != 59.98 || cd["currency"] != "USD" {
		t.Errorf("value/currency: %+v", cd)
	}
	if cd["content_type"] != "product" {
		t.Errorf("content_type: %+v", cd["content_type"])
	}
	// Purchase carries Meta's native order id (its dedup key).
	if cd["order_id"] != "ord-9" {
		t.Errorf("order_id: %+v", cd["order_id"])
	}
	ids, _ := cd["content_ids"].([]string)
	if len(ids) != 2 || ids[0] != "SKU1" || ids[1] != "SKU2" {
		t.Errorf("content_ids: %+v", cd["content_ids"])
	}
	if cd["num_items"] != 3 {
		t.Errorf("num_items = %v, want 3", cd["num_items"])
	}
	contents, _ := cd["contents"].([]map[string]any)
	if len(contents) != 2 || contents[0]["id"] != "SKU1" || contents[0]["quantity"] != 2.0 || contents[0]["item_price"] != 24.99 {
		t.Errorf("contents: %+v", cd["contents"])
	}
}

// ── Umami (Hanzo Analytics — public /api/send, credential-less) ───────────────

func TestUmamiBuild(t *testing.T) {
	// A pageview is sent WITHOUT a name (Umami's pageview vs. custom-event rule) and
	// its URL splits into hostname + path.
	pv := umamiBuild(Config{"websiteId": "W1"}, Conversion{
		Standard: EventPageView, Name: "$pageview", URL: "https://shop.example/pricing?x=1", Referrer: "https://google.com",
		User: UserData{ExternalID: "v1"},
	})
	if pv.Type != "event" || pv.Payload.Website != "W1" {
		t.Fatalf("envelope: %+v", pv)
	}
	if pv.Payload.Name != "" {
		t.Errorf("pageview must have no name, got %q", pv.Payload.Name)
	}
	if pv.Payload.Hostname != "shop.example" || pv.Payload.URL != "/pricing?x=1" {
		t.Errorf("url split: host=%q url=%q", pv.Payload.Hostname, pv.Payload.URL)
	}
	if pv.Payload.Referrer != "https://google.com" || pv.Payload.DistinctID != "v1" {
		t.Errorf("referrer/distinct: %+v", pv.Payload)
	}
	// The visitor id MUST serialize under Umami's `id` session key, never the ignored
	// `distinctId` — the exact JSON-tag regression a struct-field assert cannot catch.
	if raw, _ := json.Marshal(pv.Payload); !strings.Contains(string(raw), `"id":"v1"`) || strings.Contains(string(raw), "distinctId") {
		t.Errorf("visitor id must serialize as `id`, got %s", raw)
	}
	// A commerce event carries its canonical name (sans $) + value/currency data.
	pur := umamiBuild(Config{"websiteId": "W1"}, Conversion{
		Standard: EventPurchase, Name: "order_completed", Value: 30, Currency: "USD", User: UserData{ExternalID: "v2"},
	})
	if pur.Payload.Name != "order_completed" {
		t.Errorf("name = %q", pur.Payload.Name)
	}
	if pur.Payload.Data["value"] != 30.0 || pur.Payload.Data["currency"] != "USD" {
		t.Errorf("data: %+v", pur.Payload.Data)
	}
}

func TestUmamiSendEndToEnd(t *testing.T) {
	var gotEnv umamiEnvelope
	var gotUA, gotXFF, gotPath, gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotRaw = string(b)
		_ = json.Unmarshal(b, &gotEnv)
		_, _ = w.Write([]byte("token-abc")) // Umami returns a plain text token
	}))
	defer srv.Close()
	old := umamiHost
	umamiHost = srv.URL
	defer func() { umamiHost = old }()

	// Credential-less: the secret argument is empty and ignored.
	res, err := umami{}.Send(context.Background(), Config{"websiteId": "W9"}, "",
		[]Conversion{{Standard: EventPurchase, Name: "order_completed", Value: 5, Currency: "USD",
			User: UserData{ExternalID: "v1", UserAgent: "Mozilla/5.0", IP: "203.0.113.7"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotPath != "/api/send" {
		t.Errorf("path = %q, want /api/send", gotPath)
	}
	if gotEnv.Payload.Website != "W9" || gotEnv.Payload.Name != "order_completed" {
		t.Errorf("envelope: %+v", gotEnv.Payload)
	}
	// The END USER's UA + IP are forwarded so Umami attributes the session/geo.
	if gotUA != "Mozilla/5.0" || gotXFF != "203.0.113.7" {
		t.Errorf("forwarded UA/IP: ua=%q xff=%q", gotUA, gotXFF)
	}
	// The visitor id lands on the wire under Umami's `id` session key (not `distinctId`),
	// so identity stitches to the known visitor instead of falling back to IP+UA.
	if gotEnv.Payload.DistinctID != "v1" || !strings.Contains(gotRaw, `"id":"v1"`) || strings.Contains(gotRaw, "distinctId") {
		t.Errorf("visitor id must serialize as `id`: distinct=%q raw=%s", gotEnv.Payload.DistinctID, gotRaw)
	}
}

func TestUmamiRequiresWebsite(t *testing.T) {
	if _, err := (umami{}).Send(context.Background(), Config{}, "", nil); err == nil {
		t.Fatal("missing websiteId must error")
	}
}

// ── PostHog (Hanzo Insights — /v1/e capture, api_key in body) ─────────────────

func TestPostHogBuild(t *testing.T) {
	body := posthogBuild("phc_key", []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 12, Currency: "USD", URL: "https://x.example/y", User: UserData{ExternalID: "v1"}},
		{Standard: EventPageView, Name: "$pageview", User: UserData{ExternalID: ""}}, // no distinct id → dropped
	})
	if body.APIKey != "phc_key" {
		t.Fatalf("api_key = %q", body.APIKey)
	}
	if len(body.Batch) != 1 {
		t.Fatalf("want 1 event (empty distinct dropped), got %d", len(body.Batch))
	}
	e := body.Batch[0]
	if e.Event != "order_completed" || e.DistinctID != "v1" {
		t.Errorf("event/distinct: %+v", e)
	}
	if e.Properties["value"] != 12.0 || e.Properties["currency"] != "USD" || e.Properties["$current_url"] != "https://x.example/y" {
		t.Errorf("properties: %+v", e.Properties)
	}
}

func TestPostHogSendEndToEnd(t *testing.T) {
	var gotBody posthogBatch
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	defer srv.Close()
	old := posthogHost
	posthogHost = srv.URL
	defer func() { posthogHost = old }()

	res, err := posthog{}.Send(context.Background(), Config{}, "phc_secret",
		[]Conversion{{Standard: EventLead, Name: "plan_clicked", User: UserData{ExternalID: "v1"}}})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("sent = %d", res.Sent)
	}
	if gotPath != "/v1/e" {
		t.Errorf("path = %q, want /v1/e", gotPath)
	}
	// The api_key rides the BODY (never the URL); the mock echoes it back for the assert.
	if gotBody.APIKey != "phc_secret" || len(gotBody.Batch) != 1 || gotBody.Batch[0].Event != "plan_clicked" {
		t.Errorf("body: %+v", gotBody)
	}
}

func TestPostHogRequiresKey(t *testing.T) {
	if _, err := (posthog{}).Send(context.Background(), Config{}, "", nil); err == nil {
		t.Fatal("missing api_key must error")
	}
}

// ── scaffolds (payload shape against the same interface) ──────────────────────

func TestTikTokBuild(t *testing.T) {
	body := tiktokBuild(Config{"pixelCode": "PC1"}, []Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 5, Currency: "USD", User: UserData{Email: "a@b.com", Clicks: map[string]string{"ttclid": "tt1"}}},
	})
	if body.EventSource != "web" || body.EventSourceID != "PC1" {
		t.Fatalf("source: %+v", body)
	}
	if body.Data[0].Event != "CompletePayment" {
		t.Errorf("event = %q, want CompletePayment", body.Data[0].Event)
	}
	if body.Data[0].User["email"] != sha("a@b.com") || body.Data[0].User["ttclid"] != "tt1" {
		t.Errorf("user: %+v", body.Data[0].User)
	}
}

func TestRedditBuild(t *testing.T) {
	body := redditBuild([]Conversion{
		{Standard: EventPurchase, Name: "order_completed", Value: 7, Currency: "USD", EventID: "c1", User: UserData{Email: "a@b.com", Clicks: map[string]string{"rdt_cid": "r1"}}},
		{Standard: EventCustom, Name: "feature_used", User: UserData{Email: "a@b.com"}},
	})
	if body.Events[0].EventType["tracking_type"] != "Purchase" || body.Events[0].ClickID != "r1" {
		t.Errorf("purchase event: %+v", body.Events[0])
	}
	if body.Events[0].EventMetadata["value_decimal"] != 7.0 || body.Events[0].EventMetadata["conversion_id"] != "c1" {
		t.Errorf("metadata: %+v", body.Events[0].EventMetadata)
	}
	if body.Events[1].EventType["tracking_type"] != "Custom" || body.Events[1].EventType["custom_event_name"] != "feature_used" {
		t.Errorf("custom event: %+v", body.Events[1].EventType)
	}
}

func TestLinkedInBuild(t *testing.T) {
	e, ok := linkedinBuild(Config{"conversionId": "77"}, Conversion{Standard: EventPurchase, Value: 30, Currency: "USD", User: UserData{Email: "a@b.com"}})
	if !ok {
		t.Fatal("build with email should succeed")
	}
	if e.Conversion != "urn:lla:llaPartnerConversion:77" {
		t.Errorf("urn = %q", e.Conversion)
	}
	if e.User.UserIDs[0].IDType != "SHA256_EMAIL" || e.User.UserIDs[0].IDValue != sha("a@b.com") {
		t.Errorf("user id: %+v", e.User.UserIDs[0])
	}
	if e.ConversionValue == nil || e.ConversionValue.Amount != "30.00" {
		t.Errorf("value: %+v", e.ConversionValue)
	}
	// No email ⇒ no LinkedIn match key ⇒ skipped.
	if _, ok := linkedinBuild(Config{"conversionId": "77"}, Conversion{User: UserData{ExternalID: "x"}}); ok {
		t.Error("build without email must be skipped")
	}
}

func TestXBuildAndScaffold(t *testing.T) {
	convs := xBuild([]Conversion{{Standard: EventPurchase, Value: 9, Currency: "USD", User: UserData{Email: "a@b.com", Clicks: map[string]string{"twclid": "tw1"}}}})
	if len(convs[0].Identifiers) != 2 || convs[0].Identifiers[0].HashedEmail != sha("a@b.com") {
		t.Errorf("identifiers: %+v", convs[0].Identifiers)
	}
	if convs[0].Value != "9.00" {
		t.Errorf("value = %q", convs[0].Value)
	}
	// Send is an honest scaffold: it refuses (OAuth1 not wired) rather than faking.
	if _, err := (xDest{}).Send(context.Background(), Config{"pixelId": "o1"}, "tok",
		[]Conversion{{Standard: EventPurchase, User: UserData{Email: "a@b.com"}}}); err == nil {
		t.Fatal("x Send must return the honest not-enabled error")
	}
}

// TestRegistryComplete asserts every platform self-registered with a coherent Spec.
func TestRegistryComplete(t *testing.T) {
	want := []string{"ga4", "meta", "tiktok", "linkedin", "x", "reddit", "analytics", "insights"}
	m := snapshot()
	for _, id := range want {
		d, ok := m[id]
		if !ok {
			t.Fatalf("destination %q not registered", id)
		}
		if d.Name() == "" || d.Category() == "" {
			t.Errorf("%s: empty name/category", id)
		}
		spec := d.Spec()
		if len(spec.Fields) == 0 {
			t.Errorf("%s: no config fields declared", id)
		}
	}
}
