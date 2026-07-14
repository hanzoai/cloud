package company

import (
	"errors"
	"strings"
	"testing"
)

// verifiedFounder is a KYC-passed founder owning the whole company — the minimal
// input that satisfies guardKYCVerified.
func verifiedFounder() Founder {
	return Founder{Name: "Ada Lovelace", Email: "ada@example.com", EquityBps: 10000, KYCStatus: KYCVerified}
}

// fullFormation returns a formation at structure with everything the DOWNSTREAM
// guards will need already populated, so a test can drive it stage by stage.
func fullFormation() *Formation {
	return &Formation{
		Org:          "acme",
		Structure:    StructureCCorp,
		Jurisdiction: JurisdictionDE,
		Name:         "Acme Inc.",
		Stage:        StageStructure,
		Founders:     []Founder{verifiedFounder()},
	}
}

// TestHappyPath drives the full formation path structure → … → company, asserting
// each legal, guard-satisfied transition advances the stage.
func TestHappyPath(t *testing.T) {
	f := fullFormation()

	steps := []struct {
		to    Stage
		setup func(*Formation)
	}{
		{StageFounders, nil},
		{StagePayment, nil}, // founder already verified
		{StageDocuments, func(f *Formation) { f.Paid = true }},
		{StageEsign, func(f *Formation) { f.DocumentIDs = []string{"doc_1"} }},
		{StageGenesis, func(f *Formation) { f.Signed = true }},
		{StageCompany, func(f *Formation) { f.Genesis = &Genesis{Root: "0xabc"} }},
	}
	for _, s := range steps {
		if s.setup != nil {
			s.setup(f)
		}
		if err := Advance(f, s.to); err != nil {
			t.Fatalf("advance to %s: %v", s.to, err)
		}
		if f.Stage != s.to {
			t.Fatalf("stage want %s, got %s", s.to, f.Stage)
		}
	}
	if f.Stage != StageCompany {
		t.Fatalf("terminal want %s, got %s", StageCompany, f.Stage)
	}
}

// TestIllegalTransitionsRefused proves the machine refuses every edge not in the
// transition table — you cannot skip stages or jump backward.
func TestIllegalTransitionsRefused(t *testing.T) {
	cases := []struct {
		from Stage
		to   Stage
	}{
		{StageStructure, StageCompany},   // cannot jump straight to done
		{StageStructure, StagePayment},   // cannot pay before founders
		{StageStructure, StageDocuments}, // cannot skip to docs
		{StageFounders, StageDocuments},  // cannot skip payment
		{StagePayment, StageEsign},       // cannot skip document generation
		{StageDocuments, StageGenesis},   // cannot skip signing
		{StageEsign, StageCompany},       // cannot skip genesis
		{StageCompany, StageStructure},   // terminal has no out-edge
		{StageImport, StageFounders},     // import path does not rejoin the formation path
	}
	for _, c := range cases {
		f := fullFormation()
		f.Stage = c.from
		// satisfy every possible guard so the ONLY reason to refuse is the missing edge
		f.Paid, f.Signed = true, true
		f.DocumentIDs = []string{"doc_1"}
		f.Genesis = &Genesis{Root: "0xabc"}
		f.AlreadyIncorporated, f.Imported, f.CapTableImported = true, true, true
		err := Advance(f, c.to)
		if !errors.Is(err, errIllegalTransition) {
			t.Fatalf("%s → %s: want errIllegalTransition, got %v", c.from, c.to, err)
		}
		if f.Stage != c.from {
			t.Fatalf("%s → %s: refused transition must not mutate stage, got %s", c.from, c.to, f.Stage)
		}
	}
}

// TestPaymentGate is the load-bearing gate test: from payment you cannot reach
// documents until Paid is true, and the moment it is, the SAME transition succeeds.
func TestPaymentGate(t *testing.T) {
	f := fullFormation()
	f.Stage = StagePayment
	f.Paid = false

	err := Advance(f, StageDocuments)
	if err == nil {
		t.Fatal("payment gate: advancing to documents unpaid must fail")
	}
	if errors.Is(err, errIllegalTransition) {
		t.Fatalf("payment gate: the edge exists — failure must be the guard, not illegal-transition: %v", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Fatalf("payment gate: error should name the $999 fee, got %q", err)
	}
	if f.Stage != StagePayment {
		t.Fatalf("payment gate: blocked transition must not advance, got %s", f.Stage)
	}

	// Pay, then the identical transition is admitted.
	f.Paid = true
	if err := Advance(f, StageDocuments); err != nil {
		t.Fatalf("payment gate: paid advance to documents failed: %v", err)
	}
	if f.Stage != StageDocuments {
		t.Fatalf("payment gate: paid advance want documents, got %s", f.Stage)
	}
}

// TestKYCGate proves the payment step is unreachable until every founder is verified.
func TestKYCGate(t *testing.T) {
	f := fullFormation()
	f.Stage = StageFounders
	f.Founders = []Founder{
		{Name: "Ada", Email: "ada@x.com", KYCStatus: KYCVerified},
		{Name: "Bo", Email: "bo@x.com", KYCStatus: KYCPending}, // one unverified
	}
	if err := Advance(f, StagePayment); err == nil {
		t.Fatal("kyc gate: advancing to payment with an unverified founder must fail")
	} else if errors.Is(err, errIllegalTransition) {
		t.Fatalf("kyc gate: the edge exists — failure must be the guard: %v", err)
	}
	if f.Stage != StageFounders {
		t.Fatalf("kyc gate: blocked transition must not advance, got %s", f.Stage)
	}

	// Verify the second founder; the transition now succeeds.
	f.Founders[1].KYCStatus = KYCVerified
	if err := Advance(f, StagePayment); err != nil {
		t.Fatalf("kyc gate: all-verified advance failed: %v", err)
	}
}

// TestKYCGateNoFounders proves KYC cannot pass with zero founders.
func TestKYCGateNoFounders(t *testing.T) {
	f := fullFormation()
	f.Stage = StageFounders
	f.Founders = nil
	if err := Advance(f, StagePayment); err == nil {
		t.Fatal("kyc gate: zero founders must not pass")
	}
}

// TestSkipPath proves the already-incorporated shortcut: structure → import →
// company, bypassing KYC, payment, documents, e-sign, and genesis entirely.
func TestSkipPath(t *testing.T) {
	f := &Formation{Org: "globex", Stage: StageStructure}

	// Skip is refused until the org declares it is already incorporated.
	if err := Advance(f, StageImport); err == nil {
		t.Fatal("skip: import must be refused before alreadyIncorporated is set")
	} else if errors.Is(err, errIllegalTransition) {
		t.Fatalf("skip: the edge exists — failure must be the guard: %v", err)
	}

	f.AlreadyIncorporated = true
	if err := Advance(f, StageImport); err != nil {
		t.Fatalf("skip: import advance failed: %v", err)
	}
	if f.Stage != StageImport {
		t.Fatalf("skip: want import, got %s", f.Stage)
	}

	// Completing the import requires BOTH corporate docs and the cap table.
	if err := Advance(f, StageCompany); err == nil {
		t.Fatal("skip: completing with nothing imported must fail")
	}
	f.CapTableImported = true
	if err := Advance(f, StageCompany); err == nil {
		t.Fatal("skip: completing with cap table but no docs must fail")
	}
	f.Imported = true
	if err := Advance(f, StageCompany); err != nil {
		t.Fatalf("skip: complete import failed: %v", err)
	}
	if f.Stage != StageCompany {
		t.Fatalf("skip: terminal want company, got %s", f.Stage)
	}
}

// TestSkipPathNeverTouchesFormationGates proves the skip path does NOT require
// payment/KYC/genesis: a formation that skipped reaches company with Paid=false,
// no founders, and no genesis.
func TestSkipPathNeverTouchesFormationGates(t *testing.T) {
	f := &Formation{Org: "globex", Stage: StageStructure, AlreadyIncorporated: true}
	if err := Advance(f, StageImport); err != nil {
		t.Fatalf("skip import: %v", err)
	}
	f.Imported, f.CapTableImported = true, true
	if err := Advance(f, StageCompany); err != nil {
		t.Fatalf("skip complete: %v", err)
	}
	if f.Paid {
		t.Fatal("skip path must not require payment")
	}
	if len(f.Founders) != 0 || f.Genesis != nil {
		t.Fatal("skip path must not require founders/KYC/genesis")
	}
}

// TestStructureGuard proves structure/jurisdiction/name are all validated before
// leaving the initial stage.
func TestStructureGuard(t *testing.T) {
	cases := []struct {
		name string
		f    *Formation
	}{
		{"bad structure", &Formation{Stage: StageStructure, Structure: "gmbh", Jurisdiction: JurisdictionDE, Name: "X"}},
		{"bad jurisdiction", &Formation{Stage: StageStructure, Structure: StructureLLC, Jurisdiction: "CA", Name: "X"}},
		{"empty name", &Formation{Stage: StageStructure, Structure: StructureLLC, Jurisdiction: JurisdictionWY, Name: "  "}},
	}
	for _, c := range cases {
		if err := Advance(c.f, StageFounders); err == nil {
			t.Fatalf("%s: structure guard must reject", c.name)
		}
	}
	ok := &Formation{Stage: StageStructure, Structure: StructureDAOLLC, Jurisdiction: JurisdictionWY, Name: "Zoo DAO LLC"}
	if err := Advance(ok, StageFounders); err != nil {
		t.Fatalf("valid structure must pass: %v", err)
	}
}

// TestNextStages reports the machine's out-edges for the UI.
func TestNextStages(t *testing.T) {
	if got := NextStages(&Formation{Stage: StageStructure}); len(got) != 2 {
		t.Fatalf("structure has 2 out-edges (founders, import), got %v", got)
	}
	if got := NextStages(&Formation{Stage: StageCompany}); len(got) != 0 {
		t.Fatalf("terminal company has no out-edge, got %v", got)
	}
}
