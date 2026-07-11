// Copyright © 2026 Hanzo AI. MIT License.

package square

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

// TestConfigFromEnvSeedsWebhookValidation is the regression guard for the
// "square processor not configured" 401 on every inbound webhook: the
// registry Provider must be usable for SESSIONLESS signature validation
// straight from deployment env, with no per-tenant Configure() in front.
// It drives the REAL verification path end-to-end: env → configFromEnv →
// Configure → ValidateWebhook over a Square-spec signature
// (base64(HMAC-SHA256(key, notificationURL+body))).
func TestConfigFromEnvSeedsWebhookValidation(t *testing.T) {
	const (
		key = "test-signature-key"
		url = "https://pay.example.test/v1/billing/webhooks/square"
	)
	t.Setenv("SQUARE_ENVIRONMENT", "production")
	t.Setenv("SQUARE_ACCESS_TOKEN", "tok")
	t.Setenv("SQUARE_LOCATION_ID", "loc")
	t.Setenv("SQUARE_APPLICATION_ID", "app")
	t.Setenv("SQUARE_WEBHOOK_SIGNATURE_KEY", key)
	t.Setenv("SQUARE_WEBHOOK_URL", url)

	cfg, ok := configFromEnv()
	if !ok {
		t.Fatal("configFromEnv() not ok with full env")
	}
	p := NewProvider(cfg)
	if !p.IsAvailable(context.Background()) {
		t.Fatal("env-seeded provider not available")
	}

	body := []byte(fmt.Sprintf(
		`{"merchant_id":"m","type":"test.ping","event_id":"seed-test","created_at":%q,"data":{"type":"t","id":"i","object":{}}}`,
		time.Now().UTC().Format(time.RFC3339)))
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(url))
	mac.Write(body)
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	evt, err := p.ValidateWebhook(context.Background(), body, sig)
	if err != nil || evt == nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if evt.ID != "seed-test" {
		t.Errorf("event id = %q", evt.ID)
	}

	if _, err := p.ValidateWebhook(context.Background(), append(body, ' '), sig); err == nil {
		t.Error("tampered body accepted")
	}
}

// TestConfigFromEnvSandboxAndAbsent covers the credential-set switch and the
// disabled path.
func TestConfigFromEnvSandboxAndAbsent(t *testing.T) {
	t.Setenv("SQUARE_ENVIRONMENT", "sandbox")
	t.Setenv("SQUARE_ACCESS_TOKEN", "prod-tok")
	t.Setenv("SQUARE_LOCATION_ID", "prod-loc")
	t.Setenv("SQUARE_SANDBOX_ACCESS_TOKEN", "sb-tok")
	t.Setenv("SQUARE_SANDBOX_LOCATION_ID", "sb-loc")
	t.Setenv("SQUARE_WEBHOOK_SIGNATURE_KEY", "")
	t.Setenv("SQUARE_WEBHOOK_URL", "")

	cfg, ok := configFromEnv()
	if !ok || cfg.AccessToken != "sb-tok" || cfg.LocationID != "sb-loc" || cfg.Environment != "sandbox" {
		t.Errorf("sandbox switch broken: ok=%v cfg=%+v", ok, cfg)
	}

	t.Setenv("SQUARE_ENVIRONMENT", "")
	t.Setenv("SQUARE_ACCESS_TOKEN", "")
	t.Setenv("SQUARE_SANDBOX_ACCESS_TOKEN", "")
	if _, ok := configFromEnv(); ok {
		t.Error("configFromEnv ok without an access token")
	}
}
