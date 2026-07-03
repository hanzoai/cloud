package framework

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

func mountApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// call issues a request with an explicit principal (org+user) and optional admin.
func call(t *testing.T, app *zip.App, method, path, org, user string, admin bool, body any) (int, []byte) {
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
	}
	if user != "" {
		req.Header.Set("X-User-Id", user) // the validated-principal signal
	}
	if admin {
		req.Header.Set("X-User-IsAdmin", "true")
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// do is the common case: user "u_<org>", not admin.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	return call(t, app, method, path, org, "u_"+org, false, body)
}

func taskDocType() map[string]any {
	return map[string]any{
		"name": "Task", "module": "Projects", "isSubmittable": true, "autoname": "TASK-.#####",
		"fields": []map[string]any{
			{"fieldname": "subject", "fieldtype": "Data", "reqd": true},
			{"fieldname": "priority", "fieldtype": "Select", "options": "Low\nHigh"},
			{"fieldname": "estimate", "fieldtype": "Int"},
		},
	}
}

// TestDocTypeAndDocumentRoundTrip is the end-to-end wire proof: define a DocType,
// CRUD documents against the generic surface, and the metadata-driven naming +
// validation all hold.
func TestDocTypeAndDocumentRoundTrip(t *testing.T) {
	app := mountApp(t)

	// Define the DocType (bootstrap member is a manager on a fresh org).
	code, body := do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", taskDocType())
	if code != http.StatusCreated {
		t.Fatalf("define doctype want 201, got %d (%s)", code, body)
	}
	// The static /doctypes route must win over the generic /:doctype route.
	code, body = do(t, app, http.MethodGet, "/v1/framework/doctypes", "acme", nil)
	var dtList struct {
		Data []DocType `json:"data"`
	}
	_ = json.Unmarshal(body, &dtList)
	if code != http.StatusOK || len(dtList.Data) != 1 || dtList.Data[0].Name != "Task" {
		t.Fatalf("list doctypes want [Task], got %d %+v", code, dtList.Data)
	}

	// Create a document via the generic surface; naming series applies.
	code, body = do(t, app, http.MethodPost, "/v1/framework/Task", "acme",
		map[string]any{"subject": "Ship framework", "priority": "High", "estimate": 5})
	if code != http.StatusCreated {
		t.Fatalf("create doc want 201, got %d (%s)", code, body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	name, _ := doc["name"].(string)
	if name == "" || doc["subject"] != "Ship framework" || doc["docstatus"] != float64(0) {
		t.Fatalf("create doc round-trip mismatch: %+v", doc)
	}
	if doc["estimate"] != float64(5) { // Int coerced
		t.Fatalf("estimate want 5, got %v", doc["estimate"])
	}

	// Get it back.
	code, body = do(t, app, http.MethodGet, "/v1/framework/Task/"+name, "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("get doc want 200, got %d (%s)", code, body)
	}

	// Update it.
	code, body = do(t, app, http.MethodPut, "/v1/framework/Task/"+name, "acme",
		map[string]any{"subject": "Ship it", "priority": "Low", "estimate": 8})
	_ = json.Unmarshal(body, &doc)
	if code != http.StatusOK || doc["subject"] != "Ship it" || doc["estimate"] != float64(8) {
		t.Fatalf("update doc want 200/updated, got %d %+v", code, doc)
	}

	// List.
	code, body = do(t, app, http.MethodGet, "/v1/framework/Task", "acme", nil)
	var list struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Data) != 1 {
		t.Fatalf("list docs want 1, got %d %+v", code, list.Data)
	}

	// Delete.
	if code, _ := do(t, app, http.MethodDelete, "/v1/framework/Task/"+name, "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete doc want 204, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/Task/"+name, "acme", nil); code != http.StatusNotFound {
		t.Fatalf("get deleted want 404, got %d", code)
	}
}

// TestWireValidation covers the field-type + reference rejections over HTTP.
func TestWireValidation(t *testing.T) {
	app := mountApp(t)
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", taskDocType())

	// Missing reqd subject → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"priority": "High"}); code != http.StatusBadRequest {
		t.Fatalf("missing reqd want 400, got %d", code)
	}
	// Bad Select option → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"subject": "x", "priority": "Urgent"}); code != http.StatusBadRequest {
		t.Fatalf("bad select want 400, got %d", code)
	}
	// Int as text → 400.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"subject": "x", "estimate": "five"}); code != http.StatusBadRequest {
		t.Fatalf("bad int want 400, got %d", code)
	}
	// Unknown doctype → 404.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/Ghost", "acme", map[string]any{"a": "b"}); code != http.StatusNotFound {
		t.Fatalf("unknown doctype want 404, got %d", code)
	}
}

// TestSubmitCancelLifecycle covers docstatus transitions over HTTP.
func TestSubmitCancelLifecycle(t *testing.T) {
	app := mountApp(t)
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", taskDocType())
	_, body := do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"subject": "x"})
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	name := doc["name"].(string)

	// submit 0→1.
	code, body := do(t, app, http.MethodPost, "/v1/framework/Task/"+name+"/submit", "acme", nil)
	_ = json.Unmarshal(body, &doc)
	if code != http.StatusOK || doc["docstatus"] != float64(1) {
		t.Fatalf("submit want docstatus 1, got %d %v", code, doc["docstatus"])
	}
	// editing a submitted doc → 409.
	if code, _ := do(t, app, http.MethodPut, "/v1/framework/Task/"+name, "acme", map[string]any{"subject": "y"}); code != http.StatusConflict {
		t.Fatalf("edit submitted want 409, got %d", code)
	}
	// deleting a submitted doc → 409.
	if code, _ := do(t, app, http.MethodDelete, "/v1/framework/Task/"+name, "acme", nil); code != http.StatusConflict {
		t.Fatalf("delete submitted want 409, got %d", code)
	}
	// cancel 1→2.
	code, body = do(t, app, http.MethodPost, "/v1/framework/Task/"+name+"/cancel", "acme", nil)
	_ = json.Unmarshal(body, &doc)
	if code != http.StatusOK || doc["docstatus"] != float64(2) {
		t.Fatalf("cancel want docstatus 2, got %d %v", code, doc["docstatus"])
	}
	// re-submit a cancelled doc → 409.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/Task/"+name+"/submit", "acme", nil); code != http.StatusConflict {
		t.Fatalf("resubmit cancelled want 409, got %d", code)
	}
}

// TestForgedPrincipalRefused is the F4 guard: a caller who forges X-Org-Id with
// NO validated principal (no X-User-Id) is refused 403 on EVERY route, never
// served or allowed to mutate another tenant's data. This asserts principal
// AUTHENTICITY, not just WHERE org=? scoping.
func TestForgedPrincipalRefused(t *testing.T) {
	app := mountApp(t)
	// Seed a real tenant through the validated path.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "victim", taskDocType())

	forged := func(method, path string, body any) int {
		var r io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, path, r)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Org-Id", "victim") // forged; equals a real tenant
		// deliberately NO X-User-Id — the anonymous-forge signature.
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("forged %s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/framework/doctypes", nil},
		{http.MethodPost, "/v1/framework/doctypes", taskDocType()},
		{http.MethodGet, "/v1/framework/Task", nil},
		{http.MethodPost, "/v1/framework/Task", map[string]any{"subject": "pwn"}},
		{http.MethodGet, "/v1/framework/Task/TASK-00001", nil},
		{http.MethodDelete, "/v1/framework/Task/TASK-00001", nil},
		{http.MethodGet, "/v1/framework/roles", nil},
		{http.MethodPost, "/v1/framework/roles", map[string]any{"user": "x", "role": "System Manager"}},
		{http.MethodGet, "/v1/framework/summary", nil},
	}
	for _, r := range routes {
		if code := forged(r.method, r.path, r.body); code != http.StatusForbidden {
			t.Fatalf("forged %s %s want 403, got %d", r.method, r.path, code)
		}
	}
}

// TestCrossOrgIsolation proves one validated tenant cannot see or touch another's
// doctypes or documents, even with exact names.
func TestCrossOrgIsolation(t *testing.T) {
	app := mountApp(t)
	// acme defines Task and creates a doc.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "acme", taskDocType())
	_, body := do(t, app, http.MethodPost, "/v1/framework/Task", "acme", map[string]any{"subject": "acme secret"})
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	name := doc["name"].(string)

	// evil (its own valid principal) cannot see acme's Task doctype.
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/doctypes/Task", "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil GET acme doctype want 404, got %d", code)
	}
	// evil listing acme's Task doctype documents → 404 (doctype unknown in evil).
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/Task", "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil list Task want 404, got %d", code)
	}
	// Even if evil defines its OWN Task, it cannot read acme's document by name.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", "evil", taskDocType())
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/Task/"+name, "evil", nil); code != http.StatusNotFound {
		t.Fatalf("evil GET acme doc want 404, got %d", code)
	}
	// evil's own list is empty (its own Task has no docs).
	code, body := do(t, app, http.MethodGet, "/v1/framework/Task", "evil", nil)
	var list struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if code != http.StatusOK || len(list.Data) != 0 {
		t.Fatalf("evil Task list want empty, got %d %+v", code, list.Data)
	}
	// acme's doc survives.
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/Task/"+name, "acme", nil); code != http.StatusOK {
		t.Fatalf("acme doc must survive, got %d", code)
	}
}

// TestPermissionEnforcement covers the full permission model: bootstrap, strict
// enforcement after configuration, role-scoped rights, System Manager + global
// admin bypass.
func TestPermissionEnforcement(t *testing.T) {
	app := mountApp(t)
	const org = "acme"

	// u1 (bootstrap manager) defines a Ticket restricted to the Agent role.
	ticket := map[string]any{
		"name":   "Ticket",
		"fields": []map[string]any{{"fieldname": "subject", "fieldtype": "Data", "reqd": true}},
		"permissions": []map[string]any{
			{"role": "Agent", "read": true, "create": true},
		},
	}
	if code, b := call(t, app, http.MethodPost, "/v1/framework/doctypes", org, "u1", false, ticket); code != http.StatusCreated {
		t.Fatalf("u1 define ticket want 201, got %d (%s)", code, b)
	}

	// u1 grants ITSELF System Manager (else assigning roles below locks u1 out of
	// bootstrap), then grants u2 the Agent role.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/roles", org, "u1", false, map[string]any{"user": "u1", "role": RoleSystemManager}); code != http.StatusCreated {
		t.Fatalf("u1 self-grant SM want 201, got %d", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/roles", org, "u1", false, map[string]any{"user": "u2", "role": "Agent"}); code != http.StatusCreated {
		t.Fatalf("grant u2 Agent want 201, got %d", code)
	}

	// The org is now configured: u3 (no roles) is DENIED read on Ticket.
	if code, _ := call(t, app, http.MethodGet, "/v1/framework/Ticket", org, "u3", false, nil); code != http.StatusForbidden {
		t.Fatalf("u3 read Ticket want 403, got %d", code)
	}
	// u3 cannot create either.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/Ticket", org, "u3", false, map[string]any{"subject": "x"}); code != http.StatusForbidden {
		t.Fatalf("u3 create Ticket want 403, got %d", code)
	}
	// u3 (not a manager anymore) cannot define doctypes.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/doctypes", org, "u3", false, taskDocType()); code != http.StatusForbidden {
		t.Fatalf("u3 define doctype want 403, got %d", code)
	}

	// u2 (Agent) CAN read + create.
	if code, _ := call(t, app, http.MethodGet, "/v1/framework/Ticket", org, "u2", false, nil); code != http.StatusOK {
		t.Fatalf("u2 read Ticket want 200, got %d", code)
	}
	code, body := call(t, app, http.MethodPost, "/v1/framework/Ticket", org, "u2", false, map[string]any{"subject": "help"})
	if code != http.StatusCreated {
		t.Fatalf("u2 create Ticket want 201, got %d (%s)", code, body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	tname := doc["name"].(string)
	// but Agent has NO delete right → 403.
	if code, _ := call(t, app, http.MethodDelete, "/v1/framework/Ticket/"+tname, org, "u2", false, nil); code != http.StatusForbidden {
		t.Fatalf("u2 delete Ticket want 403 (no delete perm), got %d", code)
	}

	// A GLOBAL ADMIN bypasses per-doctype perms.
	if code, _ := call(t, app, http.MethodDelete, "/v1/framework/Ticket/"+tname, org, "root", true, nil); code != http.StatusNoContent {
		t.Fatalf("global admin delete Ticket want 204, got %d", code)
	}
	// System Manager (u1) can define doctypes.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/doctypes", org, "u1", false, taskDocType()); code != http.StatusCreated {
		t.Fatalf("u1 (SM) define doctype want 201, got %d", code)
	}
}

// TestPasswordRedactedOverWire proves a Password field never leaves the wire in
// clear or as a hash.
func TestPasswordRedactedOverWire(t *testing.T) {
	app := mountApp(t)
	const org = "acme"
	cred := map[string]any{
		"name": "Cred", "autoname": "field:key",
		"fields": []map[string]any{
			{"fieldname": "key", "fieldtype": "Data", "reqd": true},
			{"fieldname": "secret", "fieldtype": "Password"},
		},
	}
	do(t, app, http.MethodPost, "/v1/framework/doctypes", org, cred)
	code, body := do(t, app, http.MethodPost, "/v1/framework/Cred", org, map[string]any{"key": "api", "secret": "hunter2"})
	if code != http.StatusCreated {
		t.Fatalf("create cred want 201, got %d (%s)", code, body)
	}
	if bytes.Contains(body, []byte("hunter2")) {
		t.Fatalf("plaintext password leaked on the wire: %s", body)
	}
	if bytes.Contains(body, []byte("$argon2id$")) {
		t.Fatalf("password hash leaked on the wire: %s", body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	if doc["secret"] != redactedMarker {
		t.Fatalf("secret want redacted marker, got %v", doc["secret"])
	}
}

// TestListFilterAndOrder exercises json_extract filtering + ordering over the
// wire — proving the bound-parameter argument alignment in ListDocuments.
func TestListFilterAndOrder(t *testing.T) {
	app := mountApp(t)
	const org = "acme"
	do(t, app, http.MethodPost, "/v1/framework/doctypes", org, taskDocType())
	for _, tk := range []map[string]any{
		{"subject": "a", "priority": "Low", "estimate": 3},
		{"subject": "b", "priority": "High", "estimate": 1},
		{"subject": "c", "priority": "High", "estimate": 2},
	} {
		if code, b := do(t, app, http.MethodPost, "/v1/framework/Task", org, tk); code != http.StatusCreated {
			t.Fatalf("seed want 201, got %d (%s)", code, b)
		}
	}
	// unmarshalList decodes into a FRESH target (json.Unmarshal merges into reused
	// maps, so a shared variable would carry stale keys between assertions).
	unmarshalList := func(b []byte) []map[string]any {
		var l struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b, &l); err != nil {
			t.Fatalf("unmarshal list: %v (%s)", err, b)
		}
		return l.Data
	}

	// Filter priority=High → 2 rows.
	code, body := do(t, app, http.MethodGet, `/v1/framework/Task?filters={"priority":"High"}`, org, nil)
	if rows := unmarshalList(body); code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("filter High want 2 rows, got %d %+v", code, rows)
	}
	// order_by estimate asc → first is estimate 1.
	code, body = do(t, app, http.MethodGet, "/v1/framework/Task?order_by=estimate%20asc", org, nil)
	if rows := unmarshalList(body); code != http.StatusOK || len(rows) != 3 || rows[0]["estimate"] != float64(1) {
		t.Fatalf("order_by estimate asc want first=1, got %d %+v", code, rows)
	}
	// Unknown filter field → 400 (not a silent cross-field query).
	if code, _ := do(t, app, http.MethodGet, `/v1/framework/Task?filters={"nope":"x"}`, org, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown filter want 400, got %d", code)
	}
	// fields projection returns only requested + envelope keys.
	code, body = do(t, app, http.MethodGet, "/v1/framework/Task?fields=subject&limit=1", org, nil)
	rows := unmarshalList(body)
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("projection want 1 row, got %d", code)
	}
	if _, hasSubject := rows[0]["subject"]; !hasSubject {
		t.Fatalf("projection must keep subject: %+v", rows[0])
	}
	if _, hasPriority := rows[0]["priority"]; hasPriority {
		t.Fatalf("projection must drop priority: %+v", rows[0])
	}
}

// TestLinkAndFetchFrom proves Link relations validate in-org and fetch_from
// auto-populates from the linked document.
func TestLinkAndFetchFrom(t *testing.T) {
	app := mountApp(t)
	const org = "acme"
	// Customer with a name + region.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", org, map[string]any{
		"name": "Customer", "autoname": "field:code",
		"fields": []map[string]any{
			{"fieldname": "code", "fieldtype": "Data", "reqd": true},
			{"fieldname": "region", "fieldtype": "Data"},
		},
	})
	// Order links to Customer and fetches its region.
	do(t, app, http.MethodPost, "/v1/framework/doctypes", org, map[string]any{
		"name": "SalesOrder",
		"fields": []map[string]any{
			{"fieldname": "customer", "fieldtype": "Link", "options": "Customer", "reqd": true},
			{"fieldname": "region", "fieldtype": "Data", "fetchFrom": "customer.region"},
		},
	})
	do(t, app, http.MethodPost, "/v1/framework/Customer", org, map[string]any{"code": "ACME", "region": "West"})

	// Valid link + fetch_from.
	code, body := do(t, app, http.MethodPost, "/v1/framework/SalesOrder", org, map[string]any{"customer": "ACME"})
	if code != http.StatusCreated {
		t.Fatalf("create order want 201, got %d (%s)", code, body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	if doc["region"] != "West" {
		t.Fatalf("fetch_from want region=West, got %v", doc["region"])
	}
	// Dangling link → 422.
	if code, _ := do(t, app, http.MethodPost, "/v1/framework/SalesOrder", org, map[string]any{"customer": "GHOST"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("dangling link want 422, got %d", code)
	}
}

// TestNoOrgRefused is the belt-and-suspenders no-principal check on the read path.
func TestNoOrgRefused(t *testing.T) {
	app := mountApp(t)
	for _, p := range []string{"/v1/framework/doctypes", "/v1/framework/roles", "/v1/framework/summary", "/v1/framework/Task"} {
		if code, _ := call(t, app, http.MethodGet, p, "", "", false, nil); code != http.StatusForbidden {
			t.Fatalf("no-principal GET %s want 403, got %d", p, code)
		}
	}
}
