package team

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"sync"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/environ"
	"github.com/hanzoai/cloud/sqlpool"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest; !cgo → pure-Go modernc). Importing modernc directly would
	// double-register "sqlite" under CGO and panic at init. Blank import registers
	// the driver — the SAME one clients/tracker, clients/crm and clients/agents use.
	_ "github.com/hanzoai/sqlite"
)

// docStore keeps ONE SQLite database per (org, workspace) — the SPA data plane is
// SQLite scoped per tenant, no KV and no Postgres. The workspace is the PROJECT of
// its org, so hanzoai/namespace names the file: <dir>/orgs/<org>/projects/<ws>/docs.db.
// An org's data is physically isolated (full multitenancy) and the whole tree can
// live on one durable mount. The storage LOGIC is ported verbatim from
// team-go/pkg/transactor/store.go; only the driver binding is adapted to
// hanzoai/sqlite (the cloud-native ONE driver) and the pragmas are set via Exec
// instead of DSN query params.
type docStore struct {
	dir string
	mu  sync.Mutex
	dbs map[namespace.Namespace]*sql.DB
}

func newStore(dir string) *docStore {
	return &docStore{dir: dir, dbs: map[namespace.Namespace]*sql.DB{}}
}

var pathSanitize = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// seg makes a value safe as a single BLOB-KEY segment (see blobKey in files.go —
// its last component is a client-supplied blobId). Any char outside
// [A-Za-z0-9_.-] becomes '_' (killing '/'), AND the two dot-only components "."
// and ".." — which ARE inside that class and would otherwise pass through as the
// current/parent directory — are mapped to "_". So a segment can never be a path
// traversal or escape its box.
//
// It does NOT name a database. hanzoai/namespace does that, injectively; seg is
// not injective ("a/b" and "a_b" both fold to "a_b") and a fold is a tenant break
// wherever it decides which file a tenant reads.
func seg(s string) string {
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	return pathSanitize.ReplaceAllString(s, "_")
}

// db opens (creating on first use) the (org, workspace) SQLite file and caches
// the handle. WAL + busy_timeout make concurrent sessions safe; a single open
// connection serializes writes (sqlite is single-writer) which is plenty for a
// per-workspace store. On a network FS that lacks WAL shared-memory the journal
// mode falls back via the SQLITE_JOURNAL_MODE env knob.
func (s *docStore) db(org, workspace string) (*sql.DB, error) {
	// The namespace is the key AND the name: cached by the value rather than by a
	// rendering of it, so two spellings that resolve to one file can never become
	// two open handles on that file. cloud.OrgNamespace is the ONE door — org is
	// the VERIFIED workspace-token claim, never a client header.
	ns, err := cloud.OrgNamespace(org, workspace)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[ns]; ok {
		return db, nil
	}
	journal := environ.Or("SQLITE_JOURNAL_MODE", "WAL") // DELETE/TRUNCATE on FUSE/S3 mounts
	db, err := cek.Open(ns, "docs", s.dir)
	if err != nil {
		return nil, err
	}
	sqlpool.Single(db)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=" + journal,
		"PRAGMA foreign_keys=OFF",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS docs (
		id    TEXT PRIMARY KEY,
		class TEXT NOT NULL,
		space TEXT,
		json  TEXT NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_docs_class ON docs(class);
	CREATE INDEX IF NOT EXISTS idx_docs_space ON docs(space);`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.dbs[ns] = db
	return db, nil
}

// get returns the stored JSON for a doc, or nil if absent.
func (s *docStore) get(org, workspace, id string) (map[string]any, error) {
	db, err := s.db(org, workspace)
	if err != nil {
		return nil, err
	}
	var raw string
	err = db.QueryRow(`SELECT json FROM docs WHERE id = ?`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// put upserts a doc keyed by its _id; _class/space mirror into columns for the
// findAll candidate scan.
func (s *docStore) put(org, workspace string, doc map[string]any) error {
	db, err := s.db(org, workspace)
	if err != nil {
		return err
	}
	id, _ := doc["_id"].(string)
	class, _ := doc["_class"].(string)
	space, _ := doc["space"].(string)
	if id == "" || class == "" {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO docs (id, class, space, json) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET class=excluded.class, space=excluded.space, json=excluded.json`,
		id, class, space, string(raw))
	return err
}

// del removes a doc by id.
func (s *docStore) del(org, workspace, id string) error {
	db, err := s.db(org, workspace)
	if err != nil {
		return err
	}
	_, err = db.Exec(`DELETE FROM docs WHERE id = ?`, id)
	return err
}

// byClasses returns the JSON of every doc whose _class is in the given set (the
// caller passes the descendant set of the queried class). Go-side matchQuery,
// sort and limit run over these.
func (s *docStore) byClasses(org, workspace string, classes []string) ([]map[string]any, error) {
	db, err := s.db(org, workspace)
	if err != nil {
		return nil, err
	}
	if len(classes) == 0 {
		return nil, nil
	}
	args := make([]any, len(classes))
	ph := make([]byte, 0, len(classes)*2)
	for i, c := range classes {
		args[i] = c
		if i > 0 {
			ph = append(ph, ',')
		}
		ph = append(ph, '?')
	}
	rows, err := db.Query(`SELECT json FROM docs WHERE class IN (`+string(ph)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			continue
		}
		out = append(out, doc)
	}
	return out, rows.Err()
}

// count returns how many docs the (org, workspace) holds — used to seed system
// spaces exactly once.
func (s *docStore) count(org, workspace string) (int, error) {
	db, err := s.db(org, workspace)
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM docs`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Close closes every cached per-workspace handle. Idempotent.
func (s *docStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for k, db := range s.dbs {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.dbs, k)
	}
	return firstErr
}
