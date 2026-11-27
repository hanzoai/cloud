package destination

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// openai.go forwards conversions to the OpenAI Conversions API (server-side).
// Config: pixelId (the OpenAI measurement pixel id, e.g. TXKHZduRxZZGp4B1ysfiwm)
// and optional validateOnly ("true" to validate without recording).
// Secret: api_key — rides the Authorization: Bearer header; PII match keys are
// SHA-256 hashed before leaving.

const openaiID = "openai"

var openaiEventsURL = "https://bzr.openai.com/v1/events"

var openaiEventName = map[StandardEvent]string{
	EventPageView:      "page_view",
	EventViewContent:   "view_content",
	EventViewList:      "view_content",
	EventSearch:        "search",
	EventLead:          "lead",
	EventSignUp:        "sign_up",
	EventStartCheckout: "checkout_started",
	EventAddToCart:     "add_to_cart",
	EventPurchase:      "purchase",
	EventContact:       "lead",
}

type openaiDest struct{}

func init() { register(openaiDest{}) }

func (openaiDest) ID() string       { return openaiID }
func (openaiDest) Name() string     { return "OpenAI" }
func (openaiDest) Category() string { return categoryAdvertising }

func (openaiDest) Spec() Spec {
	return Spec{
		Fields: []DestinationField{
			{Key: "pixelId", Label: "Pixel ID", Required: true, Example: "TXKHZduRxZZGp4B1ysfiwm"},
			{Key: "validateOnly", Label: "Validate Only", Required: false, Example: "false"},
		},
		Secrets: []string{"api_key"},
	}
}

type openaiBody struct {
	ValidateOnly bool          `json:"validate_only"`
	Events       []openaiEvent `json:"events"`
}

type openaiEvent struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	TimestampMS  int64          `json:"timestamp_ms"`
	SourceURL    string         `json:"source_url,omitempty"`
	ActionSource string         `json:"action_source"`
	Data         map[string]any `json:"data,omitempty"`
	User         map[string]any `json:"user,omitempty"`
}

func openaiValidateOnly(cfg Config, batch []Conversion) bool {
	v := strings.ToLower(cfg.get("validateOnly"))
	if v == "true" || v == "1" {
		return true
	}
	if len(batch) == 0 {
		return false
	}
	for _, cv := range batch {
		if !cv.Test {
			return false
		}
	}
	return true
}

// openaiBuild renders a batch into the OpenAI Conversions API JSON payload.
func openaiBuild(cfg Config, batch []Conversion) openaiBody {
	events := make([]openaiEvent, 0, len(batch))
	for _, cv := range batch {
		for _, typ := range namesOn(openaiID, cv, openaiEventName) {
			e := openaiEvent{
				ID:           cv.EventID,
				Type:         typ,
				TimestampMS:  eventTimeMS(cv.Time),
				SourceURL:    cv.URL,
				ActionSource: "web",
			}
			data := map[string]any{}
			if typ == "checkout_started" || cv.Standard == EventStartCheckout || cv.Name == "checkout_started" {
				data["type"] = "contents"
			}
			if cv.Value > 0 {
				data["value"] = cv.Value
				data["currency"] = cv.Currency
			}
			if len(data) > 0 {
				e.Data = data
			}
			if user := openaiUser(cv.User); len(user) > 0 {
				e.User = user
			}
			events = append(events, e)
		}
	}
	return openaiBody{
		ValidateOnly: openaiValidateOnly(cfg, batch),
		Events:       events,
	}
}

func openaiUser(u UserData) map[string]any {
	user := map[string]any{}
	if h := u.em(); h != "" {
		user["email"] = h
	}
	if h := hashPhone(u.Phone); h != "" {
		user["phone"] = h
	}
	if h := sha256hex(strings.ToLower(strings.TrimSpace(u.ExternalID))); h != "" {
		user["external_id"] = h
	}
	if oppref := u.click("oppref"); oppref != "" {
		user["click_id"] = oppref
	}
	if u.IP != "" {
		user["ip_address"] = u.IP
	}
	if u.UserAgent != "" {
		user["user_agent"] = u.UserAgent
	}
	return user
}

func (d openaiDest) Send(ctx context.Context, cfg Config, secret string, batch []Conversion) (Result, error) {
	pixel := cfg.get("pixelId")
	if pixel == "" {
		return Result{}, fmt.Errorf("openai: pixelId is required")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return Result{}, fmt.Errorf("openai: api_key is required")
	}
	if len(batch) == 0 {
		return Result{}, nil
	}
	body := openaiBuild(cfg, batch)
	if len(body.Events) == 0 {
		return Result{}, nil
	}
	endpoint := openaiEventsURL + "?pid=" + url.QueryEscape(pixel)
	headers := map[string]string{
		"Authorization": "Bearer " + secret,
	}
	if err := postJSON(ctx, openaiID, endpoint, headers, body, nil); err != nil {
		return Result{}, err
	}
	return Result{Sent: len(body.Events)}, nil
}
