package edge

import (
	"context"
	"testing"
)

func staticDefaults() Policy {
	return Policy{CORSOrigins: []string{"*.hanzo.ai"}, PerIPRPM: 100, WindowSec: 1}
}

// TestStaticOnly: an empty dataDir yields a working static-only store — reads
// return the static default, writes error (never silently vanish).
func TestStaticOnly(t *testing.T) {
	s, err := New("", "admin", staticDefaults())
	if err == nil {
		t.Fatal("empty dataDir must report the degraded (static-only) mode")
	}
	if got := s.Platform().PerIPRPM; got != 100 {
		t.Fatalf("static Platform PerIPRPM = %d, want 100", got)
	}
	if _, err := s.Put(context.Background(), "admin", Policy{PerIPRPM: 5}); err == nil {
		t.Fatal("Put on a static-only store must error, not silently drop")
	}
	if s.OrgRPM("acme") != 0 {
		t.Fatal("static-only OrgRPM must be 0")
	}
}

// TestPlatformLayering: the admin-org row layers over the static defaults, and a
// partial PUT is additive (merge), not a full replace.
func TestPlatformLayering(t *testing.T) {
	s, err := New(t.TempDir(), "admin", staticDefaults())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	// Unprovisioned ⇒ static.
	if p := s.Platform(); p.PerIPRPM != 100 || len(p.CORSOrigins) != 1 {
		t.Fatalf("unprovisioned Platform = %+v, want static", p)
	}

	// PUT only CORS ⇒ CORS overridden, PerIP/window inherited from static.
	if _, err := s.PutPlatform(context.Background(), Policy{CORSOrigins: []string{"https://a.io", "https://b.io"}}); err != nil {
		t.Fatalf("PutPlatform CORS: %v", err)
	}
	p := s.Platform()
	if len(p.CORSOrigins) != 2 || p.CORSOrigins[0] != "https://a.io" {
		t.Fatalf("CORS not applied: %+v", p.CORSOrigins)
	}
	if p.PerIPRPM != 100 || p.WindowSec != 1 {
		t.Fatalf("partial PUT must inherit static PerIP/window, got %d/%d", p.PerIPRPM, p.WindowSec)
	}

	// PUT only PerIPRPM ⇒ merges onto the existing platform row (CORS survives).
	if _, err := s.PutPlatform(context.Background(), Policy{PerIPRPM: 500}); err != nil {
		t.Fatalf("PutPlatform PerIP: %v", err)
	}
	p = s.Platform()
	if p.PerIPRPM != 500 {
		t.Fatalf("PerIPRPM = %d, want 500", p.PerIPRPM)
	}
	if len(p.CORSOrigins) != 2 {
		t.Fatalf("additive PUT lost the prior CORS: %+v", p.CORSOrigins)
	}
}

// TestPerOrgOrgRPM: a per-org row sets that org's ceiling; a distinct org falls
// back to the platform default's OrgRPM; Effective merges platform + org OrgRPM.
func TestPerOrgOrgRPM(t *testing.T) {
	s, err := New(t.TempDir(), "admin", staticDefaults())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if _, err := s.Put(context.Background(), "acme", Policy{OrgRPM: 60}); err != nil {
		t.Fatalf("Put org: %v", err)
	}
	if got := s.OrgRPM("acme"); got != 60 {
		t.Fatalf("OrgRPM(acme) = %d, want 60", got)
	}
	if got := s.OrgRPM("other"); got != 0 {
		t.Fatalf("OrgRPM(other) with no row/platform default = %d, want 0", got)
	}

	// A platform default OrgRPM applies to any org that has no own row.
	if _, err := s.PutPlatform(context.Background(), Policy{OrgRPM: 30}); err != nil {
		t.Fatalf("PutPlatform OrgRPM: %v", err)
	}
	if got := s.OrgRPM("other"); got != 30 {
		t.Fatalf("OrgRPM(other) = %d, want the platform default 30", got)
	}
	if got := s.OrgRPM("acme"); got != 60 {
		t.Fatalf("OrgRPM(acme) = %d, own row must still win over the platform default", got)
	}

	// Effective = platform edge policy (CORS/per-IP) + this org's OrgRPM.
	eff := s.Effective("acme")
	if eff.OrgRPM != 60 || eff.PerIPRPM != 100 || len(eff.CORSOrigins) != 1 {
		t.Fatalf("Effective(acme) = %+v, want platform edge + OrgRPM 60", eff)
	}
}

// TestPerOrgCacheMethodsResolvers: the per-org cache/method knobs round-trip and
// resolve — default vs longest-prefix cache TTL, method allowlist — and a distinct
// org inherits neither (isolation).
func TestPerOrgCacheMethodsResolvers(t *testing.T) {
	s, err := New(t.TempDir(), "admin", staticDefaults())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if _, err := s.Put(context.Background(), "acme", Policy{
		CacheTTLSec: 30,
		CachePaths:  map[string]int{"/v1": 60, "/v1/models": 300},
		Methods:     []string{"GET", "POST"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if got := s.CacheTTL("acme", "/other"); got != 30 {
		t.Fatalf("default CacheTTL = %d, want 30", got)
	}
	if got := s.CacheTTL("acme", "/v1/health"); got != 60 {
		t.Fatalf("prefix /v1 CacheTTL = %d, want 60", got)
	}
	if got := s.CacheTTL("acme", "/v1/models/x"); got != 300 {
		t.Fatalf("longest-prefix CacheTTL = %d, want 300", got)
	}
	if got := s.Methods("acme"); len(got) != 2 || got[0] != "GET" {
		t.Fatalf("Methods(acme) = %v, want [GET POST]", got)
	}

	// Isolation: another org inherits neither acme's cache nor its methods.
	if got := s.CacheTTL("globex", "/v1/models/x"); got != 0 {
		t.Fatalf("CacheTTL(globex) = %d, want 0", got)
	}
	if got := s.Methods("globex"); got != nil {
		t.Fatalf("Methods(globex) = %v, want nil", got)
	}
}

// TestPlatformDefaultsPerOrgKnobs: a platform-row default (cache/methods) applies
// to any org with no own row, and an org's own row still wins over it.
func TestPlatformDefaultsPerOrgKnobs(t *testing.T) {
	s, err := New(t.TempDir(), "admin", staticDefaults())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if _, err := s.PutPlatform(context.Background(), Policy{CacheTTLSec: 15, Methods: []string{"GET"}}); err != nil {
		t.Fatalf("PutPlatform: %v", err)
	}
	if got := s.CacheTTL("nobody", "/x"); got != 15 {
		t.Fatalf("platform-default CacheTTL(nobody) = %d, want 15", got)
	}
	if _, err := s.Put(context.Background(), "acme", Policy{CacheTTLSec: 99}); err != nil {
		t.Fatalf("Put acme: %v", err)
	}
	if got := s.CacheTTL("acme", "/x"); got != 99 {
		t.Fatalf("own-row CacheTTL(acme) = %d, want 99 (wins over platform default)", got)
	}
}

// TestValidate: structural bounds are enforced, and a coherent config passes.
func TestValidate(t *testing.T) {
	for name, p := range map[string]Policy{
		"ttl over max":   {CacheTTLSec: MaxCacheTTLSec + 1},
		"unknown method": {Methods: []string{"TRACE"}},
		"bad path key":   {CachePaths: map[string]int{"v1": 10}},
		"negative rate":  {OrgRPM: -1},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if err := (Policy{OrgRPM: 60, CacheTTLSec: 30, CachePaths: map[string]int{"/v1": 5}, Methods: []string{"GET"}}).Validate(); err != nil {
		t.Fatalf("valid config must pass, got %v", err)
	}
}

// TestPersistAcrossReopen: a written policy survives a store reopen (real SQLite).
func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(dir, "admin", staticDefaults())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s1.PutPlatform(context.Background(), Policy{PerIPRPM: 777}); err != nil {
		t.Fatalf("PutPlatform: %v", err)
	}
	_ = s1.Close()

	s2, err := New(dir, "admin", staticDefaults())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	if got := s2.Platform().PerIPRPM; got != 777 {
		t.Fatalf("persisted PerIPRPM = %d, want 777", got)
	}
}
