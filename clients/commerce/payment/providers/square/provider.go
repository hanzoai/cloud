// Package square is the unified Square payment provider. It mirrors the
// braintree/ package shape so the BD tenant-scoped payment resolver can
// call processor.Get(processor.Square) and Configure(...) per-request the
// same way it already does for Braintree.
//
// This package intentionally wraps the existing SDK-heavy client in
// thirdparty/square instead of duplicating the Square Go SDK plumbing.
// The split mirrors the codebase's "transport in thirdparty, lifecycle in
// payment/providers" convention: this file owns the Configure/IsAvailable
// contract the processor registry expects; thirdparty/square owns the
// actual HTTP calls against the Square API.
//
// Credentials are resolved per-request from BD's payment_providers
// collection (see bd/payment_provider_config.go). At init() time we only
// register an empty Provider with the global registry — Configure(cfg) is
// the only path that produces a usable client. This matches how Blue-fix-1
// wired Braintree: env vars are a one-way bootstrap, the DB is truth.
package square

import (
	"context"
	"os"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
	thirdparty "github.com/hanzoai/cloud/clients/commerce/thirdparty/square"
)

// Config holds Square API credentials and environment settings. Field
// names match the BD payment_providers JSON schema (camelCase) so callers
// can unmarshal directly into this struct.
type Config struct {
	// ApplicationID is the Square Developer Dashboard application ID.
	// Required for the Web Payments SDK (browser-side tokenization).
	ApplicationID string
	// AccessToken is the server-side OAuth bearer that authenticates
	// Create/Refund payment calls against the Square API.
	AccessToken string
	// LocationID pins charges to a specific Square merchant location.
	LocationID string
	// WebhookSignatureKey is the HMAC-SHA256 key that verifies
	// payment.* webhook deliveries. Also called "Webhook Signature Key"
	// in the Square dashboard.
	WebhookSignatureKey string
	// WebhookURL is the EXACT notification URL configured in the Square
	// dashboard webhook subscription. Square signs HMAC over
	// notificationURL+body, so this must match the dashboard value (not the
	// inbound request Host). Required to validate live webhook signatures.
	WebhookURL string
	// Environment must be "sandbox" or "production".
	Environment string
}

// Provider implements processor.PaymentProcessor for Square. Every method
// delegates to an underlying *thirdparty.SquareProcessor so there is one
// and only one HTTP client talking to Square. Configure() rebuilds the
// inner client whenever creds rotate — the BD runtime-config resolver
// calls Configure per request, so a key swap via the admin UI is visible
// within one RTT.
type Provider struct {
	*processor.BaseProcessor
	config Config
	inner  *thirdparty.SquareProcessor
}

func init() {
	// Register the provider, seeded from the deployment env when creds are
	// present. BD's per-tenant resolver still calls Configure() before every
	// Charge() (the DB is truth for outbound money); the env seed exists for
	// the SESSIONLESS inbound path — webhook validation. tryValidateWebhook
	// reaches this registry slot directly, with no tenant resolver in front,
	// so an unseeded Provider made every Square delivery 401 "processor not
	// configured": this package imports thirdparty/square, so thirdparty's
	// env-registering init either ran first and was clobbered by this
	// Register, or deferred to the already-registered empty Provider. Either
	// order left the slot unconfigured. Seeding here makes init order moot.
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Square, thirdparty.SquareSupportedCurrencies()),
	}
	if cfg, ok := configFromEnv(); ok {
		p.Configure(cfg)
	}
	processor.Register(p)
}

// configFromEnv builds a Config from the deployment environment — the same
// variables thirdparty/square's standalone init reads (SQUARE_ENVIRONMENT
// picking the sandbox credential set). ok is false when the minimum charge
// credentials (access token + location) are absent; webhook fields may still
// be empty, in which case ValidateWebhook fail-closes per its own contract.
func configFromEnv() (Config, bool) {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("SQUARE_ENVIRONMENT")))
	isSandbox := env == "sandbox" || env == "test"

	token := strings.TrimSpace(os.Getenv("SQUARE_ACCESS_TOKEN"))
	locationID := strings.TrimSpace(os.Getenv("SQUARE_LOCATION_ID"))
	appID := strings.TrimSpace(os.Getenv("SQUARE_APPLICATION_ID"))
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
	if token == "" || locationID == "" {
		return Config{}, false
	}
	return Config{
		ApplicationID:       appID,
		AccessToken:         token,
		LocationID:          locationID,
		WebhookSignatureKey: strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_SIGNATURE_KEY")),
		WebhookURL:          strings.TrimSpace(os.Getenv("SQUARE_WEBHOOK_URL")),
		Environment:         env,
	}, true
}

// NewProvider builds and configures a Provider in one shot. Prefer this
// constructor in unit tests and one-off scripts; BD uses the init()
// + Configure() pattern at runtime.
func NewProvider(cfg Config) *Provider {
	p := &Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Square, thirdparty.SquareSupportedCurrencies()),
	}
	p.Configure(cfg)
	return p
}

// Configure rebuilds the inner SDK client from cfg. Safe to call
// concurrently with other Configure() calls only up to the guarantee
// provided by processor.BaseProcessor.SetConfigured; callers that race
// on Configure must serialise themselves.
func (p *Provider) Configure(cfg Config) {
	p.config = cfg
	if cfg.AccessToken == "" || cfg.LocationID == "" {
		p.inner = nil
		p.SetConfigured(false)
		return
	}
	p.inner = thirdparty.NewProcessor(thirdparty.Config{
		AccessToken:   cfg.AccessToken,
		LocationID:    cfg.LocationID,
		WebhookSecret: cfg.WebhookSignatureKey,
		WebhookURL:    cfg.WebhookURL,
		Environment:   cfg.Environment,
	})
	p.SetConfigured(true)
}

// Type returns the processor type.
func (p *Provider) Type() processor.ProcessorType {
	return processor.Square
}

// IsAvailable reports whether the processor has been configured. Does not
// issue a network probe — that is the /test endpoint's job in BD.
func (p *Provider) IsAvailable(ctx context.Context) bool {
	return p.inner != nil && p.config.AccessToken != "" && p.config.LocationID != ""
}

// SupportedCurrencies returns the currencies Square supports. Delegates
// to thirdparty to keep the list in one place.
func (p *Provider) SupportedCurrencies() []currency.Type {
	return thirdparty.SquareSupportedCurrencies()
}

// ApplicationID returns the browser-facing application ID. The Web
// Payments SDK needs this plus the locationID to initialise.
func (p *Provider) ApplicationID() string { return p.config.ApplicationID }

// LocationID returns the configured merchant location. Exposed so BD can
// hand it to the fund SPA alongside the application id.
func (p *Provider) LocationID() string { return p.config.LocationID }

// Environment returns "sandbox" or "production". BD uses this to pick the
// correct Web Payments SDK URL (sandbox.web.squarecdn.com vs web.squarecdn.com).
func (p *Provider) Environment() string { return p.config.Environment }

// Compile-time interface check.
var _ processor.PaymentProcessor = (*Provider)(nil)
