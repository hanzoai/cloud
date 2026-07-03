package help

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

// TestFixturesValid proves every Help DocType is a well-formed schema, that its Link
// targets resolve WITHIN the lane, and that names are slug-clean (reachable through
// the console's `/cloud` path filter).
func TestFixturesValid(t *testing.T) {
	dts := DocTypes()
	names := map[string]framework.DocType{}
	for _, dt := range dts {
		if err := dt.Validate(); err != nil {
			t.Fatalf("fixture %q invalid: %v", dt.Name, err)
		}
		if !slugClean(dt.Name) {
			t.Fatalf("DocType name %q is not slug-clean", dt.Name)
		}
		names[dt.Name] = dt
	}
	for _, dt := range dts {
		for _, f := range dt.Fields {
			if f.Fieldtype == framework.FieldLink {
				if _, ok := names[f.Options]; !ok {
					t.Fatalf("%s.%s links to %q which is not a Help DocType (Help must be self-contained)", dt.Name, f.Fieldname, f.Options)
				}
			}
		}
	}
}

// TestModelSpec locks the ticket workflow contract: the status Select
// (Open/Pending/Resolved/Closed) that IS the lifecycle, series naming, and that
// nothing in Help is submittable (workflow is a status write, not a submit).
func TestModelSpec(t *testing.T) {
	byName := map[string]framework.DocType{}
	for _, dt := range DocTypes() {
		if dt.Module != Module {
			t.Fatalf("%s: module want %q, got %q", dt.Name, Module, dt.Module)
		}
		if dt.IsSubmittable {
			t.Fatalf("%s must not be submittable (Help uses status workflow)", dt.Name)
		}
		byName[dt.Name] = dt
	}
	for _, want := range []string{dtTicket, dtAgent, dtTeam, dtCannedResponse, dtSLA} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("missing Help DocType %q", want)
		}
	}
	ticket := byName[dtTicket]
	status, ok := fieldOf(ticket, "status")
	if !ok || status.Fieldtype != framework.FieldSelect || status.Default != "Open" || status.Options != "Open\nPending\nResolved\nClosed" {
		t.Fatalf("hd-ticket.status must be a Select defaulting to Open with the four states, got %+v", status)
	}
	if a, ok := fieldOf(ticket, "assigned_agent"); !ok || a.Fieldtype != framework.FieldLink || a.Options != dtAgent {
		t.Fatalf("hd-ticket.assigned_agent must Link hd-agent, got %+v", a)
	}
}

// TestInstallAndTicketFlow drives the whole model through the real engine over HTTP:
// install Help STANDALONE (no ERP), create an agent, open a ticket linking it
// (status Open), then advance the status to Resolved — proving the ticket lifecycle
// is a status field on the generic engine, and Help is self-contained.
func TestInstallAndTicketFlow(t *testing.T) {
	app := mount(t)
	const org = "acme"

	if code, body := call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil); code != http.StatusOK {
		t.Fatalf("install help want 200, got %d (%s)", code, body)
	}

	// An agent to assign (hash… no — field:agent_name → name is the slug of the name).
	agent := mustCreate(t, app, org, dtAgent, map[string]any{"agent_name": "jane", "email": "jane@acme.test"})
	if agent["name"] != "jane" {
		t.Fatalf("agent name (field:agent_name) want jane, got %v", agent["name"])
	}

	// Open a ticket linking the agent; status defaults to Open.
	tk := mustCreate(t, app, org, dtTicket, map[string]any{
		"subject": "Cannot sign in", "priority": "High", "customer": "bob@acme.test", "assigned_agent": "jane",
	})
	if tk["status"] != "Open" {
		t.Fatalf("new ticket status want Open (default), got %v", tk["status"])
	}
	name, _ := tk["name"].(string)

	// No resolved tickets yet.
	if n := len(listDocs(t, app, org, `/v1/framework/`+dtTicket+`?filters={"status":"Resolved"}`)); n != 0 {
		t.Fatalf("resolved tickets want 0, got %d", n)
	}

	// Advance the status → Resolved (a status write, the whole record on update).
	if code, body := call(t, app, http.MethodPut, "/v1/framework/"+dtTicket+"/"+name, org, map[string]any{
		"subject": "Cannot sign in", "priority": "High", "assigned_agent": "jane", "status": "Resolved", "resolution": "Reset password",
	}); code != http.StatusOK {
		t.Fatalf("resolve ticket want 200, got %d (%s)", code, body)
	}
	if n := len(listDocs(t, app, org, `/v1/framework/`+dtTicket+`?filters={"status":"Resolved"}`)); n != 1 {
		t.Fatalf("resolved tickets want 1, got %d", n)
	}

	// A dangling agent Link is refused (422) — Link integrity within the org.
	if code, _ := call(t, app, http.MethodPost, "/v1/framework/"+dtTicket, org, map[string]any{
		"subject": "Ghost", "assigned_agent": "nobody",
	}); code != http.StatusUnprocessableEntity {
		t.Fatalf("dangling agent link want 422, got %d", code)
	}
}

// TestInstallIdempotent proves a second install creates nothing (create-if-absent).
func TestInstallIdempotent(t *testing.T) {
	app := mount(t)
	const org = "idem"
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)
	code, body := call(t, app, http.MethodPost, "/v1/framework/modules/help/install", org, nil)
	var res struct {
		Created  []string `json:"created"`
		Existing []string `json:"existing"`
	}
	_ = json.Unmarshal(body, &res)
	if code != http.StatusOK || len(res.Created) != 0 || len(res.Existing) != len(DocTypes()) {
		t.Fatalf("idempotent install want created=0 existing=%d, got %d %+v", len(DocTypes()), code, res)
	}
}

// TestTenantIsolation proves Help DocTypes are invisible across orgs.
func TestTenantIsolation(t *testing.T) {
	app := mount(t)
	call(t, app, http.MethodPost, "/v1/framework/modules/help/install", "orgA", nil)
	mustCreate(t, app, "orgA", dtTicket, map[string]any{"subject": "A ticket"})
	if n := len(listDocs(t, app, "orgB", "/v1/framework/doctypes")); n != 0 {
		t.Fatalf("orgB must have zero doctypes, got %d", n)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/framework/"+dtTicket, "orgB", nil); code != http.StatusNotFound {
		t.Fatalf("orgB ticket read want 404 (not installed), got %d", code)
	}
}

// ---- harness ----

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := framework.Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("mount framework: %v", err)
	}
	t.Cleanup(func() { _ = framework.Shutdown() })
	return app
}

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

func call(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
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
	req.Header.Set("X-User-Id", "u_"+org)
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

func slugClean(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return s != ""
}
