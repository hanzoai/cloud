package processor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// Registry manages available payment processors
type Registry struct {
	mu         sync.RWMutex
	processors map[ProcessorType]PaymentProcessor
	config     *RegistryConfig
}

// RegistryConfig holds configuration for processor selection.
//
// Fiat selection has exactly ONE mechanism: ProcessorPriority gives the
// preference order and DisabledProcessors is the deny policy. There is no
// separate "default fiat processor" knob — the old DefaultFiatProcessor field
// was never consulted by SelectProcessor for fiat (only the priority list was),
// so it was a dead, complected knob and has been removed. Crypto selection keeps
// a single preferred processor (DefaultCryptoProcessor) plus a currency-driven
// fallback order, because crypto routing is by currency, not preference order.
type RegistryConfig struct {
	// DefaultCryptoProcessor is the preferred processor for crypto currencies.
	DefaultCryptoProcessor ProcessorType

	// ProcessorPriority is the fiat preference order (first available wins).
	ProcessorPriority []ProcessorType

	// DisabledProcessors is the deny policy: a processor in this set is NEVER
	// returned by SelectProcessor from ANY branch (explicit preference, crypto,
	// or fiat). This is how Stripe is held un-selectable for new charges.
	DisabledProcessors map[ProcessorType]bool
}

// DefaultConfig returns the default registry configuration.
//
// Fiat: Square is the first (preferred) processor and Stripe is DISABLED by
// default — "we do NOT use Stripe" for new charges. The disable is deterministic
// and cred-independent: SelectProcessor skips DisabledProcessors in every branch,
// so a hydrated Stripe secret key can NEVER win a charge. Stripe stays REGISTERED
// (its migrations, webhook ingestion and existing-order handlers are untouched) —
// it is removed only from NEW-charge selection.
//
// Crypto: DefaultCryptoProcessor (MPC) plus the crypto fallback order in
// SelectProcessor; unchanged.
//
// Per-env override (ONE knob): COMMERCE_DISABLED_PROCESSORS, a comma-separated
// list of processor types that REPLACES the default deny set. Unset OR empty keeps
// the safe default (Stripe disabled) — an empty k8s placeholder must NOT silently
// re-enable Stripe. To disable NOTHING, set the explicit sentinel
// COMMERCE_DISABLED_PROCESSORS=none. Crypto stays overridable via
// COMMERCE_DEFAULT_CRYPTO_PROCESSOR.
func DefaultConfig() *RegistryConfig {
	return &RegistryConfig{
		DefaultCryptoProcessor: envProcessor("COMMERCE_DEFAULT_CRYPTO_PROCESSOR", MPC),
		ProcessorPriority: []ProcessorType{
			Square, // fiat default — pay.hanzo.ai (Square + crypto)
			Stripe, // historical; disabled for new charges by default (see disabledProcessors)
			Adyen,
			PayPal,
			Braintree,
			Recurly,
			LemonSqueezy,
			// crypto
			MPC,
			SolanaPay,
			Circle,
			CoinbaseCommerce,
			OpenNode,
			MoonPay,
			Ethereum,
			Bitcoin,
			Wire,
		},
		DisabledProcessors: disabledProcessors(),
	}
}

// disabledProcessors returns the deny set that gates processor selection.
//
// Default: Stripe is OFF for new-charge selection ("we do NOT use Stripe"). Only
// SELECTION is denied — Stripe's migrations, webhook ingestion and existing-order
// handlers stay fully intact for already-charged historical data.
//
// Override per-deployment with COMMERCE_DISABLED_PROCESSORS (comma-separated
// processor types), which REPLACES the default. UNSET or EMPTY/whitespace keeps
// the safe default {Stripe} — an empty env placeholder (common in k8s) must never
// silently re-enable Stripe. The explicit sentinel "none" disables nothing.
func disabledProcessors() map[ProcessorType]bool {
	raw, ok := os.LookupEnv("COMMERCE_DISABLED_PROCESSORS")
	if !ok || strings.TrimSpace(raw) == "" {
		return map[ProcessorType]bool{Stripe: true}
	}
	if strings.EqualFold(strings.TrimSpace(raw), "none") {
		return map[ProcessorType]bool{}
	}
	disabled := make(map[ProcessorType]bool)
	for _, name := range strings.Split(raw, ",") {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" && name != "none" {
			disabled[ProcessorType(name)] = true
		}
	}
	return disabled
}

// DisabledByPolicy reports whether processor t is denied by the deployment's
// disabled-processor policy (COMMERCE_DISABLED_PROCESSORS; default: Stripe).
//
// It is the SINGLE source of truth for "is this processor off for new charges,"
// shared by the registry (via DisabledProcessors) AND by out-of-registry
// selectors such as the checkout-session resolver, so the policy lives in ONE
// place across every new-charge modality (direct charge and hosted checkout).
func DisabledByPolicy(t ProcessorType) bool {
	return disabledProcessors()[t]
}

// envProcessor reads a processor type from an env var, falling back to def when unset.
func envProcessor(key string, def ProcessorType) ProcessorType {
	if v := os.Getenv(key); v != "" {
		return ProcessorType(v)
	}
	return def
}

var (
	globalRegistry *Registry
	once           sync.Once
)

// Global returns the global registry instance
func Global() *Registry {
	once.Do(func() {
		globalRegistry = NewRegistry(DefaultConfig())
	})
	return globalRegistry
}

// NewRegistry creates a new processor registry
func NewRegistry(config *RegistryConfig) *Registry {
	if config == nil {
		config = DefaultConfig()
	}
	return &Registry{
		processors: make(map[ProcessorType]PaymentProcessor),
		config:     config,
	}
}

// Register adds a processor to the registry
func (r *Registry) Register(p PaymentProcessor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.processors[p.Type()] = p
}

// Unregister removes a processor from the registry
func (r *Registry) Unregister(t ProcessorType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.processors, t)
}

// Get retrieves a processor by type
func (r *Registry) Get(t ProcessorType) (PaymentProcessor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.config.DisabledProcessors[t] {
		return nil, fmt.Errorf("processor %s is disabled", t)
	}

	p, ok := r.processors[t]
	if !ok {
		return nil, fmt.Errorf("processor %s not registered", t)
	}
	return p, nil
}

// Registered returns a processor by type IGNORING the disabled-processor policy.
//
// Use this only for INBOUND paths — webhook validation, admin/historical
// operations — that must reach a registered processor even when it is disabled
// for new-charge SELECTION (e.g. Stripe, which stays able to validate existing
// customers' webhooks). Outbound charge selection must use Get / SelectProcessor,
// which honor the deny policy.
func (r *Registry) Registered(t ProcessorType) (PaymentProcessor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	p, ok := r.processors[t]
	if !ok {
		return nil, fmt.Errorf("processor %s not registered", t)
	}
	return p, nil
}

// GetCrypto retrieves a crypto processor
func (r *Registry) GetCrypto(t ProcessorType) (CryptoProcessor, error) {
	p, err := r.Get(t)
	if err != nil {
		return nil, err
	}

	cp, ok := p.(CryptoProcessor)
	if !ok {
		return nil, fmt.Errorf("processor %s does not support crypto operations", t)
	}
	return cp, nil
}

// GetSubscription retrieves a subscription processor
func (r *Registry) GetSubscription(t ProcessorType) (SubscriptionProcessor, error) {
	p, err := r.Get(t)
	if err != nil {
		return nil, err
	}

	sp, ok := p.(SubscriptionProcessor)
	if !ok {
		return nil, fmt.Errorf("processor %s does not support subscriptions", t)
	}
	return sp, nil
}

// GetCustomer retrieves a customer processor
func (r *Registry) GetCustomer(t ProcessorType) (CustomerProcessor, error) {
	p, err := r.Get(t)
	if err != nil {
		return nil, err
	}

	cp, ok := p.(CustomerProcessor)
	if !ok {
		return nil, fmt.Errorf("processor %s does not support customer management", t)
	}
	return cp, nil
}

// SelectProcessor chooses the best processor for the payment
func (r *Registry) SelectProcessor(ctx context.Context, req PaymentRequest) (PaymentProcessor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Check for explicit preference
	if pref, ok := req.Options["processor"].(ProcessorType); ok {
		if p, ok := r.processors[pref]; ok {
			if !r.config.DisabledProcessors[pref] && p.IsAvailable(ctx) {
				return p, nil
			}
		}
	}

	// Check for string preference
	if prefStr, ok := req.Options["processor"].(string); ok {
		pref := ProcessorType(prefStr)
		if p, ok := r.processors[pref]; ok {
			if !r.config.DisabledProcessors[pref] && p.IsAvailable(ctx) {
				return p, nil
			}
		}
	}

	// For crypto currencies, use crypto processor
	if IsCryptoCurrency(req.Currency) {
		if p, ok := r.processors[r.config.DefaultCryptoProcessor]; ok {
			if !r.config.DisabledProcessors[r.config.DefaultCryptoProcessor] && p.IsAvailable(ctx) {
				return p, nil
			}
		}
		// Fallback to any available crypto processor
		for _, t := range []ProcessorType{Circle, SolanaPay, MPC, Ethereum, Bitcoin} {
			if p, ok := r.processors[t]; ok {
				if !r.config.DisabledProcessors[t] && p.IsAvailable(ctx) {
					if supportssCurrency(p, req.Currency) {
						return p, nil
					}
				}
			}
		}
	}

	// For fiat, use priority order
	for _, t := range r.config.ProcessorPriority {
		if r.config.DisabledProcessors[t] {
			continue
		}
		if p, ok := r.processors[t]; ok {
			if p.IsAvailable(ctx) && supportssCurrency(p, req.Currency) {
				return p, nil
			}
		}
	}

	return nil, fmt.Errorf("no processor available for currency %s", req.Currency)
}

// SelectSubscriptionProcessor chooses a processor that supports subscriptions
func (r *Registry) SelectSubscriptionProcessor(ctx context.Context, req PaymentRequest) (SubscriptionProcessor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Try priority order for subscription-capable processors
	for _, t := range r.config.ProcessorPriority {
		if r.config.DisabledProcessors[t] {
			continue
		}
		if p, ok := r.processors[t]; ok {
			if sp, ok := p.(SubscriptionProcessor); ok {
				if p.IsAvailable(ctx) && supportssCurrency(p, req.Currency) {
					return sp, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no subscription processor available for currency %s", req.Currency)
}

// Available returns all available processors
func (r *Registry) Available(ctx context.Context) []PaymentProcessor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]PaymentProcessor, 0, len(r.processors))
	for t, p := range r.processors {
		if !r.config.DisabledProcessors[t] && p.IsAvailable(ctx) {
			result = append(result, p)
		}
	}
	return result
}

// RegisteredProcessors returns ALL registered processors IGNORING the
// disabled-processor policy. For inbound webhook validation: a processor that is
// disabled for new-charge selection (e.g. Stripe) must still be reachable to
// validate its existing customers' webhook deliveries. Callers filter by
// IsAvailable themselves. Outbound selection must use Available / SelectProcessor.
func (r *Registry) RegisteredProcessors() []PaymentProcessor {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]PaymentProcessor, 0, len(r.processors))
	for _, p := range r.processors {
		result = append(result, p)
	}
	return result
}

// ListTypes returns all registered processor types
func (r *Registry) ListTypes() []ProcessorType {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]ProcessorType, 0, len(r.processors))
	for t := range r.processors {
		result = append(result, t)
	}
	return result
}

// SetConfig updates the registry configuration
func (r *Registry) SetConfig(config *RegistryConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config = config
}

// DisableProcessor disables a processor
func (r *Registry) DisableProcessor(t ProcessorType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.config.DisabledProcessors[t] = true
}

// EnableProcessor enables a processor
func (r *Registry) EnableProcessor(t ProcessorType) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.config.DisabledProcessors, t)
}

// supportssCurrency checks if a processor supports a currency
func supportssCurrency(p PaymentProcessor, c currency.Type) bool {
	for _, supported := range p.SupportedCurrencies() {
		if supported == c {
			return true
		}
	}
	return false
}

// Package-level convenience functions

// Register adds a processor to the global registry
func Register(p PaymentProcessor) {
	Global().Register(p)
}

// Get retrieves a processor from the global registry
func Get(t ProcessorType) (PaymentProcessor, error) {
	return Global().Get(t)
}

// SelectProcessor selects a processor from the global registry
func SelectProcessor(ctx context.Context, req PaymentRequest) (PaymentProcessor, error) {
	return Global().SelectProcessor(ctx, req)
}
