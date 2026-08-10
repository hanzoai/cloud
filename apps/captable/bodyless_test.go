package captable

// bodyless_test.go is the wire proof for the six routes typed in this pass — the
// round detail read and the five deletes. Every one of them is BODYLESS, which is
// what made it typeable: its whole input is one path segment, so a Go In cannot
// accept less than the route always did.
//
// The proof does not trust a recorded golden. It re-derives the pre-typing answer
// on every run by dispatching the SAME bundle route with the SAME params straight
// on the tenant's store — which is exactly what the untyped relay these ops
// replaced wrote — and compares status, Content-Type and body bytes.
//
// Both arms matter and they are different mechanisms:
//
//   - the 2xx arm goes through zip's typed JSON writer, so it proves the Go model
//     round-trips the bundle's bytes unchanged;
//   - the non-2xx arm goes through bundleErr → bundleEnvelope, so it proves the
//     bundle's OWN envelope still reaches the client — including the `errors` list
//     zip's {status,code,error} has nowhere to put.

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/zap-proto/zip"
)

// bundleJSON is the Content-Type the untyped relay sent and bundleEnvelope keeps:
// dispatch() sets the bare form, with no charset.
const bundleJSON = "application/json"

// TestRoundDetailIsByteIdenticalToTheBundle pins GET /v1/captable/rounds/:id — the
// only parameterised READ — on both arms: a round that exists (a PRICED round with
// a cheque, so every nullable column in the detail is exercised) and one that does
// not.
func TestRoundDetailIsByteIdenticalToTheBundle(t *testing.T) {
	app := mountApp(t)
	seedTenant(t, app, "acme")

	// Every round the seed wrote: the closed PRICED one (closeDate, pricePerShare,
	// preMoneyValuation and shareClassId all set, one investment) and the OPEN SAFE
	// one (all four null, no investments).
	_, body := req(t, app, http.MethodGet, "/v1/captable/rounds", "acme", nil)
	var rounds struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &rounds); err != nil || len(rounds.Data) != 2 {
		t.Fatalf("expected the seed's two rounds: %v (%s)", err, body)
	}

	var withCheque bool
	for _, r := range rounds.Data {
		wantStatus, want := bundleBytes(t, "acme", "rounds.get", map[string]string{"id": r.ID})
		if wantStatus != http.StatusOK {
			t.Fatalf("bundle rounds.get %s: %d (%s)", r.ID, wantStatus, want)
		}
		gotStatus, got, ct := probe(t, app, http.MethodGet, "/v1/captable/rounds/"+r.ID, "acme", nil)
		if gotStatus != http.StatusOK {
			t.Fatalf("GET rounds/%s want 200, got %d (%s)", r.ID, gotStatus, got)
		}
		if string(got) != string(want) {
			t.Fatalf("GET rounds/%s (%s) is not byte-identical to the bundle\n typed:  %s\n bundle: %s", r.ID, r.Name, got, want)
		}
		if ct == "" {
			t.Fatalf("GET rounds/%s sent no Content-Type", r.ID)
		}
		// Non-vacuity: one of the two rounds must actually carry a cheque, or the
		// investments arm of the model is never compared against real rows.
		var detail struct {
			Investments []map[string]any `json:"investments"`
		}
		if json.Unmarshal(got, &detail) == nil && len(detail.Investments) > 0 {
			withCheque = true
			if _, ok := detail.Investments[0]["comments"]; !ok {
				t.Fatalf("round detail dropped the cheque's comments: %s", got)
			}
		}
	}
	if !withCheque {
		t.Fatalf("no seeded round carried an investment — the detail's investments arm was never compared")
	}

	// The 404 arm: the BUNDLE's own envelope, byte for byte, under the bare
	// application/json the relay sent.
	wantStatus, want := bundleBytes(t, "acme", "rounds.get", map[string]string{"id": "nope"})
	if wantStatus != http.StatusNotFound {
		t.Fatalf("bundle rounds.get on a missing id want 404, got %d (%s)", wantStatus, want)
	}
	gotStatus, got, ct := probe(t, app, http.MethodGet, "/v1/captable/rounds/nope", "acme", nil)
	if gotStatus != http.StatusNotFound {
		t.Fatalf("GET rounds/nope want 404, got %d (%s)", gotStatus, got)
	}
	if string(got) != string(want) {
		t.Fatalf("the typed 404 is not the bundle's\n typed:  %s\n bundle: %s", got, want)
	}
	if ct != bundleJSON {
		t.Fatalf("the typed 404 moved Content-Type: %q, want %q", ct, bundleJSON)
	}
}

// deletable pairs a delete route's HTTP path prefix with its bundle route and with
// the read that lists the rows it removes, so one table drives all five.
var deletable = []struct {
	prefix string // /v1/captable/<collection>
	route  string // the bundle route the relay dispatched
	list   string // the typed read that enumerates the collection
	envel  bool   // true when the list answers {"data":[…]} rather than a bare array
}{
	{"/v1/captable/shares", "shares.delete", "/v1/captable/shares", true},
	{"/v1/captable/options", "options.delete", "/v1/captable/options", true},
	{"/v1/captable/safes", "safes.delete", "/v1/captable/safes", true},
	{"/v1/captable/convertibles", "convertibles.delete", "/v1/captable/convertibles", true},
	{"/v1/captable/stakeholders", "stakeholders.delete", "/v1/captable/stakeholders", false},
}

// ids reads a collection through its typed list op and returns the row ids.
func ids(t *testing.T, app *zip.App, org, path string, enveloped bool) []string {
	t.Helper()
	code, body := req(t, app, http.MethodGet, path, org, nil)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d (%s)", path, code, body)
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if enveloped {
		var env struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, body)
		}
		rows = env.Data
	} else if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, body)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// TestDeletesAreByteIdenticalToTheBundle drives every typed DELETE on both arms
// against TWO identically-seeded tenants: the typed HTTP route removes acme's row
// while a direct bundle dispatch removes bravo's matching row, so the two answers
// are comparable without either one having to be recorded.
func TestDeletesAreByteIdenticalToTheBundle(t *testing.T) {
	app := mountApp(t)
	seedTenant(t, app, "acme")
	seedTenant(t, app, "bravo")

	// A holder with NO securities, in both tenants — the only stakeholder a delete
	// is allowed to remove, since the bundle refuses to orphan issued equity.
	for _, org := range []string{"acme", "bravo"} {
		code, body := req(t, app, http.MethodPost, "/v1/captable/stakeholders", org, map[string]any{
			"name": "Grace Hopper", "email": "grace@example.com",
			"stakeholderType": "INDIVIDUAL", "currentRelationship": "ADVISOR",
		})
		if code != http.StatusCreated {
			t.Fatalf("%s: seed the unencumbered holder: %d (%s)", org, code, body)
		}
	}

	for _, d := range deletable {
		// The 404 arm first: an id neither tenant holds. No mutation, so both sides
		// see the same state.
		wantStatus, want := bundleBytes(t, "bravo", d.route, map[string]string{"id": "nope"})
		if wantStatus != http.StatusNotFound {
			t.Fatalf("bundle %s on a missing id want 404, got %d (%s)", d.route, wantStatus, want)
		}
		gotStatus, got, ct := probe(t, app, http.MethodDelete, d.prefix+"/nope", "acme", nil)
		if gotStatus != http.StatusNotFound {
			t.Fatalf("DELETE %s/nope want 404, got %d (%s)", d.prefix, gotStatus, got)
		}
		if string(got) != string(want) {
			t.Fatalf("DELETE %s/nope 404 is not the bundle's\n typed:  %s\n bundle: %s", d.prefix, got, want)
		}
		if ct != bundleJSON {
			t.Fatalf("DELETE %s/nope moved Content-Type: %q, want %q", d.prefix, ct, bundleJSON)
		}

		// The success arm: acme's row through the typed route, bravo's matching row
		// straight through the bundle.
		target := func(org string) string {
			t.Helper()
			rows := ids(t, app, org, d.list, d.envel)
			if len(rows) == 0 {
				t.Fatalf("%s: %s is empty — nothing to delete", org, d.list)
			}
			if d.route != "stakeholders.delete" {
				return rows[0]
			}
			// Only the unencumbered holder is deletable; find it by email.
			_, body := req(t, app, http.MethodGet, d.list, org, nil)
			var holders []struct {
				ID    string `json:"id"`
				Email string `json:"email"`
			}
			if err := json.Unmarshal(body, &holders); err != nil {
				t.Fatalf("%s: decode stakeholders: %v (%s)", org, err, body)
			}
			for _, h := range holders {
				if h.Email == "grace@example.com" {
					return h.ID
				}
			}
			t.Fatalf("%s: the unencumbered holder is missing: %s", org, body)
			return ""
		}

		acmeID, bravoID := target("acme"), target("bravo")
		wantStatus, want = bundleBytes(t, "bravo", d.route, map[string]string{"id": bravoID})
		if wantStatus != http.StatusOK {
			t.Fatalf("bundle %s on %s want 200, got %d (%s)", d.route, bravoID, wantStatus, want)
		}
		gotStatus, got = req(t, app, http.MethodDelete, d.prefix+"/"+acmeID, "acme", nil)
		if gotStatus != http.StatusOK {
			t.Fatalf("DELETE %s/%s want 200, got %d (%s)", d.prefix, acmeID, gotStatus, got)
		}
		if string(got) != string(want) {
			t.Fatalf("DELETE %s success body is not the bundle's\n typed:  %s\n bundle: %s", d.prefix, got, want)
		}

		// It really deleted: the row is gone from the typed list.
		for _, id := range ids(t, app, "acme", d.list, d.envel) {
			if id == acmeID {
				t.Fatalf("DELETE %s/%s answered 200 but the row is still listed", d.prefix, acmeID)
			}
		}
	}
}

// TestDeleteStakeholderRelaysTheValidationList is the arm that MOTIVATES
// bundleEnvelope: refusing to orphan issued equity is a 400 whose body carries an
// `errors` LIST, and zip's {status,code,error} envelope has nowhere to put it. The
// typed op must send the bundle's bytes, list and all.
func TestDeleteStakeholderRelaysTheValidationList(t *testing.T) {
	app := mountApp(t)
	seedTenant(t, app, "acme")
	seedTenant(t, app, "bravo")

	holder := func(org string) string {
		t.Helper()
		_, body := req(t, app, http.MethodGet, "/v1/captable/stakeholders", org, nil)
		var holders []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if err := json.Unmarshal(body, &holders); err != nil {
			t.Fatalf("%s: decode stakeholders: %v (%s)", org, err, body)
		}
		for _, h := range holders {
			if h.Email == "ada@example.com" { // the founder: holds shares AND options
				return h.ID
			}
		}
		t.Fatalf("%s: the founder is missing: %s", org, body)
		return ""
	}

	wantStatus, want := bundleBytes(t, "bravo", "stakeholders.delete", map[string]string{"id": holder("bravo")})
	if wantStatus != http.StatusBadRequest {
		t.Fatalf("bundle refusal want 400, got %d (%s)", wantStatus, want)
	}
	gotStatus, got, ct := probe(t, app, http.MethodDelete, "/v1/captable/stakeholders/"+holder("acme"), "acme", nil)
	if gotStatus != http.StatusBadRequest {
		t.Fatalf("DELETE an encumbered holder want 400, got %d (%s)", gotStatus, got)
	}
	if string(got) != string(want) {
		t.Fatalf("the typed 400 is not the bundle's\n typed:  %s\n bundle: %s", got, want)
	}
	if ct != bundleJSON {
		t.Fatalf("the typed 400 moved Content-Type: %q, want %q", ct, bundleJSON)
	}

	// The list is the whole point: a client renders it, so it must survive.
	var env struct {
		Success bool     `json:"success"`
		Message string   `json:"message"`
		Errors  []string `json:"errors"`
	}
	if err := json.Unmarshal(got, &env); err != nil {
		t.Fatalf("decode the refusal: %v (%s)", err, got)
	}
	if env.Success || len(env.Errors) != 1 {
		t.Fatalf("the bundle's validation list did not survive typing: %s", got)
	}

	// And the holder is still there — a refused delete removes nothing.
	for _, id := range ids(t, app, "acme", "/v1/captable/stakeholders", false) {
		if id == holder("acme") {
			return
		}
	}
	t.Fatal("a refused delete removed the holder anyway")
}

// TestBodylessOpsAreOrgScoped proves the six ops typed in this pass gate exactly
// like the relays they replaced: no validated principal is 403 with the SAME body a
// still-untyped captable route sends, and one tenant's id never resolves in
// another's cap table.
func TestBodylessOpsAreOrgScoped(t *testing.T) {
	app := mountApp(t)
	seedTenant(t, app, "acme")

	// The reference refusal comes from a route that is STILL untyped, so this
	// measures the typed ops against the untyped plane rather than against itself.
	wantCode, wantBody := req(t, app, http.MethodPut, "/v1/captable/company", "", map[string]any{"name": "x"})
	if wantCode != http.StatusForbidden {
		t.Fatalf("untyped relay with no principal want 403, got %d (%s)", wantCode, wantBody)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/captable/rounds/any"},
		{http.MethodDelete, "/v1/captable/stakeholders/any"},
		{http.MethodDelete, "/v1/captable/shares/any"},
		{http.MethodDelete, "/v1/captable/options/any"},
		{http.MethodDelete, "/v1/captable/safes/any"},
		{http.MethodDelete, "/v1/captable/convertibles/any"},
	} {
		code, body := req(t, app, tc.method, tc.path, "", nil)
		if code != http.StatusForbidden {
			t.Fatalf("%s %s with no principal want 403, got %d (%s)", tc.method, tc.path, code, body)
		}
		if string(body) != string(wantBody) {
			t.Fatalf("%s %s refusal moved\n typed:  %s\n untyped: %s", tc.method, tc.path, body, wantBody)
		}
	}

	// acme's round id resolves for acme and is NOT FOUND for globex — the same
	// answer a nonexistent id gives, so an id is no oracle for another tenant.
	rounds := ids(t, app, "acme", "/v1/captable/rounds", true)
	if len(rounds) == 0 {
		t.Fatal("the seed wrote no rounds")
	}
	if code, body := req(t, app, http.MethodGet, "/v1/captable/rounds/"+rounds[0], "acme", nil); code != http.StatusOK {
		t.Fatalf("acme reading its own round: %d (%s)", code, body)
	}
	crossCode, crossBody := req(t, app, http.MethodGet, "/v1/captable/rounds/"+rounds[0], "globex", nil)
	missCode, missBody := req(t, app, http.MethodGet, "/v1/captable/rounds/nope", "globex", nil)
	if crossCode != http.StatusNotFound {
		t.Fatalf("globex reading acme's round want 404, got %d (%s)", crossCode, crossBody)
	}
	if string(crossBody) != string(missBody) || missCode != crossCode {
		t.Fatalf("a cross-tenant id is distinguishable from a missing one\n cross: %d %s\n miss:  %d %s",
			crossCode, crossBody, missCode, missBody)
	}

	// And globex deleting acme's share is a 404 too, not a cross-tenant write.
	shares := ids(t, app, "acme", "/v1/captable/shares", true)
	if len(shares) == 0 {
		t.Fatal("the seed wrote no shares")
	}
	if code, body := req(t, app, http.MethodDelete, "/v1/captable/shares/"+shares[0], "globex", nil); code != http.StatusNotFound {
		t.Fatalf("globex deleting acme's share want 404, got %d (%s)", code, body)
	}
	if slices.Contains(ids(t, app, "acme", "/v1/captable/shares", true), shares[0]) {
		return
	}
	t.Fatal("globex's cross-tenant DELETE removed acme's share")
}
