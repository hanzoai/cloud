package company

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// testTimeout is a generous per-request ceiling for the in-memory fiber test
// harness. fiber's default (1s) is too tight: under the race detector or an
// occasional GC / cold-sqlite pause a fast handler can momentarily exceed it,
// which would flake the suite. The handlers themselves do no blocking I/O.
const testTimeout = 30 * time.Second

// ---- fakes (isolate the machine + HTTP surface from the sibling subsystems) ----

type fakeKYC struct{}

func (fakeKYC) Name() string { return "fake" }
func (fakeKYC) Start(_ context.Context, _ string, f Founder) (string, string, string, error) {
	return "kyc_" + f.Email, "https://verify.example/" + f.Email, KYCVerified, nil // auto-verify
}
func (fakeKYC) Check(context.Context, string) (string, error) { return KYCVerified, nil }

type fakeCharge struct {
	err     error
	charged int64
}

func (fc *fakeCharge) Charge(_ context.Context, org string, cents int64, _ string) (string, error) {
	if fc.err != nil {
		return "", fc.err
	}
	fc.charged = cents
	return "pay_fake", nil
}

type fakeDocs struct{ n int }

func (fd *fakeDocs) Ingest(_ context.Context, _, name, _ string, _ []byte) (string, error) {
	fd.n++
	return "doc_" + name, nil
}

type fakeEsign struct{}

func (fakeEsign) Name() string { return "fake" }
func (fakeEsign) Request(context.Context, string, []string, []Signer) (string, error) {
	return "esign_fake", nil
}
func (fakeEsign) Status(context.Context, string, string) (bool, error) { return false, nil }

type fakeCapTable struct {
	seedCount int
	imported  int
	roundName string
}

func (fc *fakeCapTable) SetIncorporation(context.Context, string, string, string, string, string) error {
	return nil
}
func (fc *fakeCapTable) SeedFounders(context.Context, string, string, []Founder) error {
	fc.seedCount++
	return nil
}
func (fc *fakeCapTable) AddStakeholders(_ context.Context, _ string, h []Stakeholder) (int, error) {
	fc.imported += len(h)
	return len(h), nil
}
func (fc *fakeCapTable) RecordRound(_ context.Context, _ string, r RoundInput) (string, error) {
	fc.roundName = r.Name
	return "round_fake", nil
}

type fakeAnchor struct{}

func (fakeAnchor) Configured() bool { return false }
func (fakeAnchor) Anchor(_ context.Context, f *Formation) (*Genesis, error) {
	g := genesisRoot(f)
	return &Genesis{Root: "0x" + hexRoot(g), Status: "pending"}, nil
}

func hexRoot(b [32]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xf]
	}
	return string(out)
}

type fakeGoogle struct{}

func (fakeGoogle) ListFolder(context.Context, string, string) ([]DriveFile, error) {
	return []DriveFile{
		{ID: "f1", Name: "articles.pdf", MimeType: "application/pdf"},
		{ID: "f2", Name: "board-consent.pdf", MimeType: "application/pdf"},
	}, nil
}
func (fakeGoogle) Download(context.Context, string, DriveFile) ([]byte, string, error) {
	return []byte("%PDF-1.4 fake"), "application/pdf", nil
}
func (fakeGoogle) SheetValues(context.Context, string, string, string) ([][]string, error) {
	return [][]string{
		{"Name", "Email", "Type", "Relationship"},
		{"Ada Lovelace", "ada@acme.com", "INDIVIDUAL", "FOUNDER"},
		{"Seed Fund LP", "gp@seed.vc", "INSTITUTION", "INVESTOR"},
	}, nil
}

func fakes() providerSet {
	return providerSet{
		kyc: fakeKYC{}, charge: &fakeCharge{}, docs: &fakeDocs{}, esign: fakeEsign{},
		captable: &fakeCapTable{}, anchor: fakeAnchor{}, filing: stubFiling{},
		upgrade: captableUpgrader{}, google: fakeGoogle{},
	}
}

// mountFake mounts company on a bare app and swaps in fake providers. Returns the
// app and the fakes for assertions.
func mountFake(t *testing.T) (*zip.App, *fakeCharge, *fakeCapTable) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	ps := fakes()
	// MarkCompany in the fake flow must not depend on a mounted captable: use a
	// no-op upgrader.
	ps.upgrade = noopUpgrade{}
	mounted.State.prov = ps
	return app, ps.charge.(*fakeCharge), ps.captable.(*fakeCapTable)
}

type noopUpgrade struct{}

func (noopUpgrade) MarkCompany(context.Context, *Formation) error { return nil }

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Fiber().Test(rq, fiber.TestConfig{Timeout: testTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

func stageOf(m map[string]any) string {
	f, _ := m["formation"].(map[string]any)
	if f == nil {
		return ""
	}
	s, _ := f["stage"].(string)
	return s
}

// TestHTTPFormationFlow drives the full formation path over HTTP and asserts it
// lands at company with the fee charged and the cap table seeded.
func TestHTTPFormationFlow(t *testing.T) {
	app, charge, ct := mountFake(t)
	const org = "acme"

	if code, _ := do(t, app, http.MethodPost, "/v1/company", org, map[string]any{
		"structure": "c-corp", "jurisdiction": "DE", "name": "Acme Inc.",
	}); code != http.StatusCreated {
		t.Fatalf("begin want 201, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "founders"}); code != http.StatusOK {
		t.Fatalf("advance founders want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/founders", org, map[string]any{
		"founders": []map[string]any{{"name": "Ada", "email": "ada@acme.com", "equityBps": 10000}},
	}); code != http.StatusOK {
		t.Fatalf("founders want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/kyc", org, nil); code != http.StatusOK {
		t.Fatalf("kyc want 200, got %d", code)
	}
	// KYC done → advance to payment.
	if code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"}); code != http.StatusOK {
		t.Fatalf("advance payment want 200, got %d (%v)", code, m)
	}
	// Pay the fee.
	if code, _ := do(t, app, http.MethodPost, "/v1/company/payment", org, nil); code != http.StatusOK {
		t.Fatalf("payment want 200, got %d", code)
	}
	if charge.charged != formationFeeCents {
		t.Fatalf("charged %d, want %d", charge.charged, formationFeeCents)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "documents"}); code != http.StatusOK {
		t.Fatalf("advance documents want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/documents", org, nil); code != http.StatusOK {
		t.Fatalf("documents want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "esign"}); code != http.StatusOK {
		t.Fatalf("advance esign want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/esign", org, nil); code != http.StatusOK {
		t.Fatalf("esign want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/esign/complete", org, map[string]any{"signed": true}); code != http.StatusOK {
		t.Fatalf("esign complete want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "genesis"}); code != http.StatusOK {
		t.Fatalf("advance genesis want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/genesis", org, nil); code != http.StatusOK {
		t.Fatalf("genesis want 200, got %d", code)
	}
	if ct.seedCount != 1 {
		t.Fatalf("cap table seeded %d times at genesis, want 1", ct.seedCount)
	}
	code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"})
	if code != http.StatusOK {
		t.Fatalf("advance company want 200, got %d", code)
	}
	if stageOf(m) != "company" {
		t.Fatalf("terminal stage want company, got %q", stageOf(m))
	}
}

// TestHTTPPaymentGate proves the $999 gate over HTTP: advancing to documents before
// paying is refused (422), and admitted the moment the fee is paid.
func TestHTTPPaymentGate(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "gate"

	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"structure": "llc", "jurisdiction": "WY", "name": "Gate LLC"})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "founders"})
	do(t, app, http.MethodPost, "/v1/company/founders", org, map[string]any{
		"founders": []map[string]any{{"name": "Bo", "email": "bo@gate.com", "equityBps": 10000}},
	})
	do(t, app, http.MethodPost, "/v1/company/kyc", org, nil)
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"})

	// Unpaid: the documents transition is refused with 422 (the guard, not a 409).
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "documents"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unpaid advance to documents want 422, got %d", code)
	}
	// Pay, then the same advance is admitted.
	do(t, app, http.MethodPost, "/v1/company/payment", org, nil)
	if code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "documents"}); code != http.StatusOK {
		t.Fatalf("paid advance to documents want 200, got %d", code)
	} else if stageOf(m) != "documents" {
		t.Fatalf("stage want documents, got %q", stageOf(m))
	}
}

// TestHTTPIllegalAdvance proves the transition door refuses an illegal jump with 409.
func TestHTTPIllegalAdvance(t *testing.T) {
	app, _, _ := mountFake(t)
	const org = "jump"
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"structure": "c-corp", "jurisdiction": "DE", "name": "Jump Inc."})
	if code, _ := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"}); code != http.StatusConflict {
		t.Fatalf("illegal jump to company want 409, got %d", code)
	}
}

// TestHTTPSkipImport proves the already-incorporated skip path: skip → import →
// (Drive docs + Sheet cap table) → company, never touching KYC/payment/genesis.
func TestHTTPSkipImport(t *testing.T) {
	app, charge, ct := mountFake(t)
	const org = "globex"

	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"alreadyIncorporated": true})
	if code, m := do(t, app, http.MethodPost, "/v1/company/skip", org, nil); code != http.StatusOK || stageOf(m) != "import" {
		t.Fatalf("skip want 200/import, got %d/%q", code, stageOf(m))
	}
	// Import corporate docs from a Drive folder.
	if code, m := do(t, app, http.MethodPost, "/v1/company/import/documents", org, map[string]any{"folderId": "FOLDER"}); code != http.StatusOK {
		t.Fatalf("import documents want 200, got %d", code)
	} else if int(m["ingested"].(float64)) != 2 {
		t.Fatalf("want 2 docs ingested, got %v", m["ingested"])
	}
	// Import the cap table from a Sheet.
	if code, m := do(t, app, http.MethodPost, "/v1/company/import/captable", org, map[string]any{"spreadsheetId": "SHEET"}); code != http.StatusOK {
		t.Fatalf("import captable want 200, got %d", code)
	} else if int(m["stakeholdersImported"].(float64)) != 2 {
		t.Fatalf("want 2 stakeholders, got %v", m["stakeholdersImported"])
	}
	if ct.imported != 2 {
		t.Fatalf("captable adapter recorded %d stakeholders", ct.imported)
	}
	// Finish.
	if code, m := do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"}); code != http.StatusOK || stageOf(m) != "company" {
		t.Fatalf("advance company want 200/company, got %d/%q", code, stageOf(m))
	}
	if charge.charged != 0 {
		t.Fatalf("skip path must never charge, charged %d", charge.charged)
	}
}

// TestHTTPForbiddenWithoutPrincipal proves the tenant gate: no validated principal → 403.
func TestHTTPForbiddenWithoutPrincipal(t *testing.T) {
	app, _, _ := mountFake(t)
	if code, _ := do(t, app, http.MethodGet, "/v1/company", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal GET want 403, got %d", code)
	}
}

// TestHTTPFundraiseRound proves a post-incorporation round is recorded via captable.
func TestHTTPFundraiseRound(t *testing.T) {
	app, _, ct := mountFake(t)
	const org = "acme"
	// Fast-forward to company via the skip path.
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"alreadyIncorporated": true})
	do(t, app, http.MethodPost, "/v1/company/skip", org, nil)
	do(t, app, http.MethodPost, "/v1/company/import/documents", org, map[string]any{"folderId": "F"})
	do(t, app, http.MethodPost, "/v1/company/import/captable", org, map[string]any{"spreadsheetId": "S"})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "company"})

	if code, _ := do(t, app, http.MethodPost, "/v1/company/fundraise/round", org, map[string]any{
		"name": "Seed", "roundType": "PRICED", "targetAmount": 2000000,
	}); code != http.StatusCreated {
		t.Fatalf("fundraise round want 201, got %d", code)
	}
	if ct.roundName != "Seed" {
		t.Fatalf("round not recorded, got %q", ct.roundName)
	}
}

// TestGenesisIdempotent proves a repeated POST /genesis does NOT re-seed the cap
// table (which would double-issue founder share certificates).
func TestGenesisIdempotent(t *testing.T) {
	app, _, ct := mountFake(t)
	const org = "acme"
	do(t, app, http.MethodPost, "/v1/company", org, map[string]any{"structure": "c-corp", "jurisdiction": "DE", "name": "Acme Inc."})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "founders"})
	do(t, app, http.MethodPost, "/v1/company/founders", org, map[string]any{
		"founders": []map[string]any{{"name": "Ada", "email": "ada@acme.com", "equityBps": 10000}},
	})
	do(t, app, http.MethodPost, "/v1/company/kyc", org, nil)
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "payment"})
	do(t, app, http.MethodPost, "/v1/company/payment", org, nil)
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "documents"})
	do(t, app, http.MethodPost, "/v1/company/documents", org, nil)
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "esign"})
	do(t, app, http.MethodPost, "/v1/company/esign", org, nil)
	do(t, app, http.MethodPost, "/v1/company/esign/complete", org, map[string]any{"signed": true})
	do(t, app, http.MethodPost, "/v1/company/advance", org, map[string]any{"to": "genesis"})

	if code, _ := do(t, app, http.MethodPost, "/v1/company/genesis", org, nil); code != http.StatusOK {
		t.Fatalf("first genesis want 200, got %d", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/company/genesis", org, nil); code != http.StatusOK {
		t.Fatalf("second genesis want 200, got %d", code)
	}
	if ct.seedCount != 1 {
		t.Fatalf("genesis must seed the cap table exactly once, got %d", ct.seedCount)
	}
}
