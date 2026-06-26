package provisioningsvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
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

// newTestSvc builds a svc with a temp store, KMS-degraded secrets (no env),
// and a mock provisioner under each given kind.
func newTestSvc(t *testing.T, kinds ...string) (*svc, *mockProv) {
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
	return &svc{store: newTestStore(t), sec: openSecrets("hanzo", log), reg: reg, log: log}, mp
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
	b.PhysicalName = a.PhysicalName      // but collides on physical_name
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

func TestSanitizeOrg(t *testing.T) {
	cases := map[string]string{
		"acme":                  "acme",
		"Acme Corp":             "acme-corp",
		" hanzo ":               "hanzo",
		"a@b.c":                 "a-b-c",
		"--weird--":             "weird",
		strings.Repeat("z", 50): strings.Repeat("z", 32),
	}
	for in, want := range cases {
		if got := sanitizeOrg(in); got != want {
			t.Errorf("sanitizeOrg(%q) = %q, want %q", in, got, want)
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
	app.Post("/v1/sql", s.create("sql"))

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

// TestCreateKMSDegradePersistsNoPlaintext: with KMS unconfigured, create returns
// the generated password ONCE and persists NO plaintext (stored row carries an
// empty secret_ref and the password appears in no stored column).
func TestCreateKMSDegradePersistsNoPlaintext(t *testing.T) {
	s, mp := newTestSvc(t, "kv")
	if s.sec.Enabled() {
		t.Fatal("precondition: KMS must be degraded for this test")
	}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Post("/v1/kv", s.create("kv"))

	req, _ := http.NewRequest("POST", "/v1/kv", strings.NewReader(`{"name":"cache"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 201", resp.StatusCode, body)
	}
	var cr createResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Password is returned exactly once on create under KMS-degrade...
	if cr.Password == "" {
		t.Fatal("expected password returned once on create under KMS-degrade")
	}
	if cr.Password != mp.gotPw {
		t.Fatalf("returned password %q != password handed to backend %q", cr.Password, mp.gotPw)
	}

	// ...but NOTHING is persisted in plaintext.
	row, err := s.store.Get(context.Background(), "acme", "kv", "cache")
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
