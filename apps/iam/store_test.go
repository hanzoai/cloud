// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/iam/pkg/model"
	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"

	// The store is keyed now, so this test binary needs a master before it opens
	// one. devmaster mints a per-process key; production resolves the real one
	// through credz.Boot from KMS.
	_ "github.com/hanzoai/cloud/internal/devmaster"
)

// seedIdentity writes one user into the identity store at path, the way IAM's own
// code does — orm.New + the owner/name id — and closes it, so what follows reads a
// file on disk rather than a handle in this process.
// dataDirWithStore is a DataDir that already HOLDS an identity store — the state
// every real deployment is in, and now the only state this subsystem will open. Mount
// refuses an absent store on purpose (a missing volume must not be answered by
// minting an empty identity service), so a test that mounts has to look like
// production rather than like a blank disk.
func dataDirWithStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	seedIdentity(t, StorePath(dir), "hanzo", "z")
	return dir
}

// mountSigningKey projects one RS256 key per cert NAME into a directory and points
// IAM_SIGNING_KEYS at it — the whole of what a deployment does to supply signing
// material. The file name is the cert name, which is the JWKS `kid`, so what is
// projected and what a verifier looks up are one string.
//
// It is the ONE way a test stands up a signable store: the private half is
// memory-only (schema.Cert marks it json:"-"), so a row cannot carry one and
// writing the column instead would test a shape that no longer exists.
func mountSigningKey(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate signing key for %s: %v", name, err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		if err := os.WriteFile(filepath.Join(dir, name), pemBytes, 0o600); err != nil {
			t.Fatalf("project signing key for %s: %v", name, err)
		}
	}
	t.Setenv("IAM_SIGNING_KEYS", dir)
	return dir
}

func seedIdentity(t *testing.T, path, org, name string) {
	t.Helper()
	db, err := iamstore.Open("sqlite", path, "")
	if err != nil {
		t.Fatalf("open the identity store at %s: %v", path, err)
	}
	row := orm.New[model.User](db)
	row.Owner, row.Name, row.Email = org, name, name+"@"+org+".test"
	row.SetId(org + "/" + name)
	if err := row.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed user %s/%s: %v", org, name, err)
	}
	// A signing certificate, because a store without one cannot serve identity at
	// all — nothing can be issued and nothing already issued can be checked — and
	// Mount now refuses such a store rather than answering an empty keyset. A user
	// alone was a store production never has.
	//
	// Owned by the RESERVED org, and with its key mounted, for the same reason. A
	// signing cert is trusted only under a reserved owner — that is what stops a
	// tenant shadowing a platform kid — and its private half comes from the
	// deployment, never from the row. A tenant-owned cert with no key beside it is
	// the other store production never has: it publishes a kid nothing can sign
	// for, so Mount refuses it too.
	cert := orm.New[model.Cert](db)
	cert.Owner, cert.Name = "admin", "cert-"+org
	cert.Type, cert.CryptoAlgorithm, cert.BitSize = "x509", "RS256", 2048
	cert.SetId("admin/cert-" + org)
	if err := cert.CreateCtx(context.Background()); err != nil {
		t.Fatalf("seed signing cert for %s: %v", org, err)
	}
	mountSigningKey(t, "cert-"+org)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// THE CONTRACT: the subsystem opens the store that holds the identities.
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
func TestMountOpensTheStoreThatHoldsTheIdentities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	path := StorePath(dir)
	seedIdentity(t, path, "hanzo", "z")

	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore refused the data dir holding the identity store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	users, err := orm.TypedQuery[model.User](db).GetAll(ctx)
	if err != nil {
		t.Fatalf("read users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("the opened store holds %d users, want the 1 that is in %s — a fully-mounted, completely empty identity service is not a deployment", len(users), path)
	}
	if got := users[0].Owner + "/" + users[0].Name; got != "hanzo/z" {
		t.Errorf("read %q, want hanzo/z", got)
	}
}

// The store IAM's CLI is pointed at and the store this subsystem opens are ONE file.
//
// It is asserted on the PATH because that is the whole of the cutover: a deployment
// mounts the identity volume at {DataDir}/iam and there is nothing else to say. A
// path that drifted from IAM's own would not fail loudly — it would open an empty
// database and serve it, which is exactly what was happening.
func TestTheStorePathIsTheOneIAMWrites(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "iam", "iam.db")

	if got := StorePath(dir); got != want {
		t.Fatalf("store path = %s, want %s (the --db the standalone iam is given)", got, want)
	}

	// An ABSENT store must be refused, not created. This process is pointed at a
	// store that exists; if the volume holding it is missing or mounted elsewhere,
	// creating one here would mint an empty identity service and serve it —
	// every account gone, jwks {"keys":[]}, and nothing failing to say so.
	if _, _, err := openStore(dir); err == nil {
		t.Fatal("openStore CREATED an identity store that was not there — a missing mount would silently replace identity with an empty database")
	}

	// Present and openable: the path is exactly the file the standalone iam writes,
	// so the two binaries read one store rather than two.
	seedIdentity(t, want, "hanzo", "z")
	_, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore on an existing store: %v", err)
	}
	_ = conn.Close()
}

// sqliteMagic is the 16-byte header every unencrypted SQLite file starts with.
var sqliteMagic = []byte("SQLite format 3\x00")

// THE IDENTITY STORE IS ENCRYPTED AT REST, and a store that arrives plaintext is
// converted by the act of opening it.
//
// This is the assertion that used to run the other way. It recorded plaintext as a
// named cost, on the reasoning that cek could not be pointed at this file and that
// re-keying an existing one was a migration nobody had signed up for. Both halves
// have since been answered — cek.OpenAt keys a file wherever it is mounted, and
// cek.Convert re-keys one without losing a row — so what was a cost is now a gate.
//
// It matters because of what is IN this file: internal/oidc/jwks.go reads
// cert.PrivateKey out of it, so a lifted volume used to yield the token-signing keys
// for the whole fleet, along with every user, every org and every provider secret.
//
// The precondition is production's: seedIdentity writes the store with IAM's own
// plain opener, which is exactly the file a deployment has on disk today.
func TestTheIdentityStoreIsEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := StorePath(dir)

	seedIdentity(t, path, "hanzo", "z")
	head, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.HasPrefix(head, sqliteMagic) {
		t.Fatal("the precondition is wrong: the seed is not a plaintext SQLite database, so this test is not proving a conversion")
	}

	db, conn, err := openStore(dir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	head, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read %s: %v", path, err)
	}
	if bytes.HasPrefix(head, sqliteMagic) {
		t.Fatal("the identity store is STILL a plaintext SQLite file after being opened — a lifted volume yields every identity and the token-signing keys")
	}

	// A conversion that leaves a readable copy beside the store has not removed the
	// exposure, it has moved it.
	for _, leftover := range []string{".plain.bak", ".cek.tmp"} {
		if _, err := os.Stat(path + leftover); err == nil {
			t.Errorf("a plaintext copy was left at %s", path+leftover)
		}
	}

	// And it is the SAME store: encrypting it is worth nothing if the identities
	// did not come with it.
	users, err := orm.TypedQuery[model.User](db).GetAll(ctx)
	if err != nil {
		t.Fatalf("read users from the encrypted store: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("the encrypted store holds %d users, want the 1 that was in the plaintext one", len(users))
	}
	if got := users[0].Owner + "/" + users[0].Name; got != "hanzo/z" {
		t.Errorf("read %q, want hanzo/z", got)
	}
}
