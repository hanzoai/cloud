package ci

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// probeViewer runs viewer() behind a request carrying the given headers.
func probeViewer(t *testing.T, headers map[string]string) (string, bool) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	var got string
	var ok bool
	app.Get("/probe", func(c *zip.Ctx) error {
		got, ok = viewer(c)
		return c.JSON(http.StatusOK, map[string]any{})
	})
	req := httptest.NewRequest("GET", "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return got, ok
}

// The org this request is answered as is decided HERE and sent to the mounted
// surface as X-Org-Id. hanzo.ai/ci trusts that header completely — it is the
// whole of its visibility rule — so a wrong answer here is not a wrong page, it
// is one org reading another's repo names, branches and commits.
//
// The header the CALLER sent must never reach it. That is the property these
// tests exist for: the value is derived from the attested principal, and a
// caller-supplied X-Org-Id is not an input to that derivation.
func TestViewerIsDerivedFromTheAttestedCaller(t *testing.T) {
	// A SuperAdmin is answered as the reserved admin org, which is what the
	// surface reads as "sees everything".
	if org, ok := probeViewer(t, map[string]string{"X-User-IsAdmin": "true"}); !ok || org != "admin" {
		t.Fatalf("SuperAdmin viewer = %q ok=%v, want \"admin\"", org, ok)
	}

	// A validated org member is answered as its own org.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "acme", "X-User-Id": "u"}); !ok || org != "acme" {
		t.Fatalf("member viewer = %q ok=%v, want \"acme\"", org, ok)
	}

	// SuperAdmin WINS over a supplied org: an admin who also carries X-Org-Id is
	// still answered as admin, never narrowed to whatever the header claimed.
	if org, ok := probeViewer(t, map[string]string{"X-User-IsAdmin": "true", "X-Org-Id": "acme", "X-User-Id": "u"}); !ok || org != "admin" {
		t.Fatalf("admin+org viewer = %q ok=%v, want \"admin\"", org, ok)
	}
}

// No attested caller, no answer. The surface treats a missing X-Org-Id as fatal
// because absence means the request did not come through a gate; this is that
// gate, and it must refuse rather than pass an empty org that the surface would
// then have to interpret.
func TestAnUnattestedCallerIsRefused(t *testing.T) {
	if org, ok := probeViewer(t, nil); ok {
		t.Fatalf("a request with no principal resolved to %q; it must be refused", org)
	}
	// An org id with no user behind it is not a principal either.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "acme"}); ok {
		t.Fatalf("an org header with no validated user resolved to %q", org)
	}
}

// The org is keyed through the SAME injective namespace.Sanitize the rest of the
// fleet uses, so two distinct owners can never collide onto one tenant, and an
// identifier that cannot be sanitized fails closed instead of becoming a
// fabricated one.
func TestTheOrgIsSanitizedTheSameWayEverywhereElse(t *testing.T) {
	for _, raw := range []string{"acme", "ACME", "team1"} {
		org, ok := probeViewer(t, map[string]string{"X-Org-Id": raw, "X-User-Id": "u"})
		want := namespace.Sanitize(raw)
		if want == "" {
			t.Fatalf("test input %q sanitized to empty", raw)
		}
		if !ok || org != want {
			t.Errorf("viewer(%q) = %q ok=%v, want %q", raw, org, ok, want)
		}
	}
	// An identifier carrying an unsafe rune is refused, never passed through.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "bad org", "X-User-Id": "u"}); ok {
		t.Fatalf("an unsanitizable org resolved to %q; it must fail closed", org)
	}
}
