package destination

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
)

func TestOpenAISpecAndPixel(t *testing.T) {
	d := openaiDest{}
	if d.ID() != "openai" || d.Name() != "OpenAI" || d.Category() != categoryAdvertising {
		t.Fatalf("unexpected identity: %s %s %s", d.ID(), d.Name(), d.Category())
	}
	spec := d.Spec()
	if len(spec.Fields) != 2 || spec.Fields[0].Key != "pixelId" || !spec.Fields[0].Required || spec.Fields[0].Example != "TXKHZduRxZZGp4B1ysfiwm" {
		t.Fatalf("spec fields: %+v", spec.Fields)
	}
	if spec.Fields[1].Key != "validateOnly" || spec.Fields[1].Required {
		t.Fatalf("validateOnly field: %+v", spec.Fields[1])
	}
	if len(spec.Secrets) != 1 || spec.Secrets[0] != "api_key" {
		t.Fatalf("spec secrets: %+v", spec.Secrets)
	}
	if !event.HasPixel("openai") {
		t.Fatal("event.HasPixel(\"openai\") must be true")
	}
}

func TestOpenAIBuildCheckoutStartedExactShape(t *testing.T) {
	ts := time.UnixMilli(1795580000000)
	body := openaiBuild(Config{"pixelId": "TXKHZduRxZZGp4B1ysfiwm"}, []Conversion{{
		Standard: EventStartCheckout,
		Name:     "checkout_started",
		EventID:  "evt-123",
		Time:     ts,
		URL:      "https://pay.hanzo.ai/confirm/card",
	}})
	if body.ValidateOnly {
		t.Fatal("validate_only must default to false")
	}
	if len(body.Events) != 1 {
		t.Fatalf("events len = %d, want 1", len(body.Events))
	}
	ev := body.Events[0]
	if ev.ID != "evt-123" || ev.Type != "checkout_started" || ev.TimestampMS != 1795580000000 {
		t.Fatalf("event header mismatch: %+v", ev)
	}
	if ev.SourceURL != "https://pay.hanzo.ai/confirm/card" || ev.ActionSource != "web" {
		t.Fatalf("source/action mismatch: %+v", ev)
	}
	if ev.Data["type"] != "contents" || len(ev.Data) != 1 {
		t.Fatalf("checkout_started data = %+v, want {type: contents}", ev.Data)
	}
	if ev.User != nil {
		t.Fatalf("empty UserData must omit user, got %+v", ev.User)
	}
}

func TestOpenAIBuildFunnelPIIAndValidateOnly(t *testing.T) {
	ts := time.UnixMilli(1795580000000)
	batch := []Conversion{
		{
			Standard: EventStartCheckout,
			Name:     "checkout_started",
			EventID:  "e-chk",
			Time:     ts,
			Value:    20,
			Currency: "USD",
			URL:      "https://hanzo.ai/pricing",
			User: UserData{
				Email:      "  Alice@Example.COM ",
				Phone:      "+1 (555) 123-4567",
				ExternalID: "USR-9",
				IP:         "203.0.113.10",
				UserAgent:  "Mozilla/5.0",
				Clicks:     map[string]string{"oppref": "opp_99"},
			},
		},
		{
			Standard: EventPurchase,
			Name:     "order_completed",
			EventID:  "ord-99",
			Time:     ts,
			Value:    49,
			Currency: "USD",
		},
		{
			Standard: EventSignUp,
			Name:     "signup_completed",
			EventID:  "sig-1",
			Time:     ts,
		},
		{
			Standard: EventLead,
			Name:     "sales_contacted",
			EventID:  "lead-1",
			Time:     ts,
		},
	}
	body := openaiBuild(Config{"pixelId": "TXKHZduRxZZGp4B1ysfiwm", "validateOnly": "true"}, batch)
	if !body.ValidateOnly {
		t.Fatal("validateOnly=true config must set validate_only: true")
	}
	if len(body.Events) != 4 {
		t.Fatalf("events len = %d, want 4", len(body.Events))
	}
	if body.Events[0].Type != "checkout_started" || body.Events[0].Data["type"] != "contents" || body.Events[0].Data["value"] != 20.0 || body.Events[0].Data["currency"] != "USD" {
		t.Fatalf("priced checkout_started: %+v", body.Events[0])
	}
	u := body.Events[0].User
	if u["email"] != sha("alice@example.com") || u["phone"] != sha("15551234567") || u["external_id"] != sha("usr-9") || u["click_id"] != "opp_99" {
		t.Fatalf("hashed user fields wrong: %+v", u)
	}
	if body.Events[1].Type != "purchase" || body.Events[1].Data["value"] != 49.0 || body.Events[1].Data["type"] != nil {
		t.Fatalf("purchase event: %+v", body.Events[1])
	}
	if body.Events[2].Type != "sign_up" || body.Events[3].Type != "lead" {
		t.Fatalf("signup/lead types: %q %q", body.Events[2].Type, body.Events[3].Type)
	}
	raw, _ := json.Marshal(body)
	if strings.Contains(strings.ToLower(string(raw)), "alice@example.com") {
		t.Fatal("raw email leaked into OpenAI CAPI payload")
	}
}

func TestOpenAISendEndToEnd(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotQuery  url.Values
		gotAuth   string
		gotCT     string
		gotBody   openaiBody
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"received":1}`))
	}))
	defer srv.Close()

	prev := openaiEventsURL
	openaiEventsURL = srv.URL + "/v1/events"
	defer func() { openaiEventsURL = prev }()

	res, err := openaiDest{}.Send(
		context.Background(),
		Config{"pixelId": "TXKHZduRxZZGp4B1ysfiwm"},
		"sk-oai-capi-key",
		[]Conversion{{
			Standard: EventStartCheckout,
			Name:     "checkout_started",
			EventID:  "evt-live-1",
			Time:     time.UnixMilli(1795580000000),
			URL:      "https://hanzo.ai/pricing",
		}},
	)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if res.Sent != 1 {
		t.Fatalf("Sent = %d, want 1", res.Sent)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/events" || gotQuery.Get("pid") != "TXKHZduRxZZGp4B1ysfiwm" {
		t.Fatalf("request target = %s %s?%s", gotMethod, gotPath, gotQuery.Encode())
	}
	if gotAuth != "Bearer sk-oai-capi-key" || gotCT != "application/json" {
		t.Fatalf("headers: auth=%q content-type=%q", gotAuth, gotCT)
	}
	if gotBody.ValidateOnly || len(gotBody.Events) != 1 || gotBody.Events[0].Data["type"] != "contents" {
		t.Fatalf("wire body: %+v", gotBody)
	}

	if _, err := (openaiDest{}).Send(context.Background(), Config{}, "sk", nil); err == nil {
		t.Fatal("missing pixelId must error")
	}
	if _, err := (openaiDest{}).Send(context.Background(), Config{"pixelId": "TXKHZduRxZZGp4B1ysfiwm"}, "  ", nil); err == nil {
		t.Fatal("missing api_key must error")
	}
}
