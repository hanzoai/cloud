package pricing

import (
	"bytes"
	"context"
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
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
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
			if !bytes.Equal(got, wantBody) {
				t.Errorf("GET %s (%s): body moved.\n typed: %s\nbundle: %s", path, id.name, got, wantBody)
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
	if err := Mount(app, cloud.Deps{Brand: "hanzo", DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
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
		if _, ok := sectionRoutes[path]; !ok {
			t.Errorf("mountSections declares %s, which sectionRoutes does not check — "+
				"the byte-identity proof does not cover it", path)
		}
	}
}
