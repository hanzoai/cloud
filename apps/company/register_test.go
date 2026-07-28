package company

import (
	"context"
	"testing"
)

// seedBook writes a small book across several orgs and stages: the shape the
// platform register has to answer questions about.
func seedBook(t *testing.T, s *Store) context.Context {
	t.Helper()
	ctx := context.Background()
	book := []*Formation{
		{Org: "acme", Stage: StageFounders, Structure: StructureCCorp, Name: "Acme Inc.", CreatedAt: 10, UpdatedAt: 40,
			Founders: []Founder{
				{Name: "Ada", Email: "ada@acme.test", KYCStatus: KYCPending},
				{Name: "Grace", Email: "grace@acme.test", KYCStatus: KYCVerified},
			}},
		{Org: "globex", Stage: StageFounders, Structure: StructureLLC, Name: "Globex LLC", CreatedAt: 20, UpdatedAt: 20,
			Founders: []Founder{
				{Name: "Alan", Email: "alan@globex.test", KYCStatus: KYCPending},
			}},
		{Org: "initech", Stage: StageCompany, Structure: StructureCCorp, Name: "Initech Inc.", CreatedAt: 5, UpdatedAt: 60,
			Founders: []Founder{
				{Name: "Peter", Email: "peter@initech.test", KYCStatus: KYCReviewerConfirmed},
			}},
		{Org: "hooli", Stage: StageDocuments, Structure: StructureDAOLLC, Name: "Hooli DAO", CreatedAt: 30, UpdatedAt: 30,
			Founders: []Founder{
				{Name: "Gavin", Email: "gavin@hooli.test", KYCStatus: KYCVerified},
			}},
	}
	for _, f := range book {
		if err := s.Put(ctx, f); err != nil {
			t.Fatalf("seed %s: %v", f.Org, err)
		}
	}
	return ctx
}

// TestListReadsTheWholeBook is the point of the register: Get answers a question
// about ONE org and structurally cannot tell Hanzo how many entities it formed.
func TestListReadsTheWholeBook(t *testing.T) {
	s := testStore(t)
	ctx := seedBook(t, s)

	rows, err := s.List(ctx, Filter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 formations across orgs, got %d", len(rows))
	}
	// Newest activity first, so a back office sees what moved most recently.
	if rows[0].Org != "initech" || rows[1].Org != "acme" {
		t.Fatalf("want updated_at DESC (initech, acme, ...), got %s, %s", rows[0].Org, rows[1].Org)
	}
	// The projection is served from columns — no document decode required.
	if rows[0].Name != "Initech Inc." || rows[0].Structure != StructureCCorp {
		t.Fatalf("projection not populated: %+v", rows[0])
	}
}

// TestListFilters proves the stage and structure narrowings both bind.
func TestListFilters(t *testing.T) {
	s := testStore(t)
	ctx := seedBook(t, s)

	byStage, err := s.List(ctx, Filter{Stage: StageFounders})
	if err != nil {
		t.Fatalf("list by stage: %v", err)
	}
	if len(byStage) != 2 {
		t.Fatalf("want 2 at founders, got %d", len(byStage))
	}

	byStructure, err := s.List(ctx, Filter{Structure: StructureCCorp})
	if err != nil {
		t.Fatalf("list by structure: %v", err)
	}
	if len(byStructure) != 2 {
		t.Fatalf("want 2 c-corps, got %d", len(byStructure))
	}

	both, err := s.List(ctx, Filter{Stage: StageFounders, Structure: StructureCCorp})
	if err != nil {
		t.Fatalf("list by both: %v", err)
	}
	if len(both) != 1 || both[0].Org != "acme" {
		t.Fatalf("want only acme, got %+v", both)
	}
}

// TestListPages proves the page is bounded and offset advances, so a growing book
// never silently becomes an unbounded scan.
func TestListPages(t *testing.T) {
	s := testStore(t)
	ctx := seedBook(t, s)

	first, err := s.List(ctx, Filter{Limit: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %+v err=%v", first, err)
	}
	second, err := s.List(ctx, Filter{Limit: 2, Offset: 2})
	if err != nil || len(second) != 2 {
		t.Fatalf("second page: %+v err=%v", second, err)
	}
	if first[0].Org == second[0].Org {
		t.Fatalf("offset did not advance: %s twice", first[0].Org)
	}
}

// TestCountShapesTheBook proves a queue that is growing is visible as a number.
func TestCountShapesTheBook(t *testing.T) {
	s := testStore(t)
	ctx := seedBook(t, s)

	counts, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts[StageFounders] != 2 || counts[StageCompany] != 1 || counts[StageDocuments] != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

// TestPendingDecodesOnlyFoundersStage proves the one listing that decodes stays
// bounded: only StageFounders rows are read, because guardKYCVerified gates the
// edge out of that stage, so no other stage can hold KYC in flight.
func TestPendingDecodesOnlyFoundersStage(t *testing.T) {
	s := testStore(t)
	ctx := seedBook(t, s)

	pending, err := s.Pending(ctx, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("want 2 founders-stage formations, got %d", len(pending))
	}
	for _, f := range pending {
		if f.Stage != StageFounders {
			t.Fatalf("decoded a %s row; only founders should be read", f.Stage)
		}
	}
	// Oldest first, so the queue drains in the order founders have waited.
	if pending[0].Org != "globex" {
		t.Fatalf("want oldest (globex) first, got %s", pending[0].Org)
	}
	// The document round-trips — the queue needs founder detail from the JSON.
	if len(pending[0].Founders) != 1 || pending[0].Founders[0].Email != "alan@globex.test" {
		t.Fatalf("founders lost in decode: %+v", pending[0].Founders)
	}
}

// TestSettledKYC pins the queue's membership rule. verified and reviewer_confirmed
// stay DISTINCT values — the manual path never launders itself into looking
// provider-reported — but both are terminal, so neither belongs in a review queue.
func TestSettledKYC(t *testing.T) {
	for status, want := range map[string]bool{
		KYCPending:           false,
		"":                   false,
		KYCVerified:          true,
		KYCReviewerConfirmed: true,
		KYCFailed:            true,
	} {
		if got := settledKYC(status); got != want {
			t.Fatalf("settledKYC(%q) = %v, want %v", status, got, want)
		}
	}
	if KYCVerified == KYCReviewerConfirmed {
		t.Fatal("reviewer_confirmed must stay distinct from provider verified")
	}
}

// TestKnownStage proves a filter typo is a 400 rather than a silently empty page.
func TestKnownStage(t *testing.T) {
	for _, s := range []Stage{StageStructure, StageFounders, StagePayment,
		StageDocuments, StageEsign, StageGenesis, StageCompany, StageImport} {
		if !knownStage(s) {
			t.Fatalf("%q is a machine stage but knownStage says otherwise", s)
		}
	}
	for _, s := range []Stage{"", "founder", "COMPANY", "nonsense"} {
		if knownStage(s) {
			t.Fatalf("knownStage accepted %q", s)
		}
	}
}

// TestEmptyBook proves the register returns an empty list rather than nil, so a
// back office renders "no formations" instead of failing on a null.
func TestEmptyBook(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	rows, err := s.List(ctx, Filter{})
	if err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("empty book: rows=%+v err=%v", rows, err)
	}
	pending, err := s.Pending(ctx, 0)
	if err != nil || pending == nil || len(pending) != 0 {
		t.Fatalf("empty queue: pending=%+v err=%v", pending, err)
	}
}
