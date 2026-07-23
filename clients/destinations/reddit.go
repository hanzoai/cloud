package destinations

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// reddit.go forwards conversions to the Reddit Conversions API (server-side). Config:
// accountId (the Reddit Ads account the pixel lives under). Secret: access_token —
// its own token, or the org's reddit_ads OAuth token via the integrations fallback.
// The token rides the Authorization: Bearer header; email/external id are SHA-256
// hashed. Same interface as GA4/Meta.

const redditID = "reddit"

var redditEventsURL = "https://ads-api.reddit.com/api/v2.0/conversions/events"

// redditTracking maps the normalized taxonomy onto Reddit's conversion tracking
// types. EventCustom forwards under tracking_type "Custom" with the raw name.
var redditTracking = map[StandardEvent]string{
	EventPageView:      "PageVisit",
	EventViewContent:   "ViewContent",
	EventSearch:        "Search",
	EventLead:          "Lead",
	EventSignUp:        "SignUp",
	EventStartCheckout: "AddToCart",
	EventAddToCart:     "AddToCart",
	EventPurchase:      "Purchase",
	EventContact:       "Lead",
}

type reddit struct{}

func init() { register(reddit{}) }

func (reddit) ID() string       { return redditID }
func (reddit) Name() string     { return "Reddit" }
func (reddit) Category() string { return categoryAdvertising }

func (reddit) Spec() Spec {
	return Spec{
		Fields: []Field{
			{Key: "accountId", Label: "Ad Account ID", Required: true, Example: "a2_abc123"},
		},
		Secrets:  []string{"access_token"},
		Fallback: "reddit_ads",
	}
}

type redditBody struct {
	Events []redditEvent `json:"events"`
}

type redditEvent struct {
	EventAt       string         `json:"event_at"`
	EventType     map[string]any `json:"event_type"`
	ClickID       string         `json:"click_id,omitempty"`
	User          map[string]any `json:"user"`
	EventMetadata map[string]any `json:"event_metadata,omitempty"`
}

// redditBuild renders the batch into the Conversions API body. Pure — tests assert
// the tracking-type mapping + hashed match keys.
func redditBuild(batch []Conversion) redditBody {
	events := make([]redditEvent, 0, len(batch))
	for _, cv := range batch {
		tt := redditTracking[cv.Standard]
		et := map[string]any{}
		if tt == "" {
			et["tracking_type"] = "Custom"
			et["custom_event_name"] = cv.Name
		} else {
			et["tracking_type"] = tt
		}
		e := redditEvent{
			EventAt:   redditTime(cv.Time),
			EventType: et,
			ClickID:   cv.User.click("rdt_cid"),
			User:      redditUser(cv.User),
		}
		md := map[string]any{}
		if cv.EventID != "" {
			md["conversion_id"] = cv.EventID
		}
		if cv.Value > 0 {
			md["value_decimal"] = cv.Value
			md["currency"] = cv.Currency
		}
		if len(md) > 0 {
			e.EventMetadata = md
		}
		events = append(events, e)
	}
	return redditBody{Events: events}
}

func redditUser(u UserData) map[string]any {
	user := map[string]any{}
	if h := hashEmail(u.Email); h != "" {
		user["email"] = h
	}
	if h := sha256hex(strings.ToLower(strings.TrimSpace(u.ExternalID))); h != "" {
		user["external_id"] = h
	}
	if u.IP != "" {
		user["ip_address"] = sha256hex(strings.TrimSpace(u.IP))
	}
	if u.UserAgent != "" {
		user["user_agent"] = u.UserAgent
	}
	return user
}

func redditTime(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339)
}

func (d reddit) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	account := cfg.get("accountId")
	if account == "" {
		return Result{}, fmt.Errorf("reddit: accountId is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("reddit: access_token is required")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	body := redditBuild(batch)
	endpoint := redditEventsURL + "/" + account
	headers := map[string]string{
		"Authorization": "Bearer " + secret,
		"User-Agent":    "hanzo-cloud/1.0 (+https://hanzo.ai)",
	}
	if err := postJSON(ctx, redditID, endpoint, headers, body, nil); err != nil {
		return Result{}, err
	}
	return Result{Sent: len(batch)}, nil
}
