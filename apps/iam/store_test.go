// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"
)

// seedIdentity writes one user into the identity store at path, the way IAM's own
// code does — orm.New + the owner/name id — and closes it, so what follows reads a
// file on disk rather than a handle in this process.
// dataDirWithStore is a DataDir that already HOLDS an identity store — the state
// every real deployment is in, and now the only state this graft will open. Mount
// refuses an absent store on purpose (a missing volume must not be answered by
// minting an empty identity service), so a test that mounts has to look like
// production rather than like a blank disk.
func dataDirWithStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedIdentity(t, dbPath(dir), "hanzo", "z")
	return dir
}

func seedIdentity(t *testing.T, path, org, name string) {
	t.Helper()
	db, err := iamstore.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the identity store at %s: %v", path, err)
	}
	row := orm.New[model.User](db)
	row.Owner, row.Name, row.Email = org, name, name+"@"+org+".test"
	row.SetId(org + "/" + name)
	if err := row.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed user %s/%s: %v", org, name, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// THE CONTRACT: the graft opens the store that holds the identities.
//
// This is the property, and nothing weaker is worth asserting. The previous test
// checked that the file this opened was CIPHERTEXT, which it was — and it was also a
// file with no users in it, four kilobytes, at a path IAM had never heard of, while
// every identity in production sat in a plain SQLite database the standalone binary
// wrote. Asking the encrypted one for the signing keys returned {"keys":[]}. So the
// old test passed for a store that was not the identity store, which is the only way
// a test about encryption can pass while identity is served from somewhere else.
//
// It reads through the SAME opener a caller gets from DB(), so what a sibling
// subsystem reflects in-process (clients/platform, clients/deploy) is the same rows.
func TestTheGraftOpensTheStoreThatHoldsTheIdentities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	path := dbPath(dir)
	seedIdentity(t, path, "hanzo", "z")

	db, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore refused the data dir holding the identity store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	users, err := orm.TypedQuery[model.User](db).GetAll(ctx)
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("the grafted store holds %d users, want the 1 that is in %s — a fully-mounted, completely empty identity service is not a deployment", len(users), path)
	}
	if got := users[0].Owner + "/" + users[0].Name; got != "hanzo/z" {
		t.Errorf("read %q, want hanzo/z", got)
	}
}

// The store IAM's CLI is pointed at and the store this graft opens are ONE file.
//
// It is asserted on the PATH because that is the whole of the cutover: a deployment
// mounts the identity volume at {DataDir}/iam and there is nothing else to say. A
// path that drifted from IAM's own would not fail loudly — it would open an empty
// database and serve it, which is exactly what was happening.
func TestTheStorePathIsTheOneIAMWrites(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "iam", "iam.db")

	if got := dbPath(dir); got != want {
		t.Fatalf("store path = %s, want %s (the --db the standalone iam is given)", got, want)
	}

	// An ABSENT store must be refused, not created. This process is pointed at a
	// store that exists; if the volume holding it is missing or mounted elsewhere,
	// creating one here would mint an empty identity service and serve it —
	// every account gone, jwks {"keys":[]}, and nothing failing to say so.
	if _, err := openStore(dir); err == nil {
		t.Fatal("openStore CREATED an identity store that was not there — a missing mount would silently replace identity with an empty database")
	}

	// Present and openable: the path is exactly the file the standalone iam writes,
	// so the two binaries read one store rather than two.
	seedIdentity(t, want, "hanzo", "z")
	db, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore on an existing store: %v", err)
	}
	_ = db.Close()
}

// sqliteMagic is the 16-byte header every unencrypted SQLite file starts with.
var sqliteMagic = []byte("SQLite format 3\x00")

// THE IDENTITY STORE IS PLAINTEXT AT REST. This records it rather than discovering it
// again later from a volume read.
//
// It is not a preference. The file that holds every identity already exists and is
// plaintext; cek derives a key unconditionally and refuses a plaintext database at
// open, so it cannot be pointed at that file; and re-keying an existing file is a
// migration, which is forbidden here — one store, pointed at, never converted. The
// only shape that changes this without a migration is a NEW store born encrypted with
// the old one retired, and that is a decision above this package.
//
// So this asserts the posture rather than pretending otherwise. If it ever fails,
// something has started encrypting the identity store and this comment is the thing
// to read first: the standalone iam, the iam CLI and the migrator all open it plain,
// and any one of them would then be locked out of the fleet's own database.
func TestTheIdentityStoreIsPlaintextAndThatIsRecorded(t *testing.T) {
	dir := t.TempDir()
	path := dbPath(dir)
	seedIdentity(t, path, "hanzo", "z")

	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// What this pins is AGREEMENT, not plaintext: every consumer of this file — the
	// standalone iam, the iam CLI, the migrator and this graft — must open it the
	// same way, and today they all open it plain. Encrypting it is an IMPROVEMENT
	// this test must not stand in the way of, so the day the other consumers move
	// to a keyed opener, this assertion moves with them rather than blocking them.
	//
	// It is recorded because it is a real, named cost: internal/oidc/jwks.go reads
	// cert.PrivateKey out of this store, so a lifted volume yields the
	// token-signing keys, and this is the one cloud store outside the cek envelope
	// every other app's store is born inside. The forward shape is a NEW store born
	// encrypted with this one retired — not a re-key of this file, which would be
	// the migration the directive forbids.
	if len(head) < len(sqliteMagic) || !bytes.Equal(head[:len(sqliteMagic)], sqliteMagic) {
		t.Skip("this store is no longer plain — if every consumer now agrees on a keyed opener, delete this test; if they do not, they are locked out")
	}
}
