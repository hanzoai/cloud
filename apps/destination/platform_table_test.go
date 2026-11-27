package destination

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// The browser tag and this forwarder read one table, so a moment carries the
// same name on both. These pin the rows the commerce funnel depends on.

func namesFor(id string, cv Conversion) string {
	byStd := map[string]map[StandardEvent]string{ga4ID: ga4EventName, metaID: metaEventName, tiktokID: tiktokEventName, openaiID: openaiEventName}
	return strings.Join(namesOn(id, cv, byStd[id]), "+")
}

func TestCommerceFunnelNames(t *testing.T) {
	for _, c := range []struct{ event, ga4, meta string }{
		{"pricing_viewed", "view_item_list", "ViewContent"},
		{"plan_clicked", "select_item", "PlanClicked"},
		{"plan_selected", "add_to_cart", "AddToCart"},
		{"plan_changed", "remove_from_cart+add_to_cart", "CustomizeProduct"},
		{"checkout_viewed", "view_item", "ViewContent"},
		{"checkout_started", "begin_checkout", "InitiateCheckout"},
		{"payment_info_added", "add_payment_info", "AddPaymentInfo"},
		{"checkout_abandoned", "checkout_abandoned", "CheckoutAbandoned"},
		{"order_completed", "purchase", "Purchase+Subscribe"},
		{"subscription_changed", "purchase", "Subscribe"},
		{"order_refunded", "refund", "OrderRefunded"},
		{"signup_completed", "sign_up", "CompleteRegistration"},
		{"sales_contacted", "generate_lead", "Lead"},
	} {
		cv := Conversion{Name: c.event, Standard: standardOf(c.event)}
		if got := namesFor(ga4ID, cv); got != c.ga4 {
			t.Errorf("%s on GA4 = %q, want %q", c.event, got, c.ga4)
		}
		if got := namesFor(metaID, cv); got != c.meta {
			t.Errorf("%s on Meta = %q, want %q", c.event, got, c.meta)
		}
	}
}

func TestEveryAdPlatformGetsTheFourMoments(t *testing.T) {
	for _, c := range []struct{ event, kind, tiktok, openai string }{
		{"signup_completed", "sign_up", "CompleteRegistration", "sign_up"},
		{"checkout_started", "begin_checkout", "InitiateCheckout", "checkout_started"},
		{"order_completed", "purchase", "CompletePayment", "purchase"},
		{"sales_contacted", "lead", "SubmitForm", "lead"},
	} {
		for _, p := range []string{"linkedin", "x"} {
			if got := strings.Join(event.Platform(c.event, p), ""); got != c.kind {
				t.Errorf("%s on %s = %q, want %q", c.event, p, got, c.kind)
			}
		}
		if got := namesFor(tiktokID, Conversion{Name: c.event}); got != c.tiktok {
			t.Errorf("%s on TikTok = %q, want %q", c.event, got, c.tiktok)
		}
		if got := namesFor(openaiID, Conversion{Name: c.event}); got != c.openai {
			t.Errorf("%s on OpenAI = %q, want %q", c.event, got, c.openai)
		}
	}
}

func TestGA4SendsBothNamesOfAChange(t *testing.T) {
	evs := ga4EventsOf(Conversion{Name: "plan_changed", EventID: "e1"})
	if len(evs) != 2 || evs[0].Name != "remove_from_cart" || evs[1].Name != "add_to_cart" {
		t.Fatalf("plan_changed: %+v", evs)
	}
	for _, e := range evs {
		if e.Params["event_id"] != "e1" {
			t.Errorf("%s lost the shared event id", e.Name)
		}
	}
}

// A subscription change and a refund state money, so only a peer app or the
// org's own key may send one to a platform.
func TestOnlyStatedMoneyIsForwarded(t *testing.T) {
	all := Consent{Analytics: true, Marketing: true, Ads: true}
	for _, name := range []string{"subscription_changed", "order_refunded", "order_completed"} {
		typed := Conversion{Name: name, Standard: standardOf(name), Consent: all}
		if got := admit(meta{}, Config{}, []Conversion{typed}); len(got) != 0 {
			t.Errorf("%s typed by a browser reached Meta", name)
		}
		stated := typed
		stated.Source = sourcePlane
		if got := admit(meta{}, Config{}, []Conversion{stated}); len(got) != 1 {
			t.Errorf("%s stated by commerce did not reach Meta", name)
		}
	}
}

// A test-mode order is test traffic: GA4 files it under debug_mode, Meta under
// the org's Test Event Code, every other platform is sent nothing, and none of
// it carries a value.
func TestSandboxOrderIsTestTraffic(t *testing.T) {
	all := Consent{Analytics: true, Marketing: true, Ads: true}
	cv := Conversion{Name: "order_completed", Standard: EventPurchase, Source: sourcePlane, Sandbox: true,
		Value: 19, Currency: "USD", EventID: "o1", Consent: all, Items: []Item{{ID: "dev", Price: 19}},
		User: UserData{Email: "a@b.com", ExternalID: "u"}}

	got := admit(ga4{}, Config{}, []Conversion{cv})
	if len(got) != 1 || got[0].Value != 0 || got[0].Items[0].Price != 0 {
		t.Fatalf("GA4 must take the sandbox order priceless: %+v", got)
	}
	if ev := ga4EventsOf(got[0])[0]; ev.Params["debug_mode"] != true || ev.Params["value"] != nil {
		t.Errorf("GA4 sandbox purchase: %+v", ev.Params)
	}

	if len(admit(meta{}, Config{}, []Conversion{cv})) != 0 {
		t.Error("Meta without a Test Event Code must not be sent a sandbox order")
	}
	if len(admit(meta{}, Config{"testEventCode": "TEST1"}, []Conversion{cv})) != 1 {
		t.Error("Meta with a Test Event Code takes the sandbox order")
	}
	body := metaBuild(Config{"testEventCode": "TEST1"}, "tok", []Conversion{cv})
	if body.TestEventCode != "TEST1" {
		t.Errorf("Meta test code: %+v", body)
	}
	for _, d := range []Destination{tiktok{}, linkedinDest{}, xDest{}, googleads{}} {
		if len(admit(d, Config{}, []Conversion{cv})) != 0 {
			t.Errorf("%s was sent a sandbox order", d.ID())
		}
	}
}

func TestLinkedInAndXNameRulesByKind(t *testing.T) {
	cfg := Config{"signUpConversionId": "11", "purchaseConversionId": "22"}
	e, ok := linkedinBuild(cfg, Conversion{Name: "order_completed", Standard: EventPurchase, User: UserData{Email: "a@b.com"}})
	if !ok || e.Conversion != "urn:lla:llaPartnerConversion:22" {
		t.Errorf("purchase rule: %+v %v", e, ok)
	}
	if _, ok := linkedinBuild(cfg, Conversion{Name: "sales_contacted", Standard: EventLead, User: UserData{Email: "a@b.com"}}); ok {
		t.Error("a kind with no rule must not be sent")
	}
	xs := xBuild(Config{"xPurchase": "x", "purchaseConversionId": "tw-1-2"}, []Conversion{{Name: "order_completed", Standard: EventPurchase, User: UserData{Email: "a@b.com"}}})
	if xs[0].ConversionID != "tw-1-2" {
		t.Errorf("x conversion_id = %q", xs[0].ConversionID)
	}
}

// The LinkedIn, X and TikTok click ids a browser event carries are kept with the
// touch, so a sale the server states days later is credited to that ad.
func TestTouchKeepsEveryPlatformsClick(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := testStore(t)
		visit := Translate(event.SinkEvent{
			Name: "signup_completed", DistinctID: "sub-9", Time: time.Now(),
			URL:  "https://hanzo.ai/?rdt_cid=RDT9&oppref=OPP9",
			Properties: map[string]any{"li_fat_id": "LI9", "twclid": "TW9", "ttclid": "TT9", "clicked": float64(time.Now().Unix()), "consent": "analytics,marketing,ads"},
		})
		if err := st.recall(ctx, "hanzo", &visit); err != nil {
			t.Fatal(err)
		}
		sale := Translate(event.SinkEvent{Name: "order_completed", Source: sourcePlane, Product: "commerce", DistinctID: "sub-9", Time: time.Now(), Revenue: 19})
		if err := st.recall(ctx, "hanzo", &sale); err != nil {
			t.Fatal(err)
		}
		for k, want := range map[string]string{"li_fat_id": "LI9", "twclid": "TW9", "ttclid": "TT9", "rdt_cid": "RDT9", "oppref": "OPP9"} {
			if got := sale.User.click(k); got != want {
				t.Errorf("%s = %q, want %q", k, got, want)
			}
		}
	})
}

// A touches table from before those columns existed is given them.
func TestTouchesTableFromBeforeIsMigrated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := testStore(t)
		if _, err := st.db.Exec(`DROP TABLE touches`); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`CREATE TABLE touches (org TEXT NOT NULL, person TEXT NOT NULL, gclid TEXT NOT NULL DEFAULT '', gbraid TEXT NOT NULL DEFAULT '', wbraid TEXT NOT NULL DEFAULT '', fbc TEXT NOT NULL DEFAULT '', fbp TEXT NOT NULL DEFAULT '', cid TEXT NOT NULL DEFAULT '', ua TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', em TEXT NOT NULL DEFAULT '', consent TEXT NOT NULL DEFAULT '', stated INTEGER NOT NULL DEFAULT 0, clicked INTEGER NOT NULL DEFAULT 0, seen INTEGER NOT NULL, PRIMARY KEY (org, person))`); err != nil {
			t.Fatal(err)
		}
		if err := st.migrate(); err != nil {
			t.Fatalf("migrate over an old table: %v", err)
		}
		if err := st.keep(context.Background(), "hanzo", "p", Touch{Ttclid: "TT"}); err != nil {
			t.Fatalf("keep after migrate: %v", err)
		}
		if err := st.migrate(); err != nil {
			t.Fatalf("migrate is repeatable: %v", err)
		}
	})
}

// What marketing builds audiences from: a product page seen, a search, a pressed
// call to action, and the three ways in short of an account. Each forwards to
// Meta's Conversions API (under Marketing) by the same name the pixel used, so
// the two dedupe on the event id.
func TestAudienceMomentsForward(t *testing.T) {
	for _, c := range []struct{ event, ga4, meta string }{
		{"product_viewed", "view_item", "ViewContent"},
		{"search_performed", "search", "Search"},
		{"cta_clicked", "cta_click", "CTAClicked"},
		{"newsletter_subscribed", "generate_lead", "Lead"},
		{"beta_submitted", "generate_lead", "Lead"},
		{"research_access_applied", "generate_lead", "Lead"},
	} {
		cv := Conversion{Name: c.event, Standard: standardOf(c.event)}
		if got := namesFor(ga4ID, cv); got != c.ga4 {
			t.Errorf("%s on GA4 = %q, want %q", c.event, got, c.ga4)
		}
		if got := namesFor(metaID, cv); got != c.meta {
			t.Errorf("%s on Meta = %q, want %q", c.event, got, c.meta)
		}
		if !sends(metaID, cv) {
			t.Errorf("%s is not sent to Meta", c.event)
		}
	}
	if got := standardOf("search_performed"); got != EventSearch {
		t.Errorf("search_performed standard = %q, want %q", got, EventSearch)
	}
}
