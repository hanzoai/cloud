// Package store — tenant repository tests.
//
// These tests exercise the repository against a throwaway SQLite file,
// booted through a real base app so the migration actually runs. The same
// shape will work for Postgres: set COMMERCE_BASE_URL before `go test` and
// the repo talks to Postgres instead. We deliberately do NOT fake base —
// that would let regressions in the JSON-array lookup path slip past CI.
package store

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
)

// newTestStore constructs an isolated store under t.TempDir(). Cleanup is
// registered via t.Cleanup so the DB pool is closed and the on-disk files
// go away on test completion.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Config{DataDir: filepath.Join(dir, "commerce")})
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close(nil)
	})
	return s
}

// newTestTenant returns a minimal tenant fixture; tests may mutate the
// returned value before passing to Create.
func newTestTenant(name string, hosts ...string) *Tenant {
	return &Tenant{
		Name:      name,
		Hostnames: hosts,
		Brand: BrandConfig{
			DisplayName:  name,
			PrimaryColor: "#0ea5e9",
		},
		IAM: IAMConfig{
			Issuer:   "https://id.example.test",
			ClientID: name + "-client",
		},
		IDV: IDVConfig{
			Provider: "persona",
			Endpoint: "https://withpersona.com/verify",
		},
		Providers: []Provider{
			{Name: "square", Enabled: true, KMSPath: "commerce/" + name + "/square"},
			{Name: "braintree", Enabled: false, KMSPath: "commerce/" + name + "/braintree"},
		},
		BDEndpoint:         "https://bd.example.test",
		ReturnURLAllowlist: []string{"https://example.test"},
	}
}

// ─── Create ──────────────────────────────────────────────────────────────

func TestCreate_AssignsIDAndTimestamps(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("acme", "pay.example.test")

	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tenant.ID == "" {
		t.Errorf("expected ID assigned, got empty")
	}
	if tenant.Created.IsZero() {
		t.Errorf("expected Created populated")
	}
	if tenant.Updated.IsZero() {
		t.Errorf("expected Updated populated")
	}
}

func TestCreate_DuplicateNameFails(t *testing.T) {
	s := newTestStore(t)
	a := newTestTenant("acme", "pay.acme.test")
	b := newTestTenant("acme", "pay2.acme.test")

	if err := s.Tenants.Create(a); err != nil {
		t.Fatalf("first create: %v", err)
	}
	err := s.Tenants.Create(b)
	if err != ErrDuplicateTenant {
		t.Errorf("second Create err = %v, want ErrDuplicateTenant", err)
	}
}

func TestCreate_RejectsInvalidHostname(t *testing.T) {
	s := newTestStore(t)
	cases := map[string]string{
		"leading-whitespace":  " pay.example.test",
		"trailing-whitespace": "pay.example.test ",
		"embedded-newline":    "pay.example\ntest",
		"embedded-quote":      `pay"example.test`,
		"empty":               "",
		"ipv6-literal":        "[::1]",
		"raw-ipv4":            "10.0.0.1",
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			tenant := newTestTenant("tenant-"+name, h)
			if err := s.Tenants.Create(tenant); err == nil {
				t.Errorf("Create accepted hostname %q — expected rejection", h)
			}
		})
	}
}

func TestCreate_DedupesAndNormalizesHostnames(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("norm",
		"PAY.EXAMPLE.TEST",
		"pay.example.test",
		"pay.example.test.",    // trailing dot
		"pay.example.test:443", // port
	)
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(tenant.Hostnames) != 1 || tenant.Hostnames[0] != "pay.example.test" {
		t.Errorf("hostnames = %v, want [pay.example.test]", tenant.Hostnames)
	}
}

// ─── FindByHostname ──────────────────────────────────────────────────────

func TestFindByHostname_ExactAndNormalized(t *testing.T) {
	s := newTestStore(t)
	if err := s.Tenants.Create(newTestTenant(
		"acme",
		"pay.acme.test",
		"pay.dev.acme.test",
	)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		host string
		want string
	}{
		{"pay.acme.test", "acme"},
		{"pay.dev.acme.test", "acme"},
		{"PAY.ACME.TEST", "acme"},
		{"pay.acme.test:443", "acme"},
		{"pay.acme.test.", "acme"}, // trailing dot
	}
	for _, tc := range cases {
		got, err := s.Tenants.FindByHostname(tc.host)
		if err != nil {
			t.Errorf("FindByHostname(%q) err = %v, want nil", tc.host, err)
			continue
		}
		if got.Name != tc.want {
			t.Errorf("FindByHostname(%q).Name = %q, want %q", tc.host, got.Name, tc.want)
		}
	}
}

func TestFindByHostname_SubdomainMismatch(t *testing.T) {
	s := newTestStore(t)
	if err := s.Tenants.Create(newTestTenant("acme", "pay.acme.test")); err != nil {
		t.Fatal(err)
	}

	// None of these should match — exact-only.
	spoofs := []string{
		"evil.test",
		"acme.test",
		"xyzpay.acme.test",
		"pay.acme.test.evil.test",
		"a.pay.acme.test",
	}
	for _, h := range spoofs {
		_, err := s.Tenants.FindByHostname(h)
		if err != ErrTenantNotFound {
			t.Errorf("FindByHostname(%q) err = %v, want ErrTenantNotFound", h, err)
		}
	}
}

func TestFindByHostname_EmptyReturnsInvalid(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Tenants.FindByHostname("")
	if err != ErrInvalidHostname {
		t.Errorf("empty host err = %v, want ErrInvalidHostname", err)
	}
}

// A hostname NOT in any tenant's hostnames array must return ErrTenantNotFound
// regardless of how it collates alphabetically. Catches JSON-LIKE pattern
// bugs that could match substrings.
func TestFindByHostname_SubstringOracleResistance(t *testing.T) {
	s := newTestStore(t)
	if err := s.Tenants.Create(newTestTenant(
		"prefix",
		"aaaa.example.test",
	)); err != nil {
		t.Fatal(err)
	}

	// "aaaa.example.test" is stored; "aaa.example.test" must NOT hit.
	if _, err := s.Tenants.FindByHostname("aaa.example.test"); err != ErrTenantNotFound {
		t.Errorf("substring oracle leak: aaa.example.test resolved, err=%v", err)
	}
	// And neither must "aaaaa.example.test" (longer).
	if _, err := s.Tenants.FindByHostname("aaaaa.example.test"); err != ErrTenantNotFound {
		t.Errorf("substring oracle leak: aaaaa.example.test resolved, err=%v", err)
	}
}

// ─── FindByID ────────────────────────────────────────────────────────────

func TestFindByID_Found(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("byid", "pay.byid.test")
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatal(err)
	}

	got, err := s.Tenants.FindByID(tenant.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if got.Name != "byid" {
		t.Errorf("name = %q, want byid", got.Name)
	}
	if len(got.Hostnames) != 1 || got.Hostnames[0] != "pay.byid.test" {
		t.Errorf("hostnames = %v", got.Hostnames)
	}
	// Verify JSON round-trip integrity.
	if got.Brand.PrimaryColor != "#0ea5e9" {
		t.Errorf("brand.primary_color lost in round-trip: %q", got.Brand.PrimaryColor)
	}
	if len(got.Providers) != 2 || got.Providers[0].Name != "square" {
		t.Errorf("providers round-trip: %+v", got.Providers)
	}
}

func TestFindByID_Missing(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Tenants.FindByID("does-not-exist"); err != ErrTenantNotFound {
		t.Errorf("err = %v, want ErrTenantNotFound", err)
	}
	if _, err := s.Tenants.FindByID(""); err != ErrTenantNotFound {
		t.Errorf("empty id err = %v, want ErrTenantNotFound", err)
	}
}

// ─── UpdateProviders ─────────────────────────────────────────────────────

func TestUpdateProviders_Replaces(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("up", "pay.up.test")
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatal(err)
	}

	next := []Provider{
		{Name: "stripe", Enabled: true, KMSPath: "commerce/up/stripe"},
	}
	if err := s.Tenants.UpdateProviders(tenant.ID, next); err != nil {
		t.Fatalf("UpdateProviders: %v", err)
	}
	got, err := s.Tenants.FindByID(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 || got.Providers[0].Name != "stripe" {
		t.Errorf("providers after update = %+v", got.Providers)
	}
}

func TestUpdateProviders_MissingTenant(t *testing.T) {
	s := newTestStore(t)
	err := s.Tenants.UpdateProviders("no-such-id", []Provider{{Name: "x", Enabled: true}})
	if err != ErrTenantNotFound {
		t.Errorf("err = %v, want ErrTenantNotFound", err)
	}
}

// Concurrency model: last-write-wins. Two goroutines writing different
// provider lists MUST NOT corrupt the JSON column. We do not assert which
// writer wins — that is implementation-defined — only that the stored value
// is one of the two attempted payloads (never a merge, never a truncation).
func TestUpdateProviders_ConcurrencyLastWriteWins(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("race", "pay.race.test")
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatal(err)
	}

	setA := []Provider{{Name: "a-only", Enabled: true, KMSPath: "kms/a"}}
	setB := []Provider{{Name: "b-only", Enabled: true, KMSPath: "kms/b"}}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = s.Tenants.UpdateProviders(tenant.ID, setA) }()
	go func() { defer wg.Done(); _ = s.Tenants.UpdateProviders(tenant.ID, setB) }()
	wg.Wait()

	got, err := s.Tenants.FindByID(tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("providers len = %d, want 1; got %+v", len(got.Providers), got.Providers)
	}
	if got.Providers[0].Name != "a-only" && got.Providers[0].Name != "b-only" {
		t.Errorf("corrupt provider after race: %+v", got.Providers)
	}
}

// ─── List ────────────────────────────────────────────────────────────────

func TestList_OrderedByName(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"charlie", "alpha", "bravo"} {
		if err := s.Tenants.Create(newTestTenant(name, "pay."+name+".test")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Tenants.List(10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"alpha", "bravo", "charlie"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("List[%d] = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestList_LimitAndOffset(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"alpha", "bravo", "charlie", "delta"} {
		if err := s.Tenants.Create(newTestTenant(name, "pay."+name+".test")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Tenants.List(2, 1)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Name != "bravo" || got[1].Name != "charlie" {
		t.Errorf("page = [%q, %q]", got[0].Name, got[1].Name)
	}
}

// ─── Regression: raw-column inspection ───────────────────────────────────

// Defensive check: the hostnames JSON column stores a canonical array, not
// a quoted string or a whitespace-padded encoding that could weaken the
// LIKE lookup. This is the single strongest anti-regression we have for the
// tenant-resolver security property.
func TestRawHostnamesStoredAsCanonicalArray(t *testing.T) {
	s := newTestStore(t)
	tenant := newTestTenant("canon", "pay.canon.test", "pay2.canon.test")
	if err := s.Tenants.Create(tenant); err != nil {
		t.Fatal(err)
	}
	rec, err := s.App.FindRecordById("commerce_tenants", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := rec.GetString("hostnames")
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("hostnames not valid JSON array: %q", raw)
	}
	if len(parsed) != 2 || parsed[0] != "pay.canon.test" || parsed[1] != "pay2.canon.test" {
		t.Errorf("hostnames canonical array = %v, want [pay.canon.test pay2.canon.test]", parsed)
	}
}
