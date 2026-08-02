// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/iam/pkg/schema"
)

// The key doors, over the wire that production actually uses.
//
// The resolver reads the identity store DIRECTLY when it happens to be in the
// process that owns it — which is true in a single-binary deploy, in every unit
// test, and in exactly one of the fleet's processes. It is FALSE in all the
// others: each app is its own composition root, calls cloud.Serve, and gets the
// identity boundary with it, so the process authenticating a key to /v1/chat/
// completions is not the process holding the store.
//
// A test that only seeds a store therefore proves the branch that does not run in
// production. These tests take the store AWAY after mounting and drive the whole
// public door — cloud.OrgForKey — so what is exercised is the plane hop: dial the
// iam app's socket, invoke the op, decode the answer.

// seeded opens an identity store and seeds one user with an hk- key plus one
// publishable key, then returns the db.
func seeded(t *testing.T) orm.DB {
	t.Helper()
	_ = schema.Kinds()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "iam.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	u := orm.New[schema.User](db)
	u.Owner, u.Name, u.Email, u.AccessKey = "acme", "alice", "alice@acme.test", "hk-live-ALICE"
	u.SetId("acme/alice")
	if err := u.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	k := orm.New[schema.Key](db)
	k.Owner, k.Name, k.User = "acme", "web", "acme/alice"
	k.AccessKey, k.Scope = "pk-live-WEB", schema.KeyScopePublish
	k.SetId("acme/web")
	if err := k.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	return db
}

// servePlane publishes the key ops and binds the iam app's socket, so a caller in
// this process reaches them the same way a sibling process does.
func servePlane(t *testing.T, db orm.DB) {
	t.Helper()
	// The plane's sockets live in the fleet's shared runtime dir, which is
	// /run/hanzo on a node and must be a writable one here. ZIP_RUNTIME_DIR is what
	// BindRuntimeDir already resolved to, so it is also what has to be cleared: a
	// second test would otherwise bind into the first test's deleted TempDir.
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())
	cloud.ResetPlane()
	embeddedDB = db
	t.Cleanup(func() { embeddedDB = nil; cloud.ResetPlane() })

	exposeKeys()
	stop, err := cloud.ServePlane("iam", luxlog.New("iamtest"))
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
}

// A SECRET key resolves to its owner org across the plane — the path every
// non-iam process takes, and the one an in-process store test cannot reach.
func TestResolveKeyOverThePlane(t *testing.T) {
	db := seeded(t)
	servePlane(t, db)

	// The store is NOT in this process, exactly as it is not in the ai, agents or
	// gateway process. The only way to an answer is the socket.
	cloud.SetIAMStore(nil)

	org, ok := cloud.OrgForKey(context.Background(), "hk-live-ALICE")
	if !ok || org != "acme" {
		t.Fatalf("secret key over the plane = (%q,%v), want (acme,true) — this is every non-iam process", org, ok)
	}
}

// A PUBLISHABLE key resolves to its org across the plane, and to nothing else.
func TestResolveOrgOverThePlane(t *testing.T) {
	db := seeded(t)
	servePlane(t, db)
	cloud.SetIAMStore(nil)

	org, ok := cloud.OrgForKey(context.Background(), "pk-live-WEB")
	if !ok || org != "acme" {
		t.Fatalf("publishable key over the plane = (%q,%v), want (acme,true)", org, ok)
	}
	// The org-only door stays org-only across the wire: the pk- must not come back
	// as a principal, which is what keeps a browser key from becoming a read grant.
	if reason, ok := cloud.RefusalForKey(context.Background(), "pk-live-WEB"); ok || reason != "" {
		t.Fatalf("a publishable key resolved a principal over the plane: (%q,%v)", reason, ok)
	}
}

// An unknown key resolves to nobody, and the REASON survives the hop — so a holder
// whose key was revoked is told that, not "the entity does not exist".
func TestRefusalSurvivesThePlane(t *testing.T) {
	db := seeded(t)
	servePlane(t, db)
	cloud.SetIAMStore(nil)

	if org, ok := cloud.OrgForKey(context.Background(), "hk-live-NOSUCHKEY"); ok || org != "" {
		t.Fatalf("unknown key over the plane resolved (%q,%v), want (\"\",false)", org, ok)
	}
	reason, ok := cloud.RefusalForKey(context.Background(), "hk-live-NOSUCHKEY")
	if ok || reason != "key_unknown" {
		t.Fatalf("refusal over the plane = (%q,%v), want (key_unknown,false)", reason, ok)
	}
}

// With no iam app reachable at all, a key resolves to nothing — anonymous, never a
// fabricated tenant. This is the fleet-has-no-identity case, and it must fail
// closed rather than error out of the auth path.
//
// The key is unique to this test on purpose: OrgForKey answers from the process-wide
// resolver's 60s cache, so a key another test already resolved would be answered
// from that cache and this would pass without reaching the plane at all.
func TestNoIAMReachableResolvesNothing(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", t.TempDir())
	cloud.ResetPlane()
	cloud.SetIAMStore(nil)
	t.Cleanup(cloud.ResetPlane)

	if org, ok := cloud.OrgForKey(context.Background(), "hk-live-NOBODY-HOME"); ok || org != "" {
		t.Fatalf("with no identity reachable, a key resolved (%q,%v) — it must stay anonymous", org, ok)
	}
}

// The handlers themselves fail CLOSED on a store this process was supposed to own:
// answering "unresolved" would turn a boot-order fault into a silent, fleet-wide
// de-authentication of every API key.
func TestHandlersRefuseWithoutTheirOwnStore(t *testing.T) {
	embeddedDB = nil
	if _, err := resolveKey(context.Background(), &plane.KeyRef{Key: "hk-x"}); err == nil {
		t.Error("resolve-key answered without the store it owns — an outage must not read as a bad key")
	}
	if _, err := resolveOrg(context.Background(), &plane.KeyRef{Key: "pk-x"}); err == nil {
		t.Error("resolve-org answered without the store it owns")
	}
}
