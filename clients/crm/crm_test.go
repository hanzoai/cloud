package crm

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "crm.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestPerOrgIsolation is the load-bearing tenant-isolation test: two orgs write
// the same entity kinds; neither can list, get, or update the other's rows.
func TestPerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	mp, err := s.CreateCompany(ctx, Company{ID: "comp_mp", Org: "maxpower", Name: "MaxPower Inc", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatalf("create maxpower company: %v", err)
	}
	if _, err := s.CreateCompany(ctx, Company{ID: "comp_acme", Org: "acme", Name: "Acme LLC", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("create acme company: %v", err)
	}

	// maxpower lists exactly its own.
	list, err := s.ListCompanies(ctx, "maxpower", 100)
	if err != nil {
		t.Fatalf("list maxpower: %v", err)
	}
	if len(list) != 1 || list[0].Name != "MaxPower Inc" {
		t.Fatalf("maxpower must see [MaxPower Inc], got %+v", list)
	}

	// acme cannot GET maxpower's company (cross-tenant read → not found).
	if _, err := s.GetCompany(ctx, "acme", mp.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("acme GET maxpower company want errNotFound, got %v", err)
	}

	// acme cannot UPDATE maxpower's company (cross-tenant write → not found, no mutation).
	if _, err := s.UpdateCompany(ctx, Company{ID: mp.ID, Org: "acme", Name: "HIJACK", UpdatedAt: 2}); !errors.Is(err, errNotFound) {
		t.Fatalf("acme UPDATE maxpower company want errNotFound, got %v", err)
	}
	got, _ := s.GetCompany(ctx, "maxpower", mp.ID)
	if got.Name != "MaxPower Inc" {
		t.Fatalf("maxpower company must be unchanged, got %q", got.Name)
	}

	// acme cannot DELETE maxpower's company.
	deleted, err := s.DeleteCompany(ctx, "acme", mp.ID)
	if err != nil {
		t.Fatalf("acme delete: %v", err)
	}
	if deleted {
		t.Fatalf("acme must not delete maxpower's company")
	}
	if _, err := s.GetCompany(ctx, "maxpower", mp.ID); err != nil {
		t.Fatalf("maxpower company must survive acme delete: %v", err)
	}
}

// TestCompanyCRUD exercises the full company lifecycle round-trip.
func TestCompanyCRUD(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	c, err := s.CreateCompany(ctx, Company{
		ID: "comp_1", Org: "o", Name: "Widgets", DomainName: "widgets.com",
		Employees: 50, ARR: 1_000_000, Currency: "USD", ICP: true, CreatedAt: 10, UpdatedAt: 10,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !c.ICP {
		t.Fatalf("ICP round-trip lost")
	}

	got, err := s.GetCompany(ctx, "o", "comp_1")
	if err != nil || got.DomainName != "widgets.com" || got.Employees != 50 || got.ARR != 1_000_000 {
		t.Fatalf("get mismatch: %+v err=%v", got, err)
	}

	upd, err := s.UpdateCompany(ctx, Company{ID: "comp_1", Org: "o", Name: "Widgets Co", Employees: 75, Currency: "USD", UpdatedAt: 20})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Name != "Widgets Co" || upd.Employees != 75 || upd.ICP {
		t.Fatalf("update mismatch: %+v", upd)
	}

	ok, err := s.DeleteCompany(ctx, "o", "comp_1")
	if err != nil || !ok {
		t.Fatalf("delete want ok, got ok=%v err=%v", ok, err)
	}
	if _, err := s.GetCompany(ctx, "o", "comp_1"); !errors.Is(err, errNotFound) {
		t.Fatalf("after delete want errNotFound, got %v", err)
	}
}

// TestReferentialIntegrity: relations must resolve inside the org, and a
// cross-tenant or missing reference is rejected (errBadRef).
func TestReferentialIntegrity(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if _, err := s.CreateCompany(ctx, Company{ID: "comp_o", Org: "o", Name: "OrgCo", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("seed company: %v", err)
	}

	// Contact referencing a non-existent company → errBadRef.
	if _, err := s.CreateContact(ctx, Contact{ID: "cont_bad", Org: "o", FirstName: "X", CompanyID: "comp_missing", CreatedAt: 1, UpdatedAt: 1}); !errors.Is(err, errBadRef) {
		t.Fatalf("contact bad company want errBadRef, got %v", err)
	}

	// Contact referencing a company in a DIFFERENT org → errBadRef (no cross-tenant ref).
	if _, err := s.CreateCompany(ctx, Company{ID: "comp_other", Org: "other", Name: "OtherCo", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatalf("seed other company: %v", err)
	}
	if _, err := s.CreateContact(ctx, Contact{ID: "cont_x", Org: "o", FirstName: "X", CompanyID: "comp_other", CreatedAt: 1, UpdatedAt: 1}); !errors.Is(err, errBadRef) {
		t.Fatalf("contact cross-tenant company want errBadRef, got %v", err)
	}

	// Valid in-org contact.
	ct, err := s.CreateContact(ctx, Contact{ID: "cont_ok", Org: "o", FirstName: "Ada", LastName: "Lovelace", CompanyID: "comp_o", CreatedAt: 2, UpdatedAt: 2})
	if err != nil {
		t.Fatalf("valid contact: %v", err)
	}

	// Opportunity referencing valid company + contact.
	if _, err := s.CreateOpportunity(ctx, Opportunity{ID: "oppo_ok", Org: "o", Name: "Big Deal", Amount: 5000, Currency: "USD", Stage: "NEW", CompanyID: "comp_o", PointOfContact: ct.ID, CreatedAt: 3, UpdatedAt: 3}); err != nil {
		t.Fatalf("valid opp: %v", err)
	}
	// Opportunity with bad point-of-contact → errBadRef.
	if _, err := s.CreateOpportunity(ctx, Opportunity{ID: "oppo_bad", Org: "o", Name: "Bad", Stage: "NEW", PointOfContact: "cont_ghost", CreatedAt: 3, UpdatedAt: 3}); !errors.Is(err, errBadRef) {
		t.Fatalf("opp bad poc want errBadRef, got %v", err)
	}
}

// TestDeleteClearsRefs: deleting a company/contact NULLs dangling references
// within the same org so no orphaned foreign keys remain.
func TestDeleteClearsRefs(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	_, _ = s.CreateCompany(ctx, Company{ID: "comp_1", Org: "o", Name: "Co", CreatedAt: 1, UpdatedAt: 1})
	ct, _ := s.CreateContact(ctx, Contact{ID: "cont_1", Org: "o", FirstName: "P", CompanyID: "comp_1", CreatedAt: 1, UpdatedAt: 1})
	_, _ = s.CreateOpportunity(ctx, Opportunity{ID: "oppo_1", Org: "o", Name: "Deal", Stage: "NEW", CompanyID: "comp_1", PointOfContact: ct.ID, CreatedAt: 1, UpdatedAt: 1})

	if _, err := s.DeleteCompany(ctx, "o", "comp_1"); err != nil {
		t.Fatalf("delete company: %v", err)
	}
	gotCt, _ := s.GetContact(ctx, "o", "cont_1")
	if gotCt.CompanyID != "" {
		t.Fatalf("contact company ref must be cleared, got %q", gotCt.CompanyID)
	}
	gotOpp, _ := s.GetOpportunity(ctx, "o", "oppo_1")
	if gotOpp.CompanyID != "" {
		t.Fatalf("opp company ref must be cleared, got %q", gotOpp.CompanyID)
	}

	if _, err := s.DeleteContact(ctx, "o", "cont_1"); err != nil {
		t.Fatalf("delete contact: %v", err)
	}
	gotOpp, _ = s.GetOpportunity(ctx, "o", "oppo_1")
	if gotOpp.PointOfContact != "" {
		t.Fatalf("opp poc ref must be cleared, got %q", gotOpp.PointOfContact)
	}
}

// TestListFiltersAndCounts: contact-by-company and opp-by-stage filters plus the
// summary counts are per-org and correct.
func TestListFiltersAndCounts(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	_, _ = s.CreateCompany(ctx, Company{ID: "comp_a", Org: "o", Name: "A", CreatedAt: 1, UpdatedAt: 1})
	_, _ = s.CreateCompany(ctx, Company{ID: "comp_b", Org: "o", Name: "B", CreatedAt: 2, UpdatedAt: 2})
	_, _ = s.CreateContact(ctx, Contact{ID: "cont_a1", Org: "o", FirstName: "A1", CompanyID: "comp_a", CreatedAt: 1, UpdatedAt: 1})
	_, _ = s.CreateContact(ctx, Contact{ID: "cont_a2", Org: "o", FirstName: "A2", CompanyID: "comp_a", CreatedAt: 2, UpdatedAt: 2})
	_, _ = s.CreateContact(ctx, Contact{ID: "cont_b1", Org: "o", FirstName: "B1", CompanyID: "comp_b", CreatedAt: 3, UpdatedAt: 3})
	_, _ = s.CreateOpportunity(ctx, Opportunity{ID: "oppo_1", Org: "o", Name: "D1", Stage: "NEW", CreatedAt: 1, UpdatedAt: 1})
	_, _ = s.CreateOpportunity(ctx, Opportunity{ID: "oppo_2", Org: "o", Name: "D2", Stage: "PROPOSAL", CreatedAt: 2, UpdatedAt: 2})

	byCompany, _ := s.ListContacts(ctx, "o", "comp_a", 100)
	if len(byCompany) != 2 {
		t.Fatalf("comp_a should have 2 contacts, got %d", len(byCompany))
	}
	byStage, _ := s.ListOpportunities(ctx, "o", "PROPOSAL", 100)
	if len(byStage) != 1 || byStage[0].Name != "D2" {
		t.Fatalf("PROPOSAL stage should have [D2], got %+v", byStage)
	}

	companies, contacts, opps, err := s.Counts(ctx, "o")
	if err != nil || companies != 2 || contacts != 3 || opps != 2 {
		t.Fatalf("counts want 2/3/2, got %d/%d/%d err=%v", companies, contacts, opps, err)
	}
	// A different org sees zero.
	c2, ct2, o2, _ := s.Counts(ctx, "empty")
	if c2 != 0 || ct2 != 0 || o2 != 0 {
		t.Fatalf("empty org counts want 0/0/0, got %d/%d/%d", c2, ct2, o2)
	}
}
