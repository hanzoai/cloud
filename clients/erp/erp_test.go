package erp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestFixturesValid proves every ERP DocType is a well-formed schema, that its Link
// and Table targets resolve WITHIN the lane (so installing yields a self-consistent
// model with no dangling reference that would 422 at write), and that names are
// slug-clean (reachable through the console's `/cloud` path filter).
func TestFixturesValid(t *testing.T) {
	dts := DocTypes()
	names := map[string]framework.DocType{}
	for _, dt := range dts {
		if err := dt.Validate(); err != nil {
			t.Fatalf("fixture %q invalid: %v", dt.Name, err)
		}
		if !slugClean(dt.Name) {
			t.Fatalf("DocType name %q is not slug-clean (unreachable via the generic renderer)", dt.Name)
		}
		names[dt.Name] = dt
	}
	for _, dt := range dts {
		for _, f := range dt.Fields {
			switch f.Fieldtype {
			case framework.FieldLink, framework.FieldTable:
				if _, ok := names[f.Options]; !ok {
					t.Fatalf("%s.%s references %q which is not an ERP DocType", dt.Name, f.Fieldname, f.Options)
				}
			}
		}
	}
}

// TestModelSpec locks the contract the console + hooks rely on: the submittable
// transactions, their series naming, their child Table, and the read-only ledgers.
func TestModelSpec(t *testing.T) {
	byName := map[string]framework.DocType{}
	for _, dt := range DocTypes() {
		if dt.Module != Module {
			t.Fatalf("%s: module want %q, got %q", dt.Name, Module, dt.Module)
		}
		byName[dt.Name] = dt
	}
	// The six submittable transactions, each series-named with a child Table (except
	// payment-entry which has no lines).
	for _, name := range []string{dtSalesOrder, dtSalesInvoice, dtPurchaseOrder, dtStockEntry, dtJournalEntry, dtPaymentEntry} {
		dt := byName[name]
		if !dt.IsSubmittable {
			t.Fatalf("%s must be submittable", name)
		}
		if !isSeries(dt.Autoname) {
			t.Fatalf("%s must use a series autoname, got %q", name, dt.Autoname)
		}
	}
	if f, ok := fieldOf(byName[dtSalesOrder], "items"); !ok || f.Fieldtype != framework.FieldTable || f.Options != dtSalesOrderItem {
		t.Fatalf("sales-order.items must be a Table of %s, got %+v", dtSalesOrderItem, f)
	}
	// Ledgers are read-only: a single System-Manager read grant, no create/write.
	for _, name := range []string{dtGLEntry, dtStockLedger} {
		dt := byName[name]
		if dt.IsSubmittable {
			t.Fatalf("%s must not be submittable", name)
		}
		if len(dt.Perms) != 1 || dt.Perms[0].Role != framework.RoleSystemManager || dt.Perms[0].Write || dt.Perms[0].Create || dt.Perms[0].Delete {
			t.Fatalf("%s must be System-Manager read-only, got %+v", name, dt.Perms)
		}
	}
}

// TestInstallTransactRoundTrip drives the WHOLE lane through the real engine over
// HTTP: install ERP, set up masters, then exercise every hook — totals on save,
// the on_submit gates, the stock ledger on stock-entry submit, and the balanced GL
// on invoice submit.
func TestInstallTransactRoundTrip(t *testing.T) {
	app := mount(t)
	const org = "acme"

	// Install the ERP lane (the caller becomes System Manager, trust-on-first-use).
	if code, body := call(t, app, http.MethodPost, "/v1/framework/modules/erp/install", org, nil); code != http.StatusOK {
		t.Fatalf("install erp want 200, got %d (%s)", code, body)
	}

	// Masters.
	mustCreate(t, app, org, dtItem, map[string]any{"item_code": "widget-1", "item_name": "Widget", "standard_rate": 10})
	mustCreate(t, app, org, dtWarehouse, map[string]any{"warehouse_name": "main"})
	mustCreate(t, app, org, dtCustomer, map[string]any{"customer_name": "acme-co"})

	// --- Sales Order: before_save computes line amount + grand_total ---
	so := mustCreate(t, app, org, dtSalesOrder, map[string]any{
		"customer": "acme-co", "order_date": "2026-07-03",
		"items": []map[string]any{{"item": "widget-1", "qty": 2, "rate": 10}},
	})
	if gt := num(so["grand_total"]); gt != 20 {
		t.Fatalf("sales-order grand_total want 20 (2*10), got %v", so["grand_total"])
	}
	if line := firstRow(so["items"]); num(line["amount"]) != 20 {
		t.Fatalf("sales-order line amount want 20, got %v", line["amount"])
	}
	// Submit: on_submit gate passes (non-empty, positive) → docstatus 0→1.
	soName, _ := so["name"].(string)
	if doc := mustSubmit(t, app, org, dtSalesOrder, soName); num(doc["docstatus"]) != 1 {
		t.Fatalf("submitted sales-order docstatus want 1, got %v", doc["docstatus"])
	}

	// --- Stock Entry: on_submit appends the stock ledger (the stock hook) ---
	ste := mustCreate(t, app, org, dtStockEntry, map[string]any{
		"stock_entry_type": "Receipt", "posting_date": "2026-07-03",
		"items": []map[string]any{{"item": "widget-1", "qty": 5, "target_warehouse": "main"}},
	})
	steName, _ := ste["name"].(string)
	mustSubmit(t, app, org, dtStockEntry, steName)
	sle := listDocs(t, app, org, `/v1/framework/`+dtStockLedger+`?filters={"voucher_no":"`+steName+`"}`)
	if len(sle) != 1 {
		t.Fatalf("stock-entry submit want 1 ledger entry, got %d", len(sle))
	}
	if num(sle[0]["qty"]) != 5 || str(sle[0]["warehouse"]) != "main" || str(sle[0]["item"]) != "widget-1" {
		t.Fatalf("stock ledger entry wrong: %+v", sle[0])
	}

	// --- Sales Invoice: on_submit posts a balanced GL pair (the GL hook) ---
	inv := mustCreate(t, app, org, dtSalesInvoice, map[string]any{
		"customer": "acme-co", "posting_date": "2026-07-03",
		"items": []map[string]any{{"item": "widget-1", "qty": 1, "rate": 100}},
	})
	if num(inv["grand_total"]) != 100 {
		t.Fatalf("invoice grand_total want 100, got %v", inv["grand_total"])
	}
	invName, _ := inv["name"].(string)
	mustSubmit(t, app, org, dtSalesInvoice, invName)
	gl := listDocs(t, app, org, `/v1/framework/`+dtGLEntry+`?filters={"voucher_no":"`+invName+`"}`)
	if len(gl) != 2 {
		t.Fatalf("invoice submit want 2 GL entries, got %d", len(gl))
	}
	var debit, credit float64
	for _, e := range gl {
		debit += num(e["debit"])
		credit += num(e["credit"])
	}
	if debit != 100 || credit != 100 {
		t.Fatalf("GL must balance at 100/100, got debit=%v credit=%v", debit, credit)
	}
}

// TestSubmitGatesAndIntegrity proves the on_submit gates and Link integrity: an
// empty order can't submit, an unbalanced journal can't submit, and a line that
// references a non-existent item is refused at create (422).
func TestSubmitGatesAndIntegrity(t *testing.T) {
	app := mount(t)
	const org = "gates"
	call(t, app, http.MethodPost, "/v1/framework/modules/erp/install", org, nil)
	mustCreate(t, app, org, dtCustomer, map[string]any{"customer_name": "c1"})
	mustCreate(t, app, org, dtAccount, map[string]any{"account_name": "cash", "account_type": "Asset"})
	mustCreate(t, app, org, dtAccount, map[string]any{"account_name": "sales-income", "account_type": "Income"})

	// Empty order → create ok, submit refused (422 gate).
	empty := mustCreate(t, app, org, dtSalesOrder, map[string]any{"customer": "c1"})
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/"+dtSalesOrder+"/"+str(empty["name"])+"/submit", org, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty-order submit want 422, got %d", code)
	}

	// Balanced journal → submits and posts 2 GL entries.
	je := mustCreate(t, app, org, dtJournalEntry, map[string]any{
		"posting_date": "2026-07-03",
		"accounts": []map[string]any{
			{"account": "cash", "debit": 50, "credit": 0},
			{"account": "sales-income", "debit": 0, "credit": 50},
		},
	})
	if num(je["total_debit"]) != 50 || num(je["total_credit"]) != 50 {
		t.Fatalf("journal totals want 50/50, got %v/%v", je["total_debit"], je["total_credit"])
	}
	mustSubmit(t, app, org, dtJournalEntry, str(je["name"]))
	if n := len(listDocs(t, app, org, `/v1/framework/`+dtGLEntry+`?filters={"voucher_no":"`+str(je["name"])+`"}`)); n != 2 {
		t.Fatalf("balanced journal want 2 GL entries, got %d", n)
	}

	// Unbalanced journal → create ok, submit refused (double-entry gate).
	bad := mustCreate(t, app, org, dtJournalEntry, map[string]any{
		"posting_date": "2026-07-03",
		"accounts": []map[string]any{
			{"account": "cash", "debit": 50, "credit": 0},
			{"account": "sales-income", "debit": 0, "credit": 30},
		},
	})
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/"+dtJournalEntry+"/"+str(bad["name"])+"/submit", org, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("unbalanced-journal submit want 422, got %d", code)
	}

	// A sales-order line referencing a non-existent item is refused (422 errBadRef).
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/"+dtSalesOrder, org, map[string]any{
		"customer": "c1", "items": []map[string]any{{"item": "ghost-item", "qty": 1, "rate": 5}},
	}); code != http.StatusUnprocessableEntity {
		t.Fatalf("dangling item link want 422, got %d", code)
	}
}

// TestTenantIsolation proves the lane's DocTypes AND its hook-posted ledgers are
// invisible across orgs: installing + transacting in one org creates nothing in
// another, and a submit posts ledgers ONLY in the acting org.
func TestTenantIsolation(t *testing.T) {
	app := mount(t)
	// orgA installs + posts a stock ledger via a stock-entry submit.
	call(t, app, http.MethodPost, "/v1/framework/modules/erp/install", "orgA", nil)
	mustCreate(t, app, "orgA", dtItem, map[string]any{"item_code": "w1", "item_name": "W1"})
	mustCreate(t, app, "orgA", dtWarehouse, map[string]any{"warehouse_name": "wh"})
	ste := mustCreate(t, app, "orgA", dtStockEntry, map[string]any{
		"stock_entry_type": "Receipt", "items": []map[string]any{{"item": "w1", "qty": 3, "target_warehouse": "wh"}},
	})
	mustSubmit(t, app, "orgA", dtStockEntry, str(ste["name"]))

	// orgB never installed ERP → zero doctypes, and the ledger DocType is unknown (404).
	if n := len(listDocs(t, app, "orgB", "/v1/framework/doctypes")); n != 0 {
		t.Fatalf("orgB must have zero doctypes, got %d", n)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/framework/"+dtStockLedger, "orgB", nil); code != http.StatusNotFound {
		t.Fatalf("orgB stock-ledger read want 404 (not installed), got %d", code)
	}
}

// TestLedgerReadOnlyToRoles proves the hook-posted ledgers are protected from a
// granted Erp User: the user may read masters but can neither read nor create GL
// entries (ledgerPerms grants no role but System Manager). The org owner (manager)
// retains full control within its OWN tenant — the framework's model, not ours to
// override; tenant isolation (above) is the security boundary.
func TestLedgerReadOnlyToRoles(t *testing.T) {
	app := mount(t)
	const org = "roles"
	// u_roles installs → becomes System Manager (owner seed).
	if code, _ := reqAs(t, app, http.MethodPost, "/v1/framework/modules/erp/install", org, "u_owner", nil); code != http.StatusOK {
		t.Fatal("owner install failed")
	}
	// Grant a second user the Erp User role.
	if code, _ := reqAs(t, app, http.MethodPost, "/v1/framework/roles", org, "u_owner", map[string]any{"user": "u_agent", "role": RoleErpUser}); code != http.StatusCreated {
		t.Fatal("grant Erp User failed")
	}
	// The Erp User may read a master (has the grant)…
	if code, _ := reqAs(t, app, http.MethodGet, "/v1/framework/"+dtItem, org, "u_agent", nil); code != http.StatusOK {
		t.Fatalf("Erp User item read want 200, got %d", code)
	}
	// …but may NOT read the GL ledger (no grant on it)…
	if code, _ := reqAs(t, app, http.MethodGet, "/v1/framework/"+dtGLEntry, org, "u_agent", nil); code != http.StatusForbidden {
		t.Fatalf("Erp User GL read want 403, got %d", code)
	}
	// …nor create one (the ledger is written only by the posting hooks, via the store).
	if code, _ := reqAs(t, app, http.MethodPost, "/v1/framework/"+dtGLEntry, org, "u_agent",
		map[string]any{"account": "x", "debit": 1}); code != http.StatusForbidden {
		t.Fatalf("Erp User GL create want 403, got %d", code)
	}
}

// ---- helpers ----

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

// mustCreate POSTs a document and returns the created body; fails on non-201.
func mustCreate(t *testing.T, app *zip.App, org, doctype string, body map[string]any) map[string]any {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/framework/"+doctype, org, body)
	if code != http.StatusCreated {
		t.Fatalf("create %s want 201, got %d (%s)", doctype, code, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

// mustSubmit POSTs /submit and returns the body; fails on non-200.
func mustSubmit(t *testing.T, app *zip.App, org, doctype, name string) map[string]any {
	t.Helper()
	code, raw := call(t, app, http.MethodPost, "/v1/framework/"+doctype+"/"+name+"/submit", org, nil)
	if code != http.StatusOK {
		t.Fatalf("submit %s/%s want 200, got %d (%s)", doctype, name, code, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func listDocs(t *testing.T, app *zip.App, org, path string) []map[string]any {
	t.Helper()
	code, raw := call(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("list %s want 200, got %d (%s)", path, code, raw)
	}
	var res struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &res)
	return res.Data
}

// call issues a request as the org's default validated principal (u_<org>).
func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	return reqAs(t, app, method, path, org, "u_"+org, body)
}

// reqAs issues a request as a specific validated user (X-User-Id) in an org.
func reqAs(t *testing.T, app *zip.App, method, path, org, user string, body any) (int, []byte) {
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
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user) // the validated-principal signal
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func fieldOf(dt framework.DocType, name string) (framework.DocField, bool) {
	for _, f := range dt.Fields {
		if f.Fieldname == name {
			return f, true
		}
	}
	return framework.DocField{}, false
}

// num reads a JSON number (float64) from an any; 0 otherwise.
func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// firstRow returns the first child-table row from a wire value ([]any of maps).
func firstRow(v any) map[string]any {
	arr, _ := v.([]any)
	if len(arr) == 0 {
		return map[string]any{}
	}
	m, _ := arr[0].(map[string]any)
	return m
}

// isSeries reports a series autoname (not hash/field/prompt).
func isSeries(autoname string) bool {
	return autoname != "" && autoname != "hash" && autoname != "prompt" &&
		len(autoname) > 6 && autoname[:6] != "field:"
}

// slugClean mirrors the console `/cloud` path filter: no space or `%`, dash/underscore ok.
func slugClean(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}
