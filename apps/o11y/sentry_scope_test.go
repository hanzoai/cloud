package o11y

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// The Sentry/o11y product surface. Every path here is a TENANT read: an org's own
// errors, issues, logs and traces.
var productPaths = []string{
	"/v1/o11y/sentinel/issues",
	"/v1/o11y/sentinel/projects",
	"/v1/o11y/sentinel/logs",
	"/v1/o11y/sentinel/traces",
	"/v1/o11y/sentinel/stats",
	"/v1/o11y/errortracking/issues",
}

// seen records what the runtime behind the gate actually received.
type seen struct {
	served bool
	org    string
	query  url.Values
}

func gated(t *testing.T, got *seen) http.Handler {
	t.Helper()
	return gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.served = true
		got.org = r.Header.Get("X-Org-Id")
		got.query = r.URL.Query()
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
}

// member builds the request SanitizeIdentity mints for an ordinary org member:
// a validated user id, the org pinned from the principal's `owner`, and NO admin
// bit (X-User-IsAdmin is never restored from client input).
func member(org, path string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+path, nil)
	r.Header.Set("X-User-Id", "z")
	r.Header.Set("X-Org-Id", org)
	return r
}

// THE PRODUCT IS ORG-SCOPED, NOT ADMIN-ONLY.
//
// An ordinary member of an ordinary org reads their own org's errors. This is the
// regression this file exists for: gating the product on platform sudo makes it
// unusable by every customer AND, for the one identity that can reach it, shows
// every org's errors instead of their own.
func TestProductServesAnOrdinaryOrgMember(t *testing.T) {
	for _, p := range productPaths {
		var got seen
		rec := httptest.NewRecorder()
		gated(t, &got).ServeHTTP(rec, member("maxpower", p))

		if rec.Code != http.StatusOK || !got.served {
			t.Errorf("%s: got %d served=%v, want 200 served=true (a member reads their own org)", p, rec.Code, got.served)
			continue
		}
		if got.org != "maxpower" {
			t.Errorf("%s: runtime saw org %q, want maxpower (the member's own org)", p, got.org)
		}
	}
}

// A MEMBER OF ORG A CANNOT READ ORG B.
//
// The org the runtime scopes to is the SERVER-MINTED one, and nothing a caller
// writes changes it. SanitizeIdentity deletes every client X-Org-*/X-User-*
// header at ingress and re-mints X-Org-Id from the validated principal's own
// claim; the runtime reads its tenant from that alone. So a request that names
// another tenant in the query is served — scoped to the CALLER'S org, which is
// the isolation. Asserted on every product path, with the selector spellings a
// deleted denylist used to chase.
func TestMemberCannotReadAnotherOrg(t *testing.T) {
	for _, p := range productPaths {
		for _, sel := range []string{
			"org=victim", "orgId=victim", "org_id=victim", "orgID=victim",
			"orgSlug=victim", "tenant=victim", "allOrgs=true", "all_orgs=1",
			// The spellings that denylist missed, pinned so the point is not
			// "these eight are handled" but "no query key decides the tenant".
			"Org=victim", "ORG=victim", "organization=victim", "owner=victim",
			// The semicolon form that BYPASSED it outright: url.ParseQuery skips
			// a pair containing ';', so q.Has("org") was false and the raw query
			// went through untouched.
			"org=victim;x=1",
		} {
			var got seen
			rec := httptest.NewRecorder()
			gated(t, &got).ServeHTTP(rec, member("orga", p+"?"+sel))

			if rec.Code != http.StatusOK || !got.served {
				t.Errorf("%s?%s: got %d, want 200 (own-org read still works)", p, sel, rec.Code)
				continue
			}
			if got.org != "orga" {
				t.Errorf("%s?%s: runtime saw org %q, want orga — the tenant must come from the minted pin, never the query", p, sel, got.org)
			}
		}
	}
}

// The narrowings that stay: ?project= / ?product= scope WITHIN the caller's own
// org, so the gate must forward them untouched — and must not rewrite the query
// at all. The deleted denylist re-encoded the whole string whenever it matched,
// dropping pairs Go rejects and turning %20 into + inside a caller's own ?query=.
func TestMemberKeepsWithinOrgNarrowings(t *testing.T) {
	var got seen
	const raw = "project=p1&product=kms&query=a%20b"
	rec := httptest.NewRecorder()
	gated(t, &got).ServeHTTP(rec, member("orga", "/v1/o11y/sentinel/traces/abc?"+raw))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got.org != "orga" {
		t.Errorf("runtime saw org %q, want orga", got.org)
	}
	if got.query.Get("project") != "p1" || got.query.Get("product") != "kms" {
		t.Errorf("within-org narrowings lost: %v", got.query)
	}
	if got.query.Get("query") != "a b" {
		t.Errorf("caller's own ?query= was mangled: %q", got.query.Get("query"))
	}
}

// PLATFORM SUDO REACHES THE PRODUCT WITHOUT AN ORG.
//
// The admin console reads before an org is selected, so a SuperAdmin passes the
// org term. That buys REACH, not data: the runtime still scopes from X-Org-Id.
func TestSuperAdminReachesTheProductOrgless(t *testing.T) {
	var got seen
	r := member("", "/v1/o11y/sentinel/issues")
	r.Header.Set("X-User-IsAdmin", "true")

	rec := httptest.NewRecorder()
	gated(t, &got).ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || !got.served {
		t.Fatalf("got %d served=%v, want 200 served=true (sudo reaches the product org-less)", rec.Code, got.served)
	}
}

// X-User-IsAdmin is a header, and this client is a net/http handler — so pin that
// only the exact minted value counts. Anything else is a member, not sudo.
func TestOnlyTheMintedAdminBitCounts(t *testing.T) {
	for _, v := range []string{"", "false", "TRUE", "1", "yes", " true"} {
		var got seen
		// Org-LESS, so only a true sudo bit could get through the org term.
		r := member("", "/v1/o11y/sentinel/issues")
		if v != "" {
			r.Header.Set("X-User-IsAdmin", v)
		}
		rec := httptest.NewRecorder()
		gated(t, &got).ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden || got.served {
			t.Errorf("X-User-IsAdmin=%q was treated as platform sudo (got %d served=%v)", v, rec.Code, got.served)
		}
	}
}

// A validated principal with NO org fails CLOSED rather than reaching the runtime
// unscoped — an unscoped tenant read is the cross-tenant read by another name.
func TestOrglessMemberIsRefused(t *testing.T) {
	for _, org := range []string{"", "   "} {
		var got seen
		r := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/o11y/sentinel/issues", nil)
		r.Header.Set("X-User-Id", "z")
		if org != "" {
			r.Header.Set("X-Org-Id", org)
		}
		rec := httptest.NewRecorder()
		gated(t, &got).ServeHTTP(rec, r)

		if rec.Code != http.StatusForbidden || got.served {
			t.Errorf("org=%q: got %d served=%v, want 403 served=false", org, rec.Code, got.served)
		}
	}
}

// The ingest face carries neither principal nor org by design (a Sentry SDK
// presents a DSN key), so the scope decision must not touch it.
func TestIngestIsUntouchedByTheScopeDecision(t *testing.T) {
	for _, p := range []string{
		"/v1/event/8f14e45f-ceea-467a-9575-6f6b6f6b6f6b/envelope/",
		"/v1/event/8f14e45f-ceea-467a-9575-6f6b6f6b6f6b/store/",
		"/v1/o11y/api/8f14e45f/envelope/",
	} {
		var got seen
		r := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai"+p+"?org=whatever", nil)
		rec := httptest.NewRecorder()
		gated(t, &got).ServeHTTP(rec, r)

		if rec.Code != http.StatusOK || !got.served {
			t.Errorf("%s: got %d served=%v, want 200 served=true (DSN-authenticated ingest)", p, rec.Code, got.served)
		}
		if !got.query.Has("org") {
			t.Errorf("%s: ingest query was rewritten; it must pass through verbatim", p)
		}
	}
}

func httptestServe(h http.Handler, r *http.Request) {
	h.ServeHTTP(httptest.NewRecorder(), r)
}
