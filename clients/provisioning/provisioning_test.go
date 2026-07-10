package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "provisioning.db"))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleResource(name string) Resource {
	return Resource{
		ID: "rs_" + name, Org: "acme", Kind: "sql", Name: name,
		PhysicalName: physicalName("acme", name), SecretRef: "org/acme/sql/" + name,
		Host: "sql.hanzo.svc", Port: 5432, Username: physicalName("acme", name),
		DBName: physicalName("acme", name), Status: "ready", CreatedAt: 1700000000,
	}
}

// mockProv is an in-memory Provisioner: it touches no backend, records the
// password it was handed, and returns canned connection metadata.
type mockProv struct {
	cs, host, db string
	port         int
	gotPw        string
	created      int
	dropped      int
}

func (m *mockProv) Create(_ context.Context, _, _, pw string) (string, string, int, string, error) {
	m.created++
	m.gotPw = pw
	return m.cs, m.host, m.port, m.db, nil
}

func (m *mockProv) Drop(_ context.Context, _, _ string) error { m.dropped++; return nil }

// newTestSvc builds a provisioning Service with a temp store, KMS-degraded secrets (no env),
// and a mock provisioner under each given kind.
func newTestSvc(t *testing.T, kinds ...string) (*cloud.Service[state], *mockProv) {
	t.Helper()
	// Force KMS degrade so secret persistence is hermetic and never dials.
	t.Setenv("CLOUD_KMS_NODES", "")
	t.Setenv("CLOUD_KMS_PASSPHRASE", "")
	log := luxlog.New("module", "provtest")
	mp := &mockProv{cs: "redis://u:pw@kv.hanzo.svc:6379/0", host: "kv.hanzo.svc", port: 6379, db: "prefix:"}
	reg := map[string]Provisioner{}
	for _, k := range kinds {
		reg[k] = mp
	}
	return &cloud.Service[state]{Base: cloud.Base{Log: log}, State: state{store: newTestStore(t), sec: openSecrets("hanzo", log), reg: reg}}, mp
}

func TestStore_InsertGetListDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Get(ctx, "acme", "sql", "orders"); !errors.Is(err, errNotFound) {
		t.Fatalf("Get(missing) = %v, want errNotFound", err)
	}

	if err := s.Insert(ctx, sampleResource("orders")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(ctx, sampleResource("events")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Get(ctx, "acme", "sql", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PhysicalName != physicalName("acme", "orders") || got.Port != 5432 || got.Status != "ready" {
		t.Fatalf("Get returned wrong row: %+v", got)
	}

	rows, err := s.List(ctx, "acme", "sql")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List len = %d, want 2", len(rows))
	}

	// Org isolation: another org sees nothing.
	other, err := s.List(ctx, "globex", "sql")
	if err != nil {
		t.Fatalf("List(globex): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-org leak: globex saw %d rows", len(other))
	}

	deleted, err := s.Delete(ctx, "acme", "sql", "orders")
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v,%v), want (true,nil)", deleted, err)
	}
	if _, err := s.Get(ctx, "acme", "sql", "orders"); !errors.Is(err, errNotFound) {
		t.Fatalf("Get after delete = %v, want errNotFound", err)
	}
	deletedAgain, err := s.Delete(ctx, "acme", "sql", "orders")
	if err != nil || deletedAgain {
		t.Fatalf("Delete(missing) = (%v,%v), want (false,nil)", deletedAgain, err)
	}
}

func TestStore_DuplicateConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.Insert(ctx, sampleResource("orders")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	dup := sampleResource("orders")
	dup.ID = "rs_different"
	if err := s.Insert(ctx, dup); !errors.Is(err, errConflict) {
		t.Fatalf("Insert(dup) = %v, want errConflict", err)
	}
}

// TestStore_PhysicalNameConflict proves the global guard: two DIFFERENT
// (org,kind,name) rows that somehow resolve to the SAME physical_name must
// fail closed — a backend resource is never shared.
func TestStore_PhysicalNameConflict(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	a := sampleResource("orders")
	if err := s.Insert(ctx, a); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Distinct identity (different name + id), but force a physical collision.
	b := sampleResource("orders")
	b.ID, b.Name = "rs_other", "events" // satisfies UNIQUE(org,kind,name)
	b.PhysicalName = a.PhysicalName     // but collides on physical_name
	if exists, err := s.PhysicalExists(ctx, b.PhysicalName); err != nil || !exists {
		t.Fatalf("PhysicalExists = (%v,%v), want (true,nil)", exists, err)
	}
	if err := s.Insert(ctx, b); !errors.Is(err, errConflict) {
		t.Fatalf("Insert(physical collision) = %v, want errConflict", err)
	}
}

func TestNameValidation(t *testing.T) {
	valid := []string{"orders", "my-db", "a", "db1", "x0-1-2"}
	invalid := []string{"", "-bad", "bad-", "Bad", "has_underscore", "white space", "way-too-long-" + strings.Repeat("x", 60)}
	for _, n := range valid {
		if !nameRE.MatchString(n) {
			t.Errorf("expected %q valid", n)
		}
	}
	for _, n := range invalid {
		if nameRE.MatchString(n) {
			t.Errorf("expected %q invalid", n)
		}
	}
}

// TestSanitizeOrg: a clean DNS-1123-label owner maps to itself (identity, no
// suffix); a non-slug owner folds to [a-z0-9-] AND carries a 16-hex SHA-256
// disambiguation suffix. The exported IAM owner claim is already a clean slug,
// so the common path is the identity — the suffix exists only to keep distinct
// non-slug owners distinct.
func TestSanitizeOrg(t *testing.T) {
	// Clean slugs (already [a-z0-9-]): identity, no suffix. Whitespace-bearing
	// owners are NO LONGER folded to a clean slug — they are refused (see the
	// rejection block below), because trimming them is non-injective.
	identity := map[string]string{
		"acme": "acme", "hanzo": "hanzo", "my-org": "my-org", "org123": "org123",
		"a-b-c-d": "a-b-c-d", "--weird--": "--weird--",
	}
	for in, want := range identity {
		if got := sanitizeOrg(in); got != want {
			t.Errorf("sanitizeOrg(%q) = %q, want identity %q", in, got, want)
		}
	}
	// Whitespace / control / zero-width-format owners are REFUSED (→ ""), never
	// folded — trimming them (here, at the middleware, or in fasthttp's OWS trim)
	// would collapse distinct IAM orgs onto one tenant namespace (RED CRIT-2
	// residual). "\u00a0" is NBSP, "\u200b" is a zero-width space, "\t" a control.
	for _, in := range []string{" hanzo ", "hanzo ", " hanzo", "ha nzo", "hanzo\t", "hanzo\u00a0", "hanzo\u200b", "  ", "\n"} {
		if got := sanitizeOrg(in); got != "" {
			t.Errorf("sanitizeOrg(%q) = %q, want \"\" (refused: non-injective identifier)", in, got)
		}
	}
	// Owners with VISIBLE chars OUTSIDE [a-z0-9-] (uppercase, '.', '@') fold + get
	// a "-"+16hex suffix, so the result is NOT the bare fold (that bare fold is the
	// collision target) and stays a DNS-safe slug. (Whitespace is not here — it is
	// refused above, not folded.)
	for _, in := range []string{"AcmeCorp", "a@b.c", "team.a", "Widgets"} {
		got := sanitizeOrg(in)
		if len(got) < 17 || got[len(got)-17] != '-' {
			t.Errorf("sanitizeOrg(%q) = %q, want a folded slug + '-'+16hex suffix", in, got)
		}
		for _, r := range got {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				t.Errorf("sanitizeOrg(%q) = %q has unsafe char %q", in, got, r)
			}
		}
	}
}

// TestSanitizeOrgInjective is the RED cross-tenant regression: distinct raw
// owners that USED to fold to the same slug (and thus shared one physical
// bucket/DB namespace) must now map to DIFFERENT slugs. Proven for the exact
// collisions RED found: Acme/acme (case) and team.a/team-a (separator).
func TestSanitizeOrgInjective(t *testing.T) {
	collisionPairs := [][2]string{
		{"Acme", "acme"},
		{"team.a", "team-a"},
		{"a.b", "a-b"},
		{"Foo_Bar", "foo-bar"},
		{"WIDGETS", "widgets"},
	}
	for _, p := range collisionPairs {
		x, y := sanitizeOrg(p[0]), sanitizeOrg(p[1])
		if x == y {
			t.Errorf("sanitizeOrg fold collision: %q and %q both → %q (cross-tenant namespace share!)", p[0], p[1], x)
		}
		// And the derived physical namespace (what actually keys buckets/DBs) is
		// distinct too — the property that matters for isolation.
		if orgHash(x) == orgHash(y) {
			t.Errorf("orgHash collision after sanitize: %q/%q share a physical namespace", p[0], p[1])
		}
	}
	// A clean lowercase slug is unaffected (no false-splitting of legit owners).
	if sanitizeOrg("acme") != "acme" {
		t.Error("a clean slug must remain identity")
	}

	// ALIAS class: a clean slug that LOOKS like a suffixed output
	// ("foo-<16hex>") must NOT alias the sanitized output of a non-slug owner that
	// folds to "foo" and hashes to that suffix. The suffixed-looking slug is denied
	// identity and re-suffixed, so a squatted "foo-<sha256(Foo)[:8]>" can never
	// collide with "Foo".
	nonSlug := "Foo"
	foldedOut := sanitizeOrg(nonSlug) // "foo-<hash(Foo)[:8]>"
	if sanitizeOrg(foldedOut) == foldedOut {
		t.Errorf("suffix-looking slug %q kept identity — aliases the non-slug output of %q", foldedOut, nonSlug)
	}
	if sanitizeOrg(foldedOut) == sanitizeOrg(nonSlug) {
		t.Errorf("alias collision: squatted %q and non-slug %q map to the same slug", foldedOut, nonSlug)
	}
	// A normal slug that does NOT look suffixed keeps identity.
	if sanitizeOrg("my-team-42") != "my-team-42" {
		t.Error("a normal slug must keep identity (not over-suffixed)")
	}
}

// TestSanitizeOrgWhitespaceInjective is the RED CRIT-2 residual regression: an
// org identifier that differs from another ONLY by edge/internal/unicode
// whitespace (or a control/zero-width rune) must never collapse onto the same
// tenant slug. The prior code TrimSpace'd the org before hashing, so "acme",
// "acme ", "ac me" and an NBSP variant all folded to one namespace. Now each
// such variant is REFUSED (→ ""), so the set is "distinct-or-rejected, never
// colliding": the only accepted member is the clean "acme".
func TestSanitizeOrgWhitespaceInjective(t *testing.T) {
	// The exact family RED called out, plus unicode-space + control variants.
	orgs := []string{
		"acme",       // clean — the ONLY accepted member
		"acme ",      // trailing ASCII space
		" acme",      // leading ASCII space
		"ac me",      // internal ASCII space
		"acme\t",     // trailing tab (control/space)
		"acme ",      // trailing NBSP (U+00A0)
		" acme ",     // leading + trailing space
		"acme\u200b", // trailing zero-width space (Cf)
	}
	out := map[string]string{}
	for _, o := range orgs {
		got := sanitizeOrg(o)
		if o == "acme" {
			if got != "acme" {
				t.Fatalf("clean org %q must map to itself, got %q", o, got)
			}
			continue
		}
		// Every whitespace/control/format variant is refused outright.
		if got != "" {
			t.Errorf("whitespace variant %q = %q, want \"\" (refused)", o, got)
		}
	}
	// No two DISTINCT accepted (non-empty) outputs may coincide — the injectivity
	// invariant. Rejected members ("") are refused at tenant() (403) and never
	// key a namespace, so they are excluded from the collision check.
	for _, o := range orgs {
		if s := sanitizeOrg(o); s != "" {
			if prev, ok := out[s]; ok && prev != o {
				t.Fatalf("collision: %q and %q both → %q (cross-tenant namespace share!)", prev, o, s)
			}
			out[s] = o
		}
	}
}

// TestOrgHasUnsafeRuneMatchesSanitize proves the middleware-level predicate
// (cloud.OrgHasUnsafeRune, the trust boundary that gates claims.Owner before any
// header is set) and the slug normalizer agree: exactly the runes the middleware
// refuses are the ones sanitizeOrg refuses, so the two layers cannot drift and
// leave a fold path open.
func TestOrgHasUnsafeRuneMatchesSanitize(t *testing.T) {
	unsafe := []string{"acme ", " acme", "ac me", "acme\t", "acme ", "acme\u200b", "acme\ufeff", "\n"}
	for _, s := range unsafe {
		if !cloud.OrgHasUnsafeRune(s) {
			t.Errorf("cloud.OrgHasUnsafeRune(%q) = false, want true", s)
		}
		if sanitizeOrg(s) != "" {
			t.Errorf("sanitizeOrg(%q) accepted an unsafe-rune org", s)
		}
	}
	safe := []string{"acme", "Acme", "team.a", "a-b-c", "org123", "café", "emoji😀"}
	for _, s := range safe {
		if cloud.OrgHasUnsafeRune(s) {
			t.Errorf("cloud.OrgHasUnsafeRune(%q) = true, want false (visible identifier)", s)
		}
		if sanitizeOrg(s) == "" {
			t.Errorf("sanitizeOrg(%q) refused a safe org", s)
		}
	}
}

// TestPhysicalNameInjective is the regression test for the cross-tenant
// collision: a literal "org_<org>_<name>" join folded the org→name boundary so
// physicalName("acme","my-db") == physicalName("acme-my","db"). The fixed-width
// org hash must keep the two tenants distinct — for the SQL identifier AND the
// derived S3 bucket — and both projections must stay backend-valid.
func TestPhysicalNameInjective(t *testing.T) {
	a := physicalName("acme", "my-db")
	b := physicalName("acme-my", "db")
	if a == b {
		t.Fatalf("physicalName collision: both = %q", a)
	}
	if bucketName(a) == bucketName(b) {
		t.Fatalf("bucketName collision: %q == %q", bucketName(a), bucketName(b))
	}

	// physical carries only [a-z0-9_] (safe quoted SQL/Mongo/CH identifier).
	for _, r := range a {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			t.Fatalf("physicalName produced unsafe char %q in %q", r, a)
		}
	}
	// bucket is a valid S3 name: [a-z0-9-], 3..63, no leading/trailing hyphen.
	bk := bucketName(a)
	if len(bk) < 3 || len(bk) > 63 {
		t.Fatalf("bucket %q length %d outside S3 range 3..63", bk, len(bk))
	}
	if strings.HasPrefix(bk, "-") || strings.HasSuffix(bk, "-") {
		t.Fatalf("bucket %q has a leading/trailing hyphen", bk)
	}
	for _, r := range bk {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			t.Fatalf("bucketName produced unsafe char %q in %q", r, bk)
		}
	}
}

func TestGenToken(t *testing.T) {
	tok, err := genToken(24)
	if err != nil {
		t.Fatalf("genToken: %v", err)
	}
	// base64.RawURLEncoding of 24 bytes = 32 chars, alphabet has no quote chars.
	if len(tok) != 32 {
		t.Fatalf("token len = %d, want 32", len(tok))
	}
	if strings.ContainsAny(tok, "'\"`") {
		t.Fatalf("token %q contains a quote char (unsafe for SQL literals)", tok)
	}
	other, _ := genToken(24)
	if tok == other {
		t.Fatalf("genToken returned identical tokens")
	}
}

// TestCreateOrgGate: a non-admin POST with no X-Org-Id is refused 403 before
// anything is provisioned.
func TestCreateOrgGate(t *testing.T) {
	s, mp := newTestSvc(t, "sql")
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/sql", create(s, "sql"))

	req, _ := http.NewRequest("POST", "/v1/sql", strings.NewReader(`{"name":"orders"}`))
	req.Header.Set("Content-Type", "application/json")
	// No X-Org-Id, no X-User-IsAdmin.
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 403", resp.StatusCode, body)
	}
	if mp.created != 0 {
		t.Fatalf("provisioner ran %d times for a gated request, want 0", mp.created)
	}
}

// TestForgedOrgWithoutPrincipalRefused (RED HIGH): a caller that forges
// X-Org-Id but presents NO validated principal (no X-User-Id) — exactly the
// SanitizeIdentity "Phase-1 data path" residual an in-cluster pod could send with
// no bearer — must be refused 403 and provision NOTHING. Without the ctx.User()
// gate this forged request would allocate a DB in the victim's namespace and
// return its connection string + generated password, or (on DELETE) destroy it.
func TestForgedOrgWithoutPrincipalRefused(t *testing.T) {
	s, mp := newTestSvc(t, "sql")
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/sql", create(s, "sql"))
	app.Delete("/v1/sql/:name", drop(s, "sql"))
	app.Get("/v1/sql", list(s, "sql"))

	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/sql", `{"name":"orders"}`},
		{"DELETE", "/v1/sql/orders", ""},
		{"GET", "/v1/sql", ""},
	} {
		var rdr io.Reader
		if tc.body != "" {
			rdr = strings.NewReader(tc.body)
		}
		req, _ := http.NewRequest(tc.method, tc.path, rdr)
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		// Forge the victim's org WITHOUT any validated principal (no X-User-Id).
		req.Header.Set("X-Org-Id", "victim-org")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			t.Errorf("%s %s (forged org, no principal) = %d body=%s, want 403 — control-plane forge NOT closed!", tc.method, tc.path, resp.StatusCode, body)
		}
	}
	if mp.created != 0 || mp.dropped != 0 {
		t.Fatalf("provisioner ran (created=%d dropped=%d) for forged requests, want 0/0 — resources touched in victim's namespace!", mp.created, mp.dropped)
	}
}

// TestCreateKMSDegradePersistsNoPlaintext: with KMS unconfigured, a dedicated
// create returns the generated instance-admin password ONCE and persists NO
// plaintext — the row carries an empty secret_ref and the password appears in no
// stored column (it lives only in the runtime admin Secret the instance boots
// from). Exercised on the dedicated path (kv/sql/docdb/datastore) since those are
// the only kinds that mint a per-resource credential.
func TestCreateKMSDegradePersistsNoPlaintext(t *testing.T) {
	orch := newFakeOrch()
	s := newDedicatedSvc(t, orch)
	if s.State.sec.Enabled() {
		t.Fatal("precondition: KMS must be degraded for this test")
	}

	resp := postCreate(t, s, "datastore", "acme", "cache")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Password is returned exactly once on create under KMS-degrade, and it is the
	// exact credential the instance boots with (the admin Secret it reads).
	if cr.Password == "" {
		t.Fatal("expected password returned once on create under KMS-degrade")
	}
	inst := instanceName("datastore", "acme", "cache")
	sec := orch.secrets["tenant-acme/"+inst+"-admin"]
	if sec == nil {
		t.Fatalf("admin Secret not projected for the instance")
	}
	sd, _, _ := unstructured.NestedStringMap(sec.Object, "stringData")
	if sd["DATASTORE_PASSWORD"] != cr.Password {
		t.Fatalf("returned password must match the Secret the instance boots with")
	}

	// ...but NOTHING is persisted in plaintext.
	row, err := s.State.store.Get(context.Background(), "acme", "datastore", "cache")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.SecretRef != "" {
		t.Fatalf("secret_ref = %q, want empty under KMS-degrade", row.SecretRef)
	}
	for field, val := range map[string]string{
		"physical_name": row.PhysicalName, "secret_ref": row.SecretRef,
		"host": row.Host, "username": row.Username, "dbname": row.DBName, "status": row.Status,
	} {
		if val == cr.Password {
			t.Fatalf("plaintext password leaked into stored column %q", field)
		}
	}
}
