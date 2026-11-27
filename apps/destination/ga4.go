package destination

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ga4.go forwards conversions to Google Analytics 4 via the Measurement Protocol
// (server-side). Non-secret config: measurementId (G-XXXXXXX, the Data Stream id).
// Secret: api_secret (GA4 Admin → Data Streams → Measurement Protocol API secrets).
// There is no OAuth equivalent for MP, so GA4 has no integrations fallback — the
// api_secret is its sole credential.
//
// MP groups events by client_id (the visitor), so this adapter batches per
// distinct_id into one request each. The measurement_id + api_secret ride the query
// string — the ONE protocol Google mandates that carries the credential in the URL;
// send.go returns transport errors WITHOUT the URL so the secret never leaks.

const ga4ID = "ga4"

// ga4Collect is the MP collect endpoint. A package var so a test points it at a mock
// server; never mutated in production.
var (
	ga4Collect = "https://www.google-analytics.com/mp/collect"
	// ga4Debug is Google's validation server: the same request, checked and
	// answered with what is wrong with it, and never recorded.
	ga4Debug = "https://www.google-analytics.com/debug/mp/collect"
)

// ga4EventName maps the normalized taxonomy onto GA4's recommended event names. A
// StandardEvent absent here (EventCustom) forwards under the raw canonical name.
var ga4EventName = map[StandardEvent]string{
	EventPageView:      "page_view",
	EventViewContent:   "view_item",
	EventViewList:      "view_item_list",
	EventSearch:        "search",
	EventLead:          "generate_lead",
	EventSignUp:        "sign_up",
	EventStartCheckout: "begin_checkout",
	EventAddToCart:     "add_to_cart",
	EventPurchase:      "purchase",
	EventContact:       "contact",
	EventSelect:        "select_item",
	EventLogin:         "login",
}

type ga4 struct{}

func init() { register(ga4{}) }

func (ga4) ID() string       { return ga4ID }
func (ga4) Name() string     { return "Google Analytics 4" }
func (ga4) Category() string { return categoryAnalytics }

func (ga4) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "measurementId", Label: "Measurement ID", Required: true, Example: "G-XXXXXXX"},
		},
		Secrets: []string{"api_secret"},
	}
}

// ga4Payload is one MP request body: the visitor client_id, an optional user_id, and
// the batch's events. Exposed to tests as the pure render of a per-visitor group.
type ga4Payload struct {
	ClientID string      `json:"client_id"`
	UserID   string      `json:"user_id,omitempty"`
	Consent  *ga4Consent `json:"consent,omitempty"`
	Events   []ga4Event  `json:"events"`
}

// ga4Consent is the visitor's ad consent, which GA4 passes on to the Google Ads
// accounts it is linked to.
type ga4Consent struct {
	AdUserData        string `json:"ad_user_data"`
	AdPersonalization string `json:"ad_personalization"`
}

func granted(on bool) string {
	if on {
		return "GRANTED"
	}
	return "DENIED"
}

type ga4Event struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
}

// ga4Group renders the batch into one MP payload per client_id (GA4 requires a single
// client_id per request). A conversion with no resolvable visitor id is dropped (MP
// rejects an empty client_id). Pure — tests drive it directly.
func ga4Group(batch []Conversion) []ga4Payload {
	order := make([]string, 0, len(batch))
	byClient := map[string]*ga4Payload{}
	for _, cv := range batch {
		cid := ga4ClientID(cv)
		if cid == "" {
			continue
		}
		p, ok := byClient[cid]
		if !ok {
			// user_id is the person's id hashed: a sender's own id may be an
			// address, and Google's terms keep personal data out of it.
			p = &ga4Payload{ClientID: cid, UserID: sha256hex(cv.Person)}
			if cv.Consent.Stated {
				p.Consent = &ga4Consent{AdUserData: granted(cv.Consent.Marketing), AdPersonalization: granted(cv.Consent.Marketing && cv.Consent.Ads)}
			}
			byClient[cid] = p
			order = append(order, cid)
		}
		p.Events = append(p.Events, ga4EventsOf(cv)...)
	}
	out := make([]ga4Payload, 0, len(order))
	for _, cid := range order {
		out = append(out, *byClient[cid])
	}
	return out
}

// ga4EventOf renders one conversion into an MP event. purchase/value events carry
// value + currency; the page location rides page_location; the dedup id rides a
// custom param so re-delivery is idempotent on the GA4 side.
func ga4EventsOf(cv Conversion) []ga4Event {
	names := namesOn(ga4ID, cv, ga4EventName)
	if len(names) == 0 {
		names = []string{ga4SafeName(cv.Name)}
	}
	out := make([]ga4Event, len(names))
	for i, name := range names {
		out[i] = ga4EventOf(cv, name)
	}
	return out
}

// ga4EventOf renders the conversion as one event under one of its names.
func ga4EventOf(cv Conversion, name string) ga4Event {
	// One millisecond of engagement is what makes a server event count as a
	// visit's activity in GA4's reports rather than an event with no session.
	params := map[string]any{"engagement_time_msec": 1}
	if cv.Value > 0 {
		params["value"] = cv.Value
		params["currency"] = cv.Currency
	}
	if cv.URL != "" {
		params["page_location"] = cv.URL
	}
	if cv.EventID != "" {
		params["event_id"] = cv.EventID
	}
	// A purchase carries GA4's native transaction_id (its ecommerce dedup + reporting
	// key), sourced from the same dedup id the pixel shares.
	if (name == "purchase" || name == "refund") && cv.EventID != "" {
		params["transaction_id"] = cv.EventID
	}
	// The coupon an order was bought with is GA4's own purchase parameter.
	if coupon := strProp(cv.Properties, "coupon"); coupon != "" && name == "purchase" {
		params["coupon"] = coupon
	}
	// A test-mode order is debug traffic: it shows in DebugView and, with the
	// property's Developer traffic filter active, in no report.
	if cv.Sandbox {
		params["debug_mode"] = true
	}
	// Ecommerce line items → GA4's native items[] (view_item / add_to_cart /
	// begin_checkout / purchase all read it alongside value + currency).
	if len(cv.Items) > 0 {
		params["items"] = ga4Items(cv.Items)
	}
	return ga4Event{Name: name, Params: params}
}

// ga4Items renders the normalized items into GA4's items[] param, omitting empty
// fields so a sink only sees what the event carried.
func ga4Items(items []Item) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{}
		if it.ID != "" {
			m["item_id"] = it.ID
		}
		if it.Name != "" {
			m["item_name"] = it.Name
		}
		if it.Category != "" {
			m["item_category"] = it.Category
		}
		if it.Brand != "" {
			m["item_brand"] = it.Brand
		}
		if it.Variant != "" {
			m["item_variant"] = it.Variant
		}
		if it.Price > 0 {
			m["price"] = it.Price
		}
		if it.Quantity > 0 {
			m["quantity"] = it.Quantity
		}
		out = append(out, m)
	}
	return out
}

// ga4ClientID is the visitor id MP keys on: the _ga client id the page read, so
// the event joins the visit gtag counted, else the SHA-256 of our own visitor id.
// Never the id itself, which for a renewal is the wallet's name.
func ga4ClientID(cv Conversion) string {
	if c := strings.TrimSpace(cv.User.ClientID); c != "" {
		return c
	}
	return sha256hex(strings.TrimSpace(cv.User.ExternalID))
}

// ga4SafeName coerces a raw canonical event name to GA4's event-name rules
// (alphanumeric + underscore, must start with a letter, <=40 chars). GA4 rejects a
// non-conforming name outright, so a custom event is normalized rather than dropped.
func ga4SafeName(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "custom_event"
	}
	if c := out[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		out = "e_" + out
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

func (d ga4) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	mid := cfg.get("measurementId")
	if mid == "" {
		return Result{}, fmt.Errorf("ga4: measurementId is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("ga4: api_secret is required")
	}
	groups := ga4Group(batch)
	if len(groups) == 0 {
		return Result{}, nil
	}
	base := ga4Collect
	if batch[0].Test {
		base = ga4Debug
	}
	endpoint := base + "?" + url.Values{
		"measurement_id": {mid},
		"api_secret":     {secret},
	}.Encode()
	sent := 0
	for i := range groups {
		// MP returns 204 with no body on success; postJSON treats an empty 2xx as OK.
		// The validation server answers 200 with what it found wrong, if anything.
		var check ga4Validation
		if err := postJSON(ctx, ga4ID, endpoint, nil, groups[i], &check); err != nil {
			return Result{Sent: sent}, err
		}
		if len(check.Messages) > 0 {
			m := check.Messages[0]
			return Result{Sent: sent}, fmt.Errorf("ga4: %s: %s (%s)", m.Code, m.Description, m.Field)
		}
		sent += len(groups[i].Events)
	}
	if batch[0].Test {
		return Result{Sent: sent, Message: "validated by GA4's validation server; nothing recorded"}, nil
	}
	return Result{Sent: sent}, nil
}

// ga4Validation is the validation server's answer. Empty is a valid request.
type ga4Validation struct {
	Messages []struct {
		Field       string `json:"fieldPath"`
		Description string `json:"description"`
		Code        string `json:"validationCode"`
	} `json:"validationMessages"`
}
