package legal

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// testMaster is the data-plane master a test deployment is given (cloud.Deps.MasterKey):
// the store seals its personal-data columns under a key derived from it.
var testMaster = bytes.Repeat([]byte{0x5a}, 32)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := openStore(t.TempDir(), testMaster)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func rawColumn(t *testing.T, s *Store, table, col, id string) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(`SELECT `+col+` FROM `+table+` WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("read %s.%s: %v", table, col, err)
	}
	return v
}

const contract = "This Mutual NDA is made between Hanzo AI Inc. and Ada Lovelace of 12 St James's Square, London."

func fixture(t *testing.T, s *Store) (Document, Filing) {
	t.Helper()
	ctx := context.Background()
	d := Document{ID: "doc_1", Org: "acme", TemplateID: "nda", TemplateVersion: 1, Category: CategoryOps, Title: "Mutual Non-Disclosure Agreement",
		ContentType: "text/markdown", Body: contract, Status: "draft", CreatedAt: 1, UpdatedAt: 1}
	f := Filing{ID: "fil_1", Org: "acme", DocumentIDs: []string{"doc_1"}, Jurisdiction: "DE", Provider: "manual", Status: "pending",
		Note: "Ada Lovelace asked us to hold the filing until her counsel signs", CreatedAt: 2, UpdatedAt: 2}
	if err := s.CreateDocument(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateFiling(ctx, f); err != nil {
		t.Fatal(err)
	}
	return d, f
}

// A rendered contract names the parties, and a filing's note is a reviewer's free text
// about them: both are ciphertext in the database on both engines.
func TestAContractAndAFilingNoteAreSealedAtRest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		d, f := fixture(t, s)

		if v := rawColumn(t, s, "legal_document", "body", d.ID); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "Lovelace") || strings.Contains(v, "NDA") {
			t.Errorf("legal_document.body is stored as %.30q: not sealed", v)
		}
		if v := rawColumn(t, s, "legal_filing", "note", f.ID); !strings.HasPrefix(v, "seal:") || strings.Contains(v, "Lovelace") {
			t.Errorf("legal_filing.note is stored as %.30q: not sealed", v)
		}
		if rawColumn(t, s, "legal_document", "title", d.ID) != d.Title || rawColumn(t, s, "legal_document", "status", d.ID) != "draft" {
			t.Error("title or status is not plain")
		}
		if got, err := s.GetDocument(ctx, "acme", d.ID); err != nil || got.Body != contract || got.Title != d.Title {
			t.Fatalf("GetDocument = %+v, %v", got, err)
		}
		if list, err := s.ListDocuments(ctx, "acme", 10); err != nil || len(list) != 1 || list[0].Body != contract {
			t.Fatalf("ListDocuments = %+v, %v", list, err)
		}
		if list, err := s.ListFilings(ctx, "acme", 10); err != nil || len(list) != 1 || list[0].Note != f.Note {
			t.Fatalf("ListFilings = %+v, %v", list, err)
		}
		// A signature move does not touch the sealed body.
		if err := s.UpdateDocumentSign(ctx, "acme", d.ID, "signed", "docusign", "env_1", 5, 5, "u_1"); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetDocument(ctx, "acme", d.ID); err != nil || got.Body != contract || got.Status != "signed" {
			t.Fatalf("after signing: %+v, %v", got, err)
		}
	})
}

// A contract lifted into another document or tenant, or planted in the clear, is refused.
func TestASealedColumnIsBoundToItsRow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := testStore(t)
		ctx := context.Background()
		d, _ := fixture(t, s)
		d2 := d
		d2.ID, d2.Body = "doc_2", "another contract"
		if err := s.CreateDocument(ctx, d2); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE legal_document SET body = $1 WHERE id = 'doc_2'`, rawColumn(t, s, "legal_document", "body", d.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetDocument(ctx, "acme", "doc_2"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("doc 2 read doc 1's contract: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE legal_document SET body = 'planted' WHERE id = 'doc_2'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ListDocuments(ctx, "acme", 10); !errors.Is(err, tenure.ErrFieldPlain) {
			t.Fatalf("a planted plaintext read as the store's: %v", err)
		}
		if _, err := s.db.Exec(`UPDATE legal_document SET org = 'evil' WHERE id = 'doc_1'`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetDocument(ctx, "evil", "doc_1"); !errors.Is(err, tenure.ErrFieldAuth) {
			t.Fatalf("a document moved to another tenant opened: %v", err)
		}
	})
}

func TestTheStoreDoesNotOpenWithoutAMaster(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, master := range [][]byte{nil, testMaster[:31]} {
			if s, err := openStore(t.TempDir(), master); err == nil {
				_ = s.Close()
				t.Fatalf("opened with a %d-byte master", len(master))
			}
		}
	})
}

// A SQLite file written before the columns were sealed holds plaintext in them. It is
// built by hand (the rows are plaintext because the code that wrote them was), so this
// stays on SQLite: hanzo-sql has no such file.
func TestALegacyFileIsSealedInPlaceAndStillReads(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	s, err := openStore(dir, testMaster)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO legal_document (id, org, template_id, template_version, category, title, body, status, created_at, updated_at) VALUES ('doc_old', 'acme', 'nda', 1, 'ops', 'NDA', 'old contract text', 'draft', 1, 1)`,
		`INSERT INTO legal_filing (id, org, provider, status, note, created_at, updated_at) VALUES ('fil_old', 'acme', 'manual', 'pending', 'old note', 1, 1)`,
	} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(dir, testMaster)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if got, err := s.GetDocument(ctx, "acme", "doc_old"); err != nil || got.Body != "old contract text" {
		t.Fatalf("a legacy document: %+v, %v", got, err)
	}
	if list, err := s.ListFilings(ctx, "acme", 10); err != nil || len(list) != 1 || list[0].Note != "old note" {
		t.Fatalf("a legacy filing: %+v, %v", list, err)
	}
	for _, c := range []struct{ table, col, id string }{{"legal_document", "body", "doc_old"}, {"legal_filing", "note", "fil_old"}} {
		if v := rawColumn(t, s, c.table, c.col, c.id); !strings.HasPrefix(v, "seal:") {
			t.Errorf("%s.%s is still %.20q after the upgrade", c.table, c.col, v)
		}
	}
}
