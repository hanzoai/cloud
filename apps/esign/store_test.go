package esign

import (
	"encoding/base64"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cek"
)

// TestRowsKeepTheOrderTheyWereWritten proves the two orderings the bundle depends on
// read alike on both engines: a recipient with no signing order sorts BEFORE every
// numbered one (SQLite's NULLs first; PostgreSQL's last unless asked), and rows that
// tie on time come back in the order they were written (a file's rowid; a seq on
// hanzo-sql). Sequential signing and the audit trail are both read by that order.
func TestRowsKeepTheOrderTheyWereWritten(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mountApp(t)
		const org = "acme"
		pdf, err := os.ReadFile("testdata/example.pdf")
		if err != nil {
			t.Fatal(err)
		}
		code, body := do(t, app, http.MethodPost, "/v1/esign/documents", org, map[string]any{
			"title": "Order", "pdfBase64": base64.StdEncoding.EncodeToString(pdf),
		})
		if code != http.StatusCreated {
			t.Fatalf("create: %d %s", code, body)
		}
		docID := decode(t, body)["id"].(string)

		// Written in this order; listed by signing order with the unnumbered first, then
		// by when they were written.
		for _, r := range []map[string]any{
			{"email": "c@acme.test", "signingOrder": 2},
			{"email": "a@acme.test"},
			{"email": "b@acme.test", "signingOrder": 1},
			{"email": "d@acme.test"},
		} {
			if c, b := do(t, app, http.MethodPost, "/v1/esign/documents/"+docID+"/recipients", org, r); c != http.StatusCreated {
				t.Fatalf("add %v: %d %s", r["email"], c, b)
			}
		}

		_, body = do(t, app, http.MethodGet, "/v1/esign/documents/"+docID, org, nil)
		var got []string
		for _, r := range decode(t, body)["recipients"].([]any) {
			got = append(got, r.(map[string]any)["email"].(string))
		}
		if want := "a@acme.test d@acme.test b@acme.test c@acme.test"; strings.Join(got, " ") != want {
			t.Fatalf("recipients listed %v, want %s", got, want)
		}

		// The trail: created, then each recipient, in the order they were added.
		_, body = do(t, app, http.MethodGet, "/v1/esign/documents/"+docID+"/audit", org, nil)
		var trail []string
		for _, e := range decode(t, body)["entries"].([]any) {
			m := e.(map[string]any)
			label := m["type"].(string)
			if d, ok := m["data"].(map[string]any); ok && d["recipientEmail"] != nil {
				label += ":" + d["recipientEmail"].(string)
			}
			trail = append(trail, label)
		}
		want := "DOCUMENT_CREATED RECIPIENT_CREATED:c@acme.test RECIPIENT_CREATED:a@acme.test RECIPIENT_CREATED:b@acme.test RECIPIENT_CREATED:d@acme.test"
		if strings.Join(trail, " ") != want {
			t.Fatalf("audit trail read %v, want %s", trail, want)
		}
	})
}

// TestALegacyFileKeepsItsDocuments proves a tenant file made before the schema said
// BIGINT, and before recipients and the audit trail carried a seq, is the same file
// now: it opens through the seam under its own key, the migration adds seq and numbers
// the rows it has by rowid, the rows are served in the order they were written (their
// ids and times deliberately say otherwise), and a write after the upgrade comes last.
// A file is SQLite by nature, so there is no PostgreSQL leg.
func TestALegacyFileKeepsItsDocuments(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := cek.Open(ns, "esign", dir)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.NewReplacer("BIGINT", "INTEGER", "DOUBLE PRECISION", "REAL",
		",\n  seq              BIGINT NOT NULL DEFAULT 0", "",
		",\n  seq         BIGINT NOT NULL DEFAULT 0", "").Replace(schema)
	if strings.Contains(legacy, "seq") {
		t.Fatal("the legacy schema was meant to have no seq column")
	}
	for _, stmt := range []string{
		legacy,
		`INSERT INTO document_data (id, type, data, initial_data) VALUES ('dd', 'BLOB_KEY', 'k', 'k')`,
		`INSERT INTO documents (id, title, status, document_data_id, created_at, updated_at) VALUES ('doc', 'Old', 'DRAFT', 'dd', 1, 1)`,
		// Ids run backwards and the times tie, so only the rowid says what came first.
		`INSERT INTO recipients (id, document_id, email, token, created_at) VALUES ('r3', 'doc', 'third@acme.test', 't3', 5)`,
		`INSERT INTO recipients (id, document_id, email, token, created_at) VALUES ('r2', 'doc', 'second@acme.test', 't2', 5)`,
		`INSERT INTO recipients (id, document_id, email, token, created_at) VALUES ('r1', 'doc', 'first@acme.test', 't1', 5)`,
		`INSERT INTO audit_logs (id, document_id, type, created_at) VALUES ('z', 'doc', 'DOCUMENT_CREATED', 5)`,
		`INSERT INTO audit_logs (id, document_id, type, created_at) VALUES ('y', 'doc', 'RECIPIENT_CREATED', 5)`,
	} {
		if _, err := old.Exec(stmt); err != nil {
			t.Fatalf("build the old file: %v\n%s", err, stmt)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	app := mountAt(t, dir)
	const org = "acme"
	code, body := do(t, app, http.MethodGet, "/v1/esign/documents/doc", org, nil)
	if code != http.StatusOK {
		t.Fatalf("the legacy document is gone: %d %s", code, body)
	}
	var got []string
	for _, r := range decode(t, body)["recipients"].([]any) {
		got = append(got, r.(map[string]any)["email"].(string))
	}
	if want := "third@acme.test second@acme.test first@acme.test"; strings.Join(got, " ") != want {
		t.Fatalf("legacy recipients listed %v, want %s (the order they were written)", got, want)
	}

	if c, b := do(t, app, http.MethodPost, "/v1/esign/documents/doc/recipients", org, map[string]any{"email": "new@acme.test"}); c != http.StatusCreated {
		t.Fatalf("a write after the upgrade: %d %s", c, b)
	}
	_, body = do(t, app, http.MethodGet, "/v1/esign/documents/doc/audit", org, nil)
	var types []string
	for _, e := range decode(t, body)["entries"].([]any) {
		types = append(types, e.(map[string]any)["id"].(string))
	}
	if len(types) != 3 || types[0] != "z" || types[1] != "y" {
		t.Fatalf("audit entries read %v, want the two old ones in written order then the new one", types)
	}
}
