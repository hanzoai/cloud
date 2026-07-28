package destinations

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// tiktok.go forwards conversions to TikTok via the Events API (server-side). Config:
// pixelCode (the web-event source id). Secret: access_token — its own Events API
// token, or the org's tiktok_ads OAuth token via the integrations fallback. The
// token rides the Access-Token header (never the URL); PII match keys are SHA-256
// hashed per TikTok's contract. Same interface as GA4/Meta.

const tiktokID = "tiktok"

var tiktokTrack = "https://business-api.tiktok.com/open_api/v1.3/event/track/"

var tiktokEventName = map[StandardEvent]string{
	EventPageView:      "Pageview",
	EventViewContent:   "ViewContent",
	EventSearch:        "Search",
	EventLead:          "SubmitForm",
	EventSignUp:        "CompleteRegistration",
	EventStartCheckout: "InitiateCheckout",
	EventAddToCart:     "AddToCart",
	EventPurchase:      "CompletePayment",
	EventContact:       "Contact",
}

type tiktok struct{}

func init() { register(tiktok{}) }

func (tiktok) ID() string       { return tiktokID }
func (tiktok) Name() string     { return "TikTok" }
func (tiktok) Category() string { return categoryAdvertising }

func (tiktok) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "pixelCode", Label: "Pixel Code", Required: true, Example: "C1A2B3..."},
		},
		Secrets:  []string{"access_token"},
		Fallback: "tiktok_ads",
	}
}

type tiktokBody struct {
	EventSource   string        `json:"event_source"`
	EventSourceID string        `json:"event_source_id"`
	Data          []tiktokEvent `json:"data"`
}

type tiktokEvent struct {
	Event      string         `json:"event"`
	EventTime  int64          `json:"event_time"`
	EventID    string         `json:"event_id,omitempty"`
	User       map[string]any `json:"user"`
	Page       map[string]any `json:"page,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

// tiktokBuild renders the batch into the Events API body. Pure — tests assert shape.
func tiktokBuild(cfg Config, batch []Conversion) tiktokBody {
	data := make([]tiktokEvent, 0, len(batch))
	for _, cv := range batch {
		name := tiktokEventName[cv.Standard]
		if name == "" {
			name = cv.Name
		}
		e := tiktokEvent{
			Event:     name,
			EventTime: metaEventTime(cv.Time),
			EventID:   cv.EventID,
			User:      tiktokUser(cv.User),
		}
		if cv.URL != "" {
			e.Page = map[string]any{"url": cv.URL}
		}
		if cv.Value > 0 {
			e.Properties = map[string]any{"value": cv.Value, "currency": cv.Currency}
		}
		data = append(data, e)
	}
	return tiktokBody{EventSource: "web", EventSourceID: cfg.get("pixelCode"), Data: data}
}

// tiktokUser builds TikTok's user block: hashed email/phone/external id (single hex
// strings), plus the ttclid, ip, and user agent when present.
func tiktokUser(u UserData) map[string]any {
	user := map[string]any{}
	if h := hashEmail(u.Email); h != "" {
		user["email"] = h
	}
	if h := hashPhone(u.Phone); h != "" {
		user["phone"] = h
	}
	if h := sha256hex(strings.ToLower(strings.TrimSpace(u.ExternalID))); h != "" {
		user["external_id"] = h
	}
	if ttclid := u.click("ttclid"); ttclid != "" {
		user["ttclid"] = ttclid
	}
	if u.IP != "" {
		user["ip"] = u.IP
	}
	if u.UserAgent != "" {
		user["user_agent"] = u.UserAgent
	}
	return user
}

func (d tiktok) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	if cfg.get("pixelCode") == "" {
		return Result{}, fmt.Errorf("tiktok: pixelCode is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("tiktok: access_token is required")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	body := tiktokBuild(cfg, batch)
	headers := map[string]string{"Access-Token": secret}
	// TikTok wraps errors in a {code,message} envelope; a transport-level non-2xx is
	// surfaced by postJSON. code!=0 in a 200 body is a soft failure we report as-is.
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := postJSON(ctx, tiktokID, tiktokTrack, headers, body, &resp); err != nil {
		return Result{}, err
	}
	if resp.Code != 0 {
		return Result{}, fmt.Errorf("tiktok: code %d: %s", resp.Code, resp.Message)
	}
	return Result{Sent: len(batch), Message: resp.Message}, nil
}

// eventTimeMS renders a conversion time as unix milliseconds (LinkedIn), clamping a
// zero time to now. Shared by the scaffold adapters that key on ms.
func eventTimeMS(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().UnixMilli()
	}
	return t.UnixMilli()
}
