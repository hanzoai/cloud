package trust

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cek"
	htrust "github.com/hanzoai/trust"
)

// TestRecordsListInByteOrder proves a section lists by (ord, id) in the byte order of
// the id on both engines. SQLite compares text by its bytes; PostgreSQL by the
// database's collation, under which a hyphen and an underscore are ignored or sorted
// elsewhere, so the id column is declared `COLLATE "C"` there. The test database has no
// locale and would never show the difference in the order, so the column's collation is
// read from the catalog as well.
func TestRecordsListInByteOrder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		for _, id := range []string{"ab", "a_c", "a-b", "B"} {
			code, body := req(t, app, http.MethodPut, "/v1/trust/faq/"+id, other,
				map[string]any{"data": map[string]any{"question": "q " + id, "answer": "a"}})
			if code != http.StatusOK {
				t.Fatalf("write %s: %d %s", id, code, body)
			}
		}
		code, body := req(t, app, http.MethodGet, "/v1/trust/faq", other, nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, body)
		}
		var got struct {
			Faq []struct {
				Question string `json:"question"`
			} `json:"faq"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		var order []string
		for _, f := range got.Faq {
			order = append(order, strings.TrimPrefix(f.Question, "q "))
		}
		if want := "B a-b a_c ab"; strings.Join(order, " ") != want {
			t.Fatalf("faq listed %v, want %s", order, want)
		}

		if locus.DSN() == "" {
			return
		}
		var collation sql.NullString
		if err := mounted.State.host.Tx(t.Context(), other, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT collation_name FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'record' AND column_name = 'id'`).Scan(&collation)
		}); err != nil {
			t.Fatal(err)
		}
		if collation.String != "C" {
			t.Fatalf("record.id is collated %q, want C: the listing order is the database's, not the bytes'", collation.String)
		}
	})
}

// TestALegacyFileKeepsItsRecords proves a tenant file made before the schema said
// BIGINT is the same file now: it opens through the seam under its own key, the
// migration leaves its rows alone, and what it holds is served. A file is SQLite by
// nature, so there is no PostgreSQL leg.
func TestALegacyFileKeepsItsRecords(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ns, err := principal.OrgNamespace(other, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := cek.Open(ns, "trust", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(strings.NewReplacer("BIGINT", "INTEGER").Replace(htrust.Schema(""))); err != nil {
		t.Fatalf("build the old file: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO record (kind, id, ord, data, updated) VALUES ('faq', 'old', 0, '{"question":"Was this here?","answer":"Yes."}', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	app := mountAtDir(t, dir)
	code, body := req(t, app, http.MethodGet, "/v1/trust/faq", other, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "Was this here?") {
		t.Fatalf("the legacy record is gone: %d %s", code, body)
	}
	if code, body := req(t, app, http.MethodPut, "/v1/trust/faq/new", other,
		map[string]any{"data": map[string]any{"question": "And this?", "answer": "Yes."}}); code != http.StatusOK {
		t.Fatalf("a write after the upgrade: %d %s", code, body)
	}
}
