package projects

// typed_wire_test.go pins the four wire facts the typed conversion could have
// moved silently. Each is a place where zip's typed plane is MORE tolerant, or
// differently shaped, than the raw handler it replaced — so each is asserted on
// the bytes, not on a Go value.

import (
	"bytes"
	"encoding/json"
	"github.com/hanzoai/cloud/openapi"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// TestBodylessWriteStillRefuses: zip's typed decode SKIPS an empty body and
// leaves the In at its zero value, so a naive conversion would have turned a
// bodyless PATCH into "200, nothing changed" — the route has always answered
// 400. requireBody replays c.Bind's own refusal at the point in the sequence the
// raw handler reached it, so the answer is unchanged.
func TestBodylessWriteStillRefuses(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/project", "acme", map[string]any{"name": "Landing"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	// nil body ⇒ no request body at all, which is exactly what c.Bind refuses.
	if code, body := do(t, app, http.MethodPatch, "/v1/project/"+p.Slug, "acme", nil); code != http.StatusBadRequest {
		t.Fatalf("bodyless patch want 400, got %d (%s)", code, body)
	}
}

// TestExplicitNullLeavesAFieldAlone: every optional field on the update body is
// a POINTER so "absent" is distinguishable from "set to empty". encoding/json
// leaves a pointer NIL for an explicit `null` too, so `null` means "leave it" —
// the same answer the raw handler gave. Pinned because the fix for a route that
// must tell those apart is a non-pointer carrier, and swapping one in here would
// change what `{"name":null}` does without any signature moving.
func TestExplicitNullLeavesAFieldAlone(t *testing.T) {
	app := mountApp(t)
	_, body := do(t, app, http.MethodPost, "/v1/project", "acme",
		map[string]any{"name": "Landing", "description": "the original"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	code, ub := do(t, app, http.MethodPatch, "/v1/project/"+p.Slug, "acme",
		map[string]any{"name": nil, "description": nil})
	if code != http.StatusOK {
		t.Fatalf("patch want 200, got %d (%s)", code, ub)
	}
	var got projectsProject
	if err := json.Unmarshal(ub, &got); err != nil {
		t.Fatalf("json: %v (%s)", err, ub)
	}
	if got.Name != "Landing" || got.Description != "the original" {
		t.Fatalf("explicit null must leave a field alone, got name=%q description=%q", got.Name, got.Description)
	}
}

// TestDomainAnswersKeepTheirEmptyLists: the bind answer carries `bound` and the
// list answer carries `claims`, each ALWAYS present — an empty one is `[]`, not
// absent. They are two Out types for that reason: folding them into one struct
// with omitempty would drop an empty list from the wire, which is a different
// answer to "how many hosts does this site hold", not a tidier one.
func TestDomainAnswersKeepTheirEmptyLists(t *testing.T) {
	app := mountApp(t)
	_, body := do(t, app, http.MethodPost, "/v1/project", "acme", map[string]any{"name": "Landing"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	code, lb := do(t, app, http.MethodGet, "/v1/project/"+p.Slug+"/domains", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list domains want 200, got %d (%s)", code, lb)
	}
	if !bytes.Contains(lb, []byte(`"claims":[]`)) {
		t.Fatalf("an empty claims list must render as [], got %s", lb)
	}
	// A bind whose every entry cleans to nothing binds no host — and still says so
	// with an empty list rather than by omitting the field.
	code, bb := do(t, app, http.MethodPost, "/v1/project/"+p.Slug+"/domains", "acme",
		map[string]any{"domains": []string{"   "}})
	if code != http.StatusOK {
		t.Fatalf("bind want 200, got %d (%s)", code, bb)
	}
	if !bytes.Contains(bb, []byte(`"bound":[]`)) {
		t.Fatalf("an empty bound list must render as [], got %s", bb)
	}
}

// TestDeleteStays204WithNoBody: a typed op that returns a non-nil Out would have
// zip write a JSON body under the declared status, so the two deletes on this
// surface return a nil Out under zip.WithStatus(204) — the 204-with-no-bytes
// they have always answered.
func TestDeleteStays204WithNoBody(t *testing.T) {
	app := mountApp(t)
	_, body := do(t, app, http.MethodPost, "/v1/project", "acme", map[string]any{"name": "Landing"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	// Releasing a host we do not hold is the idempotent 204, so it needs no bind.
	if code, rb := do(t, app, http.MethodDelete, "/v1/project/"+p.Slug+"/domains/never.example", "acme", nil); code != http.StatusNoContent || len(rb) != 0 {
		t.Fatalf("release domain want 204 with no body, got %d (%q)", code, rb)
	}
	if code, db := do(t, app, http.MethodDelete, "/v1/project/"+p.Slug, "acme", nil); code != http.StatusNoContent || len(db) != 0 {
		t.Fatalf("delete project want 204 with no body, got %d (%q)", code, db)
	}
}

// untypedByDesign is the CLOSED list of addresses this app serves raw, each with
// the wire fact that keeps it there.
//
// The package already MEASURES each of these — the tests above drive the wires
// themselves, which is the stronger form and stays. What this list adds is the
// SUM: those tests go red when a refused route's WIRE changes, and nothing went
// red when a route was ADDED raw beside them. openapi/untyped.json catches that
// fleet-wide by count, and a count is flat when one route converts and another
// arrives raw in the same change, which is exactly what this catches.
var untypedByDesign = map[string]string{
	"GET /v1/project/tags": "the PUBLIC browser-tag config a hosted <script> fetches. It sets three " +
		"response headers the caller depends on — Access-Control-Allow-Origin: * (it is read " +
		"cross-origin from every customer site), Cache-Control on a hot path, and an explicit " +
		"charset — and it resolves the site from the publishable KEY or the request HOST, which are " +
		"request-side facts a typed op holds no request to read. It already declares its shape and " +
		"prose through openapi.Register/Describe, so what staying raw costs is the tool, not the " +
		"schema.",
	"GET /v1/project/{slug}/shot": "answers the site's screenshot BYTES; a typed op's only response " +
		"path is c.JSON(out).",
	"POST /v1/project/{slug}/deploy": "takes the built artifact as its raw body — the bytes are the " +
		"deploy — so a JSON In would refuse every real publish.",
}

// TestEveryRouteIsTypedOrNamed requires the two ledgers to SUM to the served
// surface, so a route added raw goes red without anyone remembering this file, and
// a reason that stops being true goes red the moment its op is written.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "projects", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/project") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/project") {
			typed[key] = true
		}
	}

	var untyped []string
	for key := range served {
		if !typed[key] {
			if _, named := untypedByDesign[key]; !named {
				untyped = append(untyped, key)
			}
		}
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("served but neither typed nor named: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK method. "+
			"Convert it, or name it in untypedByDesign with the wire fact that keeps it raw.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which this app no longer serves", key)
		}
		if typed[key] {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("the two ledgers must sum to the served surface: typed %d + named %d = %d, served %d",
			len(typed), len(untypedByDesign), got, want)
	}
}
