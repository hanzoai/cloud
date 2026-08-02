package agents

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// TestOnlyTenancyResolvesAStore is the structural half of the isolation
// argument. Isolation is now the FILE an org's records live in, which means the
// whole question is "what names the file". If only tenancy.go can name one, then
// reading tenancy.go is enough to know the answer for the entire package — and
// no future handler can quietly resolve a store from a path segment, a query
// value or a request body, which is the same bug in a new costume.
//
// It is a source assertion because that is the only kind that survives code
// nobody has written yet.
func TestOnlyTenancyResolvesAStore(t *testing.T) {
	resolve := regexp.MustCompile(`\.stores\.(For|Each|Has)\(`)
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "tenancy.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if loc := resolve.FindIndex(b); loc != nil {
			line := 1 + strings.Count(string(b[:loc[0]]), "\n")
			t.Errorf("%s:%d resolves a store outside tenancy.go — every store must come "+
				"from storeFor/tenantStore/mountedStore/storeForPublic so the org naming "+
				"the file is provably the validated one", name, line)
		}
	}
}

// TestOrgsGetDistinctFiles proves the boundary is physical: two orgs resolve to
// two different SQLite files, and a row written through one is not merely
// filtered out of the other's reads — it is not in the other's database at all.
//
// The second assertion is the one that matters. It reads acme's row back through
// evil's store while PASSING acme's own org to the query, so the org predicate
// cannot be what makes it fail. Only the file can.
func TestOrgsGetDistinctFiles(t *testing.T) {
	st := &state{stores: testStores(t)}
	ctx := context.Background()

	acme := storeOf(t, st, "acme")
	evil := storeOf(t, st, "evil")
	if acme == evil {
		t.Fatal("two orgs resolved to the SAME store handle")
	}
	if err := acme.CreateSession(ctx, mkSession("acme", "sess_a", "", "sess_a")); err != nil {
		t.Fatalf("create in acme: %v", err)
	}
	// The org predicate is deliberately satisfied here: we ask evil's database
	// for acme's session using acme's own org. It is absent because it was never
	// written to this file.
	if _, err := evil.GetSession(ctx, "acme", "sess_a"); err != errSessionNotFound {
		t.Fatalf("acme's session reachable from evil's file with acme's own org: %v", err)
	}
	if _, err := acme.GetSession(ctx, "acme", "sess_a"); err != nil {
		t.Fatalf("acme cannot read its own session: %v", err)
	}
}

// TestPublicBuildNeverMintsAStore covers the one read whose org comes off the
// wire from an unauthenticated caller. storeFor CREATES a file on first touch,
// which is right for a validated principal and would let a stranger mint a
// directory and an open handle per name they invent. The public build route must
// therefore go through storeForPublic, which refuses to materialise anything.
func TestPublicBuildNeverMintsAStore(t *testing.T) {
	dir := t.TempDir()
	st := &state{stores: cloud.NewOrgStore[*Store](cloud.Base{DataDir: dir}, "agents", openStore)}
	t.Cleanup(func() { _ = st.stores.CloseAll() })

	if _, ok := st.storeForPublic("a-stranger-invented-this"); ok {
		t.Fatal("storeForPublic resolved a store for an org that has none")
	}
	orgs := filepath.Join(dir, "orgs")
	if ents, err := os.ReadDir(orgs); err == nil && len(ents) > 0 {
		t.Fatalf("a public read created %d org director(ies) under %s", len(ents), orgs)
	}
	// A real org's store IS reachable once it exists.
	if _, err := st.storeFor("acme"); err != nil {
		t.Fatalf("storeFor acme: %v", err)
	}
	if _, ok := st.storeForPublic("acme"); !ok {
		t.Fatal("storeForPublic must reach an org whose store already exists")
	}
}

// TestMountedStoreRefusesAnInvalidOrg keeps the in-process seams fail-closed.
// Their contract is that the caller resolved the org server-side; an empty or
// oversized org could not have come from a validated principal, so it is refused
// before it can reach the filesystem.
func TestMountedStoreRefusesAnInvalidOrg(t *testing.T) {
	prev := mounted
	mounted = &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}, State: state{stores: testStores(t)}}
	t.Cleanup(func() { mounted = prev })

	for _, org := range []string{"", "   ", strings.Repeat("a", 4096)} {
		if _, _, err := mountedStore(org); err == nil {
			t.Errorf("mountedStore(%q) must fail closed", truncate(org))
		}
	}
	if _, _, err := mountedStore("acme"); err != nil {
		t.Fatalf("mountedStore(acme): %v", err)
	}
}

func truncate(s string) string {
	if len(s) > 20 {
		return s[:20] + "…"
	}
	return s
}
