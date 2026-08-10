package search

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (Mount says why beside its registrations): the program's composer does, once
// at the root. In a test the test IS the composer, so it owes the same install —
// skipping it does not test a stricter program, it tests one where every
// org-scoped op answers 403 for a reason production could never produce.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{Domain: "api.test"}); err != nil {
		t.Fatalf("search.Mount: %v", err)
	}
	return app
}

// post issues a search with a validated principal (X-User-Id is the signal the
// identity middleware sets only from a verified credential).
func post(t *testing.T, app *zip.App, org string, body any, principal bool) (int, Response) {
	t.Helper()
	b, _ := json.Marshal(body)
	hr := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(string(b)))
	hr.Header.Set("Content-Type", "application/json")
	if org != "" {
		hr.Header.Set("X-Org-Id", org)
	}
	if principal {
		hr.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(hr)
	if err != nil {
		t.Fatalf("POST /v1/search: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var out Response
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// TestDegradationIsExplicit is the regression test for the failure that hid a
// five-day vector outage: with no store provisioned, the surface must still
// answer 200 AND say, per backend, that it has nothing wired — never an
// unqualified empty result set that a caller reads as "no matches".
func TestDegradationIsExplicit(t *testing.T) {
	app := mount(t)
	code, out := post(t, app, "acme", Request{Query: "how do I rotate a session cookie"}, true)

	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a retrieval outage must not fail the caller's turn)", code)
	}
	if len(out.Backends) != 2 {
		t.Fatalf("every leg must be reported on every response, got %+v", out.Backends)
	}
	for _, b := range out.Backends {
		if b.Status == StatusOK {
			t.Fatalf("no store is mounted in this test, so no leg can be ok: %+v", b)
		}
		if b.Status != StatusDisabled && b.Status != StatusDegraded && b.Status != StatusSkipped {
			t.Fatalf("unknown backend status %q", b.Status)
		}
	}
	// Unprovisioned is NOT the same fact as broken, and must not be reported as
	// a failed query.
	if out.Status != StatusOK {
		t.Fatalf("with both legs merely unprovisioned the query itself did not fail; status = %q", out.Status)
	}
	if out.Hits == nil {
		t.Fatal("hits must serialize as [] not null")
	}
}

// TestOverallSeparatesUnprovisionedFromBroken pins the four-status contract. The
// distinction is the whole point: "never wired up" and "wired up and failing" are
// different operational facts and an operator must be able to tell them apart.
func TestOverallSeparatesUnprovisionedFromBroken(t *testing.T) {
	cases := []struct {
		name string
		in   []BackendStatus
		want string
	}{
		{"all ok", []BackendStatus{{Status: StatusOK}, {Status: StatusOK}}, StatusOK},
		{"unprovisioned is not a failure", []BackendStatus{{Status: StatusDisabled}, {Status: StatusSkipped}}, StatusOK},
		{"one leg down is partial", []BackendStatus{{Status: StatusOK}, {Status: StatusDegraded}}, "partial"},
		{"every consulted leg down", []BackendStatus{{Status: StatusDegraded}, {Status: StatusDegraded}}, "unavailable"},
		{"lone leg down, other unwired", []BackendStatus{{Status: StatusDegraded}, {Status: StatusDisabled}}, "unavailable"},
	}
	for _, tc := range cases {
		if got := overall(tc.in); got != tc.want {
			t.Errorf("%s: overall = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRequiresValidatedPrincipal proves the tenant boundary: a client-supplied
// org with no validated user is the forge case and must be refused, not served.
func TestRequiresValidatedPrincipal(t *testing.T) {
	app := mount(t)
	if code, _ := post(t, app, "victim", Request{Query: "anything"}, false); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an unvalidated principal", code)
	}
}

// TestEmptyQueryRejected — an empty query is a client error, not an empty result.
func TestEmptyQueryRejected(t *testing.T) {
	app := mount(t)
	if code, _ := post(t, app, "acme", Request{Query: "   "}, true); code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

// TestModeIsHonest proves an explicitly requested leg is never silently widened
// to another, and that the mode actually used is reported back.
func TestModeIsHonest(t *testing.T) {
	app := mount(t)
	_, out := post(t, app, "acme", Request{Query: "x", Mode: ModeText}, true)
	if out.Mode != ModeText {
		t.Fatalf("mode = %q, want %q", out.Mode, ModeText)
	}
	for _, b := range out.Backends {
		if b.Name == BackendVector && b.Status != StatusSkipped {
			t.Fatalf("mode=text must skip the vector leg, got %+v", b)
		}
	}
}

// TestLexicalListIdentity proves the adapter derives a stable cross-leg identity,
// which is what lets the same document found by both legs fuse into one
// reinforced row instead of appearing twice.
func TestLexicalListIdentity(t *testing.T) {
	payload := map[string]Result{}
	rows := []json.RawMessage{
		json.RawMessage(`{"doctype":"kb-page","name":"runbook","title":"Runbook"}`),
		json.RawMessage(`{"id":"orphan"}`),
		json.RawMessage(`not json`),
	}
	l := lexicalList(rows, payload)
	if len(l.Keys) != 2 {
		t.Fatalf("want 2 usable rows (bad JSON skipped), got %v", l.Keys)
	}
	if l.Keys[0] != "kb-page/runbook" {
		t.Fatalf("key = %q, want kb-page/runbook", l.Keys[0])
	}
	if payload["kb-page/runbook"].Title != "Runbook" {
		t.Fatalf("payload not captured: %+v", payload)
	}
}
