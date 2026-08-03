// Command iam-import copies an identity store from the standalone IAM into the
// store the EMBEDDED iam subsystem reads (apps/iam), so cloud can serve identity
// in-process and the separate pod can be retired.
//
// WHY THIS IS NOT A FILE COPY. The two stores differ in three ways at once. The
// standalone writes `iam.db` as a PLAINTEXT SQLite file — the literal
// "SQLite format 3" header, which is the exposure apps/iam/openStore exists to
// remove. The embed writes `global.db` through cek, keyed from the process master,
// and names the file after the PARTITION it holds rather than after the product.
// And the embed opens exactly one path, derived by namespace from the data dir, so
// a file dropped beside it is not a file it will read.
//
// So the rows are moved, not the bytes: read the source with a plain driver, write
// the destination through the same cek.Open the subsystem itself uses. The result
// is the identity graph that was in the clear, at rest under a key, in the place
// the running binary already looks.
//
// WHAT MOVES. Everything in `_entities` — hanzoai/iam stores users, organizations,
// applications, certs, providers, memberships, sessions and tokens as one document
// table, so there is one thing to copy and no per-type list to keep in step with a
// schema that grows. The signing CERTS are the load-bearing rows: an empty set is
// why the embedded store answers `{"keys":[]}` at /v1/iam/.well-known/jwks, which
// would fail every token in the fleet the moment traffic moved to it.
//
// IDEMPOTENT, AND IT REFUSES TO GUESS. Rows are keyed by (kind, name) — the identity
// hanzoai/iam already uses — so a re-run updates rather than duplicates, and an
// interrupted run is resumable. It will not write into a destination that already
// holds rows unless --overwrite says so, because merging two live identity stores is
// a decision, not a default.
//
//	iam-import --src /path/to/iam.db --dir /var/lib/cloud [--dry-run]
//
// The master key comes from the environment the cloud binary already uses
// (CLOUD_KMS_MASTER_KEY_REF, base64), because a tool that took a key on the command
// line would put it in a shell history and a process list.
package main

import (
	"database/sql"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
	_ "github.com/hanzoai/sqlite"
)

// storeSubsystem must match apps/iam's constant: it is half the key derivation and
// the whole of the path, so a different value here writes a store the subsystem
// will never open — silently, and looking like success.
const storeSubsystem = "global"

func main() {
	var (
		src       = flag.String("src", "", "path to the standalone IAM sqlite file (plaintext iam.db)")
		dir       = flag.String("dir", "/var/lib/cloud", "cloud data dir — the embed's store is resolved under it by namespace")
		dryRun    = flag.Bool("dry-run", false, "report what would be written and change nothing")
		overwrite = flag.Bool("overwrite", false, "allow writing into a destination that already holds rows")
		initSchema = flag.Bool("init-schema", false, "create _entities from the SOURCE's DDL when the destination has none (a rehearsal destination, or a data dir the embed has never booted against)")
	)
	flag.Parse()
	if *src == "" {
		die("--src is required")
	}
	if err := run(*src, *dir, *dryRun, *overwrite, *initSchema); err != nil {
		die("%v", err)
	}
}

func run(src, dir string, dryRun, overwrite, initSchema bool) error {
	master := os.Getenv("CLOUD_KMS_MASTER_KEY_REF")
	if master == "" {
		return fmt.Errorf("CLOUD_KMS_MASTER_KEY_REF is unset: without the master there is no key, and a store written under the wrong one is unreadable by the binary that must serve it")
	}
	raw, err := base64.StdEncoding.DecodeString(master)
	if err != nil {
		return fmt.Errorf("decode master key: %w", err)
	}
	if err := cek.SetMaster(raw); err != nil {
		return fmt.Errorf("set master: %w", err)
	}

	// Source: a plain file, opened read-only. immutable=1 is deliberate — the source
	// may still be attached to a running pod, and this must never write to it or
	// recover its WAL out from under the process that owns it.
	from, err := sql.Open("sqlite", "file:"+src+"?mode=ro&immutable=1")
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer from.Close()

	to, err := cek.Open(namespace.System(), storeSubsystem, dir)
	if err != nil {
		return fmt.Errorf("open destination (keyed): %w", err)
	}
	defer to.Close()

	counts, err := kindCounts(from)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}
	total := 0
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	fmt.Println("source:")
	for _, k := range kinds {
		fmt.Printf("  %-16s %d\n", k, counts[k])
		total += counts[k]
	}
	fmt.Printf("  %-16s %d\n", "TOTAL", total)
	if counts["certs"] == 0 {
		return fmt.Errorf("source holds no `certs`: those are the signing keys /v1/iam/.well-known/jwks publishes, and importing without them would leave every token in the fleet unverifiable")
	}

	// The embed OWNS the destination schema — it creates it on first boot, and a
	// store it has booted against is the normal case. --init-schema covers the two
	// that are not: a rehearsal destination, and a data dir the embed has never seen.
	// The DDL is copied from the SOURCE rather than written here, so the two stores
	// cannot disagree about a column this tool never knew about.
	if initSchema {
		if err := ensureSchema(from, to); err != nil {
			return fmt.Errorf("init schema: %w", err)
		}
	}
	var have int
	if err := to.QueryRow(`select count(*) from _entities`).Scan(&have); err != nil {
		return fmt.Errorf("read destination (is the schema present? the embed creates it at first boot; --init-schema creates it from the source): %w", err)
	}
	fmt.Printf("destination holds %d rows\n", have)
	if have > 0 && !overwrite {
		return fmt.Errorf("destination already holds %d rows; merging two identity stores is a decision — re-run with --overwrite to make it", have)
	}
	if dryRun {
		fmt.Printf("dry-run: would write %d rows into %s\n", total, dir)
		return nil
	}

	n, err := copyEntities(from, to)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	fmt.Printf("wrote %d rows\n", n)

	after, err := kindCounts(to)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	// Verify what the fleet actually depends on, not just the total: a count that
	// matches while `certs` is empty is the failure this tool exists to prevent.
	for _, k := range kinds {
		if after[k] != counts[k] {
			return fmt.Errorf("verify: %s = %d in destination, %d in source", k, after[k], counts[k])
		}
	}
	fmt.Printf("verified: every kind matches, certs=%d\n", after["certs"])
	return nil
}

// ensureSchema copies the source's own CREATE statements for _entities and its
// indexes. Reproducing them here would be a second copy of a schema this package
// does not own, free to drift from the one that holds the data.
func ensureSchema(from, to *sql.DB) error {
	rows, err := from.Query(`select sql from sqlite_master where tbl_name = '_entities' and sql is not null`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ddl []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		ddl = append(ddl, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range ddl {
		if _, err := to.Exec(d); err != nil && !alreadyExists(err) {
			return fmt.Errorf("%s: %w", d, err)
		}
	}
	return nil
}

func alreadyExists(err error) bool {
	return err != nil && (contains(err.Error(), "already exists"))
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func kindCounts(db *sql.DB) (map[string]int, error) {
	rows, err := db.Query(`select kind, count(*) from _entities group by kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// copyEntities streams every row across in ONE transaction: identity is not
// meaningful half-moved, so a failure must leave the destination as it was rather
// than serving a partial graph that looks like a working one.
func copyEntities(from, to *sql.DB) (int, error) {
	cols, err := columns(from)
	if err != nil {
		return 0, err
	}
	sel := "select " + join(cols) + " from _entities"
	rows, err := from.Query(sel)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	tx, err := to.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // rolled back unless Commit succeeds

	ins := "insert or replace into _entities (" + join(cols) + ") values (" + placeholders(len(cols)) + ")"
	stmt, err := tx.Prepare(ins)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	n := 0
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, err
		}
		if _, err := stmt.Exec(vals...); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	return n, tx.Commit()
}

// columns reads the source's own column list rather than naming them here: the
// document table gains columns as hanzoai/iam grows, and a hand-written list would
// drop the newest one silently — which for an identity store means losing a field
// nobody notices until something cannot authenticate.
func columns(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`select name from pragma_table_info('_entities') order by cid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source has no _entities table")
	}
	return out, rows.Err()
}

func join(cols []string) string {
	s := ""
	for i, c := range cols {
		if i > 0 {
			s += ", "
		}
		s += `"` + c + `"`
	}
	return s
}

func placeholders(n int) string {
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ", "
		}
		s += "?"
	}
	return s
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "iam-import: "+f+"\n", a...)
	os.Exit(1)
}
