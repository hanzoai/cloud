package iam

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
)

func copyTestStore(t *testing.T) orm.DB {
	t.Helper()
	_ = schema.Kinds()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{Path: filepath.Join(t.TempDir(), "iam.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// A copy reports what it moved and ends equal; a check of two stores that differ
// names the record and fails.
func TestCopyStoresMovesAndProves(t *testing.T) {
	ctx := context.Background()
	src, dst := copyTestStore(t), copyTestStore(t)
	u := orm.New[schema.User](src)
	u.Owner, u.Name, u.Email = "hanzo", "ada", "ada@hanzo.test"
	u.SetId("hanzo/ada")
	if err := u.CreateCtx(ctx); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := copyStores(ctx, src, dst, "file", "sql", true, false, &out); err == nil ||
		!strings.Contains(out.String(), "missing users/hanzo/ada") {
		t.Fatalf("a check of unequal stores passed: %v\n%s", err, out.String())
	}

	out.Reset()
	if err := copyStores(ctx, src, dst, "file", "sql", false, true, &out); err != nil {
		t.Fatalf("copy: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "hold the same identity records") {
		t.Fatalf("copy did not prove the stores equal:\n%s", out.String())
	}
	got, err := orm.Get[schema.User](dst, "hanzo/ada")
	if err != nil || got.Email != "ada@hanzo.test" {
		t.Fatalf("target holds %+v (%v)", got, err)
	}
}
