package storagelock

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckEnvCleanReturnsNil(t *testing.T) {
	if err := CheckEnv(func(string) string { return "" }); err != nil {
		t.Fatalf("expected nil for empty env, got %v", err)
	}
}

func TestCheckEnvNilLookup(t *testing.T) {
	if err := CheckEnv(nil); err != nil {
		t.Fatalf("expected nil for nil lookup, got %v", err)
	}
}

func TestCheckEnvDatabaseURLRejected(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://cloud:secret@postgres.hanzo.svc:5432/hanzo_cloud?sslmode=disable",
	}
	err := CheckEnv(mapLookup(env))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrPostgresForbidden) {
		if !strings.Contains(err.Error(), ErrPostgresForbidden.Error()) {
			t.Fatalf("expected wrapped ErrPostgresForbidden, got %v", err)
		}
	}
	if !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Errorf("expected error to mention DATABASE_URL, got %v", err)
	}
	if !strings.Contains(err.Error(), "Postgres DSN") {
		t.Errorf("expected error to classify as Postgres DSN, got %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("expected password to be redacted, got %v", err)
	}
}

func TestCheckEnvLegacyDriverNamePostgresRejected(t *testing.T) {
	env := map[string]string{
		"driverName": "postgres",
	}
	err := CheckEnv(mapLookup(env))
	if err == nil {
		t.Fatal("expected error for driverName=postgres")
	}
	if !strings.Contains(err.Error(), "driverName") {
		t.Errorf("expected driverName in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "legacy Python driver pin") {
		t.Errorf("expected legacy Python driver classification, got %v", err)
	}
}

func TestCheckEnvDriverNameSqliteIgnored(t *testing.T) {
	// driverName=sqlite is an intentional transition signal — must not
	// crash the pod.
	env := map[string]string{"driverName": "sqlite"}
	if err := CheckEnv(mapLookup(env)); err != nil {
		t.Errorf("expected nil for driverName=sqlite, got %v", err)
	}
}

func TestCheckEnvLegacyDBNameHanzoCloudRejected(t *testing.T) {
	env := map[string]string{"dbName": "hanzo_cloud"}
	err := CheckEnv(mapLookup(env))
	if err == nil {
		t.Fatal("expected error for dbName=hanzo_cloud")
	}
	if !strings.Contains(err.Error(), "legacy Python db pin") {
		t.Errorf("expected legacy Python db pin classification, got %v", err)
	}
}

func TestCheckEnvDBNameOtherIgnored(t *testing.T) {
	env := map[string]string{"dbName": "something_else"}
	if err := CheckEnv(mapLookup(env)); err != nil {
		t.Errorf("expected nil for dbName=something_else, got %v", err)
	}
}

func TestCheckEnvCloudDatabaseURLRejected(t *testing.T) {
	env := map[string]string{
		"CLOUD_DATABASE_URL": "postgresql://u:p@h:5432/d",
	}
	err := CheckEnv(mapLookup(env))
	if err == nil {
		t.Fatal("expected error for CLOUD_DATABASE_URL")
	}
}

func TestCheckEnvMultipleViolationsAllReported(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgresql://u:p@h:5432/d",
		"driverName":   "postgres",
		"dbName":       "hanzo_cloud",
	}
	err := CheckEnv(mapLookup(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, k := range []string{"DATABASE_URL", "driverName", "dbName"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("expected %s in error, got %v", k, err)
		}
	}
}

func TestCheckEnvWhitespaceTrimmed(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "   "}
	if err := CheckEnv(mapLookup(env)); err != nil {
		t.Fatalf("expected nil for whitespace-only value, got %v", err)
	}
}

func TestIsPostgres(t *testing.T) {
	cases := map[string]bool{
		"postgres://u@h/d":     true,
		"postgresql://u@h/d":   true,
		"  POSTGRES://U@H/D  ": true,
		"sqlite:///data/x.db":  false,
		"file:///data/x.db":    false,
		"":                     false,
	}
	for v, want := range cases {
		if got := IsPostgres(v); got != want {
			t.Errorf("IsPostgres(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestRedactStripsCredentials(t *testing.T) {
	in := "postgres://cloud:supersecret@host:5432/db?sslmode=disable"
	out := redact(in)
	if strings.Contains(out, "supersecret") {
		t.Errorf("redact did not strip password: %s", out)
	}
	if !strings.HasPrefix(out, "postgres://***@") {
		t.Errorf("expected postgres://***@ prefix, got %s", out)
	}
}

// mapLookup builds a lookup function from a map literal so tests don't
// have to mutate os.Environ.
func mapLookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}
