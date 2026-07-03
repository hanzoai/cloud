package framework

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestSpaceNamedDocTypeAndDocumentRoundTrip is the regression proof for the
// URL-encoded path-param bug: the router (zip over fasthttp) runs with Fiber's
// default UnescapePath:false, so a name that arrives percent-encoded on the wire
// ("Test Space" → "Test%20Space") was handed to the handler RAW and never matched
// the stored value — GET/PUT/DELETE/submit/cancel by name 404'd for any DocType
// or document whose name contains a space (which docTypeNameRe explicitly allows,
// e.g. "Sales Invoice"). pathParam decodes at the ONE seam, so every by-name
// operation resolves. Create+list were unaffected (no name in the path), so a
// record could be made yet be unreachable — exactly what this asserts is fixed.
func TestSpaceNamedDocTypeAndDocumentRoundTrip(t *testing.T) {
	app := mountApp(t)
	const org = "acme"

	// A DocType whose NAME contains a space, client-named documents (prompt), and
	// submittable so the submit/cancel-by-name paths are covered too.
	dt := map[string]any{
		"name": "Test Space", "autoname": "prompt", "isSubmittable": true,
		"fields": []map[string]any{{"fieldname": "body", "fieldtype": "Data", "reqd": true}},
	}
	if code, b := do(t, app, http.MethodPost, "/v1/framework/doctypes", org, dt); code != http.StatusCreated {
		t.Fatalf("define space-named doctype want 201, got %d (%s)", code, b)
	}

	// GET the DocType by its space name (encoded). Pre-fix: 404. (getDocType :name)
	code, body := do(t, app, http.MethodGet, "/v1/framework/doctypes/Test%20Space", org, nil)
	if code != http.StatusOK {
		t.Fatalf("GET doctype by space name want 200, got %d (%s)", code, body)
	}
	var gotDT DocType
	_ = json.Unmarshal(body, &gotDT)
	if gotDT.Name != "Test Space" {
		t.Fatalf("doctype name want %q, got %q", "Test Space", gotDT.Name)
	}

	// Create a document whose NAME contains a space, under the space-named DocType.
	// The doctype segment is encoded too, so this also covers access(:doctype).
	code, body = do(t, app, http.MethodPost, "/v1/framework/Test%20Space", org,
		map[string]any{"name": "Alpha Beta", "body": "one"})
	if code != http.StatusCreated {
		t.Fatalf("create space-named doc want 201, got %d (%s)", code, body)
	}
	var doc map[string]any
	_ = json.Unmarshal(body, &doc)
	if doc["name"] != "Alpha Beta" {
		t.Fatalf("doc name want %q, got %v", "Alpha Beta", doc["name"])
	}

	docPath := "/v1/framework/Test%20Space/Alpha%20Beta"

	// GET the document by name. Pre-fix: 404. (getDocument: access + docName)
	code, body = do(t, app, http.MethodGet, docPath, org, nil)
	if code != http.StatusOK {
		t.Fatalf("GET space-named doc want 200, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &doc)
	if doc["body"] != "one" {
		t.Fatalf("doc body want %q, got %v", "one", doc["body"])
	}

	// PUT (update while draft). Pre-fix: 404. (updateDocument)
	code, body = do(t, app, http.MethodPut, docPath, org, map[string]any{"body": "two"})
	if code != http.StatusOK {
		t.Fatalf("PUT space-named doc want 200, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &doc)
	if doc["body"] != "two" {
		t.Fatalf("doc body after PUT want %q, got %v", "two", doc["body"])
	}

	// submit 0→1 by name. Pre-fix: 404. (submitDocument via transition→docName)
	code, body = do(t, app, http.MethodPost, docPath+"/submit", org, nil)
	_ = json.Unmarshal(body, &doc)
	if code != http.StatusOK || doc["docstatus"] != float64(1) {
		t.Fatalf("submit space-named doc want 200/docstatus 1, got %d %v", code, doc["docstatus"])
	}

	// cancel 1→2 by name. Pre-fix: 404. (cancelDocument via transition→docName)
	code, body = do(t, app, http.MethodPost, docPath+"/cancel", org, nil)
	_ = json.Unmarshal(body, &doc)
	if code != http.StatusOK || doc["docstatus"] != float64(2) {
		t.Fatalf("cancel space-named doc want 200/docstatus 2, got %d %v", code, doc["docstatus"])
	}

	// DELETE the (cancelled) document by name → 204. Pre-fix: 404. (deleteDocument)
	if code, b := do(t, app, http.MethodDelete, docPath, org, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE space-named doc want 204, got %d (%s)", code, b)
	}
	if code, _ := do(t, app, http.MethodGet, docPath, org, nil); code != http.StatusNotFound {
		t.Fatalf("GET deleted space-named doc want 404, got %d", code)
	}

	// Replace then delete the DocType by its space name → 200 / 204.
	replace := map[string]any{
		"name": "Test Space", "autoname": "prompt",
		"fields": []map[string]any{{"fieldname": "body", "fieldtype": "Data"}},
	}
	if code, b := do(t, app, http.MethodPut, "/v1/framework/doctypes/Test%20Space", org, replace); code != http.StatusOK {
		t.Fatalf("PUT (replace) doctype by space name want 200, got %d (%s)", code, b)
	}
	if code, b := do(t, app, http.MethodDelete, "/v1/framework/doctypes/Test%20Space", org, nil); code != http.StatusNoContent {
		t.Fatalf("DELETE doctype by space name want 204, got %d (%s)", code, b)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/framework/doctypes/Test%20Space", org, nil); code != http.StatusNotFound {
		t.Fatalf("GET deleted doctype want 404, got %d", code)
	}
}

// TestSpaceNamedRoleRevoke proves a role whose NAME contains a space — the
// canonical "System Manager" — is revocable through /v1/framework/roles/:user/
// :role. Pre-fix the encoded ":role" ("System%20Manager") never matched the
// stored assignment, so RevokeRole reported "not found" (404) and a granted
// System Manager could not be removed. This is the revokeRole leg of the
// pathParam fix (:user and :role both decoded).
func TestSpaceNamedRoleRevoke(t *testing.T) {
	app := mountApp(t)
	const org = "roleorg"

	// First manager op on a fresh org seeds the caller (u_roleorg) as System
	// Manager (trust-on-first-use) and assigns "alice" the space-named role.
	if code, b := do(t, app, http.MethodPost, "/v1/framework/roles", org,
		map[string]any{"user": "alice", "role": RoleSystemManager}); code != http.StatusCreated {
		t.Fatalf("assign %q want 201, got %d (%s)", RoleSystemManager, code, b)
	}

	// The assignment is present (2 rows: the seeded owner + alice).
	code, body := do(t, app, http.MethodGet, "/v1/framework/roles", org, nil)
	var roles struct {
		Data []Role `json:"data"`
	}
	_ = json.Unmarshal(body, &roles)
	if code != http.StatusOK || !hasRole(roles.Data, "alice", RoleSystemManager) {
		t.Fatalf("roles must contain alice/%q, got %d %+v", RoleSystemManager, code, roles.Data)
	}

	// Revoke by encoded path. Pre-fix: 404 (encoded ":role" never matched).
	if code, b := do(t, app, http.MethodDelete, "/v1/framework/roles/alice/System%20Manager", org, nil); code != http.StatusNoContent {
		t.Fatalf("revoke space-named role want 204, got %d (%s)", code, b)
	}

	// Gone.
	code, body = do(t, app, http.MethodGet, "/v1/framework/roles", org, nil)
	_ = json.Unmarshal(body, &roles)
	if code != http.StatusOK || hasRole(roles.Data, "alice", RoleSystemManager) {
		t.Fatalf("alice/%q must be revoked, got %d %+v", RoleSystemManager, code, roles.Data)
	}
}

func hasRole(rows []Role, user, role string) bool {
	for _, r := range rows {
		if r.User == user && r.Role == role {
			return true
		}
	}
	return false
}
