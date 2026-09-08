package pricing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// This file is the PROOF that typing the fifteen fixed-section routes did not
// move their wire, and it is a proof rather than a claim because it re-derives
// the pre-typing answer on every run instead of trusting a recorded golden.
//
// What the raw handler wrote was exactly rawDispatch's bytes under
// rawDispatch's status (dispatch → passthrough → c.Bytes). So the assertion
// below is the whole contract: for every section address, the LIVE typed route's
// status and body must equal what rawDispatch hands back for the same section.
// If a future Out struct reorders a key, renames a field, drops one the catalog
// added, or turns a 503 into something else, this fails.
//
// It also pins the ONE thing typing DID move, so the move can never happen
// again silently: the response Content-Type. The raw pass-through set a bare
// "application/json"; fiber's typed JSON writer sets
// "application/json; charset=utf-8" — the form every other answer on this
// surface, typed op and zip error alike, already sent.

// sectionRoutes maps each section address to the @hanzo/pricing bundle route it
// serves — the same pairing mountSections declares, written once more here so
// the test names the wire it is checking rather than reaching into the router.
var sectionRoutes = map[string]string{
	"/v1/pricing/compute":         "compute",
	"/v1/pricing/compute/presets": "compute/presets",
	"/v1/pricing/cloud":           "cloud",
	"/v1/pricing/cloud/plans":     "cloud/plans",
	"/v1/pricing/cloud/regions":   "cloud/regions",
	"/v1/pricing/cloud/storage":   "cloud/storage",
	"/v1/pricing/subscriptions":   "subscriptions",
	"/v1/pricing/blockchain":      "blockchain",
	"/v1/pricing/iam":             "iam",
	"/v1/pricing/base":            "base",
	"/v1/pricing/paas":            "paas",
	"/v1/pricing/policy":          "policy",
	"/v1/pricing/tools":           "tools",
	"/v1/pricing/gpu":             "gpu",
	"/v1/pricing/datastore":       "datastore",
	"/v1/pricing/services":        "services",
}

// toolSection is the one section address whose body is deliberately NOT the
// bundle's: its two web rows are repriced from the rate card, because a number
// the platform CHARGES by and also publishes has exactly one home (tariff.go).
// What those rows say is proved in TestTheToolListTakesItsWebRowsFromTheCard;
// everything else about the address — its status, its type and its invariance
// across callers — is still proved by the loop below.
const toolSection = "/v1/pricing/tools"

// assembled are the addresses mountSections declares that are not sections at
// all. They are built in Go from the charged floors rather than read from the
// bundle, so a byte-identity proof against the bundle cannot apply — and naming
// them here is what stops a route escaping the proof by simply not being in
// sectionRoutes.
var assembled = map[string]bool{"/v1/pricing/tariff": true}

// TestSectionsAreByteIdenticalToTheBundle drives every section route on the live
// router and asserts its body is byte-for-byte what the bundle's own answer is.
//
// Four identity shapes, because a section carries no model or provider identity
// and therefore must NOT vary with the caller: anonymous, a member org, a
// SuperAdmin, and the forged X-Org-Id with no validated principal that the
// enablement attack tests pin. A section that answered differently for one of
// them would be gating something it has no business gating.
func TestSectionsAreByteIdenticalToTheBundle(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	fa := app.Fiber()

	ids := []struct {
		name string
		hdr  map[string]string
	}{
		{"anonymous", nil},
		{"member", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}},
		{"superadmin", map[string]string{"X-User-IsAdmin": "true"}},
		{"forged-org", map[string]string{"X-Org-Id": "acme"}},
	}

	for path, route := range sectionRoutes {
		// The bundle's own answer: the bytes and status the raw pass-through wrote.
		wantStatus, wantBody, err := rawDispatch(context.Background(), route, nil)
		if err != nil {
			t.Fatalf("%s: rawDispatch(%q): %v", path, route, err)
		}
		if wantStatus != http.StatusOK {
			t.Fatalf("%s: bundle answered %d for %q — the shipped catalog holds every section",
				path, wantStatus, route)
		}
		// The tool section's answer is compared to the FIRST caller's rather than
		// to the bundle's, which keeps the caller-invariance this loop exists to
		// prove: the rate card carries no model or provider identity either, so a
		// repriced row that varied by caller would be gating money.
		var first []byte
		for _, id := range ids {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			for k, v := range id.hdr {
				req.Header.Set(k, v)
			}
			resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("GET %s (%s): %v", path, id.name, err)
			}
			got, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != wantStatus {
				t.Errorf("GET %s (%s): status %d, bundle says %d", path, id.name, resp.StatusCode, wantStatus)
			}
			switch {
			case path != toolSection:
				if !bytes.Equal(got, wantBody) {
					t.Errorf("GET %s (%s): body moved.\n typed: %s\nbundle: %s", path, id.name, got, wantBody)
				}
			case first == nil:
				first = got
			case !bytes.Equal(got, first):
				t.Errorf("GET %s (%s): the repriced tool list varies with the caller.\n  this: %s\n first: %s",
					path, id.name, got, first)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
				t.Errorf("GET %s (%s): Content-Type %q, want %q", path, id.name, ct,
					"application/json; charset=utf-8")
			}
		}
	}
}

// TestSectionsDegradeWithTheBundlesStatus pins the ONE branch the shipped
// catalog cannot reach: a section the pricing data does not hold. The bundle
// answers 503 there (goja/bundle.js), and a typed op must still answer 503 —
// the status comes from the bundle, not from a declaration, which is what
// dispatchErr is for.
//
// It also pins the one thing typing DID move on this branch, so it can never
// move again unnoticed: the 503 body was the bundle's own {"error":…} and is now
// zip's error envelope, {"status":503,"error":…} — same status, same message, the
// added field being the shape this surface's every other error already had (the
// 403s in admin.go and enablement.go, the 404 from GET /v1/pricing/model/:name).
func TestSectionsDegradeWithTheBundlesStatus(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	// Strip the two sections that have a 503 arm. The next Mount rebuilds the
	// host from the embed, so this cannot leak into another test.
	host.SetGlobal("__PRICING_DATA__", map[string]any{"updated": "2026-01-01T00:00:00Z"})

	want := map[string]string{
		"/v1/pricing/compute":         "Compute pricing not yet available",
		"/v1/pricing/compute/presets": "Compute presets not yet available",
		"/v1/pricing/cloud":           "Cloud pricing not yet available",
		"/v1/pricing/cloud/plans":     "Cloud plans not yet available",
		"/v1/pricing/cloud/regions":   "Cloud regions not yet available",
		"/v1/pricing/cloud/storage":   "Storage pricing not yet available",
	}
	fa := app.Fiber()
	for path, msg := range want {
		resp, err := fa.Test(httptest.NewRequest(http.MethodGet, path, nil), fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("GET %s with the section absent: status %d, want 503 (the bundle's own)", path, resp.StatusCode)
		}
		if !bytes.Contains(body, []byte(msg)) {
			t.Errorf("GET %s: body %s, want the bundle's message %q", path, body, msg)
		}
	}
}

// TestSectionsCoverEverySectionRoute fails if mountSections declares an address
// sectionRoutes does not check, or checks one it no longer declares. Without it
// the byte-identity proof above could silently stop covering a route — which is
// how a golden goes stale.
func TestSectionsCoverEverySectionRoute(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountSections(app, ops{log: luxlog.New("test")})
	declared := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes() {
		if r.Path == "/" {
			// Middleware territory: every app.Use (the composer's bridge,
			// telemetry) rides fiber's "/" route as a handler CHAIN, by
			// design, so it is not a section address and never was one.
			continue
		}
		if r.Method == http.MethodGet {
			declared[r.Path] = true
		}
	}
	for path := range sectionRoutes {
		if !declared[path] {
			t.Errorf("sectionRoutes checks %s, which mountSections no longer declares", path)
		}
	}
	for path := range declared {
		if assembled[path] {
			continue
		}
		if _, ok := sectionRoutes[path]; !ok {
			t.Errorf("mountSections declares %s, which sectionRoutes does not check — "+
				"the byte-identity proof does not cover it", path)
		}
	}
	// The other direction, so the exemption list shrinks rather than outlives its
	// cause: an address that is no longer declared has no business being excused.
	for path := range assembled {
		if !declared[path] {
			t.Errorf("assembled excuses %s from the byte-identity proof, and mountSections "+
				"no longer declares it", path)
		}
	}
}

// TestTheToolListTakesItsWebRowsFromTheCard proves the one deliberate departure
// from the bundle, in both directions.
//
// The bundle has always carried a "Web Search" row at its own price, and the
// platform charges for a web call at the rate card's. Those are ONE fact, and two
// writings of it is how a published price ends up advertising something other
// than what is billed — which is exactly what happened to the first-party models
// this surface overlays (see commerce.go). So the card wins the number, and this
// reads BOTH halves: the web rows come from the card, and every other row is
// still the bundle's own bytes.
//
// MUTATION: point repriceTools at a constant instead of cloud.Card and the first
// half fails; reprice a row the card does not own and the second half fails.
func TestTheToolListTakesItsWebRowsFromTheCard(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	t.Setenv("CLOUD_BRAND", "hanzo")
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })

	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, toolSection, nil),
		fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("GET %s: %v", toolSection, err)
	}
	body, _ := io.ReadAll(resp.Body)
	var served struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(body, &served); err != nil {
		t.Fatalf("decode the served tool list: %v", err)
	}

	// What the card says a web call costs, in the decimal this list carries.
	want := map[string]float64{}
	for _, r := range cloud.Card(context.Background()).Rates {
		switch r.Name {
		case "web_search":
			want["Web Search"] = float64(r.Micros) / 1e6
		case "web_fetch":
			want["Web Fetch"] = float64(r.Micros) / 1e6
		}
	}
	if len(want) != 2 {
		t.Fatalf("the card prices %d web rows, want 2 — web_search and web_fetch", len(want))
	}

	// What the bundle says about every row, so the untouched ones can be checked
	// against it rather than against a golden that goes stale.
	_, raw, err := rawDispatch(context.Background(), "tools", nil)
	if err != nil {
		t.Fatalf("rawDispatch(tools): %v", err)
	}
	var bundle struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("decode the bundle's tool list: %v", err)
	}
	bundled := map[string]map[string]any{}
	for _, row := range bundle.Tools {
		name, _ := row["name"].(string)
		bundled[name] = row
	}

	seen := map[string]bool{}
	for _, row := range served.Tools {
		name, _ := row["name"].(string)
		price, _ := row["price"].(float64)
		if rate, owned := want[name]; owned {
			seen[name] = true
			if price != rate {
				t.Errorf("%s is published at %v and the card charges %v — a rate with two homes",
					name, price, rate)
			}
			continue
		}
		was, held := bundled[name]
		if !held {
			t.Errorf("%q is on the served list and not on the bundle's, and the card does not price it", name)
			continue
		}
		if p, _ := was["price"].(float64); p != price {
			t.Errorf("%s moved from %v to %v, and it is not the card's to reprice", name, p, price)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("the card prices %s and the served list does not carry it", name)
		}
	}
}
