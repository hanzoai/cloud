package projects

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestSiteHostBindingIsFirstComeAndTenantSafe proves the global subdomain
// namespace is safe against cross-org hijack. Two orgs may both own a project
// slugged "maxpower" (org-scoped uniqueness — see TestProjectCRUD), so the public
// host `maxpower.hanzo.app` must resolve to exactly ONE of them, deterministically,
// and the loser must never be able to steal or read the winner's binding.
func TestSiteHostBindingIsFirstComeAndTenantSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Both orgs own a project with the SAME slug.
	hz := mkProject("hanzo", "maxpower", "Hanzo Max")
	hz.Status, hz.Bucket = "live", "hanzo-sites"
	ac := mkProject("acme", "maxpower", "Acme Max")
	ac.Status, ac.Bucket = "live", "hanzo-sites"
	if err := s.CreateProject(ctx, hz); err != nil {
		t.Fatalf("create hanzo: %v", err)
	}
	if err := s.CreateProject(ctx, ac); err != nil {
		t.Fatalf("create acme: %v", err)
	}

	// hanzo publishes first → owns the subdomain.
	if err := s.BindHost(ctx, "maxpower", "hanzo", "maxpower", 100); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	if p, err := s.ResolveHost(ctx, "maxpower"); err != nil || p.Org != "hanzo" {
		t.Fatalf("resolve after first bind = (%+v,%v), want org=hanzo", p, err)
	}

	// acme tries to claim the SAME host → refused, binding unchanged. This is the
	// anti-hijack guarantee: a second org can never take a live subdomain.
	if err := s.BindHost(ctx, "maxpower", "acme", "maxpower", 200); !errors.Is(err, errHostTaken) {
		t.Fatalf("acme bind = %v, want errHostTaken", err)
	}
	if p, _ := s.ResolveHost(ctx, "maxpower"); p.Org != "hanzo" {
		t.Fatalf("host hijacked to %q", p.Org)
	}

	// Owner re-bind (a redeploy) is idempotent.
	if err := s.BindHost(ctx, "maxpower", "hanzo", "maxpower", 300); err != nil {
		t.Fatalf("owner re-bind: %v", err)
	}

	// A non-owner unbind is a no-op (scoped by org+slug); the binding survives.
	if err := s.UnbindHost(ctx, "maxpower", "acme", "maxpower"); err != nil {
		t.Fatalf("noop unbind: %v", err)
	}
	if p, err := s.ResolveHost(ctx, "maxpower"); err != nil || p.Org != "hanzo" {
		t.Fatalf("binding dropped by non-owner unbind: (%+v,%v)", p, err)
	}

	// The owner releases it, then acme may legitimately claim it.
	if err := s.UnbindHost(ctx, "maxpower", "hanzo", "maxpower"); err != nil {
		t.Fatalf("owner unbind: %v", err)
	}
	if _, err := s.ResolveHost(ctx, "maxpower"); !errors.Is(err, errNotFound) {
		t.Fatalf("resolve after unbind, want notFound, got %v", err)
	}
	if err := s.BindHost(ctx, "maxpower", "acme", "maxpower", 400); err != nil {
		t.Fatalf("acme reclaim: %v", err)
	}
	if p, _ := s.ResolveHost(ctx, "maxpower"); p.Org != "acme" {
		t.Fatalf("reclaim resolved to %q, want acme", p.Org)
	}
}

// TestSiteResolverAdapter checks the sites.Resolver adapter: an unbound slug is a
// clean not-found (false, nil), and a bound one yields the authoritative Site with
// the org-bounded prefix — never derived from anything but the store.
func TestSiteResolverAdapter(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	r := siteResolver{store: s}

	if _, found, err := r.Resolve(ctx, "ghost"); found || err != nil {
		t.Fatalf("unbound slug = (found=%v,err=%v), want (false,nil)", found, err)
	}

	p := mkProject("acme", "myblog", "My Blog")
	p.Status, p.Bucket = "live", "hanzo-sites"
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.BindHost(ctx, "myblog", "acme", "myblog", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	site, found, err := r.Resolve(ctx, "myblog")
	if err != nil || !found {
		t.Fatalf("resolve = (found=%v,err=%v)", found, err)
	}
	if site.Org != "acme" || site.Slug != "myblog" || site.Bucket != "hanzo-sites" ||
		site.Prefix != "acme/myblog" || site.Status != "live" {
		t.Fatalf("unexpected site: %+v", site)
	}
}

// TestBindHostRejectsReserved proves the storage invariant: BindHost refuses a
// reserved label (here `api`) even when a project with that slug somehow exists
// (created via the store, bypassing the handler guard). So site_hosts can NEVER
// physically contain a reserved host, and a reserved subdomain never resolves —
// the serve-time gate is a backstop, not the sole guard.
func TestBindHostRejectsReserved(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := mkProject("acme", "api", "API")
	p.Status = "live"
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.BindHost(ctx, "api", "acme", "api", 100); !errors.Is(err, errReservedHost) {
		t.Fatalf("BindHost(api) = %v, want errReservedHost", err)
	}
	if _, err := s.ResolveHost(ctx, "api"); !errors.Is(err, errNotFound) {
		t.Fatalf("reserved host must not resolve, got %v", err)
	}
}

// TestCreateRejectsReservedSlug proves the create-time guard through the real HTTP
// handler: a reserved slug is a 400; a normal slug still creates. ONE reserved-list
// source (sites.IsReserved) is enforced here and at BindHost.
func TestCreateRejectsReservedSlug(t *testing.T) {
	app := mountApp(t)
	for _, slug := range []string{"api", "admin", "login", "wallet", "hanzo"} {
		status, body := do(t, app, "POST", "/v1/projects", "acme", map[string]any{"name": "X", "slug": slug})
		if status != http.StatusBadRequest {
			t.Errorf("create slug %q status=%d body=%s, want 400", slug, status, body)
		}
	}
	if status, body := do(t, app, "POST", "/v1/projects", "acme", map[string]any{"name": "My Site", "slug": "my-site"}); status != http.StatusCreated {
		t.Fatalf("create normal slug status=%d body=%s, want 201", status, body)
	}
}
