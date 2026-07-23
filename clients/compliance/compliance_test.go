package compliance

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
	"github.com/hanzoai/cloud/clients/idv"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const testTimeout = 30 * time.Second

// TestMain seeds a random cek master key so the encrypted-at-rest store opens on an
// encryption-capable test build (mirrors clients/integrations, flags, git, venue). On
// a pure-Go build cek ignores it and uses the plaintext dev path.
func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

// fakeProvider is an injectable idv.Provider whose Start/Check statuses are fixed by
// the test — so we can drive the seam, including a HOSTILE provider that tries to
// report a terminal decision on Start.
type fakeProvider struct {
	name        string
	startStatus idv.Status
	checkStatus idv.Status
	verifyURL   string
}

func (f fakeProvider) Name() string { return f.name }
func (f fakeProvider) Start(context.Context, string, idv.Subject) (idv.Session, error) {
	return idv.Session{Ref: "ref_" + f.name, VerifyURL: f.verifyURL, Status: f.startStatus}, nil
}
func (f fakeProvider) Check(context.Context, string, string) (idv.Result, error) {
	return idv.Result{Ref: "ref_" + f.name, Status: f.checkStatus}, nil
}

// mount brings compliance up on a bare app with a real in-memory audit recorder, and
// returns the app + the recorder for assertions. The provider defaults to Manual.
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

// setProvider swaps the wired verification provider after mount (mirrors how the
// company suite swaps its provider set).
func setProvider(p idv.Provider) { mounted.State.idv = p }

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
		rq.Header.Set("X-User-Id", "u_"+org) // a validated principal (principal.Org gate)
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

// startCheck creates a subject + verification inline and returns the check view.
func startCheck(t *testing.T, app *zip.App, org, email string) map[string]any {
	t.Helper()
	code, m := do(t, app, http.MethodPost, "/v1/compliance/verifications", org, map[string]any{
		"kind": "individual", "email": email, "name": "Test Person", "ref": "ext-1",
	})
	if code != http.StatusCreated {
		t.Fatalf("start verification want 201, got %d (%v)", code, m)
	}
	return m
}

// TestManualNeverAutoApproves is the load-bearing property with the DEFAULT provider:
// creating and refreshing a verification never yields a verified status. The only
// route to a terminal status is the authenticated callback.
func TestManualNeverAutoApproves(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"

	chk := startCheck(t, app, org, "ada@acme.test")
	if chk["status"] != string(idv.StatusPending) {
		t.Fatalf("fresh verification status = %v, want pending", chk["status"])
	}
	if chk["provider"] != "manual" {
		t.Fatalf("default provider = %v, want manual", chk["provider"])
	}
	id, _ := chk["id"].(string)

	// Refresh (manual Check reports pending) — still not verified.
	_, m := do(t, app, http.MethodPost, "/v1/compliance/verifications/"+id+"/refresh", org, nil)
	if m["status"] != string(idv.StatusPending) {
		t.Fatalf("manual refresh status = %v, want pending (never self-verifies)", m["status"])
	}
}

// TestStartClampsHostileTerminal proves the product-boundary clamp: even a provider
// that returns provider_verified from Start yields a PENDING check — a "start" is
// never a decision.
func TestStartClampsHostileTerminal(t *testing.T) {
	app, _ := mount(t)
	setProvider(fakeProvider{name: "hostile", startStatus: idv.StatusVerified, checkStatus: idv.StatusVerified})
	const org = "acme"

	chk := startCheck(t, app, org, "eve@acme.test")
	if chk["status"] != string(idv.StatusPending) {
		t.Fatalf("hostile Start terminal not clamped: status = %v, want pending", chk["status"])
	}
}

// TestCallbackIsTheDecisionPath proves a terminal status is reachable only via the
// authenticated, audited callback — and is attributed to the acting reviewer.
func TestCallbackIsTheDecisionPath(t *testing.T) {
	app, rec := mount(t)
	const org = "acme"
	chk := startCheck(t, app, org, "ada@acme.test")
	id, _ := chk["id"].(string)

	code, m := do(t, app, http.MethodPost, "/v1/compliance/verifications/callback", org,
		map[string]any{"id": id, "status": string(idv.StatusVerified)})
	if code != http.StatusOK {
		t.Fatalf("callback want 200, got %d (%v)", code, m)
	}
	if m["status"] != string(idv.StatusVerified) {
		t.Fatalf("after callback status = %v, want provider_verified", m["status"])
	}
	if m["decidedBy"] != "u_"+org {
		t.Fatalf("decidedBy = %v, want the acting reviewer u_%s", m["decidedBy"], org)
	}
	// A decision must be in the tamper-evident trail.
	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: org, Action: "compliance.verification.decision", Limit: 10})
	if err != nil || len(rows) == 0 {
		t.Fatalf("expected a decision audit record, got %d (%v)", len(rows), err)
	}
}

// TestTenantIsolation proves org B can neither read nor list org A's records.
func TestTenantIsolation(t *testing.T) {
	app, _ := mount(t)
	// org A creates a subject + verification.
	_, sub := do(t, app, http.MethodPost, "/v1/compliance/subjects", "orga",
		map[string]any{"kind": "individual", "email": "a@orga.test"})
	subID, _ := sub["id"].(string)
	chk := startCheck(t, app, "orga", "a@orga.test")
	chkID, _ := chk["id"].(string)

	// org B cannot read them (404 — indistinguishable from "does not exist").
	if code, _ := do(t, app, http.MethodGet, "/v1/compliance/subjects/"+subID, "orgb", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant subject read = %d, want 404", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/compliance/verifications/"+chkID, "orgb", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant verification read = %d, want 404", code)
	}
	// org B's list is empty.
	_, list := do(t, app, http.MethodGet, "/v1/compliance/verifications", "orgb", nil)
	if data, _ := list["data"].([]any); len(data) != 0 {
		t.Fatalf("cross-tenant list leaked %d rows", len(data))
	}
	// org B cannot decide on org A's check either.
	if code, _ := do(t, app, http.MethodPost, "/v1/compliance/verifications/callback", "orgb",
		map[string]any{"id": chkID, "status": string(idv.StatusVerified)}); code != http.StatusNotFound {
		t.Fatalf("cross-tenant decision = %d, want 404", code)
	}
}

// TestAuditCarriesNoPII proves the audit trail references opaque ids only — the
// subject's email/name never reach a record.
func TestAuditCarriesNoPII(t *testing.T) {
	app, rec := mount(t)
	const org, email = "acme", "secret.person@acme.test"
	startCheck(t, app, org, email)

	rows, _, err := rec.Query(context.Background(), audit.Filter{Org: org, Limit: 100})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("expected audit records for the org")
	}
	sawStart := false
	for _, r := range rows {
		blob, _ := json.Marshal(r)
		if strings.Contains(string(blob), email) || strings.Contains(string(blob), "Test Person") {
			t.Fatalf("PII leaked into audit record: %s", blob)
		}
		if r.Action == "compliance.verification.start" {
			sawStart = true
			if !strings.Contains(string(r.After), "checkId") {
				t.Fatalf("start record should reference the opaque checkId: %s", r.After)
			}
		}
	}
	if !sawStart {
		t.Fatalf("no compliance.verification.start audit record found")
	}
}

// TestListSubjectsOmitsPII proves the list projection carries no name/email, while
// the explicit single-subject read (to the owning org) does.
func TestListSubjectsOmitsPII(t *testing.T) {
	app, _ := mount(t)
	const org, email = "acme", "ada@acme.test"
	_, sub := do(t, app, http.MethodPost, "/v1/compliance/subjects", org,
		map[string]any{"kind": "individual", "email": email, "name": "Ada Lovelace"})
	subID, _ := sub["id"].(string)

	_, list := do(t, app, http.MethodGet, "/v1/compliance/subjects", org, nil)
	blob, _ := json.Marshal(list["data"])
	if strings.Contains(string(blob), email) || strings.Contains(string(blob), "Ada Lovelace") {
		t.Fatalf("subject list leaked PII: %s", blob)
	}
	// The explicit single read returns the contact PII to the owning org.
	_, one := do(t, app, http.MethodGet, "/v1/compliance/subjects/"+subID, org, nil)
	if one["email"] != email {
		t.Fatalf("single subject read should return the email to the owner, got %v", one["email"])
	}
}

// TestProviderSeamRefreshReportsDecision proves refresh reflects a provider's settled
// decision (provider-reported), attributed to the provider.
func TestProviderSeamRefreshReportsDecision(t *testing.T) {
	app, _ := mount(t)
	setProvider(fakeProvider{name: "persona", startStatus: idv.StatusPending, checkStatus: idv.StatusVerified, verifyURL: "https://verify.example/x"})
	const org = "acme"

	chk := startCheck(t, app, org, "ada@acme.test")
	if chk["verifyUrl"] != "https://verify.example/x" {
		t.Fatalf("hosted verify URL not surfaced: %v", chk["verifyUrl"])
	}
	id, _ := chk["id"].(string)
	_, m := do(t, app, http.MethodPost, "/v1/compliance/verifications/"+id+"/refresh", org, nil)
	if m["status"] != string(idv.StatusVerified) {
		t.Fatalf("refresh status = %v, want provider_verified", m["status"])
	}
	if m["decidedBy"] != "persona" {
		t.Fatalf("decidedBy = %v, want the provider name", m["decidedBy"])
	}
}

// TestAccreditationTracking proves accreditation is TRACKED, not certified: a create
// cannot record reviewer_confirmed (that requires a reviewer decision), and the
// decision endpoint attributes the confirmation to the reviewer.
func TestAccreditationTracking(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"
	_, sub := do(t, app, http.MethodPost, "/v1/compliance/subjects", org,
		map[string]any{"kind": "individual", "email": "inv@acme.test"})
	subID, _ := sub["id"].(string)

	// A create cannot self-confirm.
	if code, _ := do(t, app, http.MethodPost, "/v1/compliance/accreditation", org, map[string]any{
		"subjectId": subID, "method": "self_attested", "basis": "income", "status": "reviewer_confirmed",
	}); code != http.StatusBadRequest {
		t.Fatalf("create with reviewer_confirmed = %d, want 400", code)
	}
	// A plain create defaults to asserted.
	code, acc := do(t, app, http.MethodPost, "/v1/compliance/accreditation", org, map[string]any{
		"subjectId": subID, "method": "self_attested", "basis": "income",
	})
	if code != http.StatusCreated || acc["status"] != string(AccAsserted) {
		t.Fatalf("create = %d status %v, want 201 asserted", code, acc["status"])
	}
	accID, _ := acc["id"].(string)
	// The reviewer decision attributes the confirmation.
	_, dec := do(t, app, http.MethodPost, "/v1/compliance/accreditation/"+accID+"/decision", org,
		map[string]any{"status": "reviewer_confirmed"})
	if dec["status"] != string(AccReviewerConfirmed) || dec["reviewerSub"] != "u_"+org {
		t.Fatalf("decision = %v, want reviewer_confirmed by u_%s", dec, org)
	}
}

// TestForbiddenWithoutPrincipal proves the org gate: an unvalidated request (no
// X-User-Id) is refused, never served another tenant's data.
func TestForbiddenWithoutPrincipal(t *testing.T) {
	app, _ := mount(t)
	if code, _ := do(t, app, http.MethodGet, "/v1/compliance/status", "", nil); code != http.StatusForbidden {
		t.Fatalf("no-principal status = %d, want 403", code)
	}
	if code, _ := do(t, app, http.MethodPost, "/v1/compliance/subjects", "", map[string]any{"kind": "individual", "email": "x@y.z"}); code != http.StatusForbidden {
		t.Fatalf("no-principal create = %d, want 403", code)
	}
}

// TestStatusIsHonest proves the posture read reports provider-reported counts + the
// boundary disclaimer, never a boolean "compliant".
func TestStatusIsHonest(t *testing.T) {
	app, _ := mount(t)
	const org = "acme"
	startCheck(t, app, org, "ada@acme.test")
	code, m := do(t, app, http.MethodGet, "/v1/compliance/status", org, nil)
	if code != http.StatusOK {
		t.Fatalf("status want 200, got %d", code)
	}
	if _, hasCompliant := m["compliant"]; hasCompliant {
		t.Fatalf("status must NOT assert a boolean 'compliant'")
	}
	if !strings.Contains(mapString(m, "disclaimer"), "does not determine or certify") {
		t.Fatalf("status missing the boundary disclaimer: %v", m["disclaimer"])
	}
}

func mapString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}
