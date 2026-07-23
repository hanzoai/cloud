package legal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/cek"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const testTimeout = 30 * time.Second

func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

// ---- pure engine tests (no HTTP) ----

// TestRenderIsDeterministic proves the engine is a pure function: identical inputs
// yield byte-identical output, so a rendered contract is reproducible.
func TestRenderIsDeterministic(t *testing.T) {
	tmpl, _ := builtin("nda")
	data := map[string]string{
		"effective_date": "2026-01-01", "company_name": "Acme Inc.",
		"counterparty_name": "Beta LLC", "governing_law": "Delaware",
	}
	a, err := Render(tmpl, data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	b, err := Render(tmpl, data)
	if err != nil {
		t.Fatalf("render 2: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("render is not deterministic:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(string(a), "Acme Inc.") || !strings.Contains(string(a), "Beta LLC") {
		t.Fatalf("merge fields not substituted: %s", a)
	}
}

// TestRenderFailsClosedOnMissingField proves a missing merge field is an error listing
// it — never a blank rendered into a contract.
func TestRenderFailsClosedOnMissingField(t *testing.T) {
	tmpl, _ := builtin("nda")
	_, err := Render(tmpl, map[string]string{"company_name": "Acme"})
	if err == nil {
		t.Fatalf("expected a missing-field error")
	}
	for _, want := range []string{"counterparty_name", "effective_date", "governing_law"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should name missing field %q: %v", want, err)
		}
	}
}

// TestCounselNoticeOnSecurities proves the counsel-review boundary is embedded in
// every rendered securities/formation document, and absent from an ops document.
func TestCounselNoticeOnSecurities(t *testing.T) {
	safe, _ := builtin("safe")
	out, err := Render(safe, map[string]string{
		"date": "2026-01-01", "company_name": "Acme", "investor_name": "VC", "purchase_amount": "$100,000",
		"valuation_cap": "$10,000,000", "discount_rate": "20%", "governing_law": "Delaware",
	})
	if err != nil {
		t.Fatalf("render safe: %v", err)
	}
	if !strings.HasPrefix(string(out), CounselNotice) {
		t.Fatalf("securities doc must lead with the counsel notice, got:\n%s", out[:120])
	}
	nda, _ := builtin("nda")
	out2, _ := Render(nda, map[string]string{"effective_date": "x", "company_name": "x", "counterparty_name": "x", "governing_law": "x"})
	if strings.HasPrefix(string(out2), CounselNotice) {
		t.Fatalf("ops doc should not carry the counsel notice")
	}
}

// ---- HTTP harness ----

func mount(t *testing.T) (*zip.App, *audit.Recorder) {
	t.Helper()
	// A unique per-test path (not ":memory:") so each test's cek sidecar is written and
	// read under this process's key — concurrent test binaries never share a sidecar.
	rec, err := audit.Open(filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Audit: rec}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(); _ = rec.Close() })
	return app, rec
}

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

func docFrom(m map[string]any) map[string]any {
	d, _ := m["document"].(map[string]any)
	return d
}

// TestGenerateAndFailClosed proves the generation path renders + seals a document and
// fails closed (400) on a missing merge field.
func TestGenerateAndFailClosed(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"

	// Missing fields → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda", "data": map[string]string{"company_name": "Acme"},
	}); code != http.StatusBadRequest {
		t.Fatalf("missing fields want 400, got %d", code)
	}
	// Complete data → 201 with a rendered body.
	code, m := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda",
		"data": map[string]string{"effective_date": "2026-01-01", "company_name": "Acme Inc.",
			"counterparty_name": "Beta LLC", "governing_law": "Delaware"},
	})
	if code != http.StatusCreated {
		t.Fatalf("generate want 201, got %d (%v)", code, m)
	}
	doc := docFrom(m)
	if doc["status"] != string(StatusDraft) {
		t.Fatalf("new doc status = %v, want draft", doc["status"])
	}
	if !strings.Contains(mapStr(doc, "body"), "Acme Inc.") {
		t.Fatalf("rendered body missing merge value: %v", doc["body"])
	}
	if mapStr(m, "disclaimer") == "" {
		t.Fatalf("generation response must carry the boundary disclaimer")
	}
}

// TestOrgOverrideResolution proves an org override supersedes the builtin (bumped
// version, org origin) and drives generation.
func TestOrgOverrideResolution(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"

	code, _ := do(t, app, http.MethodPut, "/v1/legal/templates/nda", org, map[string]any{
		"body":   "# Custom NDA for {{.company_name}}",
		"fields": []map[string]string{{"key": "company_name", "label": "Company"}},
	})
	if code != http.StatusOK {
		t.Fatalf("override want 200, got %d", code)
	}
	// The resolved template is now the org version (2, origin org).
	_, gt := do(t, app, http.MethodGet, "/v1/legal/templates/nda", org, nil)
	tpl, _ := gt["template"].(map[string]any)
	if tpl["origin"] != "org" || tpl["version"].(float64) != 2 {
		t.Fatalf("resolved template = %v, want org version 2", tpl)
	}
	// Generation uses the override body + its (reduced) field set.
	_, m := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda", "data": map[string]string{"company_name": "Acme"},
	})
	if !strings.Contains(mapStr(docFrom(m), "body"), "Custom NDA for Acme") {
		t.Fatalf("generated doc did not use the override: %v", docFrom(m)["body"])
	}
	// Another org still sees the builtin.
	_, other := do(t, app, http.MethodGet, "/v1/legal/templates/nda", "beta", nil)
	if tb, _ := other["template"].(map[string]any); tb["origin"] != "builtin" {
		t.Fatalf("override leaked across tenants: %v", tb)
	}
}

// TestOverrideCannotDropCounselReview proves an override of a securities/formation
// builtin cannot downgrade the counsel-review boundary.
func TestOverrideCannotDropCounselReview(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"
	code, m := do(t, app, http.MethodPut, "/v1/legal/templates/safe", org, map[string]any{
		"body": "# Our SAFE {{.company_name}}", "counselReview": false,
		"fields": []map[string]string{{"key": "company_name", "label": "Company"}},
	})
	if code != http.StatusOK {
		t.Fatalf("override safe want 200, got %d", code)
	}
	tpl, _ := m["template"].(map[string]any)
	if tpl["counselReview"] != true {
		t.Fatalf("counsel-review boundary was dropped on override: %v", tpl["counselReview"])
	}
	// And the rendered override still carries the counsel notice.
	_, gen := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "safe", "data": map[string]string{"company_name": "Acme"},
	})
	if !strings.HasPrefix(mapStr(docFrom(gen), "body"), CounselNotice) {
		t.Fatalf("override SAFE lost the counsel notice")
	}
}

// TestTenantIsolation proves org B cannot read org A's document.
func TestTenantIsolation(t *testing.T) {
	app, _ := mount(t)
	_, m := do(t, app, http.MethodPost, "/v1/legal/documents", "orga", map[string]any{
		"templateId": "nda",
		"data": map[string]string{"effective_date": "2026-01-01", "company_name": "A",
			"counterparty_name": "B", "governing_law": "Delaware"},
	})
	docID := mapStr(docFrom(m), "id")
	if code, _ := do(t, app, http.MethodGet, "/v1/legal/documents/"+docID, "orgb", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant document read = %d, want 404", code)
	}
	_, list := do(t, app, http.MethodGet, "/v1/legal/documents", "orgb", nil)
	if data, _ := list["data"].([]any); len(data) != 0 {
		t.Fatalf("cross-tenant list leaked %d rows", len(data))
	}
}

// TestAuditCarriesNoBody proves the audit trail references the opaque documentId only
// — the rendered body (with names/terms) never reaches a record.
func TestAuditCarriesNoBody(t *testing.T) {
	app, rec := mount(t)
	const org = "acme"
	_, m := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda",
		"data": map[string]string{"effective_date": "2026-01-01", "company_name": "Acme Inc.",
			"counterparty_name": "Secret Counterparty LLC", "governing_law": "Delaware"},
	})
	docID := mapStr(docFrom(m), "id")

	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: org, Action: "legal.document.generate", Limit: 10})
	if err != nil || len(rows) == 0 {
		t.Fatalf("expected a generate audit record, got %d (%v)", len(rows), err)
	}
	for _, r := range rows {
		blob, _ := json.Marshal(r)
		if strings.Contains(string(blob), "Secret Counterparty") {
			t.Fatalf("document content leaked into audit: %s", blob)
		}
		if !strings.Contains(string(r.After), docID) {
			t.Fatalf("audit record should reference the opaque documentId")
		}
	}
}

// TestEsignLifecycle proves the sign request + completion transitions.
func TestEsignLifecycle(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"
	_, m := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "nda",
		"data": map[string]string{"effective_date": "2026-01-01", "company_name": "Acme",
			"counterparty_name": "Beta", "governing_law": "Delaware"},
	})
	docID := mapStr(docFrom(m), "id")

	code, sm := do(t, app, http.MethodPost, "/v1/legal/documents/"+docID+"/sign", org, map[string]any{
		"signers": []map[string]string{{"name": "Ada", "email": "ada@acme.test"}},
	})
	if code != http.StatusOK || docFrom(sm)["status"] != string(StatusOutForSig) {
		t.Fatalf("sign request → %d status %v, want out_for_signature", code, docFrom(sm)["status"])
	}
	// The stub never self-completes.
	_, nc := do(t, app, http.MethodPost, "/v1/legal/documents/"+docID+"/sign/complete", org, nil)
	if nc["signed"] != false {
		t.Fatalf("stub esign must not self-complete, got %v", nc["signed"])
	}
	// Explicit completion signal → signed.
	_, sc := do(t, app, http.MethodPost, "/v1/legal/documents/"+docID+"/sign/complete", org, map[string]any{"signed": true})
	if docFrom(sc)["status"] != string(StatusSigned) {
		t.Fatalf("completion → status %v, want signed", docFrom(sc)["status"])
	}
}

// TestFilingHonestManual proves a filing records the honest "manual" state (no
// fabricated filing id) and is tenant-scoped over the documents it names.
func TestFilingHonestManual(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"
	_, m := do(t, app, http.MethodPost, "/v1/legal/documents", org, map[string]any{
		"templateId": "ip-assignment",
		"data":       map[string]string{"date": "2026-01-01", "company_name": "Acme", "assignor_name": "Ada", "governing_law": "Delaware"},
	})
	docID := mapStr(docFrom(m), "id")
	code, fm := do(t, app, http.MethodPost, "/v1/legal/filings", org, map[string]any{
		"documentIds": []string{docID}, "jurisdiction": "DE",
	})
	if code != http.StatusCreated {
		t.Fatalf("filing want 201, got %d", code)
	}
	f, _ := fm["filing"].(map[string]any)
	if f["status"] != string(FilingManual) || f["provider"] != "manual" {
		t.Fatalf("filing = %v, want honest manual state", f)
	}
	// A filing that names another org's document is refused.
	if code, _ := do(t, app, http.MethodPost, "/v1/legal/filings", "beta", map[string]any{
		"documentIds": []string{docID},
	}); code != http.StatusNotFound {
		t.Fatalf("cross-tenant filing = %d, want 404", code)
	}
}

// TestForbiddenWithoutPrincipal proves the org gate.
func TestForbiddenWithoutPrincipal(t *testing.T) {
	app, _ := mount(t)
	if code, _ := do(t, app, http.MethodGet, "/v1/legal/templates", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal templates = %d, want 403", code)
	}
}

func mapStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
