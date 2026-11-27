package destination

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// meta.go forwards conversions to Meta (Facebook + Instagram) via the Conversions
// API — the server-side complement of the Meta Pixel, sharing the pixel id and the
// event_id so a browser pixel event and this server event DEDUPLICATE. Non-secret
// config: pixelId (the dataset/pixel id) and an optional testEventCode (Events
// Manager "Test Events", used by the test endpoint only). Secret: access_token — a CAPI token from Events Manager,
// OR, when none is sealed locally, the org's existing meta_ads OAuth token, reused
// through the integrations custody client (Spec.Fallback = "meta_ads").
//
// The access_token rides the JSON body (never the URL). PII match keys (email, phone,
// external id) are SHA-256 hashed before send per Meta's advanced-matching contract;
// the ip, user agent, fbp cookie, and a derived fbc ride as Meta specifies.

const metaID = "meta"

// metaGraph is the Graph API base. A package var so a test points it at a mock
// server; never mutated in production.
var metaGraph = "https://graph.facebook.com/v26.0"

// metaEventName maps the normalized taxonomy onto Meta's standard event names. A
// StandardEvent absent here (EventCustom) forwards under the raw canonical name (a
// Meta custom event).
var metaEventName = map[StandardEvent]string{
	EventPageView:      "PageView",
	EventViewContent:   "ViewContent",
	EventViewList:      "ViewContent",
	EventSearch:        "Search",
	EventLead:          "Lead",
	EventSignUp:        "CompleteRegistration",
	EventStartCheckout: "InitiateCheckout",
	EventAddToCart:     "AddToCart",
	EventPurchase:      "Purchase",
	EventContact:       "Contact",
}

type meta struct{}

func init() { register(meta{}) }

func (meta) ID() string       { return metaID }
func (meta) Name() string     { return "Meta (Facebook & Instagram)" }
func (meta) Category() string { return categoryAdvertising }

func (meta) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "pixelId", Label: "Pixel / Dataset ID", Required: true, Example: "1234567890"},
			{Key: "testEventCode", Label: "Test Event Code", Required: false, Example: "TEST12345"},
		},
		Secrets:  []string{"access_token"},
		Fallback: "meta_ads", // reuse the integrations Meta connection's token
	}
}

// metaBody is the CAPI request body: the events + the access token. Exposed to tests
// as the pure render of a batch.
type metaBody struct {
	Data          []metaEvent `json:"data"`
	AccessToken   string      `json:"access_token"`
	TestEventCode string      `json:"test_event_code,omitempty"`
}

type metaEvent struct {
	EventName      string         `json:"event_name"`
	EventTime      int64          `json:"event_time"`
	EventID        string         `json:"event_id,omitempty"`
	ActionSource   string         `json:"action_source"`
	EventSourceURL string         `json:"event_source_url,omitempty"`
	UserData       map[string]any `json:"user_data"`
	CustomData     map[string]any `json:"custom_data,omitempty"`
	// Limited Data Use: set when the visitor turned off personalised ads. Country
	// and state 0 ask Meta to apply it where the visitor's law requires it.
	Options []string `json:"data_processing_options,omitempty"`
	Country *int     `json:"data_processing_options_country,omitempty"`
	State   *int     `json:"data_processing_options_state,omitempty"`
}

// metaBuild renders the batch into the CAPI body. Pure — tests assert the payload
// shape (hashed match keys, dedup event_id, value/currency) without a network call.
func metaBuild(cfg Config, secret string, batch []Conversion) metaBody {
	data := make([]metaEvent, 0, len(batch))
	for _, cv := range batch {
		// Each name is its own event under the one event_id: Meta keeps one per
		// (name, id), so the browser's copy of each is the same event.
		for _, name := range namesOn(metaID, cv, metaEventName) {
			e := metaEvent{
				EventName:      name,
				EventTime:      metaEventTime(cv.Time),
				EventID:        cv.EventID,
				ActionSource:   metaSource(cv),
				EventSourceURL: cv.URL,
				UserData:       metaUserData(cv.User),
			}
			if cd := metaCustomData(cv, name); len(cd) > 0 {
				e.CustomData = cd
			}
			// Limited Data Use when personalised ads are off. Meta places a visitor by
			// IP to decide where it applies, and a server event carries none, so it is
			// stated as California's: the restriction then always holds.
			if !cv.Consent.Ads {
				country, state := 1, 1000
				e.Options, e.Country, e.State = []string{"LDU"}, &country, &state
			}
			data = append(data, e)
		}
	}
	body := metaBody{Data: data, AccessToken: secret}
	// The Test Event Code files events under Test Events instead of counting them,
	// so it rides a test and never a live conversion.
	if len(batch) > 0 && (batch[0].Test || batch[0].Sandbox) {
		body.TestEventCode = cfg.get("testEventCode")
	}
	return body
}

// metaCustomData renders a conversion's commerce fields into Meta's custom_data:
// value/currency, and — for an ecommerce event — the native content signals
// (content_ids, contents[{id,quantity,item_price}], content_type, num_items) that
// Meta's dynamic ads and Purchase optimization read. A purchase also carries order_id,
// its native dedup key. Empty when the event carries neither value nor items.
func metaCustomData(cv Conversion, name string) map[string]any {
	cd := map[string]any{}
	if cv.Value > 0 {
		cd["value"] = cv.Value
		cd["currency"] = cv.Currency
	}
	if len(cv.Items) > 0 {
		ids := make([]string, 0, len(cv.Items))
		contents := make([]map[string]any, 0, len(cv.Items))
		num := 0
		for _, it := range cv.Items {
			if it.ID != "" {
				ids = append(ids, it.ID)
			}
			c := map[string]any{}
			if it.ID != "" {
				c["id"] = it.ID
			}
			if it.Quantity > 0 {
				c["quantity"] = it.Quantity
				num += int(it.Quantity)
			} else {
				num++
			}
			if it.Price > 0 {
				c["item_price"] = it.Price
			}
			contents = append(contents, c)
		}
		if len(ids) > 0 {
			cd["content_ids"] = ids
		}
		cd["contents"] = contents
		cd["content_type"] = "product"
		cd["num_items"] = num
	}
	if name == "Purchase" && cv.EventID != "" {
		cd["order_id"] = cv.EventID
	}
	return cd
}

// metaSource is where the conversion happened, in Meta's words. A website event
// must name its page and the visitor's browser; a sale the server stated with
// neither, a renewal above all, happened in no browser, and Meta's name for that
// is system_generated.
func metaSource(cv Conversion) string {
	if !cv.Recurring && cv.URL != "" && cv.User.UserAgent != "" {
		return "website"
	}
	return "system_generated"
}

// metaEventTime clamps to now when the event carries no timestamp (Meta rejects a
// zero/absent event_time).
func metaEventTime(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}

// metaUserData builds the advanced-matching user_data: hashed email/phone/external
// id (arrays per Meta), and the un-hashed network signals (ip, user agent, fbp) plus
// a derived fbc. Only present keys are set — Meta requires at least one.
func metaUserData(u UserData) map[string]any {
	ud := map[string]any{}
	if h := u.em(); h != "" {
		ud["em"] = []string{h}
	}
	if h := hashPhone(u.Phone); h != "" {
		ud["ph"] = []string{h}
	}
	if h := sha256hex(strings.ToLower(strings.TrimSpace(u.ExternalID))); h != "" {
		ud["external_id"] = []string{h}
	}
	if u.IP != "" {
		ud["client_ip_address"] = u.IP
	}
	if u.UserAgent != "" {
		ud["client_user_agent"] = u.UserAgent
	}
	if u.FBP != "" {
		ud["fbp"] = u.FBP
	}
	if fbc := fbcOf(u); fbc != "" {
		ud["fbc"] = fbc
	}
	return ud
}

// metaResponse is the CAPI success shape: how many events Meta received.
type metaResponse struct {
	EventsReceived int    `json:"events_received"`
	FBTraceID      string `json:"fbtrace_id"`
}

func (d meta) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	pixel := cfg.get("pixelId")
	if pixel == "" {
		return Result{}, fmt.Errorf("meta: pixelId is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("meta: access_token is required")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	body := metaBuild(cfg, secret, batch)
	if len(body.Data) == 0 {
		return Result{}, nil
	}
	if batch[0].Test && body.TestEventCode == "" {
		return Result{}, fmt.Errorf("meta: set a Test Event Code to test without recording a real %s", body.Data[0].EventName)
	}
	endpoint := metaGraph + "/" + pixel + "/events"
	var resp metaResponse
	if err := postJSON(ctx, metaID, endpoint, nil, body, &resp); err != nil {
		return Result{}, err
	}
	sent := resp.EventsReceived
	if sent == 0 {
		sent = len(body.Data)
	}
	return Result{Sent: sent, Message: resp.FBTraceID}, nil
}
