package company

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "company.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestStorePerOrgIsolation proves one org's formation is invisible to another.
func TestStorePerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	acme := &Formation{Org: "acme", Stage: StageStructure, Structure: StructureCCorp, Name: "Acme Inc.", CreatedAt: 1, UpdatedAt: 1}
	if err := s.Put(ctx, acme); err != nil {
		t.Fatalf("put acme: %v", err)
	}

	// globex has no formation.
	if _, err := s.Get(ctx, "globex"); !errors.Is(err, errNotFound) {
		t.Fatalf("globex should have no formation, got %v", err)
	}

	// acme reads its own back, with fields intact.
	got, err := s.Get(ctx, "acme")
	if err != nil || got.Name != "Acme Inc." || got.Structure != StructureCCorp {
		t.Fatalf("acme read-back: %+v err=%v", got, err)
	}
}

// TestStoreRoundTrip proves the full aggregate (founders, genesis, imported docs)
// survives a marshal/unmarshal round-trip through the store.
func TestStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	in := &Formation{
		Org: "acme", Stage: StageGenesis, Structure: StructureCCorp, Jurisdiction: JurisdictionDE,
		Name: "Acme Inc.", Paid: true, PaymentRef: "pay_1", Signed: true, EsignRef: "es_1",
		Founders: []Founder{
			{Name: "Ada", Email: "ada@acme.com", EquityBps: 6000, KYCStatus: KYCVerified},
			{Name: "Bo", Email: "bo@acme.com", EquityBps: 4000, KYCStatus: KYCVerified},
		},
		DocumentIDs: []string{"doc_1", "doc_2"},
		Genesis:     &Genesis{Root: "0xabc", Status: "pending", ChainID: 36963},
		CreatedAt:   1, UpdatedAt: 2,
	}
	if err := s.Put(ctx, in); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := s.Get(ctx, "acme")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Founders) != 2 || got.Founders[0].EquityBps != 6000 {
		t.Fatalf("founders lost: %+v", got.Founders)
	}
	if got.Genesis == nil || got.Genesis.Root != "0xabc" || !got.Paid || !got.Signed {
		t.Fatalf("scalar/genesis fields lost: %+v", got)
	}
	if len(got.DocumentIDs) != 2 {
		t.Fatalf("document ids lost: %+v", got.DocumentIDs)
	}

	// Update (upsert) preserves identity.
	in.Stage = StageCompany
	if err := s.Put(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = s.Get(ctx, "acme")
	if got.Stage != StageCompany {
		t.Fatalf("upsert stage want company, got %s", got.Stage)
	}
}

// TestParseCapTableRows proves the import parser maps a founder-style sheet to
// stakeholders and rejects a sheet missing required columns.
func TestParseCapTableRows(t *testing.T) {
	rows := [][]string{
		{"Name", "Email", "Type", "Relationship"},
		{"Ada Lovelace", "ada@acme.com", "INDIVIDUAL", "FOUNDER"},
		{"Seed Fund", "gp@seed.vc", "INSTITUTION", "INVESTOR"},
		{"", "", "", ""}, // blank row skipped
	}
	holders, err := parseCapTableRows(rows)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(holders) != 2 {
		t.Fatalf("want 2 stakeholders, got %d", len(holders))
	}
	if holders[0].Email != "ada@acme.com" || holders[0].CurrentRelationship != "FOUNDER" {
		t.Fatalf("row 1 mismapped: %+v", holders[0])
	}

	if _, err := parseCapTableRows([][]string{{"Foo", "Bar"}, {"x", "y"}}); err == nil {
		t.Fatal("a sheet without name/email columns must be rejected")
	}
}
