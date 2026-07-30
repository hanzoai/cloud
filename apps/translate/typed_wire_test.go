package translate

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file makes the translate surface's typed partition a GATE instead of a
// paragraph. "2 of 3" is prose, and prose cannot fail: a route added tomorrow as a
// raw func(*zip.Ctx) error would leave the claim standing and the route invisible
// to every projection — no schema, no description, no MCP tool, no CLI command, no
// SDK method.

// untypedByDesign is the CLOSED list of translate operations that are NOT typed
// ops, each with the wire fact that keeps it raw. The address is written the way
// the DOCUMENT writes it, which is the identity every projection keys on.
var untypedByDesign = map[string]string{
	// The translate door itself. Its refusal is a DOMAIN body, and a typed op
	// cannot write one — the apps/ml and apps/company class, re-measured against
	// the PINNED zip (v1.18.11) rather than inherited as prose.
	//
	// A bulk-tier gate or engine refusal answers cloud.DenyResource, the fleet-wide
	// NESTED {"error":{"code","message"}} at 402/503 that the console routes to a
	// top-up prompt. A typed op's ONLY refusal is a returned error, which zip's
	// errorHandler renders as the FLAT {"status","code","error"}; writing the nested
	// body from inside the op does not escape it either, because a nil Out makes zip
	// stamp cmp.Or(op.Status, 204) over the 402. One Out and one declared status per
	// op — the multi-status gap (task #78), not an oversight. The two call sites are
	// translate.go's `s.Bill.Gate(...) -> cloud.DenyResource` and its
	// ErrInsufficientBalance / ErrSpendCapExceeded branch; cloud.DenyResource
	// (deny.go) is where the nested shape is written.
	"POST /v1/translate": "a bulk-tier spend denial answers 402/503 carrying the fleet's NESTED " +
		"{\"error\":{\"code\",\"message\"}} domain body (cloud.DenyResource); a typed op's only refusal is a " +
		"returned error, which zip renders as the flat HTTPError with nowhere to put it.",
}

// translateOps reads BOTH projections of the live router at their one shared
// address form: what the document says is served, and which of those carry a typed
// registry entry. EVERY served operation counts, so a route mounted at an address
// nobody expected is caught rather than filtered out.
func translateOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mount(t, &model{render: upper})
	doc, err := openapi.Spec(app, openapi.Info{Title: "translate", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		typed[key] = op.Description
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a translate operation is neither a typed
// op nor named above — so the next route added here is typed by default, and
// dropping one out of the registry takes a deliberate edit carrying a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := translateOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it, or add it to untypedByDesign with the reason typing it would move the wire.",
			strings.Join(untyped, ", "))
	}
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which translate no longer serves", key)
		}
		if _, ok := typed[key]; ok {
			t.Errorf("untypedByDesign names %q, which IS a typed op — delete the entry", key)
		}
	}
	// The two ledgers must SUM to the served surface: neither may quietly shrink.
	if got, want := len(typed)+len(untypedByDesign), len(served); got != want {
		t.Errorf("typed(%d) + named(%d) = %d, served = %d — the ledgers must partition the surface",
			len(typed), len(untypedByDesign), got, want)
	}
	// The MEASURED partition, so the prose cannot drift from the binary.
	if len(served) != 3 || len(typed) != 2 {
		t.Errorf("served = %d (want 3), typed = %d (want 2)", len(served), len(typed))
	}
}

// TestEveryTypedOpIsDescribed proves the lifted prose reached the binary. That
// prose IS the product surface: it becomes the OpenAPI description AND the MCP tool
// description a model reads to pick the tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := translateOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed translate ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/translate/...", key)
		}
	}
}

// TestMemoryLimitReadsLikeTheUntypedOne pins the one query parameter whose typing
// could have moved a wire. The raw handler read ?limit with strconv.Atoi and fell
// back to the default on ANY error; zip's setScalar leaves an int field at zero
// when it cannot parse, so an unparseable, a zero and a negative limit must all
// still be the server default rather than a page of zero rows.
func TestMemoryLimitReadsLikeTheUntypedOne(t *testing.T) {
	app := mount(t, &model{render: upper})
	// Two entries, so a limit that came through as 0 would return an EMPTY page and
	// a limit that fell back to the default returns both.
	for _, src := range []string{"one", "two"} {
		if code, body := call(t, app, "PUT", "/v1/translate/memory", "acme",
			map[string]any{"source": src, "target": "es", "text": "x", "state": "approved"}); code != 200 {
			t.Fatalf("seed %q: %d (%s)", src, code, body)
		}
	}
	for _, q := range []string{"", "?limit=abc", "?limit=0", "?limit=-5", "?limit="} {
		code, body := call(t, app, "GET", "/v1/translate/memory"+q, "acme", nil)
		if code != 200 {
			t.Fatalf("GET %s: %d (%s)", q, code, body)
		}
		var page MemoryPage
		if err := json.Unmarshal(body, &page); err != nil {
			t.Fatalf("decode %s: %v (%s)", q, err, body)
		}
		if len(page.Data) != 2 {
			t.Fatalf("GET %s returned %d rows, want 2 — a non-positive or unparseable limit is the "+
				"server default, exactly as strconv.Atoi's error branch was", q, len(page.Data))
		}
	}
	// And a real limit still narrows.
	code, body := call(t, app, "GET", "/v1/translate/memory?limit=1", "acme", nil)
	if code != 200 {
		t.Fatalf("GET ?limit=1: %d (%s)", code, body)
	}
	var page MemoryPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(page.Data) != 1 {
		t.Fatalf("?limit=1 returned %d rows, want 1", len(page.Data))
	}
}

// TestReviewIsAttributedToTheValidatedUser pins the fact that made `reviewer` an
// entry in cloud's allowedRequestUses rather than a field on ReviewRequest: the
// actor is the VALIDATED user id, never anything the caller sends. A body claiming
// someone else wrote the review must not be able to sign their name to it.
func TestReviewIsAttributedToTheValidatedUser(t *testing.T) {
	app := mount(t, &model{render: upper})
	code, body := call(t, app, "PUT", "/v1/translate/memory", "acme", map[string]any{
		"source": "Hello", "target": "es", "text": "Hola", "state": "approved",
		"actor": "someone-else",
	})
	if code != 200 {
		t.Fatalf("review: %d (%s)", code, body)
	}
	var e MemoryEntry
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if e.Actor != "u-acme" {
		t.Fatalf("actor = %q, want the validated user id \"u-acme\" — a review is attributed to the "+
			"principal, never to a body field", e.Actor)
	}
}
