package cek

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// WHAT A KEYED STORE LOOKS LIKE ON DISK IS THE ONLY ANSWER THAT COUNTS.
//
// Capable does not read a build tag or look for a symbol; it makes a keyed
// database, writes to it, and asks the same classifier the open path asks. This
// holds it to that: whatever it answers must agree with what is actually on the
// disk, so the probe and the store can never disagree about one file.
func TestCapableAgreesWithTheFileItWrote(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.db")

	dek, err := sqlitedrv.NewDEK()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	db, err := openKeyed(path, dek)
	if err != nil {
		t.Fatalf("open keyed: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE probe(x)"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	encrypted := classify(path) == stateEncrypted
	capable := Capable() == nil
	if encrypted != capable {
		t.Fatalf("a keyed store came out encrypted=%v while Capable says %v — the probe "+
			"and the store disagree about the same build", encrypted, capable)
	}
	t.Logf("this build encrypts a keyed store: %v", encrypted)
}

// AND IT SAYS WHAT TO DO ABOUT IT. A refusal a reader cannot act on is the same
// as a silent one: the message has to carry the build that fixes it, because
// nothing else in the process knows.
func TestAnIncapableBuildNamesTheRecipe(t *testing.T) {
	err := Capable()
	if err == nil {
		t.Skip("this build encrypts, so there is no refusal to read")
	}
	for _, want := range []string{"libsqlcipher", "CGO_ENABLED=1", DevUnencryptedEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// A PLAINTEXT FILE IS RECOGNISED AS ONE. classify is what Capable trusts, so a
// file this package did not write must still land on the right side of it —
// otherwise the probe could pass on a build that writes plaintext.
func TestAPlaintextFileIsNotMistakenForAnEncryptedOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plain.db")
	if err := os.WriteFile(path, append([]byte(sqliteMagic), make([]byte, 4096)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := classify(path); got != statePlaintext {
		t.Fatalf("a file opening with the SQLite magic classified as %v, want statePlaintext", got)
	}
}
