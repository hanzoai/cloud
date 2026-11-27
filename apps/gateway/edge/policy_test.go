package edge

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

func staticDefaults() Policy {
	return Policy{CORSOrigins: []string{"*.hanzo.ai"}, PerIPRPM: 100, WindowSec: 1}
}

// TestStaticOnly: an empty dataDir yields a working static-only store — reads
// return the static default, writes error (never silently vanish).
func TestStaticOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// TestPlatformLayering: the admin-org row layers over the static defaults, and a
// partial PUT is additive (merge), not a full replace.
func TestPlatformLayering(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// TestPerOrgOrgRPM: a per-org row sets that org's ceiling; a distinct org falls
// back to the platform default's OrgRPM; Effective merges platform + org OrgRPM.
func TestPerOrgOrgRPM(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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

		// The platform's OrgRPM is every org's default CAP, not an org_rpm it inherits:
		// an org with no row of its own sets nothing, and is held at the cap.
		if _, err := s.PutPlatform(context.Background(), Policy{OrgRPM: 30}); err != nil {
			t.Fatalf("PutPlatform OrgRPM: %v", err)
		}
		if got := s.OrgRPM("other"); got != 0 {
			t.Fatalf("OrgRPM(other) = %d, want 0: the platform's org_rpm does not inherit", got)
		}
		if got := s.Cap("other"); got != 30 {
			t.Fatalf("Cap(other) = %d, want the platform's 30", got)
		}
		if got := s.OrgRPM("acme"); got != 60 {
			t.Fatalf("OrgRPM(acme) = %d, want its own 60", got)
		}

		// Effective = platform edge policy (CORS/per-IP) + the ceiling and cap IN FORCE:
		// acme's own 60 is above the platform's cap of 30, so it reads back as the 30 the
		// limiter enforces, never the stored 60.
		eff := s.Effective("acme")
		if eff.OrgRPM != 30 || eff.OrgCap != 30 || eff.PerIPRPM != 100 || len(eff.CORSOrigins) != 1 {
			t.Fatalf("Effective(acme) = %+v, want platform edge, org_rpm 30 and org_cap 30 in force", eff)
		}
		if _, err := s.Put(context.Background(), "acme", Policy{OrgRPM: 20}); err != nil {
			t.Fatalf("Put org: %v", err)
		}
		if eff := s.Effective("acme"); eff.OrgRPM != 20 || eff.OrgCap != 30 {
			t.Fatalf("Effective(acme) under its cap = org_rpm %d org_cap %d, want 20 and 30", eff.OrgRPM, eff.OrgCap)
		}
	})
}

// TestPerOrgCacheMethodsResolvers: the per-org cache/method knobs round-trip and
// resolve — default vs longest-prefix cache TTL, method allowlist — and a distinct
// org inherits neither (isolation).
func TestPerOrgCacheMethodsResolvers(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// TestPlatformDefaultsPerOrgKnobs: a platform-row default (cache/methods) applies
// to any org with no own row, and an org's own row still wins over it.
func TestPlatformDefaultsPerOrgKnobs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
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
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
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
	})
}

// A per-IP cap the edge would multiply past an int is refused at the write, so no
// stored policy can overflow into a limit that refuses everything.
func TestValidateBoundsThePerIPCap(t *testing.T) {
	if err := (Policy{PerIPRPM: MaxPerIPRPM}).Validate(); err != nil {
		t.Fatalf("the largest cap was refused: %v", err)
	}
	if err := (Policy{PerIPRPM: MaxPerIPRPM + 1}).Validate(); err == nil {
		t.Fatal("a cap past MaxPerIPRPM was accepted")
	}
}

// Partial PUTs at once all land: the read, the merge and the write are one
// transaction, so a PUT that set only one field is not overwritten by one that read
// the row before it and wrote it after.
func TestConcurrentPartialPutsAllLand(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := New(t.TempDir(), "admin", staticDefaults())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		ctx := context.Background()
		var wg sync.WaitGroup
		puts := []Policy{{OrgRPM: 111}, {CacheTTLSec: 222}, {Methods: []string{"GET"}}, {OrgCap: 333}}
		for _, p := range puts {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.Put(ctx, "acme", p); err != nil {
					t.Errorf("put: %v", err)
				}
			}()
		}
		wg.Wait()
		got, ok, err := s.Get(ctx, "acme")
		if err != nil || !ok {
			t.Fatalf("get = %v, %v", ok, err)
		}
		if got.OrgRPM != 111 || got.CacheTTLSec != 222 || len(got.Methods) != 1 || got.OrgCap != 333 {
			t.Fatalf("a partial PUT was overwritten: %+v", got)
		}
		if _, err := s.Put(ctx, "bad\x00org", Policy{OrgRPM: 1}); err == nil {
			t.Fatal("a NUL in an org was stored")
		}
	})
}
