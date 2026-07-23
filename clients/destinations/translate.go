package destinations

import (
	"strings"

	"github.com/hanzoai/cloud/clients/analytics"
)

// translate.go maps the canonical EVENTS vocabulary (@hanzo/event: signup_completed,
// checkout_started, order_completed, …) onto the normalized StandardEvent taxonomy,
// ONCE, and builds a Conversion from an analytics.SinkEvent. Each adapter renders
// StandardEvent → its own platform name; this file is the ONE canonical→normalized
// map, so a new platform never re-derives it.

// standardOf maps a canonical event name to its normalized StandardEvent. A name not
// present here translates to EventCustom ("") and is forwarded under its raw name.
// The mapping follows the @hanzo/event GOALS (signup / sale / upgrade-intent funnels)
// so a platform's optimizer sees the same conversions the Insights goals count.
var standardOf = map[string]StandardEvent{
	"$pageview":        EventPageView,
	"pricing_viewed":   EventViewContent,
	"signup_viewed":    EventViewContent,
	"plan_clicked":     EventLead,
	"signup_submitted": EventLead,
	"waitlist_joined":  EventLead,
	"referral_used":    EventLead,
	"signup_completed": EventSignUp,
	"checkout_started": EventStartCheckout,
	"order_completed":  EventPurchase,
}

// Translate maps one canonical event onto the normalized Conversion every adapter
// renders. It is pure — no I/O — so the mapping is driven directly by tests. The raw
// (pre-warehouse-scrub) properties carry the match keys the User set is lifted from.
func Translate(ev analytics.SinkEvent) Conversion {
	return Conversion{
		Standard:   standardOf[ev.Name], // "" ⇒ custom, forwarded as ev.Name
		Name:       ev.Name,
		EventID:    ev.MessageID,
		Time:       ev.Time,
		Value:      conversionValue(ev),
		Currency:   conversionCurrency(ev),
		User:       liftUser(ev),
		URL:        ev.URL,
		Properties: ev.Properties,
	}
}

// conversionValue is the monetary value of a conversion in MAJOR units: the event's
// first-class Revenue, else a value/revenue/amount property. 0 when none is present
// (a non-purchase event carries no value, and adapters omit the value field then).
func conversionValue(ev analytics.SinkEvent) float64 {
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
func conversionCurrency(ev analytics.SinkEvent) string {
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
func liftUser(ev analytics.SinkEvent) UserData {
	p := ev.Properties
	u := UserData{
		ExternalID: firstNonEmpty(ev.DistinctID, ev.AnonymousID),
		Email:      strProp(p, "email", "$email", "userEmail"),
		Phone:      strProp(p, "phone", "$phone", "phoneNumber"),
		IP:         strProp(p, "ip", "$ip", "client_ip_address", "ipAddress"),
		UserAgent:  strProp(p, "userAgent", "$user_agent", "client_user_agent", "ua"),
		FBP:        strProp(p, "fbp", "_fbp"),
	}
	clicks := map[string]string{}
	for _, k := range []string{"fbclid", "gclid", "ttclid", "twclid", "rdt_cid", "li_fat_id", "msclkid"} {
		if v := strProp(p, k); v != "" {
			clicks[k] = v
		}
	}
	if len(clicks) > 0 {
		u.Clicks = clicks
	}
	return u
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

func firstNonEmpty(a, b string) string {
	if s := strings.TrimSpace(a); s != "" {
		return s
	}
	return strings.TrimSpace(b)
}
