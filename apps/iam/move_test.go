package iam

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
)

// stubSQL stands an in-memory store in for hanzoai/sql, or an error for a server
// that does not answer.
func stubSQL(t *testing.T, db orm.DB, err error) {
	t.Helper()
	was := openSQL
	openSQL = func(string, string) (orm.DB, error) {
		if err != nil {
			return nil, err
		}
		return noClose{db}, nil
	}
	t.Cleanup(func() { openSQL = was })
}

// noClose keeps the stand-in open across the boots a test runs.
type noClose struct{ orm.DB }

func (noClose) Close() error { return nil }

func usersIn(t *testing.T, db orm.DB) []string {
	t.Helper()
	all, err := orm.TypedQuery[model.User](db).GetAll(context.Background())
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	var out []string
	for _, u := range all {
		out = append(out, u.Owner+"/"+u.Name)
	}
	return out
}

// THE MOVE IS THE FIRST SHARED BOOT: every identity record goes to the backend,
// the two compare equal, the file is marked moved and the backend serves. The
// file never serves again: a boot that asks for it is refused.
func TestTheFirstSharedBootMovesTheIdentities(t *testing.T) {
	dir := t.TempDir()
	seedIdentity(t, StorePath(dir), "hanzo", "z")
	shared := copyTestStore(t)
	stubSQL(t, shared, nil)
	t.Setenv(cloud.IAMStoreEnv, "sql")

	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("the first shared boot: %v", err)
	}
	if conn != nil {
		t.Fatal("the shared boot handed back the file's connection: the file serves")
	}
	if got := usersIn(t, db); len(got) != 1 || got[0] != "hanzo/z" {
		t.Fatalf("the backend serves %v, want [hanzo/z]", got)
	}
	m, ok := movedMark(dir)
	if !ok || m.Records["users"] != 1 || m.Backend != "sql" {
		t.Fatalf("mark = %+v, %v; want the move recorded with its one user", m, ok)
	}

	if _, _, err := openStore(dir); err != nil {
		t.Fatalf("a later shared boot: %v", err)
	}

	t.Setenv(cloud.IAMStoreEnv, "")
	if _, _, err := openStore(dir); err == nil || !strings.Contains(err.Error(), "moved to hanzoai/sql") {
		t.Fatalf("the frozen file was served after the move: %v", err)
	}
}

// A BACKEND THAT DOES NOT ANSWER MOVES NOTHING: the file still holds the
// identities, so it serves, and no mark is left.
func TestAnUnansweredMoveLeavesTheIdentitiesInTheFile(t *testing.T) {
	dir := t.TempDir()
	seedIdentity(t, StorePath(dir), "hanzo", "z")
	stubSQL(t, nil, errors.New("connection refused"))
	t.Setenv(cloud.IAMStoreEnv, "sql")

	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("an unanswered move refused the file too: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if conn == nil {
		t.Fatal("the file did not serve")
	}
	if got := usersIn(t, db); len(got) != 1 {
		t.Fatalf("the file serves %v, want its one user", got)
	}
	if _, ok := movedMark(dir); ok {
		t.Fatal("a move that did not happen left a mark")
	}
}

// THE WAY BACK: `iam copy --reverse` copies the backend into the file, proves them
// equal and lifts the mark, so the file serves again with the backend unset.
func TestTheReverseCopyFreesTheFile(t *testing.T) {
	dir := t.TempDir()
	seedIdentity(t, StorePath(dir), "hanzo", "z")
	shared := copyTestStore(t)
	stubSQL(t, shared, nil)
	t.Setenv(cloud.IAMStoreEnv, "sql")
	if _, _, err := openStore(dir); err != nil {
		t.Fatalf("move: %v", err)
	}

	t.Setenv(env.DataDirKey, dir)
	var out bytes.Buffer
	if err := RunCopy(context.Background(), []string{"--reverse"}, &out); err != nil {
		t.Fatalf("reverse: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(StorePath(dir) + movedSuffix); !os.IsNotExist(err) {
		t.Fatalf("the reverse left the mark: %v", err)
	}
	t.Setenv(cloud.IAMStoreEnv, "")
	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("the file does not serve after the reverse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if got := usersIn(t, db); len(got) != 1 || got[0] != "hanzo/z" {
		t.Fatalf("the file serves %v after the reverse", got)
	}
}

// lossy is a backend that answers every write and keeps none of the users.
type lossy struct{ orm.DB }

func (l lossy) Put(ctx context.Context, key orm.Key, src any) (orm.Key, error) {
	if key.Kind() == "users" {
		return key, nil
	}
	return l.DB.Put(ctx, key, src)
}

func (lossy) Close() error { return nil }

// A COPY THAT DOES NOT COMPARE EQUAL MOVES NOTHING: the comparison after the copy
// finds the user the backend dropped, the file stays the authority and serves, and
// no mark is left to say otherwise.
func TestAMoveThatDoesNotCompareEqualStays(t *testing.T) {
	dir := t.TempDir()
	seedIdentity(t, StorePath(dir), "hanzo", "z")
	was := openSQL
	shared := copyTestStore(t)
	openSQL = func(string, string) (orm.DB, error) { return lossy{shared}, nil }
	t.Cleanup(func() { openSQL = was })
	t.Setenv(cloud.IAMStoreEnv, "sql")

	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("a failed comparison refused the file too: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if conn == nil {
		t.Fatal("the backend serves after its comparison failed")
	}
	if got := usersIn(t, db); len(got) != 1 {
		t.Fatalf("the file serves %v, want its one user", got)
	}
	if _, ok := movedMark(dir); ok {
		t.Fatal("a move whose comparison failed left a mark")
	}
	if _, err := openShared(context.Background(), "sql", dir); !errors.Is(err, errStayed) || !strings.Contains(err.Error(), "missing users/") {
		t.Fatalf("the refusal does not name the record that differs: %v", err)
	}
}
