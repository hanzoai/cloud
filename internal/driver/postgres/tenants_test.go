package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/locus"
)

// Every spelling the estate stores one PostgreSQL URL in is read as the URL pgx
// dials, and nothing that is not one is guessed at.
func TestAdminURLReadsEverySpellingOfOneURL(t *testing.T) {
	for in, want := range map[string]string{
		"postgres://hanzo:pw@hanzo-sql:5432/hanzo":           "postgres://hanzo:pw@hanzo-sql:5432/hanzo",
		"postgresql://hanzo:pw@hanzo-sql:5432/hanzo":         "postgres://hanzo:pw@hanzo-sql:5432/hanzo",
		"postgresql+asyncpg://hanzo:pw@hanzo-sql:5432/hanzo": "postgres://hanzo:pw@hanzo-sql:5432/hanzo",
		" postgres://u:p@db/x?sslmode=disable ":              "postgres://u:p@db/x?sslmode=disable",
	} {
		u, ok := adminURL(in)
		if !ok || u.String() != want {
			t.Errorf("adminURL(%q) = %v %v, want %s", in, u, ok, want)
		}
	}
	for _, in := range []string{"placeholder-623c26b3074d3955", "host=db user=u", "mysql://u:p@db/x", "postgres:///x", "postgres://db/x"} {
		if u, ok := adminURL(in); ok {
			t.Errorf("adminURL(%q) = %v, want refused", in, u)
		}
	}
}

// A deployment on hanzo-sql that has not named the administrator does not get a SQLite file
// private to one pod for rows every replica must share: the store refuses to open, and the
// error names the variable to set.
func TestOpenRefusesAHanzoSQLDeploymentWithoutTheAdministrator(t *testing.T) {
	t.Setenv(locus.Env, "postgres://cloud:pw@hanzo-sql:5432/hanzo")
	t.Setenv(AdminEnv, "")
	dir := t.TempDir()
	db, closeDB, err := locus.Tenants(context.Background(), locus.Tenancy{Name: "library", DataDir: dir})
	if err == nil {
		_ = closeDB()
		t.Fatalf("a store opened on %v with %s unset", db, AdminEnv)
	}
	if !strings.Contains(err.Error(), AdminEnv) || !strings.Contains(err.Error(), locus.Env) {
		t.Fatalf("the refusal does not name the variables: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a refused open left %d files in the data directory", len(entries))
	}
}
