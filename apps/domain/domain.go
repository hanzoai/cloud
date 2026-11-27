package domain

import (
	"context"
	"errors"
)

// Sentinels the orchestration returns; the HTTP adapter maps them to status codes.
var (
	// ErrInsufficientFunds — the org's prepaid balance cannot cover the price. → 402.
	ErrInsufficientFunds = errors.New("domain: insufficient balance for this purchase")
	// ErrUnavailable — the name is not purchasable (taken, reserved, or unsupported TLD). → 409.
	ErrUnavailable = errors.New("domain: name is not available to register")
	// ErrAlreadyOwned — this org already holds this domain. → 409.
	ErrAlreadyOwned = errors.New("domain: already registered to this org")
	// ErrNotOwned — a renew/transfer/manage on a domain this org does not hold. → 404/403.
	ErrNotOwned = errors.New("domain: not registered to this org")
	// ErrNotConfigured — the registrar has no credentials. → 503.
	ErrNotConfigured = errors.New("domain: registrar not configured")
)

// Biller is the two-phase deposit→charge a purchase bills through. Authorize refuses
// when the org's prepaid balance cannot cover the marked-up price (BEFORE the
// registrar is touched); Capture debits it AFTER the registrar succeeds. Backed by
// cloud's Meter (Gate → Authorize, Meter → Capture).
type Biller interface {
	// Authorize returns ErrInsufficientFunds when the balance can't cover cents, nil
	// to proceed, or another error when the balance is unknown (fail-closed).
	Authorize(ctx context.Context, org string, cents int64) error
	// Capture records the debit against the org's ledger.
	//
	// It takes NO key. It used to take the purchase's own ref — a string naming the
	// DOMAIN — and that reached the ledger as the debit's idempotency key, so buying one
	// name twice charged once. A domain can be renewed, and can lapse and be bought back:
	// each of those is its own act, and an act is named by whoever writes the entry.
	Capture(org string, cents int64)
}

// Zones ensures an authoritative DNS zone exists for a freshly-registered domain and
// returns the nameservers to point it at. Backed by hanzoai/dns.
type Zones interface {
	EnsureZone(ctx context.Context, org, domainName string) (nameservers []string, err error)
}

// Holding is the domain↔org ownership row Hanzo issues on a successful purchase.
type Holding struct {
	Org          string   `json:"org"`                   // the org that owns the domain
	Domain       string   `json:"domain"`                // the name owned
	RegisteredAt int64    `json:"registeredAt"`          // unix seconds
	ExpiresAt    string   `json:"expiresAt,omitempty"`   // when the registration lapses, RFC3339
	PriceCents   int64    `json:"priceCents"`            // what the customer paid (sell)
	CostCents    int64    `json:"costCents"`             // wholesale cost
	Nameservers  []string `json:"nameservers,omitempty"` // the authoritative nameservers the name points at
	Order        int64    `json:"order,omitempty"`       // registrar order id
}

// Store persists ownership records.
type Store interface {
	Put(rec Holding) error
	Get(org, domainName string) (Holding, bool, error)
	ListByOrg(org string) ([]Holding, error)
}

// Config tunes pricing and the DNS handoff. Which registrar answers, and which of its
// environments, is the registrar's own (registrar.go) — not a field here.
type Config struct {
	Markup      Markup   // wholesale → sell
	Nameservers []string // Hanzo authoritative NS to point purchased domains at (fallback when Zones returns none)
}

// Service is the transport-free orchestrator.
type Service struct {
	reg   Registrar
	bill  Biller
	zones Zones
	store Store
	cfg   Config
}

// NewService builds the orchestrator. All four backends are required; pass a no-op
// Zones/Store in a context that doesn't need them.
func NewService(reg Registrar, bill Biller, zones Zones, store Store, cfg Config) *Service {
	return &Service{reg: reg, bill: bill, zones: zones, store: store, cfg: cfg}
}
