package ci

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
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
func TestTheOrgIsTheKeyEveryOtherSurfaceUses(t *testing.T) {
	for _, raw := range []string{"acme", "ACME", "team1"} {
		org, ok := probeViewer(t, map[string]string{"X-Org-Id": raw, "X-User-Id": "u"})
		if !ok || org != raw {
			t.Errorf("viewer(%q) = %q ok=%v, want %q verbatim", raw, org, ok, raw)
		}
	}
	// A rune that is unsafe in a resource NAME is not unsafe in an isolation KEY,
	// and refusing it here would answer a caller the other surfaces serve. What
	// the org must survive is the forwarding below, which is where its grammar
	// actually matters.
	if org, ok := probeViewer(t, map[string]string{"X-Org-Id": "bad org", "X-User-Id": "u"}); !ok || org != "bad org" {
		t.Fatalf("viewer(\"bad org\") = %q ok=%v; the isolation rule admits it", org, ok)
	}
}

// The org reaches the mounted surface as a query value, so the escape is the
// property that matters — not a fold upstream of it. A raw space would end the
// value and hand hanzo.ai/ci an org it was never asked for.
func TestForwardedOrgIsEscaped(t *testing.T) {
	if got := "org=" + url.QueryEscape("bad org"); got != "org=bad+org" {
		t.Fatalf("forwarded query = %q, want org=bad+org", got)
	}
}

// ONE RULE, TWO READERS. principal.OrgOf is the org-isolation decision itself;
// a capability that re-derives the scope key from the same two headers must
// reach the same answer, or the same caller carries one org here and another
// everywhere else. That divergence is silent: the key still looks like an org,
// so the surface answers 200 with the runs of a tenant that does not exist.
//
// The cases below are the ones where a fold and the canonical rule part company
// — an id that is not already a clean short slug, and one inside the canonical
// length bound but past the fold's.
func TestViewer_AgreesWithCanonicalOrgRule(t *testing.T) {
	for _, org := range []string{
		"acme",                  // clean slug: both rules agree anyway
		"Acme",                  // case: a fold rewrites it, the rule does not
		strings.Repeat("a", 40), // 40 ≤ MaxOrgLen, past the fold's 32
		strings.Repeat("b", principal.MaxOrgLen),
	} {
		t.Run(org[:min(len(org), 12)], func(t *testing.T) {
			h := map[string]string{"X-User-Id": "u-1", "X-Org-Id": org}
			got, ok := probeViewer(t, h)
			want, wantOK := principal.OrgOf("u-1", org)
			if ok != wantOK || got != want {
				t.Fatalf("viewer(%q) = (%q,%v); the canonical rule says (%q,%v)",
					org, got, ok, want, wantOK)
			}
		})
	}
}

// Past the bound the canonical rule refuses. A fold cannot express that refusal:
// it truncates and suffixes, turning an org claim the boundary rejects into a
// well-formed key.
func TestViewer_RefusesOrgPastCanonicalBound(t *testing.T) {
	org := strings.Repeat("c", principal.MaxOrgLen+1)
	if got, ok := probeViewer(t, map[string]string{"X-User-Id": "u-1", "X-Org-Id": org}); ok {
		t.Fatalf("viewer admitted an org past MaxOrgLen as %q", got)
	}
}
