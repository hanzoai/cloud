package cloud

// Tests for the org SUB-SCOPE half of the identity trust boundary: X-Project-Id
// is MINTED from the validated `project` claim (never a raw client header), a
// forged client X-Project-Id is ignored and cannot override the claim, a claim
// FOREIGN to the acted-as org is refused (admin org-switch), X-App-Id rides as a
// caller label, and every sub-scope is dropped on the anonymous path — while the
// existing X-Org-Id anti-forgery is untouched. Reuses the JWKS + token helpers
// from middleware_identity_test.go (same package).

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// fakeScopeResolver answers ownership from a static project→org map. mine iff the
// project is owned by org; other iff it is owned by some different org.
type fakeScopeResolver struct{ owner map[string]string }

func (f fakeScopeResolver) ProjectOwnership(_ context.Context, org, id string) (mine, other bool, err error) {
	o, ok := f.owner[id]
	switch {
	case !ok:
		return false, false, nil
	case o == org:
		return true, false, nil
	default:
		return false, true, nil
	}
}

// errScopeResolver always fails — exercises the fail-closed path.
type errScopeResolver struct{}

func (errScopeResolver) ProjectOwnership(context.Context, string, string) (bool, bool, error) {
	return false, false, errors.New("registry down")
}

// withResolvers installs rs as the boundary's resolver set for one test and
// restores the prior set on cleanup (package-global, so isolate carefully).
func withResolvers(t *testing.T, rs ...OrgScopeResolver) {
	t.Helper()
	orgResolverMu.Lock()
	old := orgResolvers
	orgResolvers = append([]OrgScopeResolver(nil), rs...)
	orgResolverMu.Unlock()
	t.Cleanup(func() {
		orgResolverMu.Lock()
		orgResolvers = old
		orgResolverMu.Unlock()
	})
}

// TestProjectIsForeign is the pure decision, independent of HTTP.
func TestProjectIsForeign(t *testing.T) {
	owns := fakeScopeResolver{owner: map[string]string{"site-a": "acme", "secret": "beta"}}
	withResolvers(t, owns)

	cases := []struct {
		name         string
		org, project string
		want         bool
	}{
		{"empty org", "", "site-a", false},
		{"empty project", "acme", "", false},
		{"own project", "acme", "site-a", false},
		{"another org's project", "acme", "secret", true},
		{"unregistered free-form label", "acme", "team-scratch", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := projectIsForeign(context.Background(), tc.org, tc.project); got != tc.want {
				t.Fatalf("projectIsForeign(%q,%q)=%v want %v", tc.org, tc.project, got, tc.want)
			}
		})
	}
}

// TestProjectIsForeign_FailClosed: a registry error with no confirming "mine"
// refuses the claim; a second registry that DOES own it wins (never refused).
func TestProjectIsForeign_FailClosed(t *testing.T) {
	t.Run("error alone → foreign", func(t *testing.T) {
		withResolvers(t, errScopeResolver{})
		if !projectIsForeign(context.Background(), "acme", "anything") {
			t.Fatal("registry error with no owner must fail CLOSED (foreign)")
		}
	})
	t.Run("error + owning registry → not foreign", func(t *testing.T) {
		withResolvers(t, errScopeResolver{}, fakeScopeResolver{owner: map[string]string{"p": "acme"}})
		if projectIsForeign(context.Background(), "acme", "p") {
			t.Fatal("a registry that OWNS the project must win over a peer's error")
		}
	})
	t.Run("no registries → never foreign", func(t *testing.T) {
		withResolvers(t) // empty
		if projectIsForeign(context.Background(), "acme", "whatever") {
			t.Fatal("with no registry, nothing is foreign")
		}
	})
}

// scopeCap records the sub-scope headers a downstream handler observes.
type scopeCap struct {
	org, user, project, app string
	admin                   bool
}

// newScopeApp wires the REAL SanitizeIdentity in front of a probe that records
// the sanitized identity + sub-scopes exactly as a subsystem would read them.
func newScopeApp(t *testing.T, v *identityValidator) (*zip.App, *scopeCap) {
	t.Helper()
	got := &scopeCap{}
	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(c *zip.Ctx) error {
		got.org = c.Org()
		got.user = c.User()
		got.admin = c.IsAdmin()
		got.project = c.Header("X-Project-Id")
		got.app = c.Header("X-App-Id")
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	return app, got
}

func setHdr(kv map[string]string) func(*http.Request) {
	return func(r *http.Request) {
		for k, v := range kv {
			r.Header.Set(k, v)
		}
	}
}

func both(fns ...func(*http.Request)) func(*http.Request) {
	return func(r *http.Request) {
		for _, f := range fns {
			f(r)
		}
	}
}

func TestSanitizeIdentity_SubScopes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, []string{"hanzo-console"}, 0)
	future := time.Now().Add(time.Hour)

	// acme owns "site-a"; beta owns "secret".
	withResolvers(t, fakeScopeResolver{owner: map[string]string{"site-a": "acme", "secret": "beta"}})

	// withProject signs a token for owner carrying a `project` claim — the ONLY
	// source X-Project-Id is ever minted from (a client header is never trusted).
	withProject := func(owner, email string, isAdmin bool, project string) string {
		c := tokenClaims("hanzo-console", owner, email, isAdmin, future)
		c.Project = project
		return signWith(t, key, c)
	}

	t.Run("own project claim is minted", func(t *testing.T) {
		app, got := newScopeApp(t, v)
		probe(t, app, bearer(withProject("acme", "joe@acme.io", false, "site-a")))
		if got.org != "acme" || got.project != "site-a" {
			t.Fatalf("own project claim must bind X-Project-Id: org=%q project=%q", got.org, got.project)
		}
	})

	t.Run("unregistered free-form project claim survives", func(t *testing.T) {
		app, got := newScopeApp(t, v)
		probe(t, app, bearer(withProject("acme", "joe@acme.io", false, "team-scratch")))
		if got.project != "team-scratch" {
			t.Fatalf("unregistered within-org claim must survive, got %q", got.project)
		}
	})

	t.Run("default project claim mints no header", func(t *testing.T) {
		// No project claim ⟹ default ⟹ header absent (minimal-canonical form).
		app, got := newScopeApp(t, v)
		probe(t, app, bearer(withProject("acme", "joe@acme.io", false, "")))
		if got.project != "" {
			t.Fatalf("absent project claim must mint no header, got %q", got.project)
		}
		// The literal "default" is likewise omitted.
		app2, got2 := newScopeApp(t, v)
		probe(t, app2, bearer(withProject("acme", "joe@acme.io", false, "default")))
		if got2.project != "" {
			t.Fatalf("literal default project must mint no header, got %q", got2.project)
		}
	})

	t.Run("forged client X-Project-Id is IGNORED (never a source)", func(t *testing.T) {
		// Token has NO project claim; the client forges X-Project-Id. It must NOT
		// survive — the header binds ONLY the validated claim.
		app, got := newScopeApp(t, v)
		probe(t, app, both(bearer(withProject("acme", "joe@acme.io", false, "")),
			setHdr(map[string]string{"X-Project-Id": "site-a"})))
		if got.project != "" {
			t.Fatalf("forged client X-Project-Id must be ignored, got %q", got.project)
		}
	})

	t.Run("client X-Project-Id cannot override the claim (evade defense)", func(t *testing.T) {
		// Token claims "site-a"; the client tries to relabel to "team-scratch" to
		// evade the site-a cap. The CLAIM wins; the client value is dropped.
		app, got := newScopeApp(t, v)
		probe(t, app, both(bearer(withProject("acme", "joe@acme.io", false, "site-a")),
			setHdr(map[string]string{"X-Project-Id": "team-scratch"})))
		if got.project != "site-a" {
			t.Fatalf("claim must win over a forged client X-Project-Id, got %q", got.project)
		}
	})

	t.Run("app rides as a caller label on the validated path", func(t *testing.T) {
		app, got := newScopeApp(t, v)
		probe(t, app, both(bearer(withProject("acme", "joe@acme.io", false, "")),
			setHdr(map[string]string{"X-App-Id": "web"})))
		if got.app != "web" {
			t.Fatalf("app label must survive the validated path, got %q", got.app)
		}
	})

	t.Run("anonymous request strips every sub-scope", func(t *testing.T) {
		app, got := newScopeApp(t, v)
		probe(t, app, setHdr(map[string]string{
			"X-Org-Id":     "victim", // Phase-1 data residual — but never validated
			"X-Project-Id": "site-a", // forged; no principal
			"X-App-Id":     "web",
		}))
		if got.user != "" {
			t.Fatalf("anonymous must carry no validated user, got %q", got.user)
		}
		if got.project != "" || got.app != "" {
			t.Fatalf("anonymous sub-scopes must be stripped: project=%q app=%q", got.project, got.app)
		}
	})

	t.Run("SuperAdmin org-switch refuses a cross-org project claim", func(t *testing.T) {
		// SuperAdmin acts as beta but its token claims acme's "site-a" → the claim
		// is FOREIGN to the acted-as org (beta) → stripped.
		app, got := newScopeApp(t, v)
		probe(t, app, both(bearer(withProject("admin", "z@hanzo.ai", true, "site-a")),
			setHdr(map[string]string{"X-Org-Id": "beta"})))
		if got.org != "beta" || !got.admin || got.project != "" {
			t.Fatalf("admin-as-beta with acme's project must strip it: org=%q admin=%v project=%q", got.org, got.admin, got.project)
		}
		// When the admin's claim IS beta's project it is non-foreign to beta → kept.
		app2, got2 := newScopeApp(t, v)
		probe(t, app2, both(bearer(withProject("admin", "z@hanzo.ai", true, "secret")),
			setHdr(map[string]string{"X-Org-Id": "beta"})))
		if got2.org != "beta" || got2.project != "secret" {
			t.Fatalf("admin-as-beta with beta's project must keep it: org=%q project=%q", got2.org, got2.project)
		}
	})

	t.Run("X-Org-Id forgery still blocked (no regression)", func(t *testing.T) {
		app, got := newScopeApp(t, v)
		// A normal user cannot widen org by asserting X-Org-Id: victim.
		probe(t, app, both(bearer(withProject("acme", "joe@acme.io", false, "")),
			setHdr(map[string]string{"X-Org-Id": "victim"})))
		if got.org != "acme" || got.admin {
			t.Fatalf("client X-Org-Id must not widen scope: org=%q admin=%v", got.org, got.admin)
		}
	})
}
