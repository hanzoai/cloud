package commerce

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	cstore "github.com/hanzoai/commerce/store"
)

// A Base the module opened on files is moved to two schemas of hanzo-sql once, every
// value it held is read back through the server, and what it writes after is the
// server's: the old files are left as they were.
func TestCommerceBaseMovesOffItsFiles(t *testing.T) {
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "lazy")
	dir := t.TempDir()
	legacy := filepath.Join(dir, "base")

	old, err := cstore.New(cstore.Config{DataDir: legacy})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"a": "one", "b": "two"} {
		if err := old.KV.Set(k, []byte(v), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(nil); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	host, err := placeBase(ctx, nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	if host.Data == nil || host.Aux == nil {
		t.Fatalf("the Base stayed on its files: %+v", host)
	}
	s, err := cstore.New(cstore.Config{DataDir: t.TempDir(), Host: host})
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"a": "one", "b": "two"} {
		got, err := s.KV.Get(k)
		if err != nil || string(got) != v {
			t.Fatalf("%s = %q, %v; the move lost it", k, got, err)
		}
	}
	if err := s.KV.Set("c", []byte("three"), 0); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(nil); err != nil {
		t.Fatal(err)
	}

	// Placed again, it is the server's and moves nothing: what was written there stays.
	host, err = placeBase(ctx, nil, dir)
	if err != nil || host.Data == nil {
		t.Fatalf("second placement: %+v, %v", host, err)
	}
	s, err = cstore.New(cstore.Config{DataDir: t.TempDir(), Host: host})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(nil)
	if got, err := s.KV.Get("c"); err != nil || string(got) != "three" {
		t.Fatalf("c = %q, %v", got, err)
	}

	// The files are the rollback: they are where they were and hold the old values only.
	f, err := cstore.New(cstore.Config{DataDir: legacy})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(nil)
	if got, err := f.KV.Get("a"); err != nil || string(got) != "one" {
		t.Fatalf("the old file: a = %q, %v", got, err)
	}
	if _, err := f.KV.Get("c"); err == nil {
		t.Fatal("what the server took was written to the file too")
	}
}

// Without the policy to move, or without a server, the module keeps its files.
func TestCommerceBaseStaysOnItsFilesWithoutAMove(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(locus.Env, "")
	if h, err := placeBase(context.Background(), nil, dir); err != nil || h.Data != nil || h.Aux != nil || h.Files != nil {
		t.Fatalf("no server: %+v, %v", h, err)
	}

	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "")
	old, err := cstore.New(cstore.Config{DataDir: filepath.Join(dir, "base")})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(nil); err != nil {
		t.Fatal(err)
	}
	if h, err := placeBase(context.Background(), nil, dir); err != nil || h.Data != nil || h.Aux != nil {
		t.Fatalf("no move policy: %+v, %v", h, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "base", "data.db")); err != nil {
		t.Fatal(err)
	}
}
