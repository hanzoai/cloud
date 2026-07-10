package captable

import (
	"context"
	"encoding/json"
	"testing"

	hcaptable "github.com/hanzoai/captable"
	"github.com/hanzoai/cloud/clients/gojabase"
)

// TestFullLifecycle drives the REAL embedded captable bundle against a REAL
// per-tenant SQLite through gojabase — the end-to-end proof that the bundle's SQL
// matches the Go host schema and that the whole cap-table fold round-trips
// through Base: company → stakeholders → share class → equity plan → share
// issuance → options → SAFE → priced round + investment (dilution) → transfer →
// computed cap table. Any column/route drift fails here, not in production.
func newHost(t *testing.T) *gojabase.Host {
	t.Helper()
	bundle, err := hcaptable.Bundle()
	if err != nil {
		t.Fatal(err)
	}
	h, err := gojabase.New(gojabase.Config{
		Name:    "captable",
		Bundle:  bundle,
		Schema:  schema,
		DataDir: t.TempDir(),
		OnOpen:  seedCompany,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// do dispatches a route and returns (status, decoded body).
func do(t *testing.T, h *gojabase.Host, org, route string, params map[string]string, body any) (int, any) {
	t.Helper()
	resp, err := h.Dispatch(context.Background(), org, gojabase.Request{Route: route, Params: params, Body: body})
	if err != nil {
		t.Fatalf("dispatch %s: %v", route, err)
	}
	var decoded any
	if len(resp.Body) > 0 {
		if err := json.Unmarshal(resp.Body, &decoded); err != nil {
			t.Fatalf("decode %s body %q: %v", route, resp.Body, err)
		}
	}
	return resp.Status, decoded
}

func mustStatus(t *testing.T, want, got int, route string, body any) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status=%d want %d (body=%+v)", route, got, want, body)
	}
}

// firstID pulls the "id" of the first element of a list body that is either an
// array or a {data:[...]} wrapper.
func firstID(t *testing.T, body any) string {
	t.Helper()
	var arr []any
	switch v := body.(type) {
	case []any:
		arr = v
	case map[string]any:
		if d, ok := v["data"].([]any); ok {
			arr = d
		}
	}
	if len(arr) == 0 {
		t.Fatalf("no rows in %+v", body)
	}
	row, _ := arr[0].(map[string]any)
	id, _ := row["id"].(string)
	if id == "" {
		t.Fatalf("first row has no id: %+v", arr[0])
	}
	return id
}

func TestFullLifecycle(t *testing.T) {
	h := newHost(t)
	const org = "acme"

	// company seeded on open.
	st, body := do(t, h, org, "company.get", nil, nil)
	mustStatus(t, 200, st, "company.get", body)

	// add two stakeholders (array body).
	st, body = do(t, h, org, "stakeholders.add", nil, []map[string]any{
		{"name": "Ada Lovelace", "email": "ada@example.com", "stakeholderType": "INDIVIDUAL", "currentRelationship": "FOUNDER"},
		{"name": "Byron Fund", "email": "byron@example.com", "stakeholderType": "INSTITUTION", "currentRelationship": "INVESTOR"},
	})
	mustStatus(t, 201, st, "stakeholders.add", body)

	st, listBody := do(t, h, org, "stakeholders.list", nil, nil)
	mustStatus(t, 200, st, "stakeholders.list", listBody)
	arr, _ := listBody.([]any)
	if len(arr) != 2 {
		t.Fatalf("want 2 stakeholders, got %d", len(arr))
	}
	// find the founder + investor ids
	var founderID, investorID string
	for _, r := range arr {
		m := r.(map[string]any)
		if m["email"] == "ada@example.com" {
			founderID = m["id"].(string)
		} else {
			investorID = m["id"].(string)
		}
	}

	// create a share class.
	st, body = do(t, h, org, "shareClasses.create", nil, map[string]any{
		"name": "Common", "classType": "COMMON", "initialSharesAuthorized": 10000000,
		"boardApprovalDate": "2026-01-01", "stockholderApprovalDate": "2026-01-02",
		"votesPerShare": 1, "parValue": 0.0001, "pricePerShare": 0.0001, "seniority": 0,
		"conversionRights": "CONVERTS_TO_FUTURE_ROUND", "convertsToShareClassId": nil,
		"liquidationPreferenceMultiple": 1, "participationCapMultiple": 0,
	})
	mustStatus(t, 201, st, "shareClasses.create", body)
	_, scList := do(t, h, org, "shareClasses.list", nil, nil)
	shareClassID := firstID(t, scList)

	// issue 8,000,000 founder shares.
	st, body = do(t, h, org, "shares.add", nil, map[string]any{
		"stakeholderId": founderID, "shareClassId": shareClassID, "certificateId": "CS-1",
		"quantity": 8000000, "status": "ACTIVE", "issueDate": "2026-01-03",
		"boardApprovalDate": "2026-01-03", "companyLegends": []string{"US_SECURITIES_ACT"},
		"cliffYears": 0, "vestingYears": 4,
	})
	mustStatus(t, 201, st, "shares.add", body)

	// duplicate certificate → 409.
	st, dupBody := do(t, h, org, "shares.add", nil, map[string]any{
		"stakeholderId": founderID, "shareClassId": shareClassID, "certificateId": "CS-1",
		"quantity": 1, "status": "ACTIVE", "issueDate": "2026-01-03", "boardApprovalDate": "2026-01-03",
		"cliffYears": 0, "vestingYears": 0,
	})
	mustStatus(t, 409, st, "shares.add dup", dupBody)

	// cap table: founder owns 100% of 8,000,000.
	st, ct := do(t, h, org, "captable", nil, nil)
	mustStatus(t, 200, st, "captable", ct)
	ctm := ct.(map[string]any)
	totals := ctm["totals"].(map[string]any)
	if totals["outstandingShares"].(float64) != 8000000 {
		t.Fatalf("outstanding=%v want 8000000", totals["outstandingShares"])
	}

	// equity plan + option grant to founder.
	st, body = do(t, h, org, "equityPlans.create", nil, map[string]any{
		"name": "2026 Plan", "boardApprovalDate": "2026-01-05", "initialSharesReserved": 1000000,
		"shareClassId": shareClassID, "defaultCancellatonBehavior": "RETURN_TO_POOL",
	})
	mustStatus(t, 201, st, "equityPlans.create", body)
	_, epList := do(t, h, org, "equityPlans.list", nil, nil)
	equityPlanID := firstID(t, epList)

	st, body = do(t, h, org, "options.add", nil, map[string]any{
		"grantId": "GR-1", "stakeholderId": founderID, "equityPlanId": equityPlanID,
		"quantity": 500000, "exercisePrice": 0.10, "type": "ISO", "status": "ACTIVE",
		"cliffYears": 1, "vestingYears": 4, "issueDate": "2026-01-06", "expirationDate": "2036-01-06",
		"vestingStartDate": "2026-01-06", "boardApprovalDate": "2026-01-06", "rule144Date": "2026-07-06",
	})
	mustStatus(t, 201, st, "options.add", body)

	// SAFE from the investor.
	st, body = do(t, h, org, "safes.create", nil, map[string]any{
		"publicId": "SAFE-1", "stakeholderId": investorID, "capital": 250000,
		"type": "POST_MONEY", "status": "ACTIVE", "valuationCap": 10000000,
		"issueDate": "2026-01-07", "boardApprovalDate": "2026-01-07",
	})
	mustStatus(t, 201, st, "safes.create", body)

	// priced round: investor buys shares at $1.00 → 100,000 shares.
	st, roundBody := do(t, h, org, "rounds.create", nil, map[string]any{
		"name": "Seed", "roundType": "PRICED", "targetAmount": 1000000,
		"shareClassId": shareClassID, "pricePerShare": 1.0, "preMoneyValuation": 8000000,
	})
	mustStatus(t, 201, st, "rounds.create", roundBody)
	roundID := roundBody.(map[string]any)["id"].(string)

	st, invBody := do(t, h, org, "rounds.investments.add", map[string]string{"id": roundID}, map[string]any{
		"stakeholderId": investorID, "amount": 100000, "date": "2026-01-08",
	})
	mustStatus(t, 201, st, "rounds.investments.add", invBody)
	if invBody.(map[string]any)["sharesIssued"].(float64) != 100000 {
		t.Fatalf("sharesIssued=%v want 100000", invBody.(map[string]any)["sharesIssued"])
	}

	// cap table now: 8,100,000 outstanding, 8,600,000 fully diluted (+500k options).
	_, ct2 := do(t, h, org, "captable", nil, nil)
	t2 := ct2.(map[string]any)["totals"].(map[string]any)
	if t2["outstandingShares"].(float64) != 8100000 {
		t.Fatalf("outstanding=%v want 8100000", t2["outstandingShares"])
	}
	if t2["fullyDilutedShares"].(float64) != 8600000 {
		t.Fatalf("fullyDiluted=%v want 8600000", t2["fullyDilutedShares"])
	}

	// transfer 1,000,000 founder shares to the investor (partial → new cert).
	st, trBody := do(t, h, org, "shares.transfer", nil, map[string]any{
		"shareId": firstShareID(t, h, org), "toStakeholderId": investorID,
		"quantity": 1000000, "certificateId": "CS-2",
	})
	mustStatus(t, 200, st, "shares.transfer", trBody)

	// investor now holds 1,000,000 (transfer) + 100,000 (round) = 1,100,000 shares.
	_, ct3 := do(t, h, org, "captable", nil, nil)
	byStake := ct3.(map[string]any)["byStakeholder"].([]any)
	var investorShares float64
	for _, r := range byStake {
		m := r.(map[string]any)
		if m["stakeholderId"] == investorID {
			investorShares = m["shares"].(float64)
		}
	}
	if investorShares != 1100000 {
		t.Fatalf("investor shares=%v want 1100000", investorShares)
	}
	t.Logf("cap table after round + transfer: %s", mustJSON(t, ct3))
}

// firstShareID returns the founder's original certificate share id.
func firstShareID(t *testing.T, h *gojabase.Host, org string) string {
	t.Helper()
	_, body := do(t, h, org, "shares.list", nil, nil)
	data := body.(map[string]any)["data"].([]any)
	for _, r := range data {
		m := r.(map[string]any)
		if m["certificateId"] == "CS-1" {
			return m["id"].(string)
		}
	}
	t.Fatal("CS-1 share not found")
	return ""
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
