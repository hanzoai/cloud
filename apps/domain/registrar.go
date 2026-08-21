package domain

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
)

// This file is the WHOLESALE BOUNDARY: the vocabulary a registrar is asked in, and
// the registry of the registrars that answer. Nothing here names a vendor.
//
// It used to. The Registrar interface took and returned name.com's own wire structs,
// which made it name.com wearing an interface: a second registrar could not satisfy
// it without speaking name.com's JSON, and the orchestration in register.go built
// name.com request bodies by hand. The types below are what the ORCHESTRATION means
// — a quote, an order, a renewal, a transfer-in — and each registrar's file maps
// them to and from its own wire shape.
//
// A new registrar is a NEW FILE: a type, its methods, and a register() in its init().
// namecom.go is the worked example, and nothing outside it mentions name.com.

// Registrant is a WHOIS/registration contact. Registrars require registrant/admin/
// tech/ billing contacts on register; missing ones default to the reseller account.
type Registrant struct {
	FirstName string `json:"firstName,omitempty"`   // the contact's given name
	LastName  string `json:"lastName,omitempty"`    // the contact's family name
	Company   string `json:"companyName,omitempty"` // the organisation the contact acts for
	Address1  string `json:"address1,omitempty"`    // street address
	Address2  string `json:"address2,omitempty"`    // second address line
	City      string `json:"city,omitempty"`        // city or locality
	State     string `json:"state,omitempty"`       // state, province or region
	Zip       string `json:"zip,omitempty"`         // postal code
	Country   string `json:"country,omitempty"`     // ISO-3166 alpha-2, e.g. "US"
	Phone     string `json:"phone,omitempty"`       // +NN.NNNNNNN
	Fax       string `json:"fax,omitempty"`         // fax number, in the same form as phone
	Email     string `json:"email,omitempty"`       // where WHOIS correspondence is sent
}

// Contacts is the four-role contact set for a domain.
type Contacts struct {
	Registrant *Registrant `json:"registrant,omitempty"` // who owns the domain
	Admin      *Registrant `json:"admin,omitempty"`      // who administers it
	Tech       *Registrant `json:"tech,omitempty"`       // who is reached about technical matters
	Billing    *Registrant `json:"billing,omitempty"`    // who is reached about payment
}

// SearchResult is one candidate a registrar quoted: whether it can be bought, whether
// the registry prices it above the standard rate, and the WHOLESALE first-term and
// renewal prices in USD. The markup that turns those into an Offer is applied once,
// in register.go, and never here.
type SearchResult struct {
	Domain    string  // the name quoted
	TLD       string  // the top-level domain it sits under
	Available bool    // whether it can be bought right now
	Premium   bool    // whether the registry prices it above the standard rate
	Price     float64 // wholesale first-term registration, USD
	Renewal   float64 // wholesale renewal, USD
}

// CreateRequest is one registration order. Price is the wholesale price the caller
// EXPECTS to pay (the quote availability returned); a registrar that can cap the
// charge MUST refuse a real price above it, so a price change between quote and buy
// is a refusal rather than a surprise debit.
type CreateRequest struct {
	Domain      string
	Years       int
	Nameservers []string
	Contacts    *Contacts // nil ⇒ the reseller account's default WHOIS contacts
	Price       float64
}

// RenewRequest extends a registration the reseller already holds. Price caps the
// wholesale charge exactly as CreateRequest.Price does.
type RenewRequest struct {
	Domain string
	Years  int
	Price  float64
}

// TransferRequest moves a domain in from another registrar. Auth is the EPP auth code
// the losing registrar issued.
type TransferRequest struct {
	Domain string
	Auth   string
	Years  int
	Price  float64
}

// Registration is what a registrar answers to any of the three orders: the name, the
// nameservers it ended up pointing at, when the term lapses, and the registrar's own
// order id. A registrar that reports none of the last three leaves them zero.
type Registration struct {
	Domain      string
	Nameservers []string
	ExpiresAt   string // RFC3339
	Order       int64
}

// Refusal is a registrar rejecting an order in its own words. The HTTP adapter passes
// a 4xx through as the caller's problem, with the registrar's message, and turns a 5xx
// into a bad gateway.
type Refusal struct {
	Status  int
	Message string
}

func (e *Refusal) Error() string { return fmt.Sprintf("registrar %d: %s", e.Status, e.Message) }

// Registrar is the wholesale registrar Hanzo resells. Everything vendor-specific is
// behind it: the endpoint, the wire shape, the credentials, the environment.
type Registrar interface {
	// ID is the stable slug DOMAIN_REGISTRAR selects on ("namecom").
	ID() string
	// Env names which of the registrar's environments this deployment reaches. It is
	// the fact that decides whether money moves: only "prod" is the live, billable
	// registrar — anything else, including unset, is a sandbox.
	Env() string
	// Needs is the credentials this registrar reads, named so the health probe can
	// tell an operator which ones are missing instead of saying only that some are.
	Needs() []string
	// Configured reports whether those credentials are present at all.
	Configured() bool
	// Reach makes a live call that proves the credentials are accepted.
	Reach(ctx context.Context) error
	// Available quotes exact names.
	Available(ctx context.Context, names ...string) ([]SearchResult, error)
	// Search quotes names built from a keyword, plus alternate-TLD suggestions.
	// tld (optional) narrows the top-level domains.
	Search(ctx context.Context, keyword string, tld ...string) ([]SearchResult, error)
	// Create registers a domain. This is the call that debits the reseller account.
	Create(ctx context.Context, req CreateRequest) (*Registration, error)
	// Renew extends a registration.
	Renew(ctx context.Context, req RenewRequest) (*Registration, error)
	// Transfer starts an inbound transfer.
	Transfer(ctx context.Context, req TransferRequest) (*Registration, error)
}

// registrars is populated by each registrar file's register() from its init(). Go
// initializes this map before any init() runs, so every registrar is present by the
// time buildState reads it. The value is a CONSTRUCTOR: a registrar reads its
// credentials when the subsystem mounts, so rotating them is a restart, not a build.
var registrars = map[string]func() Registrar{}

// register adds a registrar. A blank or duplicate id is a programming error and panics
// at init — two registrars cannot own one slug.
func register(id string, build func() Registrar) {
	if id == "" || build == nil {
		panic("domain: register blank id or nil constructor")
	}
	if _, dup := registrars[id]; dup {
		panic("domain: duplicate registrar id " + id)
	}
	registrars[id] = build
}

// defaultRegistrar is the wholesale registrar a deployment gets when it names none.
const defaultRegistrar = "namecom"

// registrarFromEnv builds the registrar this deployment resells, named by
// DOMAIN_REGISTRAR. A name no file registered is refused at MOUNT rather than at the
// first purchase.
func registrarFromEnv() (Registrar, error) {
	id := environ.Or("DOMAIN_REGISTRAR", defaultRegistrar)
	build, ok := registrars[id]
	if !ok {
		return nil, errors.New("domain: no registrar registered as " + id +
			" (have: " + strings.Join(slices.Sorted(maps.Keys(registrars)), ", ") + ")")
	}
	return build(), nil
}
