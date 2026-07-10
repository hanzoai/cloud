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
	// Register an empty provider. BD calls Configure() before Charge()
	// on every request; callers that forget will see IsAvailable()=false.
	processor.Register(&Provider{
		BaseProcessor: processor.NewBaseProcessor(processor.Square, thirdparty.SquareSupportedCurrencies()),
	})
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
