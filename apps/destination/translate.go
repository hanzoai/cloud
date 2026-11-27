package destination

import (
	"cmp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/event"
)

// translate.go maps the canonical EVENTS vocabulary (@hanzo/event: signup_completed,
// checkout_started, order_completed, …) onto the normalized StandardEvent taxonomy,
// ONCE, and builds a Conversion from an event.SinkEvent. Each adapter renders
// StandardEvent → its own platform name; this file is the ONE canonical→normalized
// map, so a new platform never re-derives it.

// standardOf is an event's normalized StandardEvent: the `standard` of its row in
// the platform table of @hanzo/events (apps/event/catalog.json), the one place
// events are named for each platform. A name the table does not know is EventCustom
// and no ad platform is sent it, except the schema.org / GA4 action names a site
// that is not one of ours may emit, which collapse onto the same StandardEvent so
// an ecommerce event maps to each platform's native conversion whatever vocabulary
// its source spoke.
func standardOf(name string) StandardEvent {
	if s := event.StandardOf(name); s != "" {
		return StandardEvent(s)
	}
	return aliasOf[name]
}

var aliasOf = map[string]StandardEvent{
	"$pageview":      EventPageView,
	"generate_lead":  EventLead,
	"product_viewed": EventViewContent,
	"product_view":   EventViewContent,
	"product_added":  EventAddToCart,
	"add_to_cart":    EventAddToCart,
	"begin_checkout": EventStartCheckout,
	"purchase":       EventPurchase,
}

// Translate maps one canonical event onto the normalized Conversion every adapter
// renders. It is pure — no I/O — so the mapping is driven directly by tests. The raw
// (pre-warehouse-scrub) properties carry the match keys the User set is lifted from.
func Translate(ev event.SinkEvent) Conversion {
	return Conversion{
		Standard: standardOf(ev.Name), // "" ⇒ custom, which no ad platform is sent
		Name:     ev.Name,
		Source:   ev.Source,
		Vouched:  ev.Vouched,
		// A test-mode order the server stated: test traffic, never revenue.
		Sandbox: strProp(ev.Properties, "test") == "true",
		Person:  personOf(ev),
		Consent: consentOf(ev.Properties),
		Tags:    setOf(strProp(ev.Properties, "tags")),
		// A renewal or auto-reload happened in no browser, whatever page its
		// buyer's touch last saw.
		Recurring: strProp(ev.Properties, "recurring") == "true",
		// The dedup id: the browser tag's own event_id when it set one (track.js stamps
		// it into properties + fires it on the pixel), else the messageId. This is what
		// makes a browser pixel event and this server CAPI event DEDUPLICATE — without
		// it the two carry different ids and a conversion is double-counted.
		EventID:  cmp.Or(strProp(ev.Properties, "event_id"), strings.TrimSpace(ev.MessageID)),
		Time:     ev.Time,
		Value:    conversionValue(ev),
		Currency: conversionCurrency(ev),
		User:     liftUser(ev),
		// A peer states the page a form was filed from as a property; it has no
		// envelope URL of its own.
		URL:        cmp.Or(ev.URL, strProp(ev.Properties, "url")),
		Referrer:   ev.Referrer,
		Items:      liftItems(ev),
		Properties: ev.Properties,
	}
}

// liftItems lifts the ecommerce line items from an event: an explicit items/products
// array in the properties (each row a product map), else a single item synthesized
// from the first-class product id + quantity. nil when the event carries no commerce
// items — a non-commerce event yields none and every adapter omits the field.
func liftItems(ev event.SinkEvent) []Item {
	for _, key := range []string{"items", "products"} {
		raw, ok := ev.Properties[key].([]any)
		if !ok || len(raw) == 0 {
			continue
		}
		out := make([]Item, 0, len(raw))
		for _, r := range raw {
			if m, ok := r.(map[string]any); ok {
				out = append(out, itemOf(m))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	// A peer states its line item as flat attributes (its events carry strings only):
	// what commerce states for a plan sale.
	if id := strProp(ev.Properties, "item_id"); id != "" {
		it := Item{ID: id, Name: strProp(ev.Properties, "item_name"), Category: strProp(ev.Properties, "item_category"), Variant: strProp(ev.Properties, "item_variant"), Quantity: 1}
		if q, err := strconv.ParseFloat(strProp(ev.Properties, "quantity"), 64); err == nil && q > 0 {
			it.Quantity = q
		}
		if p, err := strconv.ParseFloat(strProp(ev.Properties, "item_price"), 64); err == nil {
			it.Price = p
		}
		return []Item{it}
	}
	if id := strings.TrimSpace(ev.ProductID); id != "" {
		return []Item{{ID: id, Quantity: float64(ev.Quantity)}}
	}
	return nil
}

// itemOf renders one raw product map into a normalized Item, tolerant of the GA4
// (item_id/item_name/price), Segment (product_id/sku/name), and schema.org (id/name)
// key vocabularies so ANY tracker's items array maps without the caller re-keying.
func itemOf(m map[string]any) Item {
	return Item{
		ID:       strProp(m, "item_id", "id", "sku", "product_id", "productId"),
		Name:     strProp(m, "item_name", "name", "title"),
		Category: strProp(m, "item_category", "category"),
		Brand:    strProp(m, "item_brand", "brand"),
		Variant:  strProp(m, "item_variant", "variant"),
		Price:    firstFloat(m, "price", "item_price", "value", "amount"),
		Quantity: firstFloat(m, "quantity", "qty"),
	}
}

// firstFloat returns the first numeric value across keys (0 if none) — the multi-key
// float reader (floatProp is single-key) the item lift uses for price/quantity.
func firstFloat(p map[string]any, keys ...string) float64 {
	for _, k := range keys {
		if f, ok := floatProp(p, k); ok {
			return f
		}
	}
	return 0
}

// conversionValue is the monetary value of a conversion in MAJOR units: the event's
// first-class Revenue, else a value/revenue/amount property. 0 when none is present
// (a non-purchase event carries no value, and adapters omit the value field then).
func conversionValue(ev event.SinkEvent) float64 {
	if ev.Revenue != 0 {
		return ev.Revenue
	}
	for _, k := range []string{"value", "revenue", "amount", "total"} {
		if f, ok := floatProp(ev.Properties, k); ok {
			return f
		}
	}
	return 0
}

// conversionCurrency resolves the ISO-4217 currency (upper-cased): the event's
// first-class Currency, else a currency property, else USD.
func conversionCurrency(ev event.SinkEvent) string {
	if c := strings.TrimSpace(ev.Currency); c != "" {
		return strings.ToUpper(c)
	}
	if c := strProp(ev.Properties, "currency"); c != "" {
		return strings.ToUpper(c)
	}
	return "USD"
}

// liftUser pulls the match-key set from the event. The external id is the
// distinct/person id the caller owns; email/phone/ip/user-agent/fbp and the known
// click ids are read from the raw properties. Adapters hash the PII fields before
// send; nothing here is stored.
func liftUser(ev event.SinkEvent) UserData {
	p := ev.Properties
	u := UserData{
		ExternalID: cmp.Or(strings.TrimSpace(ev.DistinctID), strings.TrimSpace(ev.AnonymousID)),
		Email:      strProp(p, "email", "$email", "userEmail"),
		Phone:      strProp(p, "phone", "$phone", "phoneNumber"),
		IP:         cmp.Or(strings.TrimSpace(ev.IP), strProp(p, "ip", "$ip", "client_ip_address", "ipAddress")),
		UserAgent:  cmp.Or(strings.TrimSpace(ev.UserAgent), strProp(p, "userAgent", "$user_agent", "client_user_agent", "ua")),
		FBP:        strProp(p, "fbp", "_fbp"),
		FBC:        strProp(p, "fbc", "_fbc"),
		ClientID:   strProp(p, "cid"),
		Hashed:     hashed(strProp(p, "em")),
	}
	if at := unix(p["clicked"]); at > 0 {
		u.Clicked = time.Unix(at, 0)
	}
	// A click id rides the event's properties when the page lifted it, and the
	// landing URL when it did not: a gclid a page never read is still in the
	// address it arrived at, and that address was measured carrying 61 of them
	// while no property carried one. The URL's click is this event's own, so its
	// time is the event's.
	var q url.Values
	if parsed, err := url.Parse(ev.URL); err == nil {
		q = parsed.Query()
	}
	clicks := map[string]string{}
	for _, k := range clickKeys {
		if v := strProp(p, k); v != "" {
			clicks[k] = v
		} else if v := strings.TrimSpace(q.Get(k)); v != "" {
			clicks[k] = v
			if u.Clicked.IsZero() {
				u.Clicked = ev.Time
			}
		}
	}
	if len(clicks) > 0 {
		u.Clicks = clicks
		// A click the page did not date is as old as the event that carried it,
		// so it still ages out of the window.
		if u.Clicked.IsZero() {
			u.Clicked = ev.Time
		}
	}
	// A click cannot have happened after the event it brought; one dated later
	// is dated at the event, so it ages out of the window with it.
	if !ev.Time.IsZero() && u.Clicked.After(ev.Time) {
		u.Clicked = ev.Time
	}
	return u
}

// unix reads a time in unix seconds, as a JSON number from a browser or as the
// text a peer's statement carries every value in.
func unix(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	}
	return 0
}

// hashed is an email's SHA-256 a sender already took, when it is one: 64 hex
// digits, lower-cased. Anything else is not a hash and is not sent as one.
func hashed(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) != 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return ""
		}
	}
	return s
}

// clickKeys are the ad click ids a landing URL can carry, one per network.
var clickKeys = []string{"gclid", "gbraid", "wbraid", "fbclid", "ttclid", "twclid", "rdt_cid", "li_fat_id", "msclkid", "epik", "oppref"}

// personOf is the identified subject an event names, or "" for an anonymous
// visitor. @hanzo/event sends the person as distinctId once identify ran and
// the browser's own id as anonymousId throughout, so the two are equal exactly
// until someone signs in. A peer's statement names a subject and no browser.
// A statement that names only the wallet that paid (its `payer`) names no person.
func personOf(ev event.SinkEvent) string {
	d := strings.TrimSpace(ev.DistinctID)
	if d == "" || d == strings.TrimSpace(ev.AnonymousID) || d == strProp(ev.Properties, "payer") {
		return ""
	}
	return d
}

// Consent is what a visitor allowed, in the categories hanzo.ai's Cookie
// Preferences name. Stated is false when the event said nothing, and nothing
// stated is nothing allowed: every visitor opts in, so a conversion is sent only
// under a choice its own event, or the buyer's kept touch, states.
type Consent struct {
	Stated    bool
	Analytics bool // first-party measurement, and GA4
	Marketing bool // conversion measurement on the ad platforms
	Ads       bool // personalised third-party marketing: audiences and retargeting
}

// consentOf reads the `consent` property: the granted categories, comma
// separated ("analytics,marketing,ads"). Present and empty means all refused;
// absent means nothing was stated, which allows nothing.
func consentOf(p map[string]any) Consent {
	v, ok := p["consent"].(string)
	if !ok {
		return Consent{}
	}
	set := setOf(v)
	return Consent{Stated: true, Analytics: set["analytics"], Marketing: set["marketing"], Ads: set["ads"]}
}

// String renders a stated consent the way the property spells it.
func (c Consent) String() string {
	var out []string
	for _, k := range []struct {
		on   bool
		name string
	}{{c.Analytics, "analytics"}, {c.Marketing, "marketing"}, {c.Ads, "ads"}} {
		if k.on {
			out = append(out, k.name)
		}
	}
	return strings.Join(out, ",")
}

// setOf splits a comma list into a set. nil for an empty list.
func setOf(s string) map[string]bool {
	var out map[string]bool
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			if out == nil {
				out = map[string]bool{}
			}
			out[part] = true
		}
	}
	return out
}

// ── property readers (best-effort, type-tolerant) ────────────────────────────

// strProp returns the first non-empty string value across keys ("" if none). It
// tolerates a value stored as any stringable scalar.
func strProp(p map[string]any, keys ...string) string {
	if p == nil {
		return ""
	}
	for _, k := range keys {
		v, ok := p[k]
		if !ok {
			continue
		}
		if s, ok := v.(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	return ""
}

// floatProp returns a numeric property as a float across the JSON number types.
func floatProp(p map[string]any, key string) (float64, bool) {
	if p == nil {
		return 0, false
	}
	switch v := p[key].(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint32:
		return float64(v), true
	default:
		return 0, false
	}
}
