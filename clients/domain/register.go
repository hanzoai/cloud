package domain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/domain/namecom"
)

// Quote is a priced availability result: the wholesale cost and the customer sell
// price (marked up), both in cents. It is what search/availability return and what a
// register bills against.
type Quote struct {
	Domain            string `json:"domain"`
	Available         bool   `json:"available"`
	Premium           bool   `json:"premium,omitempty"`
	CostCents         int64  `json:"-"`                 // wholesale (internal — not exposed to customers)
	PriceCents        int64  `json:"priceCents"`        // sell (first-term registration)
	RenewalPriceCents int64  `json:"renewalPriceCents"` // sell (renewal)
	Currency          string `json:"currency"`
	TLD               string `json:"tld,omitempty"`
}

// quoteFrom prices a registrar search result through the markup.
func (s *Service) quoteFrom(r namecom.SearchResult) Quote {
	cost := dollarsToCents(r.PurchasePrice)
	renewCost := dollarsToCents(r.RenewalPrice)
	return Quote{
		Domain:            strings.ToLower(r.DomainName),
		Available:         r.Purchasable,
		Premium:           r.Premium,
		CostCents:         cost,
		PriceCents:        s.cfg.Markup.Sell(cost),
		RenewalPriceCents: s.cfg.Markup.Sell(renewCost),
		Currency:          "usd",
		TLD:               r.TLD,
	}
}

// Availability checks exact names and returns priced quotes (availability + pricing).
func (s *Service) Availability(ctx context.Context, names ...string) ([]Quote, error) {
	if !s.reg.Configured() {
		return nil, ErrNotConfigured
	}
	resp, err := s.reg.CheckAvailability(ctx, names...)
	if err != nil {
		return nil, err
	}
	return s.quotes(resp), nil
}

// Search runs a keyword search (with alternate-TLD suggestions) and returns priced
// quotes. tldFilter (optional) narrows the TLDs.
func (s *Service) Search(ctx context.Context, keyword string, tldFilter ...string) ([]Quote, error) {
	if !s.reg.Configured() {
		return nil, ErrNotConfigured
	}
	resp, err := s.reg.Search(ctx, keyword, tldFilter...)
	if err != nil {
		return nil, err
	}
	return s.quotes(resp), nil
}

func (s *Service) quotes(resp *namecom.SearchResponse) []Quote {
	out := make([]Quote, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, s.quoteFrom(r))
	}
	return out
}

// RegisterResult is a successful purchase.
type RegisterResult struct {
	Record Record `json:"record"`
	Quote  Quote  `json:"quote"`
}

// Register buys a domain for org: quote → guard → authorize (deposit) → provision the
// DNS zone → register at the registrar pointing at Hanzo nameservers → capture
// (charge) → record ownership. The customer is charged ONLY after the registrar
// confirms — a registrar failure leaves the balance untouched.
//
// contacts is optional; when nil the registrar uses the reseller account's default
// WHOIS contacts. years defaults to 1.
func (s *Service) Register(ctx context.Context, org, domainName string, years int, contacts *namecom.Contacts) (*RegisterResult, error) {
	if !s.reg.Configured() {
		return nil, ErrNotConfigured
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if domainName == "" {
		return nil, fmt.Errorf("domain: name is required")
	}
	if years <= 0 {
		years = 1
	}

	// Idempotency guard: never double-buy a name this org already holds.
	if _, owned, err := s.store.Get(org, domainName); err != nil {
		return nil, err
	} else if owned {
		return nil, ErrAlreadyOwned
	}

	// 1. Quote — must be purchasable + priced.
	quotes, err := s.Availability(ctx, domainName)
	if err != nil {
		return nil, err
	}
	if len(quotes) == 0 {
		return nil, ErrUnavailable
	}
	q := quotes[0]
	if !q.Available || q.PriceCents <= 0 {
		return nil, ErrUnavailable
	}

	// 2. Authorize the customer charge BEFORE spending at the registrar — refuse if the
	//    prepaid balance can't cover it, so we never buy a domain we can't bill for.
	if err := s.bill.Authorize(ctx, org, q.PriceCents); err != nil {
		return nil, err
	}

	// 3. Provision the authoritative zone in hanzoai/dns and learn which nameservers to
	//    point the domain at. Best-effort: if the zone service is down we still register
	//    against the fixed Hanzo nameservers (the zone reconciles later) rather than
	//    block the purchase.
	ns, zoneErr := s.zones.EnsureZone(ctx, org, domainName)
	if zoneErr != nil || len(ns) == 0 {
		ns = s.cfg.Nameservers
	}

	// 4. Register at the registrar, born pointing at Hanzo's nameservers. purchasePrice
	//    caps the wholesale charge at the quoted cost (a price change above it is
	//    rejected by the registrar). This is the step that debits Hanzo's reseller
	//    account; the customer is not charged yet.
	created, err := s.reg.CreateDomain(ctx, namecom.CreateDomainRequest{
		Domain: namecom.DomainInput{
			DomainName:  domainName,
			Nameservers: ns,
			Contacts:    contacts,
		},
		PurchasePrice: float64(q.CostCents) / 100,
		Years:         years,
	})
	if err != nil {
		// The registrar rejected/failed — the customer was NOT charged (no Capture).
		return nil, fmt.Errorf("domain: registrar create failed: %w", err)
	}

	// 5. Capture — charge the customer's prepaid wallet now that the domain is theirs.
	ref := "domain:register:" + domainName
	s.bill.Capture(org, q.PriceCents, ref)

	// 6. Record ownership.
	rec := Record{
		Org:          org,
		Domain:       domainName,
		RegisteredAt: time.Now().Unix(),
		PriceCents:   q.PriceCents,
		CostCents:    q.CostCents,
		Nameservers:  ns,
	}
	if created.Domain != nil {
		rec.ExpiresAt = created.Domain.ExpireDate
		if len(created.Domain.Nameservers) > 0 {
			rec.Nameservers = created.Domain.Nameservers
		}
	}
	rec.Order = created.Order
	if err := s.store.Put(rec); err != nil {
		return nil, err
	}
	return &RegisterResult{Record: rec, Quote: q}, nil
}

// RenewResult is a successful renewal.
type RenewResult struct {
	Record    Record `json:"record"`
	PaidCents int64  `json:"paidCents"`
}

// Renew extends a domain this org owns: guard ownership → quote renewal → authorize →
// renew at the registrar → capture → update the record's expiry.
func (s *Service) Renew(ctx context.Context, org, domainName string, years int) (*RenewResult, error) {
	if !s.reg.Configured() {
		return nil, ErrNotConfigured
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if years <= 0 {
		years = 1
	}
	rec, owned, err := s.store.Get(org, domainName)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrNotOwned
	}

	// Quote the renewal (re-check gives the current renewal price).
	quotes, err := s.Availability(ctx, domainName)
	var priceCents, costCents int64
	if err == nil && len(quotes) > 0 {
		priceCents = quotes[0].RenewalPriceCents
		costCents = renewalCostFrom(quotes[0])
	}
	if priceCents <= 0 {
		// Fall back to what the customer originally paid so a renewal is never free.
		priceCents = rec.PriceCents
		costCents = rec.CostCents
	}

	if err := s.bill.Authorize(ctx, org, priceCents); err != nil {
		return nil, err
	}
	renewed, err := s.reg.RenewDomain(ctx, domainName, namecom.RenewDomainRequest{
		PurchasePrice: float64(costCents) / 100,
		Years:         years,
	})
	if err != nil {
		return nil, fmt.Errorf("domain: registrar renew failed: %w", err)
	}
	s.bill.Capture(org, priceCents, "domain:renew:"+domainName)

	if renewed.Domain != nil && renewed.Domain.ExpireDate != "" {
		rec.ExpiresAt = renewed.Domain.ExpireDate
	}
	if err := s.store.Put(rec); err != nil {
		return nil, err
	}
	return &RenewResult{Record: rec, PaidCents: priceCents}, nil
}

// renewalCostFrom derives the wholesale renewal cost (cents) for a quote. The renewal
// SELL price already passed through markup; recover a cost floor from it so the
// registrar price cap is set sanely (never below the known wholesale).
func renewalCostFrom(q Quote) int64 {
	if q.CostCents > 0 {
		return q.CostCents
	}
	return q.RenewalPriceCents
}

// Transfer starts an inbound transfer of a domain the customer owns elsewhere: quote
// (the transfer price is the registration price) → authorize → create the transfer →
// capture → record. authCode is the EPP auth code from the losing registrar.
func (s *Service) Transfer(ctx context.Context, org, domainName, authCode string, years int) (*RegisterResult, error) {
	if !s.reg.Configured() {
		return nil, ErrNotConfigured
	}
	domainName = strings.ToLower(strings.TrimSpace(domainName))
	if strings.TrimSpace(authCode) == "" {
		return nil, fmt.Errorf("domain: transfer requires an auth code")
	}
	if years <= 0 {
		years = 1
	}
	quotes, err := s.Availability(ctx, domainName)
	if err != nil {
		return nil, err
	}
	q := Quote{Currency: "usd", Domain: domainName}
	if len(quotes) > 0 {
		q = quotes[0]
	}
	if q.PriceCents <= 0 {
		return nil, ErrUnavailable
	}
	if err := s.bill.Authorize(ctx, org, q.PriceCents); err != nil {
		return nil, err
	}
	res, err := s.reg.CreateTransfer(ctx, namecom.TransferRequest{
		DomainName:    domainName,
		AuthCode:      authCode,
		PurchasePrice: float64(q.CostCents) / 100,
		Years:         years,
	})
	if err != nil {
		return nil, fmt.Errorf("domain: registrar transfer failed: %w", err)
	}
	s.bill.Capture(org, q.PriceCents, "domain:transfer:"+domainName)

	rec := Record{
		Org:          org,
		Domain:       domainName,
		RegisteredAt: time.Now().Unix(),
		PriceCents:   q.PriceCents,
		CostCents:    q.CostCents,
		Nameservers:  s.cfg.Nameservers,
		Order:        res.Order,
	}
	if err := s.store.Put(rec); err != nil {
		return nil, err
	}
	return &RegisterResult{Record: rec, Quote: q}, nil
}

// ListByOrg returns the domains an org holds.
func (s *Service) ListByOrg(org string) ([]Record, error) { return s.store.ListByOrg(org) }
