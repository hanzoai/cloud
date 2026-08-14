package sync

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/hanzoai/sqlite"
)

// store_test.go covers the two tables the forge cannot hold — what an advance
// did, and where a repository replicates to — and in particular what happens to
// a file written before either of them knew about the ACCOUNT.

// memStore opens an in-memory store, so a case here is about the schema and
// nothing else.
func memStore(t *testing.T) *store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st, err := openStore(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return st
}

// TestTheOldShapeIsDroppedRatherThanCarried: a file written before the account
// was part of a repository's identity holds rows that cannot be attributed to
// one — they ARE the collision, written down. So they go, and both tables are
// re-derived: an outcome by the next advance, a mirror by the import or the sync
// that declares it. Carried forward under a guessed account, a mirror row would
// keep pushing one account's refs at another account's repository.
func TestTheOldShapeIsDroppedRatherThanCarried(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	// The pre-account shape, and a row in each table.
	if _, err := db.Exec(`
CREATE TABLE outcome (org TEXT, repo TEXT, ref TEXT, conflict TEXT, at INTEGER, PRIMARY KEY (org,repo,ref));
CREATE TABLE mirror  (org TEXT, repo TEXT, host TEXT, url TEXT, PRIMARY KEY (org,repo,host));
INSERT INTO outcome VALUES ('acme','ai','refs/heads/main','',10);
INSERT INTO mirror  VALUES ('acme','ai','github.com','https://github.com/hanzo-apps/ai.git');
`); err != nil {
		t.Fatal(err)
	}

	st, err := openStore(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	states, err := st.States(ctx, "acme")
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("a row that names no account survived: %+v", states)
	}
	// The one that matters: an outbound target nobody can attribute is gone,
	// rather than pointed at whichever repository asks first.
	for _, r := range []repo{{account: "hanzoai", name: "ai"}, {account: "", name: "ai"}} {
		urls, err := st.Mirrors(ctx, "acme", r)
		if err != nil {
			t.Fatalf("mirrors: %v", err)
		}
		if len(urls) != 0 {
			t.Errorf("%s inherited an unattributed target: %v", r.flat(), urls)
		}
	}

	// And a SECOND open keeps what the first one wrote — the drop is about the
	// old shape, not about every boot.
	kept := repo{account: "hanzoai", name: "ai"}
	if err := st.SetMirror(ctx, "acme", kept, "github.com", "https://github.com/hanzoai/ai.git"); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(db); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	urls, err := st.Mirrors(ctx, "acme", kept)
	if err != nil {
		t.Fatalf("mirrors: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("re-opening the store dropped a live target: %v", urls)
	}
}

// TestOneRowPerDestination: an advance to the forge and an advance to a replica
// are different facts about the same ref, so neither can clear the other — and
// the console's roll-up still folds them into the one word it renders.
//
// Sharing a row, a clean inbound advance would report a replica that has
// diverged as in step, which is a green console over a split that is still
// there.
func TestOneRowPerDestination(t *testing.T) {
	st := memStore(t)
	ctx := context.Background()
	r := repo{account: "hanzoai", name: "ai"}

	if err := st.Record(ctx, "acme", r, mainRef, "github.com", "the replica has diverged", 20); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(ctx, "acme", r, mainRef, canonical, "", 30); err != nil {
		t.Fatal(err)
	}
	states, err := st.States(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !states[r].Conflict {
		t.Error("a clean advance INTO the forge cleared a replica's unresolved divergence")
	}
	if states[r].At != 30 {
		t.Errorf("last advance = %d, want 30", states[r].At)
	}

	// Resolving is the same write, per destination.
	if err := st.Record(ctx, "acme", r, mainRef, "github.com", "", 40); err != nil {
		t.Fatal(err)
	}
	if states, _ = st.States(ctx, "acme"); states[r].Conflict {
		t.Error("the conflict survived the advance that resolved it")
	}
}

// TestTwoAccountsKeepTheirOwnRows: every key in this file carries the account,
// so a repository name shared by two accounts is two rows and not one.
func TestTwoAccountsKeepTheirOwnRows(t *testing.T) {
	st := memStore(t)
	ctx := context.Background()
	a := repo{account: "hanzoai", name: "ai"}
	b := repo{account: "hanzo-apps", name: "ai"}

	if err := st.SetMirror(ctx, "acme", a, "github.com", "https://github.com/hanzoai/ai.git"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMirror(ctx, "acme", b, "github.com", "https://github.com/hanzo-apps/ai.git"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		r    repo
		want string
	}{
		{a, "https://github.com/hanzoai/ai.git"},
		{b, "https://github.com/hanzo-apps/ai.git"},
	} {
		urls, err := st.Mirrors(ctx, "acme", tc.r)
		if err != nil {
			t.Fatal(err)
		}
		if len(urls) != 1 || urls[0] != tc.want {
			t.Fatalf("%s targets %v, want [%s] — the other account took its row", tc.r.flat(), urls, tc.want)
		}
	}

	if err := st.Record(ctx, "acme", a, mainRef, canonical, "diverged", 10); err != nil {
		t.Fatal(err)
	}
	if err := st.Record(ctx, "acme", b, mainRef, canonical, "", 10); err != nil {
		t.Fatal(err)
	}
	states, err := st.States(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if !states[a].Conflict || states[b].Conflict {
		t.Fatalf("one account's conflict is the other's too: %+v %+v", states[a], states[b])
	}
}
