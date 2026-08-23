package esign

import (
	"encoding/base64"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stores lists every path under dir, lexically — DIRECTORIES INCLUDED, because a
// tenant store announces itself as its own directory ({dataDir}/orgs/{org}/)
// before the encrypted file inside it is flushed; a walk that skipped
// directories watched the wrong thing and saw a minted database as nothing at
// all. SQLite sidecars (-wal/-shm/-journal) are left out: they come and go with
// reads as well as writes and never name a store.
func stores(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		for _, sfx := range []string{"-wal", "-shm", "-journal"} {
			if strings.HasSuffix(name, sfx) {
				return nil
			}
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// TestUnknownTokenOpensNothing is the negative control on the signer's endpoint.
//
// That endpoint is unauthenticated by design — the token IS the credential — so
// the one thing it must never do for a caller who has not produced a real token is
// touch a per-tenant store. Opening one CREATES the encrypted file and runs its
// schema DDL, so when the `:org` segment selected the store before the token was
// checked, any string minted a tenant database and the refusal arrived after the
// file existed. The token now resolves first, through the cross-tenant index, so
// a token nobody minted reaches no store at all.
//
// The test is written so it CAN fail: the same walk that asserts nothing was
// created is run again around a request that legitimately DOES create a store,
// so a walk blind to new files is caught by its own positive control instead of
// reporting a pass it never earned.
func TestUnknownTokenOpensNothing(t *testing.T) {
	dir := t.TempDir()
	app := mountAt(t, dir)

	// An org nothing has ever written under, and tokens nobody minted.
	const ghost = "ghost-org"
	const bogus = "deadbeefdeadbeefdeadbeef"
	before := stores(t, dir)

	for _, r := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/v1/esign/o/" + ghost + "/sign/" + bogus, nil},
		{http.MethodPost, "/v1/esign/o/" + ghost + "/sign/" + bogus + "/fields/f1", map[string]any{"value": "x"}},
		{http.MethodPost, "/v1/esign/o/" + ghost + "/sign/" + bogus + "/complete", map[string]any{}},
		{http.MethodPost, "/v1/esign/o/" + ghost + "/sign/" + bogus + "/reject", map[string]any{"reason": "no"}},
		// A traversal-shaped org and token, to show the refusal is the ORDER and
		// not the sanitizer: these never reach a filename either way.
		{http.MethodGet, "/v1/esign/o/..%2f..%2fetc/sign/" + bogus, nil},
		{http.MethodGet, "/v1/esign/o/" + ghost + "/sign/..%2f..%2fpasswd", nil},
	} {
		code, body := do(t, app, r.method, r.path, "", r.body)
		if code != http.StatusNotFound {
			t.Fatalf("%s %s: want 404, got %d (%s)", r.method, r.path, code, body)
		}
	}

	if after := stores(t, dir); !slices.Equal(before, after) {
		t.Fatalf("the signer's endpoint created a database for a token nobody minted\nbefore: %v\nafter:  %v", before, after)
	}

	// POSITIVE CONTROL. The comparison above is evidence only if it can see a
	// store appear. A legitimate owner call under that same never-seen org does
	// create one, and the SAME comparison has to report it.
	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents", ghost,
		map[string]any{"title": "control", "pdfBase64": base64.StdEncoding.EncodeToString(pdf)})
	if code != http.StatusCreated {
		t.Fatalf("positive control create: want 201, got %d (%s)", code, body)
	}
	if after := stores(t, dir); slices.Equal(before, after) {
		t.Fatal("positive control FAILED: a real document create left the data directory unchanged, " +
			"so this test could not have detected a minted database and proves nothing")
	}
}

// TestTokenResolvesToItsOwnOrg proves the other half: a REAL token is refused
// under an org that is not the one it was minted under, and the refusal is the
// same 404 an unknown token gets — so the answer never separates "no such token"
// from "not yours". It also asserts the index is written when the recipient is
// ADDED, not only when the document is sent, because a token is live from the
// moment it exists.
func TestTokenResolvesToItsOwnOrg(t *testing.T) {
	dir := t.TempDir()
	app := mountAt(t, dir)
	const owner, other = "orgA", "orgB"

	pdf, err := os.ReadFile("testdata/example.pdf")
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	code, body := do(t, app, http.MethodPost, "/v1/esign/documents", owner,
		map[string]any{"title": "deal", "pdfBase64": base64.StdEncoding.EncodeToString(pdf)})
	if code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", code, body)
	}
	docID := decode(t, body)["id"].(string)

	code, body = do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", owner,
		map[string]any{"email": "signer@example.com", "name": "Signer"})
	if code != http.StatusCreated {
		t.Fatalf("add recipient: %d (%s)", code, body)
	}
	tok := decode(t, body)["token"].(string)

	// Adding the recipient indexed the token — before any send.
	got, ok, err := mounted.State.index.org(tok)
	if err != nil || !ok || got != owner {
		t.Fatalf("index after recipients.add: org=%q ok=%v err=%v; want %q", got, ok, err, owner)
	}

	// The real token under the wrong org is the same 404 an unknown token gets.
	if code, body := do(t, app, http.MethodGet, "/v1/esign/o/"+other+"/sign/"+tok, "", nil); code != http.StatusNotFound {
		t.Fatalf("real token under the wrong org: want 404, got %d (%s)", code, body)
	}
	// And under its own org it opens.
	if code, body := do(t, app, http.MethodGet, "/v1/esign/o/"+owner+"/sign/"+tok, "", nil); code != http.StatusOK {
		t.Fatalf("real token under its own org: want 200, got %d (%s)", code, body)
	}
}
