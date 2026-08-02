// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package cloud

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/iam/pkg/schema"
	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
)

// These tests drive the resolver against a REAL IAM store, because that is now what
// the resolver reads. They used to stand up an httptest server and assert on URL
// paths and JSON envelopes — a fair test of an HTTP client, and no test at all of key
// resolution: it could not see that a pk- must not resolve to a principal, or that an
// sk- naming a foreign user is refused, because the stub answered whatever the test
// told it to. Seeding rows exercises the same queries production runs.

// store opens an empty IAM store and mounts it for the resolver, exactly as
// apps/iam.Mount does, unmounting on cleanup so no test leaks a store into the next.
func store(t *testing.T) orm.DB {
	t.Helper()
	_ = schema.Kinds()
	db, err := orm.OpenSQLite(&ormdb.SQLiteDBConfig{
		Path:   filepath.Join(t.TempDir(), "iam.db"),
		Config: ormdb.SQLiteConfig{BusyTimeout: 5000, JournalMode: "WAL"},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	SetIAMStore(db)
	t.Cleanup(func() { SetIAMStore(nil); _ = db.Close() })
	return db
}

// user seeds the principal an hk-/sk- key resolves to.
func user(t *testing.T, db orm.DB, owner, name, email, hk string, admin bool) {
	t.Helper()
	u := orm.New[schema.User](db)
	u.Owner, u.Name, u.Email, u.AccessKey, u.IsAdmin = owner, name, email, hk, admin
	u.SetId(owner + "/" + name)
	if err := u.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed user %s/%s: %v", owner, name, err)
	}
}

// key seeds a schema.Key credential: `pk` is the publishable half, `sk` the
// confidential one, `scope` empty for a full secret key or schema.KeyScopePublish
// for a write-only browser key.
func key(t *testing.T, db orm.DB, owner, name, forUser, pk, sk, scope string) *schema.Key {
	t.Helper()
	k := orm.New[schema.Key](db)
	k.Owner, k.Name, k.User = owner, name, forUser
	k.AccessKey, k.AccessSecret, k.Scope = pk, sk, scope
	k.SetId(owner + "/" + name)
	if err := k.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed key %s/%s: %v", owner, name, err)
	}
	return k
}

// keys returns a resolver with all three caches live. Every test builds its own, so a
// cached answer never crosses a test boundary.
func keys() *iamKeys { return newIAMKeys() }

// A resolved key yields the SAME idClaims a JWT for that user yields, so
// SanitizeIdentity mints identical headers for a key and a session.
func TestIAMKeysLookup(t *testing.T) {
	db := store(t)
	user(t, db, "hanzo", "z", "z@hanzo.ai", "hk-abc123", true)

	c := keys().resolve(context.Background(), "hk-abc123")
	if c == nil {
		t.Fatal("resolve returned nil for a valid key")
	}
	if c.Owner != "hanzo" || c.Name != "z" || c.Email != "z@hanzo.ai" || !c.IsAdmin {
		t.Fatalf("claims = %+v, want owner=hanzo name=z email=z@hanzo.ai isAdmin=true", c)
	}
	// userID falls through to name (a key has no UUID subject) — the owner/name path
	// IAM's privileged lookups expect.
	if c.userID() != "z" || c.username() != "z" {
		t.Errorf("userID=%q username=%q, want both z", c.userID(), c.username())
	}
	// The org came from the SUBJECT (the resolved row's owner), not from a claim the
	// caller could choose — which is what lets homeOrg trust it.
	if c.subjectOrg != "hanzo" || c.homeOrg() != "hanzo" {
		t.Errorf("subjectOrg=%q homeOrg=%q, want hanzo from the resolved subject", c.subjectOrg, c.homeOrg())
	}
}

// The sk- confidential half resolves through its Key row to the user that key names,
// pinned to the key's own tenant.
func TestIAMKeysLookupSecretHalf(t *testing.T) {
	db := store(t)
	user(t, db, "acme", "alice", "alice@acme.test", "", false)
	key(t, db, "acme", "alice-key", "acme/alice", "pk-live-ACME", "sk-live-ACME", "")

	c := keys().resolve(context.Background(), "sk-live-ACME")
	if c == nil || c.Owner != "acme" || c.Name != "alice" {
		t.Fatalf("sk- resolved %+v, want acme/alice", c)
	}
}

// With IAM unmounted the resolver resolves nothing, so an API key stays anonymous
// rather than mis-resolved — and NO refusal is recorded, because "IAM is not here" is
// not a statement about the credential.
func TestIAMKeysUnmounted(t *testing.T) {
	SetIAMStore(nil)
	k := keys()
	if c := k.resolve(context.Background(), "hk-abc"); c != nil {
		t.Fatalf("unmounted resolver returned %+v, want nil", c)
	}
	if r := k.refusal(context.Background(), "hk-abc"); r != "" {
		t.Fatalf("unmounted resolver recorded refusal %q — an absent store is not a bad key", r)
	}
}

// An unknown key resolves to nil — a bad key never grants trust.
func TestIAMKeysUnknown(t *testing.T) {
	store(t)
	if c := keys().resolve(context.Background(), "hk-bad"); c != nil {
		t.Fatalf("unknown key resolved to %+v, want nil", c)
	}
}

// The cache serves a resolved key without touching the store again, and caches a miss
// too, so a bad key cannot hammer the store.
func TestIAMKeysCache(t *testing.T) {
	db := store(t)
	user(t, db, "hanzo", "z", "", "hk-x", false)
	k := keys()

	if k.resolve(context.Background(), "hk-x") == nil {
		t.Fatal("resolve nil for a seeded key")
	}
	// Take the store away entirely. A second resolve that still answers can only have
	// come from the cache — a stronger claim than counting calls.
	SetIAMStore(nil)
	if c := k.resolve(context.Background(), "hk-x"); c == nil || c.Owner != "hanzo" {
		t.Fatalf("second resolve = %+v, want the cached principal", c)
	}

	// A MISS is cached on the same terms: seeding the row afterwards must not change
	// the answer within the TTL.
	db2 := store(t)
	k2 := keys()
	if c := k2.resolve(context.Background(), "hk-later"); c != nil {
		t.Fatalf("unknown key resolved to %+v", c)
	}
	user(t, db2, "hanzo", "later", "", "hk-later", false)
	if c := k2.resolve(context.Background(), "hk-later"); c != nil {
		t.Fatalf("a cached miss was re-read from the store: %+v", c)
	}
}

// A PUBLISHABLE key resolves to its ORG and to nothing else. The org-only door is a
// separate function, not a flag, precisely so a pk- can never come back as a principal
// — a pk- ships in client JS, so a read identity minted from one is the browser-key
// catastrophe.
func TestOrgForKey_PublishableResolvesThroughTheOrgOnlyDoor(t *testing.T) {
	db := store(t)
	user(t, db, "acme", "alice", "alice@acme.test", "", false)
	key(t, db, "acme", "web", "acme/alice", "pk-live-abc", "", schema.KeyScopePublish)

	k := keys()
	if org := k.resolveOrg(context.Background(), "pk-live-abc"); org != "acme" {
		t.Fatalf("resolveOrg = %q, want acme", org)
	}
	// The SAME key at the principal door resolves to nobody, even though its row names
	// a real user in its own tenant.
	if c := k.resolve(context.Background(), "pk-live-abc"); c != nil {
		t.Fatalf("a publishable key resolved to a principal %+v — it must never authenticate", c)
	}
}

// The two doors answer different questions and the answers are not interchangeable: a
// secret key names a user, a publishable key names only an org, and neither resolves
// at the other's door.
func TestOrgForKey_EachPrefixUsesItsOwnDoor(t *testing.T) {
	db := store(t)
	user(t, db, "secret-org", "z", "", "", false)
	key(t, db, "secret-org", "backend", "secret-org/z", "pk-live-secret-half", "sk-live-abc", "")
	key(t, db, "pub-org", "web", "", "pk-live-abc", "", schema.KeyScopePublish)

	shared(t)

	if org, ok := OrgForKey(context.Background(), "sk-live-abc"); !ok || org != "secret-org" {
		t.Fatalf("secret key resolved to (%q,%v), want secret-org", org, ok)
	}
	if org, ok := OrgForKey(context.Background(), "pk-live-abc"); !ok || org != "pub-org" {
		t.Fatalf("publishable key resolved to (%q,%v), want pub-org — this is the pk- ingest path", org, ok)
	}

	k := keys()
	// A SECRET key at the org-only door learns nothing: that door answers for
	// publishable keys only, so a credential whose whole point is that it names a user
	// cannot be laundered into a bare org through it.
	if org := k.resolveOrg(context.Background(), "sk-live-abc"); org != "" {
		t.Errorf("a secret key resolved at the org-only door to %q, want \"\"", org)
	}
	// And the publishable HALF of that same secret key is not a browser key: it is
	// scope-secret, so the ingest door refuses it too.
	if org := k.resolveOrg(context.Background(), "pk-live-secret-half"); org != "" {
		t.Errorf("the pk- half of a SECRET key resolved to %q, want \"\" — only a publish-scoped key is a browser key", org)
	}
}

// resolveOrg fails CLOSED on every non-resolution — never a default tenant, so a bad
// browser key can never write into someone else's partition.
func TestResolveOrg_FailsClosed(t *testing.T) {
	// Unmounted: no store, no answer.
	SetIAMStore(nil)
	if org := keys().resolveOrg(context.Background(), "pk-x"); org != "" {
		t.Fatal("an unmounted resolver must resolve nothing")
	}

	db := store(t)
	// A publish-scoped key whose lifetime has run out.
	expired := key(t, db, "acme", "stale", "", "pk-live-EXPIRED", "", schema.KeyScopePublish)
	expired.ExpireTime = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if err := expired.UpdateCtx(context.Background()); err != nil {
		t.Fatalf("expire key: %v", err)
	}
	// A publish-scoped key with no owner at all: present, live, and still unusable.
	key(t, db, "", "ownerless", "", "pk-live-NOOWNER", "", schema.KeyScopePublish)

	for _, tc := range []struct{ name, key string }{
		{"never minted", "pk-live-UNKNOWN"},
		{"not a publishable prefix", "hk-live-abc"},
		{"expired", "pk-live-EXPIRED"},
		{"no owner on the row", "pk-live-NOOWNER"},
		{"empty", ""},
	} {
		if org := keys().resolveOrg(context.Background(), tc.key); org != "" {
			t.Errorf("%s resolved to org %q, want \"\"", tc.name, org)
		}
	}
}

// OrgForKey bounds the org it returns the same way principal.MaxOrgLen does: the org
// becomes a warehouse partition key, so an over-long value is refused rather than
// stored.
func TestOrgForKey_RefusesOutOfBoundsOrg(t *testing.T) {
	db := store(t)
	long := strings.Repeat("o", maxKeyOrgLen+1)
	key(t, db, long, "web", "", "pk-live-LONG", "", schema.KeyScopePublish)
	shared(t)

	if org, ok := OrgForKey(context.Background(), "pk-live-LONG"); ok || org != "" {
		t.Fatalf("an over-long org resolved to (%q,%v), want (\"\",false)", org, ok)
	}
	// A non-key string never reaches the store at all.
	if org, ok := OrgForKey(context.Background(), "not-a-key"); ok || org != "" {
		t.Fatalf("a non-key string resolved to (%q,%v), want (\"\",false)", org, ok)
	}
}

// ── why a key was refused ────────────────────────────────────────────────────

// The refusal REASON survives the resolver instead of being discarded.
//
// "the entity does not exist" is one sentence for causes that call for opposite
// actions from the holder, and cloud dropped everything but the (nil) principal — so a
// holder whose key had been REVOKED was sent looking for a deleted organization
// instead of minting a new key. Resolution is unchanged: a refused key is still nil,
// still anonymous. Only the diagnosis is added.
func TestIAMKeys_RefusalReasonIsCarried(t *testing.T) {
	db := store(t)
	user(t, db, "hanzo", "z", "", "hk-live-GOOD", false)
	// A key row planted in its own org that names a user in ANOTHER tenant — the
	// forgery the same-tenant pin exists to refuse. This is a security event, and it
	// must not read to an operator as a mistyped key.
	key(t, db, "attacker", "forged", "admin/z", "pk-live-F", "sk-live-FORGED", "")
	// A publishable key presented at the SECRET door: a valid credential at the wrong
	// door, which is neither unknown nor revoked.
	key(t, db, "acme", "web", "", "pk-live-WRONGDOOR", "", schema.KeyScopePublish)

	k := keys()
	for _, tc := range []struct {
		key  string
		want KeyRefusal
	}{
		{"hk-live-REVOKED", "key_unknown"},
		{"sk-live-FORGED", "key_foreign_user"},
		{"pk-live-WRONGDOOR", "key_wrong_door"},
	} {
		// A refused key is STILL nil — the reason changes no decision.
		if c := k.resolve(context.Background(), tc.key); c != nil {
			t.Fatalf("%s resolved to %+v — a refused key must stay anonymous", tc.key, c)
		}
		if got := k.refusal(context.Background(), tc.key); got != tc.want {
			t.Errorf("%s: refusal = %q, want %q", tc.key, got, tc.want)
		}
	}

	// A key that RESOLVES records no refusal.
	if c := k.resolve(context.Background(), "hk-live-GOOD"); c == nil {
		t.Fatal("a valid key must still resolve")
	}
	if got := k.refusal(context.Background(), "hk-live-GOOD"); got != "" {
		t.Errorf("a resolved key recorded refusal %q, want none", got)
	}
}

// RefusalForKey is the door a user-facing surface asks "why did this fail?", and it
// answers from the SAME cache the auth path already filled — so diagnosing a failure
// costs no extra store read.
func TestRefusalForKey(t *testing.T) {
	db := store(t)
	user(t, db, "hanzo", "z", "", "hk-live-GOOD", false)
	shared(t)

	if reason, ok := RefusalForKey(context.Background(), "hk-live-REVOKED"); ok || reason != "key_unknown" {
		t.Fatalf("RefusalForKey(revoked) = (%q,%v), want (key_unknown,false)", reason, ok)
	}
	if reason, ok := RefusalForKey(context.Background(), "hk-live-GOOD"); !ok || reason != "" {
		t.Fatalf("RefusalForKey(valid) = (%q,%v), want (\"\",true)", reason, ok)
	}
	// A publishable key is not a question this door answers: it names no principal, so
	// there is no principal-refusal to report.
	if reason, ok := RefusalForKey(context.Background(), "pk-live-abc"); ok || reason != "" {
		t.Fatalf("RefusalForKey(publishable) = (%q,%v), want (\"\",false)", reason, ok)
	}
	// A non-key string never reaches the store, and neither does a re-ask: with the
	// store gone, both answers must still come back unchanged from the cache.
	SetIAMStore(nil)
	if reason, ok := RefusalForKey(context.Background(), "not-a-key"); ok || reason != "" {
		t.Fatalf("RefusalForKey(garbage) = (%q,%v), want (\"\",false)", reason, ok)
	}
	if reason, ok := RefusalForKey(context.Background(), "hk-live-REVOKED"); ok || reason != "key_unknown" {
		t.Fatalf("re-asking why cost a store read: got (%q,%v)", reason, ok)
	}
	if reason, ok := RefusalForKey(context.Background(), "hk-live-GOOD"); !ok || reason != "" {
		t.Fatalf("re-asking about a valid key cost a store read: got (%q,%v)", reason, ok)
	}
}

// KeyHint names a key without disclosing it — enough for a holder to tell WHICH key
// failed, useless to anyone who reads the log.
func TestKeyHint_NeverDisclosesTheKey(t *testing.T) {
	const k = "hk-902abd8e-dead-beef-cafe-000000000000"
	hint := KeyHint(k)
	if hint != "hk-902abd…" {
		t.Fatalf("KeyHint = %q, want hk-902abd…", hint)
	}
	// Nothing beyond the first 9 characters ever appears.
	if strings.Contains(hint, "beef") || strings.Contains(hint, "dead") || len(hint) > 12 {
		t.Fatalf("KeyHint leaked key material: %q", hint)
	}
	// A short/empty value discloses nothing at all rather than the whole string.
	for _, short := range []string{"", "hk-", "hk-abc"} {
		if h := KeyHint(short); h != "…" {
			t.Errorf("KeyHint(%q) = %q, want …", short, h)
		}
	}
}

// shared resets the process-wide resolver so a test drives OrgForKey/RefusalForKey —
// which go through it — against its own store and its own empty caches.
func shared(t *testing.T) {
	t.Helper()
	sharedKeysOnce = sync.Once{}
	sharedKeysInst = nil
	t.Cleanup(func() { sharedKeysOnce = sync.Once{}; sharedKeysInst = nil })
}
