package square

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// squareSign reproduces Square's documented webhook signing: base64 of
// HMAC-SHA256 over (notificationURL + rawBody) keyed by the signature key.
// See https://developer.squareup.com/docs/webhooks/step3validate
func squareSign(key, notificationURL string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(notificationURL))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestValidateWebhook_SignsOverURLPlusBody(t *testing.T) {
	const (
		key = "test-signature-key"
		url = "https://commerce.hanzo.ai/v1/billing/webhooks/square"
	)
	body := []byte(`{"event_id":"evt_1","type":"payment.updated","data":{"type":"payment","id":"pay_1","object":{"id":"pay_1","status":"COMPLETED"}}}`)

	sp := NewProcessor(Config{
		AccessToken:   "tok",
		LocationID:    "loc",
		WebhookSecret: key,
		WebhookURL:    url,
		Environment:   "production",
	})

	// Correct signature (URL + body) must validate.
	good := squareSign(key, url, body)
	evt, err := sp.ValidateWebhook(nil, body, good)
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if evt == nil || evt.ID != "evt_1" || evt.Type != "payment.updated" {
		t.Fatalf("unexpected parsed event: %+v", evt)
	}

	// Body-only signature (the old, wrong algorithm) must NOT validate —
	// this is the regression guard for the real-callback bug.
	bodyOnly := squareSign(key, "", body)
	if _, err := sp.ValidateWebhook(nil, body, bodyOnly); err == nil {
		t.Fatal("body-only signature was accepted; URL prefix not enforced")
	}

	// Wrong URL must NOT validate.
	wrongURL := squareSign(key, "https://evil.example/webhook", body)
	if _, err := sp.ValidateWebhook(nil, body, wrongURL); err == nil {
		t.Fatal("signature for wrong URL was accepted")
	}
}

func TestValidateWebhook_MissingURLFailsClosed(t *testing.T) {
	sp := NewProcessor(Config{
		AccessToken:   "tok",
		LocationID:    "loc",
		WebhookSecret: "k",
		// WebhookURL intentionally empty.
		Environment: "production",
	})
	if _, err := sp.ValidateWebhook(nil, []byte(`{}`), "anything"); err == nil {
		t.Fatal("expected failure when webhook URL is unconfigured")
	}
}
