package destinations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// pinterest.go forwards conversions to the Pinterest Conversions API v5 (server-side)
// — the server-side complement of the Pinterest tag, sharing the event_id so a browser
// tag event and this server event DEDUPLICATE. Config: adAccountId (the Pinterest Ads
// account the events file under). Secret: access_token — its own token, or the org's
// pinterest_ads OAuth token via the integrations fallback. The token rides the
// Authorization: Bearer header; email/phone/external id are SHA-256 hashed per
// Pinterest's advanced-matching contract; the epik click id and the ip/user-agent ride
// as Pinterest specifies. Same interface as GA4/Meta.

const pinterestID = "pinterest"

// pinterestAPI is the v5 base. A package var so a test points it at a mock server;
// never mutated in production.
var pinterestAPI = "https://api.pinterest.com/v5"

// pinterestEventName maps the normalized taxonomy onto Pinterest's conversion event
// names — a CLOSED enum, unlike Meta's free-form custom names. A StandardEvent absent
// here (EventCustom) forwards as the "custom" enum value: the specific canonical name
// stays on the warehouse row while Pinterest groups it as custom, because the v5 API
// rejects an event_name outside its enum.
var pinterestEventName = map[StandardEvent]string{
	EventPageView:      "page_visit",
	EventViewContent:   "view_category",
	EventSearch:        "search",
	EventLead:          "lead",
	EventSignUp:        "signup",
	EventStartCheckout: "checkout",
	EventAddToCart:     "add_to_cart",
	EventPurchase:      "checkout",
	EventContact:       "lead",
}

type pinterest struct{}

func init() { register(pinterest{}) }

func (pinterest) ID() string       { return pinterestID }
func (pinterest) Name() string     { return "Pinterest" }
func (pinterest) Category() string { return categoryAdvertising }

func (pinterest) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "adAccountId", Label: "Ad Account ID", Required: true, Example: "549755885123"},
		},
		Secrets:  []string{"access_token"},
		Fallback: "pinterest_ads", // reuse the integrations Pinterest connection's token
	}
}

type pinterestBody struct {
	Data []pinterestEvent `json:"data"`
}

type pinterestEvent struct {
	EventName      string         `json:"event_name"`
	ActionSource   string         `json:"action_source"`
	EventTime      int64          `json:"event_time"`
	EventID        string         `json:"event_id,omitempty"`
	EventSourceURL string         `json:"event_source_url,omitempty"`
	UserData       map[string]any `json:"user_data"`
	CustomData     map[string]any `json:"custom_data,omitempty"`
}

// pinterestBuild renders the batch into the Conversions API body. Pure — tests assert
// the event-name mapping, hashed match keys, and dedup event_id without a network call.
func pinterestBuild(batch []Conversion) pinterestBody {
	data := make([]pinterestEvent, 0, len(batch))
	for _, cv := range batch {
		name := pinterestEventName[cv.Standard]
		if name == "" {
			name = "custom"
		}
		e := pinterestEvent{
			EventName:      name,
			ActionSource:   "web",
			EventTime:      pinterestTime(cv.Time),
			EventID:        cv.EventID,
			EventSourceURL: cv.URL,
			UserData:       pinterestUser(cv.User),
		}
		if cd := pinterestCustomData(cv); len(cd) > 0 {
			e.CustomData = cd
		}
		data = append(data, e)
	}
	return pinterestBody{Data: data}
}

// pinterestCustomData renders a conversion's commerce fields into Pinterest's
// custom_data. Pinterest wants monetary VALUES as STRINGS (value, item_price); a
// purchase also carries order_id, its native dedup key. Empty when the event carries
// neither value nor items.
func pinterestCustomData(cv Conversion) map[string]any {
	cd := map[string]any{}
	if cv.Value > 0 {
		cd["value"] = strconv.FormatFloat(cv.Value, 'f', -1, 64)
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
			if it.Quantity > 0 {
				c["quantity"] = it.Quantity
				num += int(it.Quantity)
			} else {
				num++
			}
			if it.Price > 0 {
				c["item_price"] = strconv.FormatFloat(it.Price, 'f', -1, 64)
			}
			contents = append(contents, c)
		}
		if len(ids) > 0 {
			cd["content_ids"] = ids
		}
		cd["contents"] = contents
		cd["num_items"] = num
	}
	if cv.Standard == EventPurchase && cv.EventID != "" {
		cd["order_id"] = cv.EventID
	}
	return cd
}

// pinterestTime clamps to now when the event carries no timestamp.
func pinterestTime(t time.Time) int64 {
	if t.IsZero() {
		return time.Now().Unix()
	}
	return t.Unix()
}

// pinterestUser builds the advanced-matching user_data: hashed email/phone/external id
// (arrays per Pinterest), the epik click id (un-hashed), and the un-hashed network
// signals (ip, user agent). Only present keys are set — Pinterest requires at least one.
func pinterestUser(u UserData) map[string]any {
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
	if ck := u.click("epik"); ck != "" {
		ud["click_id"] = ck
	}
	return ud
}

// pinterestResponse is the v5 events success shape: how many events Pinterest received.
type pinterestResponse struct {
	NumEventsReceived  int `json:"num_events_received"`
	NumEventsProcessed int `json:"num_events_processed"`
}

func (d pinterest) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	account := cfg.get("adAccountId")
	if account == "" {
		return Result{}, fmt.Errorf("pinterest: adAccountId is required")
	}
	if strings.TrimSpace(secret) == "" {
		return Result{}, fmt.Errorf("pinterest: access_token is required")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	body := pinterestBuild(batch)
	endpoint := pinterestAPI + "/ad_accounts/" + account + "/events"
	headers := map[string]string{"Authorization": "Bearer " + secret}
	var resp pinterestResponse
	if err := postJSON(ctx, pinterestID, endpoint, headers, body, &resp); err != nil {
		return Result{}, err
	}
	sent := resp.NumEventsReceived
	if sent == 0 {
		sent = len(batch)
	}
	return Result{Sent: sent}, nil
}
