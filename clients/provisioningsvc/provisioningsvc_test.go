package provisioningsvc

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
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
		ID: "rs_" + name, Org: "acme", Kind: "databases", Name: name,
		PhysicalName: "org_acme_" + name, SecretRef: "org/acme/databases/" + name,
		Host: "sql.hanzo.svc", Port: 5432, Username: "org_acme_" + name,
		DBName: "org_acme_" + name, Status: "ready", CreatedAt: 1700000000,
	}
}

func TestStore_InsertGetListDelete(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.Get(ctx, "acme", "databases", "orders"); !errors.Is(err, errNotFound) {
		t.Fatalf("Get(missing) = %v, want errNotFound", err)
	}

	if err := s.Insert(ctx, sampleResource("orders")); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.Insert(ctx, sampleResource("events")); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := s.Get(ctx, "acme", "databases", "orders")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PhysicalName != "org_acme_orders" || got.Port != 5432 || got.Status != "ready" {
		t.Fatalf("Get returned wrong row: %+v", got)
	}

	rows, err := s.List(ctx, "acme", "databases")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("List len = %d, want 2", len(rows))
	}

	// Org isolation: another org sees nothing.
	other, err := s.List(ctx, "globex", "databases")
	if err != nil {
		t.Fatalf("List(globex): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-org leak: globex saw %d rows", len(other))
	}

	deleted, err := s.Delete(ctx, "acme", "databases", "orders")
	if err != nil || !deleted {
		t.Fatalf("Delete = (%v,%v), want (true,nil)", deleted, err)
	}
	if _, err := s.Get(ctx, "acme", "databases", "orders"); !errors.Is(err, errNotFound) {
		t.Fatalf("Get after delete = %v, want errNotFound", err)
	}
	deletedAgain, err := s.Delete(ctx, "acme", "databases", "orders")
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

func TestSanitizeOrgAndPhysicalName(t *testing.T) {
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

	if got := physicalName("acme", "my-db"); got != "org_acme_my_db" {
		t.Errorf("physicalName = %q, want org_acme_my_db", got)
	}
	// Identifier safety: the physical name carries only [a-z0-9_].
	pn := physicalName("acme-co", "orders-2")
	for _, r := range pn {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			t.Fatalf("physicalName produced unsafe char %q in %q", r, pn)
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

func TestBucketName(t *testing.T) {
	if got := bucketName("org_acme_my_db"); got != "org-acme-my-db" {
		t.Fatalf("bucketName = %q, want org-acme-my-db", got)
	}
}
