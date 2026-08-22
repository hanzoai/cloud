package principal_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// OrgsOf is the decision itself, over the two values it turns on — the same
// split Org/OrgOf already makes, so the plane reads it the way HTTP does.
func TestOrgsOf(t *testing.T) {
	for _, tc := range []struct {
		name, user, set string
		want            []string
	}{
		{
			name: "the live operator set, in the order IAM writes it",
			user: "u", set: "hanzo,admin,lux,pars,zoo",
			want: []string{"hanzo", "admin", "lux", "pars", "zoo"},
		},
		{
			// The same fact Org fails closed on: no validated principal means the
			// header that rode along is untrusted, whatever it says.
			name: "no validated user carries no membership, however loud the header",
			user: "", set: "victim,another-victim",
			want: nil,
		},
		{
			name: "a machine carries none",
			user: "u", set: "",
			want: nil,
		},
		{
			// Both shapes a consumer would otherwise have to defend against.
			name: "empties and repeats are dropped",
			user: "u", set: "hanzo, ,hanzo,,lux",
			want: []string{"hanzo", "lux"},
		},
		{
			name: "surrounding whitespace is not part of a slug",
			user: "u", set: " hanzo , lux ",
			want: []string{"hanzo", "lux"},
		},
		{
			// The same bound Org enforces, for the same reason: an org name is a
			// directory and a key, and an unbounded one is neither.
			name: "an over-long slug is not an org",
			user: "u", set: "hanzo," + strings.Repeat("x", principal.MaxOrgLen+1),
			want: []string{"hanzo"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := principal.OrgsOf(tc.user, tc.set)
			if len(got) != len(tc.want) {
				t.Fatalf("OrgsOf(%q, %q) = %v; want %v", tc.user, tc.set, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("OrgsOf(%q, %q) = %v; want %v", tc.user, tc.set, got, tc.want)
				}
			}
		})
	}
}

// Orgs reads the request; WithOrgs parks it; OrgsFrom reads it back. The three
// have to agree or a typed op sees a different membership set than the handler
// beside it.
func TestOrgsRoundTripsThroughTheContext(t *testing.T) {
	var seen []string
	app := zip.New(zip.Config{})
	app.Get("/probe", func(c *zip.Ctx) error {
		ctx := principal.WithOrgs(c.Context(), c)
		seen = principal.OrgsFrom(ctx)
		// The direct read and the parked one are the same value, or the two
		// doors onto one fact have drifted.
		if direct := principal.Orgs(c); len(direct) != len(seen) {
			t.Errorf("Orgs=%v but OrgsFrom=%v", direct, seen)
		}
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-User-Id", "u")
	req.Header.Set("X-User-Orgs", "hanzo,lux")
	if _, err := app.Test(req); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if len(seen) != 2 || seen[0] != "hanzo" || seen[1] != "lux" {
		t.Fatalf("OrgsFrom = %v; want [hanzo lux]", seen)
	}
}

// Off the HTTP path there is no membership set, and the honest answer is none —
// never the caller's single org promoted into a set nobody signed.
func TestOrgsFromIsEmptyOffTheRequestPath(t *testing.T) {
	if got := principal.OrgsFrom(context.Background()); got != nil {
		t.Fatalf("OrgsFrom(background) = %v; want none", got)
	}
}

// An unvalidated caller parks nothing, so a typed op behind the bridge reads no
// set at all rather than the one the client sent.
func TestWithOrgsParksNothingForAnUnvalidatedCaller(t *testing.T) {
	var seen []string
	app := zip.New(zip.Config{})
	app.Get("/probe", func(c *zip.Ctx) error {
		seen = principal.OrgsFrom(principal.WithOrgs(c.Context(), c))
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-User-Orgs", "victim") // no X-User-Id: nothing validated it
	if _, err := app.Test(req); err != nil {
		t.Fatalf("Test: %v", err)
	}
	if seen != nil {
		t.Fatalf("parked %v for an unvalidated caller; want none", seen)
	}
}
