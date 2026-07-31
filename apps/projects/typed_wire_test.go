package projects

// typed_wire_test.go pins the four wire facts the typed conversion could have
// moved silently. Each is a place where zip's typed plane is MORE tolerant, or
// differently shaped, than the raw handler it replaced — so each is asserted on
// the bytes, not on a Go value.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

// TestBodylessWriteStillRefuses: zip's typed decode SKIPS an empty body and
// leaves the In at its zero value, so a naive conversion would have turned a
// bodyless PATCH into "200, nothing changed" — the route has always answered
// 400. requireBody replays c.Bind's own refusal at the point in the sequence the
// raw handler reached it, so the answer is unchanged.
func TestBodylessWriteStillRefuses(t *testing.T) {
	app := mountApp(t)
	code, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Landing"})
	if code != http.StatusCreated {
		t.Fatalf("create want 201, got %d (%s)", code, body)
	}
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	// nil body ⇒ no request body at all, which is exactly what c.Bind refuses.
	if code, body := do(t, app, http.MethodPatch, "/v1/projects/"+p.Slug, "acme", nil); code != http.StatusBadRequest {
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
	_, body := do(t, app, http.MethodPost, "/v1/projects", "acme",
		map[string]any{"name": "Landing", "description": "the original"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	code, ub := do(t, app, http.MethodPatch, "/v1/projects/"+p.Slug, "acme",
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
	_, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Landing"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	code, lb := do(t, app, http.MethodGet, "/v1/projects/"+p.Slug+"/domains", "acme", nil)
	if code != http.StatusOK {
		t.Fatalf("list domains want 200, got %d (%s)", code, lb)
	}
	if !bytes.Contains(lb, []byte(`"claims":[]`)) {
		t.Fatalf("an empty claims list must render as [], got %s", lb)
	}
	// A bind whose every entry cleans to nothing binds no host — and still says so
	// with an empty list rather than by omitting the field.
	code, bb := do(t, app, http.MethodPost, "/v1/projects/"+p.Slug+"/domains", "acme",
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
	_, body := do(t, app, http.MethodPost, "/v1/projects", "acme", map[string]any{"name": "Landing"})
	var p projectsProject
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("json: %v (%s)", err, body)
	}
	// Releasing a host we do not hold is the idempotent 204, so it needs no bind.
	if code, rb := do(t, app, http.MethodDelete, "/v1/projects/"+p.Slug+"/domains/never.example", "acme", nil); code != http.StatusNoContent || len(rb) != 0 {
		t.Fatalf("release domain want 204 with no body, got %d (%q)", code, rb)
	}
	if code, db := do(t, app, http.MethodDelete, "/v1/projects/"+p.Slug, "acme", nil); code != http.StatusNoContent || len(db) != 0 {
		t.Fatalf("delete project want 204 with no body, got %d (%q)", code, db)
	}
}
