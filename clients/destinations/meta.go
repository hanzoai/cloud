package destinations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// meta.go forwards conversions to Meta (Facebook + Instagram) via the Conversions
// API — the server-side complement of the Meta Pixel, sharing the pixel id and the
// event_id so a browser pixel event and this server event DEDUPLICATE. Non-secret
// config: pixelId (the dataset/pixel id) and an optional testEventCode (Events
// Manager "Test Events"). Secret: access_token — a CAPI token from Events Manager,
// OR, when none is sealed locally, the org's existing meta_ads OAuth token, reused
// through the integrations custody seam (Spec.Fallback = "meta_ads").
//
// The access_token rides the JSON body (never the URL). PII match keys (email, phone,
// external id) are SHA-256 hashed before send per Meta's advanced-matching contract;
// the ip, user agent, fbp cookie, and a derived fbc ride as Meta specifies.

const metaID = "meta"

// metaGraph is the Graph API base. A package var so a test points it at a mock
// server; never mutated in production.
var metaGraph = "https://graph.facebook.com/v21.0"

// metaEventName maps the normalized taxonomy onto Meta's standard event names. A
// StandardEvent absent here (EventCustom) forwards under the raw canonical name (a
// Meta custom event).
var metaEventName = map[StandardEvent]string{
	EventPageView:      "PageView",
	EventViewContent:   "ViewContent",
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
		Fields: []Field{
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
}

// metaBuild renders the batch into the CAPI body. Pure — tests assert the payload
// shape (hashed match keys, dedup event_id, value/currency) without a network call.
func metaBuild(cfg Config, secret string, batch []Conversion) metaBody {
	data := make([]metaEvent, 0, len(batch))
	for _, cv := range batch {
		name := metaEventName[cv.Standard]
		if name == "" {
			name = cv.Name
		}
		e := metaEvent{
			EventName:      name,
			EventTime:      metaEventTime(cv.Time),
			EventID:        cv.EventID,
			ActionSource:   "website",
			EventSourceURL: cv.URL,
			UserData:       metaUserData(cv.User),
		}
		if cv.Value > 0 {
			e.CustomData = map[string]any{"value": cv.Value, "currency": cv.Currency}
		}
		data = append(data, e)
	}
	return metaBody{Data: data, AccessToken: secret, TestEventCode: cfg.get("testEventCode")}
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
	if h := hashEmail(u.Email); h != "" {
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
	if fbc := metaFBC(u); fbc != "" {
		ud["fbc"] = fbc
	}
	return ud
}

// metaFBC builds Meta's click-id cookie value from an fbclid: fb.1.<unix_ms>.<fbclid>
// — the format Meta expects when the raw _fbc cookie is unavailable.
func metaFBC(u UserData) string {
	fbclid := u.click("fbclid")
	if fbclid == "" {
		return ""
	}
	return "fb.1." + strconv.FormatInt(time.Now().UnixMilli(), 10) + "." + fbclid
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
	endpoint := metaGraph + "/" + pixel + "/events"
	var resp metaResponse
	if err := postJSON(ctx, metaID, endpoint, nil, body, &resp); err != nil {
		return Result{}, err
	}
	sent := resp.EventsReceived
	if sent == 0 {
		sent = len(batch)
	}
	return Result{Sent: sent, Message: resp.FBTraceID}, nil
}
