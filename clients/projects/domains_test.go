package projects

import (
	"context"
	"testing"
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
