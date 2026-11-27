package kms

import (
	"encoding/base64"
	"errors"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	kmsstore "github.com/luxfi/kms/pkg/store"
	luxlog "github.com/luxfi/log"
)

// A store you cannot enumerate cannot be audited, rotated, or migrated, and
// reports a populated store as an empty one.
//
// Both failures were live. `WHERE path=? AND env=?` matched a coordinate
// exactly, so listing an org returned only what sat at its root and never a
// sub-path; and an omitted env was silently filled in with `default` while the
// fleet writes `prod`. Together they answered `total: 0` for a store holding
// every credential the fleet syncs — which is how a secret that was present the
// whole time was read as missing, and a service crash-looped for 18 hours next
// to it.
func TestFindEnumeratesTheWholeStore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := testStore(t)
		org := "/orgs/acme"

		// One secret at the org root, two nested a level down, one nested two
		// levels down, and one in a DIFFERENT environment.
		write(t, c, org, "prod", "ROOT_KEY")
		write(t, c, org+"/team", "prod", "SERVER_SECRET")
		write(t, c, org+"/team", "prod", "IAM_CLIENT_SECRET")
		write(t, c, org+"/team/nested", "prod", "DEEP_KEY")
		write(t, c, org+"/team", "staging", "STAGING_ONLY")

		t.Run("the org lists its whole subtree", func(t *testing.T) {
			got := names(t, c, org, "")
			want := []string{"DEEP_KEY", "IAM_CLIENT_SECRET", "ROOT_KEY", "SERVER_SECRET", "STAGING_ONLY"}
			assertNames(t, got, want)
		})

		t.Run("an omitted env means EVERY env, not a default", func(t *testing.T) {
			// The one that only exists in staging must appear. Defaulting the env is
			// what made a populated store read as empty.
			if slices.Contains(names(t, c, org, ""), "STAGING_ONLY") {
				return
			}
			t.Fatal("a secret in a non-default env was invisible to an unfiltered listing")
		})

		t.Run("a named env still filters", func(t *testing.T) {
			for _, n := range names(t, c, org, "staging") {
				if n != "STAGING_ONLY" {
					t.Fatalf("env filter leaked %q from another environment", n)
				}
			}
		})

		t.Run("a subtree root reaches its children", func(t *testing.T) {
			assertNames(t, names(t, c, org+"/team", "prod"),
				[]string{"DEEP_KEY", "IAM_CLIENT_SECRET", "SERVER_SECRET"})
		})
	})
}

// A path is a subtree root, not a string prefix: "team" must not reach
// "teamfoo", or an app's listing would pull in a neighbour's secrets.
func TestFindDoesNotMatchASiblingByPrefix(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := testStore(t)
		org := "/orgs/acme"
		write(t, c, org+"/team", "prod", "MINE")
		write(t, c, org+"/teamfoo", "prod", "NOT_MINE")

		for _, n := range names(t, c, org+"/team", "prod") {
			if n == "NOT_MINE" {
				t.Fatal("a subtree listing reached a sibling path that merely shares its prefix")
			}
		}
	})
}

// A coordinate is exact. A subtree root is compared as the literal string it is: LIKE's
// wildcards in a path do not broaden it, and case is not folded (SQLite's LIKE would
// fold it, PostgreSQL's does not, and a path is a case-sensitive coordinate everywhere
// else in this store).
func TestFindTreatsAPathAsALiteral(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := testStore(t)
		org := "/orgs/acme"
		write(t, c, org+"/team", "prod", "LOWER")
		write(t, c, org+"/Team", "prod", "UPPER")
		write(t, c, org+"/te_m", "prod", "UNDERSCORE")
		write(t, c, org+"/teXm", "prod", "OTHER")
		write(t, c, org+"/100%", "prod", "PERCENT")
		write(t, c, org+"/100x", "prod", "NOT_PERCENT")
		write(t, c, org+"/100%/deep", "prod", "PERCENT_DEEP")

		assertNames(t, names(t, c, org+"/team", "prod"), []string{"LOWER"})
		assertNames(t, names(t, c, org+"/Team", "prod"), []string{"UPPER"})
		assertNames(t, names(t, c, org+"/te_m", "prod"), []string{"UNDERSCORE"})
		assertNames(t, names(t, c, org+"/100%", "prod"), []string{"PERCENT", "PERCENT_DEEP"})
	})
}

// Text PostgreSQL would refuse (bytes that are not UTF-8) is refused at the boundary on
// both engines, so a coordinate that fits on one is not a different error on the other:
// nothing is stored under it, and a read of it finds nothing.
func TestACoordinateThatIsNotTextIsRefusedOnBothEngines(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		c := testStore(t)
		if err := c.Put("/orgs/acme", "K\xff", "prod", []byte("v")); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Put with bytes that are not UTF-8: %v, want ErrInvalidKey", err)
		}
		if err := c.Put("/orgs/acme/\xff", "K", "prod", []byte("v")); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Put under a path that is not UTF-8: %v, want ErrInvalidKey", err)
		}
		if _, err := c.Get("/orgs/acme", "K\xff", "prod"); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Get with bytes that are not UTF-8: %v, want ErrInvalidKey", err)
		}
		// The store itself holds the line too: it never stores such text and answers a
		// read of it as absent.
		if err := c.store.put(&kmsstore.Secret{Path: "/orgs/acme", Env: "prod", Name: "K\x00", Ciphertext: []byte("x"), WrappedDEK: []byte("y")}); !errors.Is(err, dialect.ErrText) {
			t.Fatalf("store.put of a NUL name: %v, want ErrText", err)
		}
		if _, err := c.store.get("/orgs/acme", "K\xff", "prod"); !errors.Is(err, kmsstore.ErrSecretNotFound) {
			t.Fatalf("store.get of bytes that are not UTF-8: %v, want not found", err)
		}
		if got, err := c.store.find(findQuery{Path: "/orgs/acme/\xff"}); err != nil || got != nil {
			t.Fatalf("store.find under a path that is not UTF-8: %v %v, want nothing", got, err)
		}
		if err := c.Delete("/orgs/acme", "K\xff", "prod"); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Delete with bytes that are not UTF-8: %v, want ErrInvalidKey", err)
		}
	})
}

func testStore(t *testing.T) *Client {
	t.Helper()
	c, err := New(Config{
		DataDir:      t.TempDir(),
		MasterKeyB64: base64.StdEncoding.EncodeToString(testMaster),
	}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	return c
}

func write(t *testing.T, c *Client, path, env, name string) {
	t.Helper()
	if err := c.Put(path, name, env, []byte("v:"+name)); err != nil {
		t.Fatalf("put %s/%s@%s: %v", path, name, env, err)
	}
}

func names(t *testing.T, c *Client, path, env string) []string {
	t.Helper()
	metas, err := c.Find(path, env)
	if err != nil {
		t.Fatalf("Find(%q,%q): %v", path, env, err)
	}
	out := make([]string, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.Name)
	}
	return out
}

func assertNames(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("got %v, missing %q", got, w)
		}
	}
}
