package projects

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
)

func TestOperatorOrgsFromEnv(t *testing.T) {
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "")
	got := operatorOrgsFromEnv("hanzo")
	if !got["hanzo"] || len(got) != 1 {
		t.Fatalf("default operator orgs = %v, want {hanzo}", got)
	}
	t.Setenv("CLOUD_PLATFORM_OPERATOR_ORGS", "hanzo, yadota ,Acme")
	got = operatorOrgsFromEnv("ignored-when-env-set")
	for _, o := range []string{"hanzo", "yadota", "acme"} {
		if !got[o] {
			t.Errorf("operator org %q missing from %v", o, got)
		}
	}
}

// Hostname syntax is internal/fqdn's contract now, and fqdn_test.go pins every
// case this file used to assert (plus the trailing root dot and the 253-byte
// bound, neither of which this path used to handle). Re-asserting them here would
// be a second copy of the same expectation — exactly the duplication that let the
// two paths drift in the first place.

// TestListHostsForProject: a site's own subdomain plus bound custom domains are
// all reported, scoped to (org, slug); another org's binding is never leaked.
func TestListHostsForProject(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.CreateProject(ctx, mkProject("yadota", "yadota", "Yadota")); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Bind the subdomain slug + two custom domains to (yadota, yadota).
	for _, h := range []string{"yadota", "yadota.tech", "www.yadota.tech"} {
		if err := s.BindHost(ctx, h, "yadota", "yadota", 100); err != nil {
			t.Fatalf("bind %q: %v", h, err)
		}
	}
	// A different org's binding must not appear in yadota's list.
	if err := s.CreateProject(ctx, mkProject("acme", "acme", "Acme")); err != nil {
		t.Fatalf("create acme: %v", err)
	}
	if err := s.BindHost(ctx, "acme.example", "acme", "acme", 100); err != nil {
		t.Fatalf("bind acme: %v", err)
	}

	hosts, err := s.ListHostsForProject(ctx, "yadota", "yadota")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hosts) != 3 {
		t.Fatalf("want 3 hosts, got %d (%v)", len(hosts), hosts)
	}
	set := map[string]bool{}
	for _, h := range hosts {
		set[h] = true
	}
	for _, want := range []string{"yadota", "yadota.tech", "www.yadota.tech"} {
		if !set[want] {
			t.Errorf("missing bound host %q in %v", want, hosts)
		}
	}
	if set["acme.example"] {
		t.Fatal("cross-org host leaked into project domain list")
	}

	// A custom domain already bound to another site is refused (first-come).
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 200); err == nil {
		t.Fatal("expected errHostTaken binding another org's custom domain")
	}
}

// TestResolveHostCustomDomain: a bound custom domain resolves to its project (the
// exact join the site edge uses for custom-domain serving), and reflects the
// project's live status.
func TestResolveHostCustomDomain(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	p := mkProject("yadota", "yadota", "Yadota")
	p.Status = "live"
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "yadota", "yadota", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	got, err := s.ResolveHost(ctx, "yadota.tech")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Org != "yadota" || got.Slug != "yadota" || got.Status != "live" {
		t.Fatalf("resolved %+v, want org=yadota slug=yadota status=live", got)
	}
}

// TestSelfHostsAreNotClaimable proves the claim gate refuses every host WE run,
// not just the published-site apex. `api.hanzo.ai` — the production API host —
// used to pass this gate: the serve gate excluded it so it could never serve, but
// the claim row is first-come and global, so the claim permanently DENIED the host
// to its real owner (verified against production: a customer claimed it and the
// hanzo org was then refused 409 on its own hostname).
func TestSelfHostsAreNotClaimable(t *testing.T) {
	// One source, set exactly as sites.New does at startup.
	sites.SetSelfDomains([]string{"hanzo.app", "hanzo.ai"})
	t.Cleanup(func() { sites.SetSelfDomains(nil) })

	s := &cloud.Service[state]{State: state{apex: "hanzo.app"}}
	for _, h := range []string{"hanzo.ai", "api.hanzo.ai", "console.hanzo.ai", "hanzo.app", "anything.hanzo.app"} {
		if !ours(s, h) {
			t.Errorf("%q is a host we operate but the claim gate would allow it", h)
		}
	}
	// A genuine customer domain is unaffected — this gate must never widen past ours.
	for _, h := range []string{"shop.yadota.tech", "yadota.tech", "hanzo.ai.evil.test"} {
		if ours(s, h) {
			t.Errorf("%q is a customer domain but the claim gate refused it", h)
		}
	}
}

// TestUnbindHostReleasesOnlyOurOwn proves a released host becomes claimable again
// and that the release is tenant-scoped. Until releaseDomain existed there was no
// writer that could drop a row at all — setDomains only adds, and delete-project
// unbinds the bare slug alone — so a mistyped or retired domain was held forever.
func TestUnbindHostReleasesOnlyOurOwn(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, org := range []string{"yadota", "acme"} {
		if err := s.CreateProject(ctx, mkProject(org, org, org)); err != nil {
			t.Fatalf("create %s: %v", org, err)
		}
	}
	if err := s.BindHost(ctx, "yadota.tech", "yadota", "yadota", 100); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// A non-owner "release" is a scoped no-op — it must not drop someone else's row.
	if err := s.UnbindHost(ctx, "yadota.tech", "acme", "acme"); err != nil {
		t.Fatalf("noop release: %v", err)
	}
	if _, err := s.ResolveHost(ctx, "yadota.tech"); err != nil {
		t.Fatal("a non-owner release dropped the owner's host")
	}
	// The owner releases it, and only then may another org claim it.
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 200); !errors.Is(err, errHostTaken) {
		t.Fatalf("still-held host rebind = %v, want errHostTaken", err)
	}
	if err := s.UnbindHost(ctx, "yadota.tech", "yadota", "yadota"); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if err := s.BindHost(ctx, "yadota.tech", "acme", "acme", 300); err != nil {
		t.Fatalf("released host must be claimable again, got %v", err)
	}
}
