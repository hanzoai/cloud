package crm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (routes() says why): the program's composer does, once at the root. In a test
// the test IS the composer, so it owes the same install — skipping it does not
// test a stricter program, it tests one where every org-scoped op answers 403
// for a reason production could never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (principal.Acting gates on it)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestHTTPRoundTripAndIsolation is the end-to-end wire proof: create→list per
// org, cross-tenant reads blocked, no-org rejected, validation enforced. This is
// the exact behavior the live curl proof exercises.
func TestHTTPRoundTripAndIsolation(t *testing.T) {
	app := mountApp(t)

	// No org → 403 on every collection.
	for _, p := range []string{"/v1/crm/companies", "/v1/crm/contacts", "/v1/crm/opportunities", "/v1/crm/summary"} {
		if code, _ := do(t, app, http.MethodGet, p, "", nil); code != http.StatusForbidden {
			t.Fatalf("no-org GET %s want 403, got %d", p, code)
		}
	}

	// maxpower creates a company.
	code, body := do(t, app, http.MethodPost, "/v1/crm/companies", "maxpower",
		map[string]any{"name": "MaxPower Inc", "domainName": "maxpower.ai", "employees": 42, "idealCustomerProfile": true})
	if code != http.StatusCreated {
		t.Fatalf("create company want 201, got %d (%s)", code, body)
	}
	var comp Company
	if err := json.Unmarshal(body, &comp); err != nil || comp.ID == "" {
		t.Fatalf("create company json: %v (%s)", err, body)
	}
	if comp.Name != "MaxPower Inc" || comp.Employees != 42 || !comp.ICP {
		t.Fatalf("create company round-trip mismatch: %+v", comp)
	}

	// maxpower creates a contact linked to that company.
	code, body = do(t, app, http.MethodPost, "/v1/crm/contacts", "maxpower",
		map[string]any{"firstName": "Dave", "lastName": "Lorenzini", "email": "dave@maxpower.ai", "jobTitle": "CEO", "companyId": comp.ID})
	if code != http.StatusCreated {
		t.Fatalf("create contact want 201, got %d (%s)", code, body)
	}
	var contact Contact
	_ = json.Unmarshal(body, &contact)

	// maxpower creates an opportunity referencing both.
	code, body = do(t, app, http.MethodPost, "/v1/crm/opportunities", "maxpower",
		map[string]any{"name": "Enterprise Deal", "amount": 5000000, "stage": "proposal", "companyId": comp.ID, "pointOfContactId": contact.ID})
	if code != http.StatusCreated {
		t.Fatalf("create opp want 201, got %d (%s)", code, body)
	}
	var opp Opportunity
	_ = json.Unmarshal(body, &opp)
	if opp.Stage != "PROPOSAL" { // lower-case input normalized
		t.Fatalf("stage normalize want PROPOSAL, got %q", opp.Stage)
	}

	// maxpower lists → sees its rows.
	code, body = do(t, app, http.MethodGet, "/v1/crm/companies", "maxpower", nil)
	var listed struct {
		Data []Company `json:"data"`
	}
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 1 || listed.Data[0].Name != "MaxPower Inc" {
		t.Fatalf("maxpower list want [MaxPower Inc], got %d %+v", code, listed.Data)
	}

	// summary reflects real counts.
	code, body = do(t, app, http.MethodGet, "/v1/crm/summary", "maxpower", nil)
	if code != http.StatusOK || !bytes.Contains(body, []byte(`"companies":1`)) || !bytes.Contains(body, []byte(`"opportunities":1`)) {
		t.Fatalf("summary want 1/1/1, got %d %s", code, body)
	}

	// acme sees NOTHING and cannot read maxpower's company by id.
	code, body = do(t, app, http.MethodGet, "/v1/crm/companies", "acme", nil)
	listed.Data = nil
	_ = json.Unmarshal(body, &listed)
	if code != http.StatusOK || len(listed.Data) != 0 {
		t.Fatalf("acme must see zero companies, got %d %+v", code, listed.Data)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/crm/companies/"+comp.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme GET maxpower company want 404, got %d", code)
	}
	// acme cannot delete maxpower's company either.
	if code, _ := do(t, app, http.MethodDelete, "/v1/crm/companies/"+comp.ID, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("acme DELETE maxpower company want 404, got %d", code)
	}
}

// TestTypedWire pins the parts of the wire only the TYPED registration can move:
// the 204-with-no-body a delete answers (a typed op says that by returning a nil
// Out), and the query parameters a list filters on (a typed op binds those off
// its In rather than reading c.Query). A regression here is invisible to the
// round-trip test above and would ship in every generated SDK.
func TestTypedWire(t *testing.T) {
	app := mountApp(t)

	mk := func(path string, body any) string {
		t.Helper()
		code, b := do(t, app, http.MethodPost, path, "o", body)
		if code != http.StatusCreated {
			t.Fatalf("seed POST %s want 201, got %d (%s)", path, code, b)
		}
		var rec struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &rec); err != nil || rec.ID == "" {
			t.Fatalf("seed POST %s id: %v (%s)", path, err, b)
		}
		return rec.ID
	}

	c1 := mk("/v1/crm/companies", map[string]any{"name": "One"})
	c2 := mk("/v1/crm/companies", map[string]any{"name": "Two"})
	p1 := mk("/v1/crm/contacts", map[string]any{"email": "a@one.example", "companyId": c1})
	mk("/v1/crm/contacts", map[string]any{"email": "b@two.example", "companyId": c2})
	mk("/v1/crm/opportunities", map[string]any{"name": "Deal A", "stage": "NEW"})
	mk("/v1/crm/opportunities", map[string]any{"name": "Deal B", "stage": "PROPOSAL"})

	count := func(path string) int {
		t.Helper()
		code, b := do(t, app, http.MethodGet, path, "o", nil)
		if code != http.StatusOK {
			t.Fatalf("GET %s want 200, got %d (%s)", path, code, b)
		}
		var listed struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(b, &listed); err != nil {
			t.Fatalf("GET %s json: %v (%s)", path, err, b)
		}
		return len(listed.Data)
	}

	// ?limit= caps the page; ?companyId= and ?stage= filter it.
	if n := count("/v1/crm/companies?limit=1"); n != 1 {
		t.Fatalf("companies?limit=1 want 1 row, got %d", n)
	}
	if n := count("/v1/crm/contacts?companyId=" + c1); n != 1 {
		t.Fatalf("contacts?companyId= want 1 row, got %d", n)
	}
	if n := count("/v1/crm/opportunities?stage=proposal"); n != 1 {
		t.Fatalf("opportunities?stage=proposal want 1 row (case-folded), got %d", n)
	}
	if n := count("/v1/crm/opportunities"); n != 2 {
		t.Fatalf("unfiltered opportunities want 2 rows, got %d", n)
	}

	// A PUT round-trips 200 with the stored record; a delete answers 204 and NO body.
	code, body := do(t, app, http.MethodPut, "/v1/crm/contacts/"+p1, "o",
		map[string]any{"email": "a@one.example", "jobTitle": "CTO"})
	if code != http.StatusOK {
		t.Fatalf("PUT contact want 200, got %d (%s)", code, body)
	}
	var updated Contact
	if err := json.Unmarshal(body, &updated); err != nil || updated.JobTitle != "CTO" {
		t.Fatalf("PUT contact round-trip: %v (%s)", err, body)
	}
	if code, body = do(t, app, http.MethodDelete, "/v1/crm/contacts/"+p1, "o", nil); code != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("DELETE contact want 204 with empty body, got %d (%q)", code, body)
	}
	// The delete is real, and a second one is a 404.
	if code, _ = do(t, app, http.MethodDelete, "/v1/crm/contacts/"+p1, "o", nil); code != http.StatusNotFound {
		t.Fatalf("re-DELETE contact want 404, got %d", code)
	}
	// An unknown application stage filter is refused before the store is touched.
	if code, _ = do(t, app, http.MethodGet, "/v1/crm/applications?stage=bogus", "o", nil); code != http.StatusBadRequest {
		t.Fatalf("applications?stage=bogus want 400, got %d", code)
	}
}

// TestHTTPValidation covers the boundary rejections the FE relies on.
func TestHTTPValidation(t *testing.T) {
	app := mountApp(t)

	// company without name → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/companies", "o", map[string]any{"domainName": "x.com"}); code != http.StatusBadRequest {
		t.Fatalf("company no-name want 400, got %d", code)
	}
	// contact with no identity fields → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/contacts", "o", map[string]any{"jobTitle": "Nobody"}); code != http.StatusBadRequest {
		t.Fatalf("contact empty want 400, got %d", code)
	}
	// opportunity with bad stage → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/opportunities", "o", map[string]any{"name": "D", "stage": "BOGUS"}); code != http.StatusBadRequest {
		t.Fatalf("opp bad stage want 400, got %d", code)
	}
	// opportunity referencing a missing company → 422.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/opportunities", "o", map[string]any{"name": "D", "companyId": "comp_ghost"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("opp bad ref want 422, got %d", code)
	}
}

// TestRed_NoPrincipalForgedOrgRefused is the F4 guard for the cross-tenant break
// RED found live: an off-gateway caller forges X-Org-Id with NO validated
// principal (no X-User-Id — the state the identity middleware leaves on the
// bearer-less path) and MUST be refused 403 on every CRM data route, never served
// another tenant's PII. This asserts ORG AUTHENTICITY, not WHERE org=? column
// scoping: the forged org is well-formed and matches a REAL seeded tenant, yet the
// request is refused before any store access.
func TestRed_NoPrincipalForgedOrgRefused(t *testing.T) {
	app := mountApp(t)

	// Seed a real tenant's PII through the legitimate (validated) path.
	if code, _ := do(t, app, http.MethodPost, "/v1/crm/companies", "victim",
		map[string]any{"name": "VictimCo", "domainName": "victim.example"}); code != http.StatusCreated {
		t.Fatalf("seed create want 201, got %d", code)
	}

	// Every CRM collection, forged as "victim" with NO X-User-Id → 403.
	for _, p := range []string{"/v1/crm/companies", "/v1/crm/contacts", "/v1/crm/opportunities", "/v1/crm/summary"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("X-Org-Id", "victim") // forged; equals the seeded tenant's org
		// deliberately NO X-User-Id — the anonymous-forge signature.
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("forged GET %s: %v", p, err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("forged GET %s want 403 (no validated principal), got %d", p, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// Belt-and-suspenders: WRITE + DELETE verbs route through the SAME principal
	// gate. A no-principal forge must never create, mutate, or delete another
	// tenant's data — assert 403 before any store access on every mutating verb.
	forged := func(method, path string, body io.Reader) int {
		req := httptest.NewRequest(method, path, body)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "victim") // forged; deliberately NO X-User-Id
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("forged %s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}
	writes := []struct {
		method, path string
		body         io.Reader
	}{
		{http.MethodPost, "/v1/crm/companies", bytes.NewReader([]byte(`{"name":"Pwned Inc"}`))},
		{http.MethodPost, "/v1/crm/contacts", bytes.NewReader([]byte(`{"email":"x@evil.example"}`))},
		{http.MethodPost, "/v1/crm/opportunities", bytes.NewReader([]byte(`{"name":"Steal"}`))},
		{http.MethodDelete, "/v1/crm/companies/comp_whatever", nil},
		{http.MethodDelete, "/v1/crm/contacts/cont_whatever", nil},
	}
	for _, w := range writes {
		if code := forged(w.method, w.path, w.body); code != http.StatusForbidden {
			t.Fatalf("forged %s %s want 403 (no validated principal), got %d", w.method, w.path, code)
		}
	}
}

// TestIntakeRateLimitScope pins WHICH routes the public-intake rate limit
// covers. crm.go registers that limiter with a second app.Group("/v1/crm", …),
// so it is a prefix-scoped Use that applies to every /v1/crm route registered
// AFTER it and to none registered before — "REGISTRATION ORDER IS WIRE HERE".
//
// That is a load-bearing wire fact with no other guard. Typing the intake, or
// merely moving a zip registration across the limiter's line, would silently
// un-meter a deliberately metered public endpoint (or throttle the CRM's own
// CRUD to 20 requests a minute per IP, which no console could use). Nothing in
// the type system says so, so it is asserted here.
func TestIntakeRateLimitScope(t *testing.T) {
	// limited reports whether the route starts answering 429 within a burst that
	// exceeds intakeRateLimit. Each case gets a FRESH app: the limiter is per-app
	// state keyed by client IP, which the test transport holds constant.
	limited := func(method, path string, body any) bool {
		app := mountApp(t)
		for range intakeRateLimit + 2 {
			if code, _ := do(t, app, method, path, "hanzo", body); code == http.StatusTooManyRequests {
				return true
			}
		}
		return false
	}

	intake := map[string]any{"company": "Acme", "contactName": "A Person", "email": "a@acme.example"}

	// Metered: the public intake, and the staff application routes registered
	// after it — the limiter is prefix-scoped, so they share it.
	for _, m := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/crm/applications", intake},
		{http.MethodGet, "/v1/crm/applications", nil},
		{http.MethodGet, "/v1/crm/applications/appl_nope", nil},
	} {
		if !limited(m.method, m.path, m.body) {
			t.Fatalf("%s %s: expected the intake rate limit to cover it, got no 429 in %d requests",
				m.method, m.path, intakeRateLimit+2)
		}
	}

	// Unmetered: every CRUD op is registered BEFORE the limiter, so a console
	// listing companies is not throttled by a marketing form's budget.
	for _, p := range []string{
		"/v1/crm/companies", "/v1/crm/contacts", "/v1/crm/opportunities", "/v1/crm/summary",
	} {
		if limited(http.MethodGet, p, nil) {
			t.Fatalf("GET %s: the intake rate limit must not cover the CRM CRUD surface", p)
		}
	}
}
