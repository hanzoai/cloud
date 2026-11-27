package team

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers the
	// "sqlite" database/sql name under both build tags (cgo → mattn+SQLCipher,
	// encrypted at rest; !cgo → pure-Go modernc). Importing modernc directly would
	// double-register "sqlite" under CGO and panic at init. Blank import registers
	// the driver — the SAME one apps/todo, apps/crm and apps/agents use.
	_ "github.com/hanzoai/sqlite"
)

// docStore keeps ONE database per (org, space): the SPA data plane is scoped per
// tenant, no KV. The space is the PROJECT of its org, so hanzoai/namespace names it:
// the SQLite file <dir>/orgs/<org>/projects/<ws>/docs.db, or on hanzo-sql (CLOUD_SQL_DSN
// set) a schema of the same name, shared by every replica. An org's data is physically
// isolated (full multitenancy). The storage LOGIC is ported verbatim from
// team-go/pkg/transactor/store.go; only the driver binding is adapted to
// locus.Open, the cloud-native ONE opener, and its SQL is the dialect both engines
// share (sdk/dialect).
//
// A docStore is the shared store, or a view of it that a writer holds one space
// through (lock): the view reads and writes that space inside the writer's
// transaction and every other space through the pools, as the shared store does.
type docStore struct {
	*spaces
	held *heldSpace
}

// spaces is what every view of one store shares: the open per-space handles and the
// write locks of SQLite.
type spaces struct {
	dir string
	mu  sync.Mutex
	dbs map[namespace.Namespace]*sql.DB
	// writes holds one mutex per (org, space) on SQLite: the read-modify-write of a
	// document and of the counters its write moves happen under it, so two writers
	// can neither both see "absent" and both create, nor both read a count and both
	// write it plus one. Reads never take it. On hanzo-sql the lock is the
	// database's, so it holds across replicas (lock).
	writes sync.Map
}

// heldSpace is the one space a writer holds, and the transaction it holds it with.
type heldSpace struct {
	org, space string
	tx         *sql.Tx
}

// querier is what a pool and a transaction both answer.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// writing is the (org, space) write lock on SQLite. Held only across a document's
// own read-modify-write — never across triggers, which write other documents.
func (s *docStore) writing(org, space string) *sync.Mutex {
	m, _ := s.writes.LoadOrStore(org+"\x00"+space, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// lock takes the (org, space) write lock and answers the store the guarded work reads
// and writes the space through, and the release that ends it. Everything the work
// does to the space goes through held: on hanzo-sql it is the transaction that holds
// the lock (Dialect.BeginWrite: one writing transaction per space across every
// replica, since each space is a schema of its own), and the work must not ask the
// space's pool for a second connection, which a burst of writers would exhaust while
// each waits for the lock. release commits it, and answers why a commit that did not
// land did not: the writes under it were not made. On SQLite there is one process and
// held is the store itself under the space's mutex.
func (s *docStore) lock(org, space string) (held *docStore, release func() error, err error) {
	db, err := s.db(org, space)
	if err != nil {
		return nil, nil, err
	}
	d := dialect.Of(db)
	if !d.Postgres() {
		m := s.writing(org, space)
		m.Lock()
		return s, func() error { m.Unlock(); return nil }, nil
	}
	tx, release, err := dialect.Held(context.Background(), dialect.LockWait, func(ctx context.Context) (*sql.Tx, error) {
		return d.BeginWrite(ctx, db)
	})
	if err != nil {
		return nil, nil, fmt.Errorf("lock %s/%s: %w", org, space, err)
	}
	return &docStore{spaces: s.spaces, held: &heldSpace{org: org, space: space, tx: tx}}, release, nil
}

// q is what reads and writes (org, space): the writer's transaction when this view
// holds that space, else the space's pool.
func (s *docStore) q(org, space string) (querier, error) {
	if h := s.held; h != nil && h.org == org && h.space == space {
		return h.tx, nil
	}
	return s.db(org, space)
}

func newStore(dir string) *docStore {
	return &docStore{spaces: &spaces{dir: dir, dbs: map[namespace.Namespace]*sql.DB{}}}
}

// converge carries a document tree written before this directory was called
// `spaces`. The account store's own converge is the same act one layer up.
//
// It is ONE rename, and that is the whole reason it is safe: the tree holds a
// database per (org, space) and cek keeps each one's wrapped key in a `.dek`
// beside it, so moving the files and leaving the keys — or the reverse — would
// leave every space encrypted with a key nothing can find. Moving the directory
// moves both, together, or neither.
//
// Guarded on both ends. A fresh deployment has no old tree; a converged one has
// the new one and the old name is gone, so a second boot does nothing. The
// failure it must never have is a SILENT one: without this the tree is simply
// not found, every space opens empty, and a deployment reports itself healthy
// while its documents sit unread on the same disk.
func converge(root string) error {
	was, is := filepath.Join(root, "workspaces"), filepath.Join(root, "spaces")
	if _, err := os.Stat(is); err == nil {
		return nil
	}
	if _, err := os.Stat(was); err != nil {
		return nil
	}
	return os.Rename(was, is)
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

// db opens (creating on first use) the (org, space) database and caches the handle.
// On SQLite a single open connection serializes writes (sqlite is single-writer),
// which is plenty for a per-space store; on hanzo-sql it is a small pool on the
// space's schema.
//
// The pragmas are the driver's, which is the whole point of having one driver:
// locus pragma_test pins wal, foreign_keys on and synchronous normal, and a store
// that sets its own reaches a different database from every other store here.
func (s *docStore) db(org, space string) (*sql.DB, error) {
	// The namespace is the key AND the name: cached by the value rather than by a
	// rendering of it, so two spellings that resolve to one file can never become
	// two open handles on that file. orgns.OrgNamespace is the ONE path — org is
	// the VERIFIED space-token claim, never a client header.
	ns, err := principal.OrgNamespace(org, space)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[ns]; ok {
		return db, nil
	}
	db, err := locus.Use(context.Background(), nil, ns, "docs", s.dir, func(db *sql.DB) (*sql.DB, error) {
		if err := migrateDocs(db); err != nil {
			return nil, err
		}
		return db, nil
	})
	if err != nil {
		return nil, err
	}
	s.dbs[ns] = db
	return db, nil
}

// migrateDocs makes the docs table, in the one transaction Dialect.Migrate holds, so
// replicas opening a space together do not run its DDL against each other.
func migrateDocs(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	d := dialect.Of(db)
	return d.Migrate(ctx, db, func(tx *sql.Tx) error {
		// CREATE INDEX locks out the table's writers even to find the index already
		// there, and a space is opened again and again, by every replica, beside
		// writers that are mid-transaction. The table and its indexes are made
		// together, so a table that is there has them.
		has, err := d.HasTable(ctx, tx, "docs")
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.ExecContext(ctx, `CREATE TABLE docs (
				id    TEXT PRIMARY KEY,
				class TEXT NOT NULL,
				space TEXT,
				json  TEXT NOT NULL
			);
			CREATE INDEX idx_docs_class ON docs(class);
			CREATE INDEX idx_docs_space ON docs(space)`); err != nil {
				return err
			}
		}
		return renameFacet(ctx, tx)
	})
}

// renameFacet moves a channel document's work facet from mixinRenamed to
// mixinChannel, on the channel classes alone (the class index keeps it off every
// message in the space). A document that still carries the old key is rewritten;
// the second open of a space finds none, so it does nothing.
func renameFacet(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, json FROM docs WHERE class IN ($1, $2) AND json LIKE $3`,
		clChannel, clDirectMessage, `%"`+mixinRenamed+`"%`)
	if err != nil {
		return err
	}
	type fix struct{ id, json string }
	var fixes []fix
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber() // a number the document held is written back as it was
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			continue // not a document this migration can read is not one it rewrites
		}
		facet, ok := doc[mixinRenamed]
		if !ok || facet == nil {
			continue
		}
		delete(doc, mixinRenamed)
		doc[mixinChannel] = facet
		out, err := json.Marshal(doc)
		if err != nil {
			_ = rows.Close()
			return err
		}
		fixes = append(fixes, fix{id, string(out)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, f := range fixes {
		if _, err := tx.ExecContext(ctx, `UPDATE docs SET json = $1 WHERE id = $2`, f.json, f.id); err != nil {
			return err
		}
	}
	return nil
}

// get returns the stored JSON for a doc, or nil if absent.
func (s *docStore) get(org, space, id string) (map[string]any, error) {
	q, err := s.q(org, space)
	if err != nil {
		return nil, err
	}
	var raw string
	err = q.QueryRowContext(context.Background(), `SELECT json FROM docs WHERE id = $1`, id).Scan(&raw)
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
func (s *docStore) put(org, space string, doc map[string]any) error {
	q, err := s.q(org, space)
	if err != nil {
		return err
	}
	id, _ := doc["_id"].(string)
	class, _ := doc["_class"].(string)
	// The transactor's documents carry a `space` of their own — the upstream
	// platform's containing space, `core:space:*` — which is a different thing
	// from the SPACE this store is partitioned by and keeps the wire spelling it
	// has always had. Only the local yields, so the two are never one name.
	docSpace, _ := doc["space"].(string)
	if id == "" || class == "" {
		return nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	// Text a document holds that PostgreSQL would refuse is refused on both engines.
	if err := errors.Join(dialect.Text(id, class, docSpace), dialect.Document(raw)); err != nil {
		return err
	}
	_, err = q.ExecContext(context.Background(), `INSERT INTO docs (id, class, space, json) VALUES ($1, $2, $3, $4)
		ON CONFLICT(id) DO UPDATE SET class=excluded.class, space=excluded.space, json=excluded.json`,
		id, class, docSpace, string(raw))
	return err
}

// del removes a doc by id.
func (s *docStore) del(org, space, id string) error {
	q, err := s.q(org, space)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(context.Background(), `DELETE FROM docs WHERE id = $1`, id)
	return err
}

// byClasses returns the JSON of every doc whose _class is in the given set (the
// caller passes the descendant set of the queried class). Go-side matchQuery,
// sort and limit run over these.
func (s *docStore) byClasses(org, space string, classes []string) ([]map[string]any, error) {
	q, err := s.q(org, space)
	if err != nil {
		return nil, err
	}
	if len(classes) == 0 {
		return nil, nil
	}
	args := make([]any, len(classes))
	for i, c := range classes {
		args[i] = c
	}
	rows, err := q.QueryContext(context.Background(), `SELECT json FROM docs WHERE class IN (`+dialect.Marks(1, len(classes))+`)`, args...)
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

// count returns how many docs the (org, space) holds — used to seed system
// spaces exactly once.
func (s *docStore) count(org, space string) (int, error) {
	q, err := s.q(org, space)
	if err != nil {
		return 0, err
	}
	var n int
	if err := q.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM docs`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Close closes every cached per-space handle. Idempotent.
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
