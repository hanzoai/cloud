// Package domain is Hanzo Domains — the DOMAIN-REGISTRATION product: search a name,
// see its price (with Hanzo's markup), buy it billed through the customer's prepaid
// wallet, and have it born pointing at Hanzo's own authoritative nameservers.
//
// This is DISTINCT from Hanzo DNS (hanzoai/dns): DNS manages records for a domain you
// already control; Domains ACQUIRES the domain. After a purchase, Domains hands the
// new zone to hanzoai/dns and points the registrar's nameservers at it — the two
// products compose (buy here, manage records there).
//
// Wholesale is resold from a registrar behind the Registrar interface (name.com Core
// API v4 today, clients/domain/namecom). The core here is transport-free: it
// orchestrates availability → price → authorize → register → provision-zone →
// capture → record over four interfaces (Registrar, Biller, Zones, Store), so the
// policy is unit-testable with no HTTP/registrar/billing backend. mount.go is the thin
// cloud adapter that binds the real backends and exposes /v1/domain/*.
package domain

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/domain/namecom"
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

// Registrar is the wholesale registrar Hanzo resells. *namecom.Client satisfies it.
type Registrar interface {
	CheckAvailability(ctx context.Context, names ...string) (*namecom.SearchResponse, error)
	Search(ctx context.Context, keyword string, tldFilter ...string) (*namecom.SearchResponse, error)
	CreateDomain(ctx context.Context, req namecom.CreateDomainRequest) (*namecom.CreateDomainResponse, error)
	RenewDomain(ctx context.Context, domainName string, req namecom.RenewDomainRequest) (*namecom.RenewDomainResponse, error)
	SetNameservers(ctx context.Context, domainName string, nameservers []string) (*namecom.Domain, error)
	CreateTransfer(ctx context.Context, req namecom.TransferRequest) (*namecom.TransferResponse, error)
	Hello(ctx context.Context) (*namecom.HelloResponse, error)
	Configured() bool
}

// Biller is the two-phase deposit→charge a purchase bills through. Authorize refuses
// when the org's prepaid balance cannot cover the marked-up price (BEFORE the
// registrar is touched); Capture debits it AFTER the registrar succeeds. Backed by
// cloud's ResourceMeter (Gate → Authorize, Meter → Capture).
type Biller interface {
	// Authorize returns ErrInsufficientFunds when the balance can't cover cents, nil
	// to proceed, or another error when the balance is unknown (fail-closed).
	Authorize(ctx context.Context, org string, cents int64) error
	// Capture records the debit against the org's ledger. ref is the idempotency /
	// attribution key (e.g. "domain:register:acme.ai").
	Capture(org string, cents int64, ref string)
}

// Zones ensures an authoritative DNS zone exists for a freshly-registered domain and
// returns the nameservers to point it at. Backed by hanzoai/dns.
type Zones interface {
	EnsureZone(ctx context.Context, org, domainName string) (nameservers []string, err error)
}

// Record is the domain↔org ownership row Hanzo issues on a successful purchase.
type Record struct {
	Org          string   `json:"org"`
	Domain       string   `json:"domain"`
	RegisteredAt int64    `json:"registeredAt"` // unix seconds
	ExpiresAt    string   `json:"expiresAt,omitempty"`
	PriceCents   int64    `json:"priceCents"` // what the customer paid (sell)
	CostCents    int64    `json:"costCents"`  // wholesale cost
	Nameservers  []string `json:"nameservers,omitempty"`
	Order        int64    `json:"order,omitempty"` // registrar order id
}

// Store persists ownership records.
type Store interface {
	Put(rec Record) error
	Get(org, domainName string) (Record, bool, error)
	ListByOrg(org string) ([]Record, error)
}

// Config tunes pricing and the DNS handoff.
type Config struct {
	Markup      Markup   // wholesale → sell
	Nameservers []string // Hanzo authoritative NS to point purchased domains at (fallback when Zones returns none)
	Env         string   // registrar env label (test/prod) — attribution only
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

// Env reports the registrar environment label (test/prod).
func (s *Service) Env() string { return s.cfg.Env }

// Configured reports whether the registrar has credentials.
func (s *Service) Configured() bool { return s.reg.Configured() }
