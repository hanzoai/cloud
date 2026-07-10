package square

import (
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

func init() {
	// Idempotent: if payment/providers/square has already registered a
	// richer Provider (the unified per-tenant shape BD expects), do
	// nothing. Only self-register when this package is imported standalone
	// (api/checkout/square/*) without the unified provider loaded.
	if existing, err := processor.Get(processor.Square); err == nil && existing != nil {
		// If the already-registered processor is this package's
		// SquareProcessor, we leave it alone (re-init is a no-op).
		// Otherwise a unified provider has claimed the slot and we
		// defer to it — BD will Configure() it per-tenant from KMS.
		return
	}

	env := strings.ToLower(strings.TrimSpace(os.Getenv("SQUARE_ENVIRONMENT")))
	isSandbox := env == "sandbox" || env == "test"

	token := strings.TrimSpace(os.Getenv("SQUARE_ACCESS_TOKEN"))
	locationID := strings.TrimSpace(os.Getenv("SQUARE_LOCATION_ID"))
	appID := strings.TrimSpace(os.Getenv("SQUARE_APPLICATION_ID"))
	webhookKey := strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_SIGNATURE_KEY"))
	// Exact dashboard notification URL — Square signs HMAC over URL+body.
	webhookURL := strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_URL"))

	if isSandbox {
		if t := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_ACCESS_TOKEN")); t != "" {
			token = t
		}
		if l := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_LOCATION_ID")); l != "" {
			locationID = l
		}
		if a := strings.TrimSpace(os.Getenv("SQUARE_SANDBOX_APPLICATION_ID")); a != "" {
			appID = a
		}
		env = "sandbox"
	} else if env == "" {
		env = "production"
	}

	_ = appID // ApplicationID stored for future use (OAuth flows, etc.)

	p := NewProcessor(Config{
		AccessToken:   token,
		LocationID:    locationID,
		WebhookSecret: webhookKey,
		WebhookURL:    webhookURL,
		Environment:   env,
	})

	processor.Register(p)
}
