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

// TestRegistryComplete asserts all six platforms self-registered with a coherent Spec.
func TestRegistryComplete(t *testing.T) {
	want := []string{"ga4", "meta", "tiktok", "linkedin", "x", "reddit"}
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
