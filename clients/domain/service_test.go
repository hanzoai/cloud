package domain

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/domain/namecom"
)

// --- mock backends ---------------------------------------------------------------

type mockReg struct {
	avail      map[string]namecom.SearchResult
	created    []namecom.CreateDomainRequest
	createErr  error
	renewed    []string
	configured bool
}

func (m *mockReg) Configured() bool { return m.configured }
func (m *mockReg) CheckAvailability(_ context.Context, names ...string) (*namecom.SearchResponse, error) {
	var out namecom.SearchResponse
	for _, n := range names {
		if r, ok := m.avail[n]; ok {
			out.Results = append(out.Results, r)
		} else {
			out.Results = append(out.Results, namecom.SearchResult{DomainName: n, Purchasable: false})
		}
	}
	return &out, nil
}
func (m *mockReg) Search(_ context.Context, _ string, _ ...string) (*namecom.SearchResponse, error) {
	out := &namecom.SearchResponse{}
	for _, r := range m.avail {
		out.Results = append(out.Results, r)
	}
	return out, nil
}
func (m *mockReg) CreateDomain(_ context.Context, req namecom.CreateDomainRequest) (*namecom.CreateDomainResponse, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.created = append(m.created, req)
	return &namecom.CreateDomainResponse{
		Domain:    &namecom.Domain{DomainName: req.Domain.DomainName, Nameservers: req.Domain.Nameservers, ExpireDate: "2027-01-01T00:00:00Z"},
		Order:     1001,
		TotalPaid: req.PurchasePrice,
	}, nil
}
func (m *mockReg) RenewDomain(_ context.Context, d string, _ namecom.RenewDomainRequest) (*namecom.RenewDomainResponse, error) {
	m.renewed = append(m.renewed, d)
	return &namecom.RenewDomainResponse{Domain: &namecom.Domain{DomainName: d, ExpireDate: "2028-01-01T00:00:00Z"}}, nil
}
func (m *mockReg) SetNameservers(_ context.Context, d string, ns []string) (*namecom.Domain, error) {
	return &namecom.Domain{DomainName: d, Nameservers: ns}, nil
}
func (m *mockReg) CreateTransfer(_ context.Context, req namecom.TransferRequest) (*namecom.TransferResponse, error) {
	return &namecom.TransferResponse{Transfer: &namecom.Transfer{DomainName: req.DomainName, Status: "pending"}, Order: 2002}, nil
}
func (m *mockReg) Hello(_ context.Context) (*namecom.HelloResponse, error) {
	return &namecom.HelloResponse{Username: "test"}, nil
}

// mockBill records authorize/capture and can refuse.
type mockBill struct {
	balance   int64 // cents; -1 = unlimited
	captured  []capture
	authCalls int
}
type capture struct {
	org   string
	cents int64
	ref   string
}

func (b *mockBill) Authorize(_ context.Context, _ string, cents int64) error {
	b.authCalls++
	if b.balance >= 0 && cents > b.balance {
		return ErrInsufficientFunds
	}
	return nil
}
func (b *mockBill) Capture(org string, cents int64, ref string) {
	b.captured = append(b.captured, capture{org, cents, ref})
	if b.balance >= 0 {
		b.balance -= cents
	}
}

// mockZones returns fixed nameservers (or an error to exercise the fallback).
type mockZones struct {
	ns  []string
	err error
}

func (z *mockZones) EnsureZone(_ context.Context, _, _ string) ([]string, error) {
	return z.ns, z.err
}

// --- helpers ---------------------------------------------------------------------

func newService(reg *mockReg, bill *mockBill, zones *mockZones) *Service {
	return NewService(reg, bill, zones, NewMemStore(), Config{
		Markup:      Markup{Multiplier: 1.15, MinMarginCents: 300},
		Nameservers: []string{"ns1.hanzo.ai", "ns2.hanzo.ai"},
		Env:         "test",
	})
}

// --- tests -----------------------------------------------------------------------

func TestMarkupSell(t *testing.T) {
	m := Markup{Multiplier: 1.15, MinMarginCents: 300}
	// 15% over $10.00 = $11.50 (min margin $1.50 < $3 → floor to cost+$3 = $13.00)
	if got := m.Sell(1000); got != 1300 {
		t.Fatalf("Sell(1000) = %d, want 1300 (min-margin floor)", got)
	}
	// 15% over $50.00 = $57.50; margin $7.50 > $3 → $57.50
	if got := m.Sell(5000); got != 5750 {
		t.Fatalf("Sell(5000) = %d, want 5750", got)
	}
	// never below cost
	if got := (Markup{Multiplier: 0.5}).Sell(1000); got != 1000 {
		t.Fatalf("Sell below cost = %d, want 1000", got)
	}
	if got := m.Sell(0); got != 0 {
		t.Fatalf("Sell(0) = %d, want 0", got)
	}
}

func TestAvailabilityPricesWithMarkup(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99, RenewalPrice: 55.99, TLD: "ai"},
	}}
	svc := newService(reg, &mockBill{balance: -1}, &mockZones{ns: []string{"ns1.hanzo.ai"}})
	qs, err := svc.Availability(context.Background(), "acme.ai")
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || !qs[0].Available {
		t.Fatalf("unexpected quotes: %+v", qs)
	}
	// cost 5599; sell = ceil(5599*1.15)=6439 (margin 840 > 300)
	if qs[0].CostCents != 5599 || qs[0].PriceCents != 6439 {
		t.Fatalf("pricing: cost=%d price=%d, want 5599/6439", qs[0].CostCents, qs[0].PriceCents)
	}
}

func TestRegisterHappyPath_BillsAndPointsNameservers(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99, RenewalPrice: 55.99, TLD: "ai"},
	}}
	bill := &mockBill{balance: 100000} // $1000
	zones := &mockZones{ns: []string{"ns1.hanzo.ai", "ns2.hanzo.ai"}}
	svc := newService(reg, bill, zones)

	res, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Registrar got a create with Hanzo nameservers + the wholesale price cap.
	if len(reg.created) != 1 {
		t.Fatalf("expected 1 create, got %d", len(reg.created))
	}
	c := reg.created[0]
	if len(c.Domain.Nameservers) != 2 || c.Domain.Nameservers[0] != "ns1.hanzo.ai" {
		t.Fatalf("domain not born on Hanzo nameservers: %v", c.Domain.Nameservers)
	}
	if c.PurchasePrice != 55.99 {
		t.Fatalf("wholesale price cap = %v, want 55.99", c.PurchasePrice)
	}
	// Customer charged the SELL price exactly once, after the registrar succeeded.
	if len(bill.captured) != 1 || bill.captured[0].cents != 6439 || bill.captured[0].ref != "domain:register:acme.ai" {
		t.Fatalf("unexpected capture: %+v", bill.captured)
	}
	if res.Record.Order != 1001 || res.Record.ExpiresAt != "2027-01-01T00:00:00Z" {
		t.Fatalf("record not populated from registrar: %+v", res.Record)
	}
	// Ownership recorded.
	owned, _ := svc.ListByOrg("acme")
	if len(owned) != 1 || owned[0].Domain != "acme.ai" {
		t.Fatalf("ownership not recorded: %+v", owned)
	}
}

func TestRegisterRefusedWhenInsufficientBalance_NoRegistrarCall(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99},
	}}
	bill := &mockBill{balance: 100} // $1.00 — nowhere near $64.39
	svc := newService(reg, bill, &mockZones{})

	_, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	if len(reg.created) != 0 {
		t.Fatalf("registrar must NOT be called when balance insufficient; got %d creates", len(reg.created))
	}
	if len(bill.captured) != 0 {
		t.Fatalf("no capture on a refused purchase; got %+v", bill.captured)
	}
}

func TestRegisterRegistrarFailure_NoCharge(t *testing.T) {
	reg := &mockReg{configured: true, createErr: errors.New("boom"), avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99},
	}}
	bill := &mockBill{balance: 100000}
	svc := newService(reg, bill, &mockZones{ns: []string{"ns1.hanzo.ai"}})

	_, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil)
	if err == nil {
		t.Fatal("expected registrar failure to propagate")
	}
	// Authorized (reserved) but NEVER captured — customer not charged for a failed buy.
	if len(bill.captured) != 0 {
		t.Fatalf("customer must not be charged on registrar failure; got %+v", bill.captured)
	}
}

func TestRegisterUnavailable(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"taken.ai": {DomainName: "taken.ai", Purchasable: false},
	}}
	svc := newService(reg, &mockBill{balance: -1}, &mockZones{})
	_, err := svc.Register(context.Background(), "acme", "taken.ai", 1, nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}

func TestRegisterAlreadyOwned(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99},
	}}
	bill := &mockBill{balance: 100000}
	svc := newService(reg, bill, &mockZones{ns: []string{"ns1.hanzo.ai"}})
	if _, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil)
	if !errors.Is(err, ErrAlreadyOwned) {
		t.Fatalf("want ErrAlreadyOwned on re-register, got %v", err)
	}
}

func TestRegisterFallsBackToConfigNameserversWhenZoneFails(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 10.00},
	}}
	bill := &mockBill{balance: 100000}
	zones := &mockZones{err: errors.New("dns down")}
	svc := newService(reg, bill, zones)

	if _, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil); err != nil {
		t.Fatal(err)
	}
	// Zone provisioning failed → registered against the configured Hanzo nameservers.
	got := reg.created[0].Domain.Nameservers
	if len(got) != 2 || got[0] != "ns1.hanzo.ai" {
		t.Fatalf("expected fallback nameservers, got %v", got)
	}
}

func TestRenewOwnedDomainBills(t *testing.T) {
	reg := &mockReg{configured: true, avail: map[string]namecom.SearchResult{
		"acme.ai": {DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99, RenewalPrice: 55.99},
	}}
	bill := &mockBill{balance: 100000}
	svc := newService(reg, bill, &mockZones{ns: []string{"ns1.hanzo.ai"}})
	if _, err := svc.Register(context.Background(), "acme", "acme.ai", 1, nil); err != nil {
		t.Fatal(err)
	}
	captBefore := len(bill.captured)
	res, err := svc.Renew(context.Background(), "acme", "acme.ai", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.renewed) != 1 || reg.renewed[0] != "acme.ai" {
		t.Fatalf("registrar renew not called: %v", reg.renewed)
	}
	if len(bill.captured) != captBefore+1 {
		t.Fatalf("renewal not billed: %+v", bill.captured)
	}
	if res.Record.ExpiresAt != "2028-01-01T00:00:00Z" {
		t.Fatalf("expiry not updated: %+v", res.Record)
	}
}

func TestRenewNotOwned(t *testing.T) {
	reg := &mockReg{configured: true}
	svc := newService(reg, &mockBill{balance: -1}, &mockZones{})
	_, err := svc.Renew(context.Background(), "acme", "nope.ai", 1)
	if !errors.Is(err, ErrNotOwned) {
		t.Fatalf("want ErrNotOwned, got %v", err)
	}
}

func TestNotConfigured(t *testing.T) {
	reg := &mockReg{configured: false}
	svc := newService(reg, &mockBill{balance: -1}, &mockZones{})
	if _, err := svc.Availability(context.Background(), "acme.ai"); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}
