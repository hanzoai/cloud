package captable

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/goja"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// typedReads pairs each typed op's HTTP path with the bundle route it dispatches.
// It is the SAME pairing routes() declares; the tests below run both sides of
// every pair, so a route added to one and not the other fails here.
var typedReads = map[string]string{
	"/v1/captable/company":       "company.get",
	"/v1/captable/stakeholders":  "stakeholders.list",
	"/v1/captable/share-classes": "shareClasses.list",
	"/v1/captable/equity-plans":  "equityPlans.list",
	"/v1/captable/shares":        "shares.list",
	"/v1/captable/options":       "options.list",
	"/v1/captable/safes":         "safes.list",
	"/v1/captable/convertibles":  "convertibles.list",
	"/v1/captable/rounds":        "rounds.list",
	"/v1/captable/investments":   "rounds.investments.list",
	"/v1/captable/summary":       "captable",
}

// bundleBytes runs a bundle route straight on the tenant's store — the RELAY the
// typed op replaced — and returns its status and body verbatim.
func bundleBytes(t *testing.T, org, route string) (int, []byte) {
	t.Helper()
	resp, err := mounted.State.host.Dispatch(context.Background(), org, goja.BaseRequest{Route: route})
	if err != nil {
		t.Fatalf("bundle dispatch %s: %v", route, err)
	}
	return resp.Status, resp.Body
}

// TestTypedReadsAreByteIdenticalToTheBundle proves the typed read plane preserved
// the wire EXACTLY. Each op decodes the bundle's JSON into a Go model and zip
// re-marshals it; this pins that round trip against the bundle's own bytes, on a
// populated tenant (every nullable column exercised both ways) AND on an empty
// one (where a nil slice would render null instead of []).
func TestTypedReadsAreByteIdenticalToTheBundle(t *testing.T) {
	app := mountApp(t)

	// An EMPTY tenant first: every list is [] or {"data":[]}, the summary has
	// zero totals, and the seeded company row is present.
	for path, route := range typedReads {
		wantStatus, want := bundleBytes(t, "empty", route)
		if wantStatus != http.StatusOK {
			t.Fatalf("%s on an empty tenant: bundle answered %d (%s)", route, wantStatus, want)
		}
		gotStatus, got := req(t, app, http.MethodGet, path, "empty", nil)
		if gotStatus != http.StatusOK {
			t.Fatalf("GET %s (empty tenant) want 200, got %d (%s)", path, gotStatus, got)
		}
		if string(got) != string(want) {
			t.Fatalf("GET %s (empty tenant) is not byte-identical to the bundle\n typed:  %s\n bundle: %s", path, got, want)
		}
	}

	// Now a POPULATED tenant, written through the UNTYPED relays (the real path),
	// covering every nullable column with and without a value.
	seedTenant(t, app, "acme")

	// Non-vacuity: the comparison below must run over REAL rows, not over the
	// empty collections that would make every pair trivially equal.
	for _, probe := range []struct{ path, want string }{
		{"/v1/captable/shares", "CS-1"},
		{"/v1/captable/safes", "SAFE-1"},
		{"/v1/captable/rounds", "Series A"},
		{"/v1/captable/summary", "ownershipPct"},
	} {
		if _, b := req(t, app, http.MethodGet, probe.path, "acme", nil); !strings.Contains(string(b), probe.want) {
			t.Fatalf("seed did not populate %s: %q missing from %s", probe.path, probe.want, b)
		}
	}

	for path, route := range typedReads {
		wantStatus, want := bundleBytes(t, "acme", route)
		if wantStatus != http.StatusOK {
			t.Fatalf("%s on a populated tenant: bundle answered %d (%s)", route, wantStatus, want)
		}
		gotStatus, got := req(t, app, http.MethodGet, path, "acme", nil)
		if gotStatus != http.StatusOK {
			t.Fatalf("GET %s want 200, got %d (%s)", path, gotStatus, got)
		}
		if string(got) != string(want) {
			t.Fatalf("GET %s is not byte-identical to the bundle\n typed:  %s\n bundle: %s", path, got, want)
		}
	}
}

// TestTypedReadsAreOrgScoped proves the typed plane gates exactly like the
// untyped one it replaced: no validated principal is 403, and one tenant never
// sees another's rows. The org comes from cloud.Bridge, never from the request
// body or an In field.
func TestTypedReadsAreOrgScoped(t *testing.T) {
	app := mountApp(t)
	seedTenant(t, app, "acme")

	// The refusal is byte-for-byte the one a still-UNTYPED captable route gives,
	// so typing did not move the gate's answer either.
	wantCode, wantBody := req(t, app, http.MethodGet, "/v1/captable/rounds/any", "", nil)
	if wantCode != http.StatusForbidden {
		t.Fatalf("untyped relay with no principal want 403, got %d (%s)", wantCode, wantBody)
	}
	for path := range typedReads {
		code, body := req(t, app, http.MethodGet, path, "", nil)
		if code != http.StatusForbidden {
			t.Fatalf("GET %s with no principal want 403, got %d (%s)", path, code, body)
		}
		if string(body) != string(wantBody) {
			t.Fatalf("GET %s refusal moved\n typed:  %s\n untyped: %s", path, body, wantBody)
		}
	}

	// globex shares the process but not the data.
	code, body := req(t, app, http.MethodGet, "/v1/captable/stakeholders", "globex", nil)
	if code != http.StatusOK {
		t.Fatalf("globex stakeholders: %d (%s)", code, body)
	}
	if strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("cross-tenant leak on the typed plane: globex saw %s", body)
	}
	code, body = req(t, app, http.MethodGet, "/v1/captable/summary", "globex", nil)
	if code != http.StatusOK {
		t.Fatalf("globex summary: %d (%s)", code, body)
	}
	var sum struct {
		Totals struct {
			OutstandingShares int64 `json:"outstandingShares"`
			Stakeholders      int64 `json:"stakeholders"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(body, &sum); err != nil {
		t.Fatalf("globex summary decode: %v (%s)", err, body)
	}
	if sum.Totals.OutstandingShares != 0 || sum.Totals.Stakeholders != 0 {
		t.Fatalf("cross-tenant leak in the computed cap table: %s", body)
	}
}

// TestEveryRouteIsTypedOrNamed closes the migration: /v1/captable has 31 routes,
// 11 typed and 20 relays that each carry a written reason at their registration
// in routes(). A new route here is typed by default, or it takes a deliberate
// edit to this count with the reason written beside it.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountApp(t)
	var total, typed int
	// GetRoutes(true) drops the middleware entries a group's Use registers under
	// the bare prefix — the SAME reading openapi.Live takes, so this counts the
	// routes the document counts and not fiber's bookkeeping.
	for _, r := range app.Fiber().GetRoutes(true) {
		if !strings.HasPrefix(r.Path, "/v1/captable") || r.Method == http.MethodHead {
			continue
		}
		total++
	}
	// The typed-op registry is the ONE value every projection reads — the same
	// one openapi.Fold lays over the router's shape.
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	for key := range reg.Ops {
		if strings.Contains(key, " /v1/captable") {
			typed++
		}
	}
	if total != 31 {
		t.Fatalf("/v1/captable has %d routes, expected 31 — type the new one or record why it cannot be", total)
	}
	if typed != 11 {
		t.Fatalf("/v1/captable has %d typed ops, expected 11", typed)
	}
}

// seedTenant writes one of every cap-table entity through the UNTYPED relay
// routes — the real HTTP path — so the typed reads below see populated rows with
// every nullable column exercised BOTH ways.
func seedTenant(t *testing.T, app *zip.App, org string) {
	t.Helper()

	mustPost := func(path string, want int, body any) []byte {
		t.Helper()
		method := http.MethodPost
		code, b := req(t, app, method, path, org, body)
		if code != want {
			t.Fatalf("seed %s want %d, got %d (%s)", path, want, code, b)
		}
		return b
	}

	// Two stakeholders: one with every optional field, one with none.
	mustPost("/v1/captable/stakeholders", http.StatusCreated, []map[string]any{
		{
			"name": "Ada Lovelace", "email": "ada@example.com",
			"stakeholderType": "INDIVIDUAL", "currentRelationship": "FOUNDER",
			"taxId": "111-11-1111", "streetAddress": "1 Analytical Way",
			"city": "London", "state": "LDN", "zipcode": "EC1",
		},
		{
			"name": "Babbage Capital", "email": "lp@babbage.vc",
			"stakeholderType": "INSTITUTION", "currentRelationship": "INVESTOR",
			"institutionName": "Babbage Capital Partners",
		},
	})
	holders := map[string]string{}
	_, body := req(t, app, http.MethodGet, "/v1/captable/stakeholders", org, nil)
	var rows []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("seed: decode stakeholders: %v (%s)", err, body)
	}
	for _, r := range rows {
		holders[r.Email] = r.ID
	}
	founder, investor := holders["ada@example.com"], holders["lp@babbage.vc"]
	if founder == "" || investor == "" {
		t.Fatalf("seed: stakeholders not readable back: %s", body)
	}

	// A share class, then an equity plan drawing on it.
	mustPost("/v1/captable/share-classes", http.StatusCreated, map[string]any{
		"name": "Common", "classType": "COMMON", "initialSharesAuthorized": 10000000,
		"boardApprovalDate": "2026-01-01", "stockholderApprovalDate": "2026-01-02",
		"votesPerShare": 1, "parValue": 0.0001, "pricePerShare": 0.25, "seniority": 0,
		"conversionRights":              "CONVERTS_TO_FUTURE_ROUND",
		"liquidationPreferenceMultiple": 1, "participationCapMultiple": 0,
	})
	_, body = req(t, app, http.MethodGet, "/v1/captable/share-classes", org, nil)
	var classes []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &classes); err != nil || len(classes) == 0 {
		t.Fatalf("seed: decode share classes: %v (%s)", err, body)
	}
	classID := classes[0].ID

	mustPost("/v1/captable/equity-plans", http.StatusCreated, map[string]any{
		"name": "2026 Stock Plan", "boardApprovalDate": "2026-01-05",
		"initialSharesReserved": 1000000, "shareClassId": classID,
		"defaultCancellatonBehavior": "RETIRE", "comments": "founding pool",
	})
	_, body = req(t, app, http.MethodGet, "/v1/captable/equity-plans", org, nil)
	var plans struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &plans); err != nil || len(plans.Data) == 0 {
		t.Fatalf("seed: decode equity plans: %v (%s)", err, body)
	}
	planID := plans.Data[0].ID

	// Two certificates: one priced (pricePerShare + capitalContribution set), one
	// bare, so the nullable REAL columns are exercised both ways.
	mustPost("/v1/captable/shares", http.StatusCreated, map[string]any{
		"stakeholderId": founder, "shareClassId": classID, "certificateId": "CS-1",
		"quantity": 1000000, "status": "ACTIVE", "issueDate": "2026-01-03",
		"boardApprovalDate": "2026-01-03", "cliffYears": 1, "vestingYears": 4,
		"pricePerShare": 0.0001, "capitalContribution": 100,
	})
	mustPost("/v1/captable/shares", http.StatusCreated, map[string]any{
		"stakeholderId": investor, "shareClassId": classID, "certificateId": "CS-2",
		"quantity": 50000, "status": "DRAFT", "issueDate": "2026-02-03",
		"boardApprovalDate": "2026-02-03", "cliffYears": 0, "vestingYears": 0,
	})

	mustPost("/v1/captable/options", http.StatusCreated, map[string]any{
		"grantId": "GR-1", "stakeholderId": founder, "equityPlanId": planID,
		"quantity": 25000, "exercisePrice": 0.25, "type": "ISO", "status": "ACTIVE",
		"cliffYears": 1, "vestingYears": 4, "issueDate": "2026-03-01",
		"expirationDate": "2036-03-01", "vestingStartDate": "2026-03-01",
		"boardApprovalDate": "2026-03-01", "rule144Date": "2026-03-01",
		"notes": "new hire grant",
	})

	// A SAFE with its optional terms and one without.
	mustPost("/v1/captable/safes", http.StatusCreated, map[string]any{
		"publicId": "SAFE-1", "stakeholderId": investor, "capital": 250000,
		"type": "POST_MONEY", "status": "ACTIVE", "issueDate": "2026-04-01",
		"boardApprovalDate": "2026-04-01", "valuationCap": 10000000,
		"discountRate": 0.2, "mfn": true, "proRata": true,
	})
	mustPost("/v1/captable/safes", http.StatusCreated, map[string]any{
		"publicId": "SAFE-2", "stakeholderId": investor, "capital": 50000,
		"type": "PRE_MONEY", "status": "DRAFT", "issueDate": "2026-04-02",
		"boardApprovalDate": "2026-04-02",
	})

	mustPost("/v1/captable/convertibles", http.StatusCreated, map[string]any{
		"publicId": "NOTE-1", "stakeholderId": investor, "capital": 100000,
		"type": "NOTE", "status": "ACTIVE", "issueDate": "2026-05-01",
		"boardApprovalDate": "2026-05-01", "conversionCap": 8000000,
		"discountRate": 0.15, "interestRate": 0.05, "mfn": true,
	})
	mustPost("/v1/captable/convertibles", http.StatusCreated, map[string]any{
		"publicId": "NOTE-2", "stakeholderId": investor, "capital": 25000,
		"type": "NOTE", "status": "DRAFT", "issueDate": "2026-05-02",
		"boardApprovalDate": "2026-05-02",
	})

	// A PRICED round (share class + price set) and a SAFE round (both null), so
	// the round list carries a nullable column filled and empty.
	priced := mustPost("/v1/captable/rounds", http.StatusCreated, map[string]any{
		"name": "Series A", "roundType": "PRICED", "targetAmount": 5000000,
		"preMoneyValuation": 20000000, "pricePerShare": 0.5, "shareClassId": classID,
	})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(priced, &created); err != nil || created.ID == "" {
		t.Fatalf("seed: decode round: %v (%s)", err, priced)
	}
	mustPost("/v1/captable/rounds", http.StatusCreated, map[string]any{
		"name": "Pre-seed", "roundType": "SAFE", "targetAmount": 500000,
	})

	// An investment into the priced round issues shares too.
	mustPost("/v1/captable/rounds/"+created.ID+"/investments", http.StatusCreated, map[string]any{
		"stakeholderId": investor, "amount": 1000000, "date": "2026-06-01",
		"comments": "lead cheque",
	})
	// Close the priced round so closeDate is non-null on one row and null on the other.
	mustPost("/v1/captable/rounds/"+created.ID+"/close", http.StatusOK, map[string]any{
		"closeDate": "2026-06-15",
	})
}
