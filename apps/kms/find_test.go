package kms

import (
	"encoding/base64"
	"testing"

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
		for _, n := range names(t, c, org, "") {
			if n == "STAGING_ONLY" {
				return
			}
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
}

// A path is a subtree root, not a string prefix: "team" must not reach
// "teamfoo", or an app's listing would pull in a neighbour's secrets.
func TestFindDoesNotMatchASiblingByPrefix(t *testing.T) {
	c := testStore(t)
	org := "/orgs/acme"
	write(t, c, org+"/team", "prod", "MINE")
	write(t, c, org+"/teamfoo", "prod", "NOT_MINE")

	for _, n := range names(t, c, org+"/team", "prod") {
		if n == "NOT_MINE" {
			t.Fatal("a subtree listing reached a sibling path that merely shares its prefix")
		}
	}
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
