// Plaintext → encrypted at-rest migration for the per-tenant SQLite stores.
//
// Once COMMERCE_KMS_MASTER_KEY is configured, the daemon opens each tenant file
// enveloped and REFUSES a db that has no DEK sidecar (fail-closed — see
// resolveDEK). Any pre-existing PLAINTEXT tenant file must therefore be converted
// first. EncryptDataDir does that, without losing a row:
//
//	read plaintext → write a fresh encrypted copy → verify per-table content
//	parity → atomically swap the encrypted copy into place (plaintext kept as a
//	.plaintext.bak backup).
//
// It is idempotent: a tenant that already has a sidecar is skipped, so it is safe
// to run repeatedly (and as a one-shot Job before flipping the key on the
// deployment). The CLI is cmd/commerce-encrypt-dbs.
package db

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	sqlitedrv "github.com/hanzoai/sqlite"
)

// MigrateReport summarizes an EncryptDataDir run.
type MigrateReport struct {
	Encrypted []string // tenant db paths converted this run
	Skipped   []string // already-encrypted (sidecar present) or empty
	Rows      int      // total rows copied
}

// EncryptDataDir walks the users/ and orgs/ trees under dataDir and encrypts every
// plaintext tenant data.db in place. masterKey is the 32-byte KMS master key
// (same value the daemon runs with). When dryRun is true it reports what WOULD be
// migrated without writing anything.
func EncryptDataDir(dataDir string, masterKey []byte, dryRun bool) (*MigrateReport, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("migrate: master key must be 32 bytes, got %d", len(masterKey))
	}
	if !sqlitedrv.EncryptionAvailable() {
		return nil, fmt.Errorf("migrate: this build cannot encrypt (pure-Go sqlite); rebuild CGO_ENABLED=1 -tags \"libsqlite3 sqlite_fts5\" linked against libsqlcipher")
	}
	// A cgo build without libsqlcipher linked would write a PLAINTEXT "encrypted"
	// destination — refuse rather than produce a file that only looks migrated.
	if !sqlitedrv.CodecLinked() {
		return nil, fmt.Errorf("migrate: cgo build but libsqlcipher is NOT linked (CodecLinked()=false) — the destination would be plaintext; rebuild with -tags \"libsqlite3 sqlite_fts5\" + CGO_LDFLAGS=-lsqlcipher")
	}

	rep := &MigrateReport{}
	for _, tt := range []struct {
		dir string
		pt  sqlitedrv.PrincipalType
	}{
		{filepath.Join(dataDir, "users"), sqlitedrv.PrincipalUser},
		{filepath.Join(dataDir, "orgs"), sqlitedrv.PrincipalOrg},
	} {
		entries, err := os.ReadDir(tt.dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return rep, fmt.Errorf("migrate: read %q: %w", tt.dir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			id := e.Name()
			dbPath := filepath.Join(tt.dir, id, "data.db")
			n, changed, err := encryptTenantFile(dbPath, masterKey, tt.pt, id, dryRun)
			if err != nil {
				return rep, fmt.Errorf("migrate %s %q: %w", tt.pt, id, err)
			}
			if changed {
				rep.Encrypted = append(rep.Encrypted, dbPath)
				rep.Rows += n
			} else {
				rep.Skipped = append(rep.Skipped, dbPath)
			}
		}
	}
	return rep, nil
}

// encryptTenantFile converts a single plaintext tenant db to an enveloped
// encrypted file. Returns (rowsCopied, changed, err). It is a no-op (changed
// false) when the file is absent or already has a DEK sidecar.
func encryptTenantFile(dbPath string, masterKey []byte, pt sqlitedrv.PrincipalType, id string, dryRun bool) (int, bool, error) {
	if fileExists(dbPath + dekSuffix) {
		return 0, false, nil // already encrypted
	}
	if !fileExists(dbPath) {
		return 0, false, nil // nothing to migrate
	}
	if dryRun {
		return 0, true, nil
	}

	// 1. Open the plaintext source READ-WRITE (never mode=ro) so SQLite runs WAL
	// recovery and every committed frame in a pending -wal becomes visible: a
	// read-only handle cannot build the -shm and would silently read ONLY the main
	// file, dropping commits a stopped daemon left uncheckpointed. A single
	// connection (no pool) then folds the WAL into the main file with a VERIFIED
	// TRUNCATE checkpoint — this both consolidates the data and proves no other
	// process holds the db (a still-running daemon makes the checkpoint busy, which
	// we surface as an error: migrating a live money db could miss in-flight rows).
	// We only issue SELECTs afterwards, so the source's logical data is unchanged;
	// it is retained as .plaintext.bak either way.
	// Open on the ONE canonical "sqlite" driver (hanzoai/sqlite) with no key — a
	// plaintext read; PragmaDSN escapes the path and emits busy_timeout+WAL in the
	// active backend's form. This path is CGO-only (encryption) where "sqlite" is
	// mattn/SQLCipher, which reads a keyless file as plain SQLite.
	srcDSN := sqlitedrv.PragmaDSN(dbPath, []sqlitedrv.Pragma{
		{Name: "busy_timeout", Value: "10000"},
		{Name: "journal_mode", Value: "WAL"},
	})
	src, err := sql.Open("sqlite", srcDSN)
	if err != nil {
		return 0, false, fmt.Errorf("open plaintext source: %w", err)
	}
	src.SetMaxOpenConns(1)
	defer src.Close()
	if err := src.Ping(); err != nil {
		return 0, false, fmt.Errorf("ping plaintext source: %w", err)
	}
	if err := checkpointWAL(src); err != nil {
		return 0, false, fmt.Errorf("fold source WAL into %q (is the daemon stopped?): %w", dbPath, err)
	}

	// 2. Create a fresh encrypted destination at a temp path (its own sidecar).
	tmpDB := dbPath + ".encrypting"
	_ = os.Remove(tmpDB)
	_ = os.Remove(tmpDB + dekSuffix)
	dek, err := resolveDEK(tmpDB, masterKey, pt, id)
	if err != nil {
		return 0, false, fmt.Errorf("mint destination DEK: %w", err)
	}
	defer zeroBytes(dek)
	_, dstDSN := encDriverDSN(tmpDB, dek, DefaultConfig().SQLite)
	dst, err := sql.Open("sqlite", dstDSN)
	if err != nil {
		return 0, false, fmt.Errorf("open encrypted destination: %w", err)
	}
	dst.SetMaxOpenConns(1) // serialize writes + the pre-cutover checkpoint (no reader contention)
	for _, stmt := range baseSchemaDDL {
		if _, err := dst.Exec(stmt); err != nil {
			dst.Close()
			return 0, false, fmt.Errorf("init destination schema: %w", err)
		}
	}

	// 3. Copy every concrete (non-virtual) user table, NULL-preserving.
	tables, err := userTables(src)
	if err != nil {
		dst.Close()
		return 0, false, err
	}
	rows := 0
	for _, tbl := range tables {
		n, err := copyTable(src, dst, tbl)
		if err != nil {
			dst.Close()
			return 0, false, fmt.Errorf("copy table %q: %w", tbl, err)
		}
		rows += n
	}

	// 4. Verify per-table content parity (multiset of row hashes) before cutover.
	for _, tbl := range tables {
		if err := verifyTableParity(src, dst, tbl); err != nil {
			dst.Close()
			return 0, false, fmt.Errorf("parity check failed for %q — aborting, source untouched: %w", tbl, err)
		}
	}
	// Fold the destination's own WAL into its main file with a VERIFIED TRUNCATE
	// checkpoint BEFORE closing, so the encrypted db is a single self-contained
	// file. The cutover renames <db> + <db>.dek only; a committed frame stranded in
	// a -wal that is not carried across would be silently lost.
	if err := checkpointWAL(dst); err != nil {
		dst.Close()
		return 0, false, fmt.Errorf("flush encrypted destination WAL: %w", err)
	}
	if err := dst.Close(); err != nil {
		return 0, false, fmt.Errorf("close destination: %w", err)
	}
	_ = src.Close()

	// Defense in depth: after a TRUNCATE checkpoint + close there must be no pending
	// WAL. If a non-empty -wal somehow survived, REFUSE to cut over rather than drop
	// its frames — the source is still intact, so the operator can retry.
	if err := assertNoPendingWAL(tmpDB); err != nil {
		return 0, false, err
	}

	// 5. Atomic cutover: move the plaintext aside, swap the encrypted pair in.
	if err := cutover(dbPath, tmpDB); err != nil {
		return 0, false, err
	}
	return rows, true, nil
}

// cutover moves the plaintext db to a .plaintext.bak backup and renames the
// encrypted temp db (+ sidecar) into the canonical path.
//
// The encrypted db is checkpointed (TRUNCATE) and asserted WAL-free before this
// runs, so it is a single self-contained file: only <db> and <db>.dek carry data
// and there is no -wal whose frames could be orphaned by renaming just the main
// file. Any zero-length -wal/-shm husks left beside the temp db are discarded.
//
// The sidecar is renamed LAST: until it lands the daemon still refuses the
// (now-encrypted) file, so a crash mid-cutover fails closed rather than exposing a
// keyless db.
func cutover(dbPath, tmpDB string) error {
	// Stale WAL/SHM companions of the plaintext db must not shadow the new file.
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(dbPath + suf)
	}
	if err := os.Rename(dbPath, dbPath+".plaintext.bak"); err != nil {
		return fmt.Errorf("backup plaintext %q: %w", dbPath, err)
	}
	if err := os.Rename(tmpDB, dbPath); err != nil {
		return fmt.Errorf("swap encrypted db into %q: %w", dbPath, err)
	}
	// Discard the temp db's now-empty WAL/SHM husks (its data was checkpointed into
	// the main file above) so they don't linger beside the canonical path.
	for _, suf := range []string{"-wal", "-shm"} {
		_ = os.Remove(tmpDB + suf)
	}
	if err := os.Rename(tmpDB+dekSuffix, dbPath+dekSuffix); err != nil {
		return fmt.Errorf("swap DEK sidecar into %q: %w", dbPath+dekSuffix, err)
	}
	return nil
}

// checkpointWAL folds the write-ahead log back into the main database file and
// truncates the -wal to zero (a self-contained single file). It errors unless the
// checkpoint FULLY completed (busy==0): a partial checkpoint would leave committed
// frames only in the -wal, which callers are about to either drop (cutover) or
// treat as folded (source read). PRAGMA wal_checkpoint always returns one row
// (busy, log, checkpointed); in a non-WAL journal mode busy is 0 and it is a
// no-op.
func checkpointWAL(db *sql.DB) error {
	var busy, logFrames, checkpointed int
	if err := db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		if err == sql.ErrNoRows {
			return nil // non-WAL journal: nothing to fold
		}
		return fmt.Errorf("wal_checkpoint: %w", err)
	}
	if busy != 0 {
		return fmt.Errorf("wal_checkpoint incomplete (busy=%d, log=%d, checkpointed=%d): another connection holds the WAL", busy, logFrames, checkpointed)
	}
	return nil
}

// assertNoPendingWAL fails if dbPath still has a non-empty -wal: after a verified
// TRUNCATE checkpoint + close there must be none, so a surviving one means
// committed frames would be lost by a main-file-only rename. The source is still
// intact at this point, so erroring lets the operator retry without data loss.
func assertNoPendingWAL(dbPath string) error {
	fi, err := os.Stat(dbPath + "-wal")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s-wal: %w", dbPath, err)
	}
	if fi.Size() > 0 {
		return fmt.Errorf("encrypted %q still has a %d-byte -wal after checkpoint+close; refusing cutover to avoid dropping committed frames", dbPath, fi.Size())
	}
	return nil
}

// userTables lists concrete (non-virtual, non-internal) tables to copy.
func userTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			return nil, err
		}
		// Skip virtual tables (e.g. sqlite-vec _vectors): they cannot be
		// INSERT-copied and hold regenerable embeddings, not source-of-truth data.
		if strings.Contains(strings.ToUpper(ddl), "VIRTUAL TABLE") {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// copyTable copies all rows of table tbl from src to dst, preserving SQL NULLs and
// storage classes via a per-column *any scan.
func copyTable(src, dst *sql.DB, tbl string) (int, error) {
	cols, err := tableColumns(src, tbl)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, nil
	}
	rows, err := src.Query(`SELECT * FROM "` + tbl + `"`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",")
	insert := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`, tbl, quoteCols(cols), placeholders)

	tx, err := dst.Begin()
	if err != nil {
		return 0, err
	}
	stmt, err := tx.Prepare(insert)
	if err != nil {
		tx.Rollback()
		return 0, err
	}
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			tx.Rollback()
			return 0, err
		}
		if _, err := stmt.Exec(vals...); err != nil {
			tx.Rollback()
			return 0, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		tx.Rollback()
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// verifyTableParity asserts src and dst hold the SAME multiset of rows for tbl.
// Each row is hashed with a NULL-sentinel + storage-class-tagged encoding, so a
// single NULL coerced to "", a bool flipped, or a row dropped/added is detected —
// which a bare COUNT(*) cannot.
func verifyTableParity(src, dst *sql.DB, tbl string) error {
	sh, err := tableRowHashes(src, tbl)
	if err != nil {
		return err
	}
	dh, err := tableRowHashes(dst, tbl)
	if err != nil {
		return err
	}
	if len(sh) != len(dh) {
		return fmt.Errorf("row count mismatch: src=%d dst=%d", len(sh), len(dh))
	}
	sort.Strings(sh)
	sort.Strings(dh)
	for i := range sh {
		if sh[i] != dh[i] {
			return fmt.Errorf("row content mismatch at sorted index %d", i)
		}
	}
	return nil
}

func tableRowHashes(db *sql.DB, tbl string) ([]string, error) {
	rows, err := db.Query(`SELECT * FROM "` + tbl + `"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var hashes []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		hashes = append(hashes, hashRow(vals))
	}
	return hashes, rows.Err()
}

// hashRow computes a canonical, storage-class-tagged, NULL-honouring hash of a
// row's column values.
func hashRow(vals []any) string {
	h := sha256.New()
	var lenbuf [8]byte
	for _, v := range vals {
		switch t := v.(type) {
		case nil:
			h.Write([]byte{0})
		case int64:
			h.Write([]byte{1})
			binary.BigEndian.PutUint64(lenbuf[:], uint64(t))
			h.Write(lenbuf[:])
		case float64:
			h.Write([]byte{2})
			binary.BigEndian.PutUint64(lenbuf[:], math.Float64bits(t))
			h.Write(lenbuf[:])
		case string:
			h.Write([]byte{3})
			writeLenPrefixed(h, []byte(t), lenbuf[:])
		case []byte:
			h.Write([]byte{4})
			writeLenPrefixed(h, t, lenbuf[:])
		case bool:
			h.Write([]byte{5})
			if t {
				h.Write([]byte{1})
			} else {
				h.Write([]byte{0})
			}
		default:
			h.Write([]byte{9})
			writeLenPrefixed(h, []byte(fmt.Sprintf("%v", t)), lenbuf[:])
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func writeLenPrefixed(h interface{ Write([]byte) (int, error) }, b, scratch []byte) {
	binary.BigEndian.PutUint64(scratch[:8], uint64(len(b)))
	h.Write(scratch[:8])
	h.Write(b)
}

func tableColumns(db *sql.DB, tbl string) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info("` + tbl + `")`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols = append(cols, name)
	}
	return cols, rows.Err()
}

func quoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = `"` + c + `"`
	}
	return strings.Join(q, ",")
}
