package iam

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	iamstore "github.com/hanzoai/iam/pkg/store"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
)

// copy.go is `iam copy` (/iam copy in the image): the move of this deployment's
// identity store from the file on its volume to hanzoai/sql, and the proof the two
// hold the same records. The move itself is a boot (openShared, moveIn): pointing
// IAM_STORE_BACKEND at sql copies, compares and freezes the file. This tool is the
// rehearsal before it and the way back after it.
//
//	iam copy           copy every record the file holds to sql, then compare
//	iam copy --prune   the same, removing sql records the file does not hold:
//	                   the last pass, run with writes frozen
//	iam copy --check   compare only
//	iam copy --reverse sql back to the file: the rollback. Once the two compare
//	                   equal it removes the moved mark, so the file serves again
//	                   when IAM_STORE_BACKEND is unset.
//
// It runs inside a cloud pod, where the master key that unlocks the file and the
// address of the server (IAM_SQL_ADDR) both are. A pass copies only what changed,
// so it can run while the file still serves; it exits non-zero unless the two
// stores compare equal. --check writes to neither store: both are opened as they
// are (sealed, iamstore.Connect), where a copy prepares them first.
//
// While IAM's store is shared (IAM_STORE_BACKEND=sql) deploy cloud with strategy
// Recreate until no replica older than iam v1.34.138 can still run: an older
// replica that rewrites a user or membership drops its nameKey, and that row is
// invisible to a case-insensitive lookup until a replica boots and prepares the
// store again. A rolling update runs both at once.
func RunCopy(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("iam copy", flag.ContinueOnError)
	fs.SetOutput(out)
	check := fs.Bool("check", false, "compare the two stores; copy nothing")
	prune := fs.Bool("prune", false, "remove the target's records the source does not hold")
	reverse := fs.Bool("reverse", false, "copy hanzoai/sql back into the file store")
	sqlAddr := fs.String("sql-addr", env.Or(SQLAddrEnv, ""), "address of hanzoai/sql (sql://host:port)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dir := cloud.DataDir()
	cloud.BootMaster(dir)
	if err := cloud.MasterErr(); err != nil {
		return fmt.Errorf("iam copy: the master key that unlocks %s: %w", StorePath(dir), err)
	}
	file, conn, err := copyFile(dir, *check)
	if err != nil {
		return err
	}
	defer conn.Close()
	shared, err := copySQL(*sqlAddr, *check)
	if err != nil {
		return fmt.Errorf("iam copy: open hanzoai/sql: %w", err)
	}
	defer shared.Close()

	src, dst, from, to := file, shared, StorePath(dir), "hanzoai/sql"
	if *reverse {
		src, dst, from, to = shared, file, to, from
	}
	if err := copyStores(ctx, src, dst, from, to, *check, *prune, out); err != nil {
		return err
	}
	if *reverse && !*check {
		if err := os.Remove(StorePath(dir) + movedSuffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("iam copy: the file holds the identities again, but its moved mark stays: %w", err)
		}
		fmt.Fprintf(out, "%s holds the identities again; unset %s to serve it\n", StorePath(dir), cloud.IAMStoreEnv)
	}
	return nil
}

// copyFile opens the file side of a copy: as it is for a comparison (--check),
// prepared for a copy.
func copyFile(dir string, check bool) (orm.DB, *sql.DB, error) {
	if check {
		return sealed(dir)
	}
	return openFile(dir)
}

// copySQL opens the hanzoai/sql side of a copy, as copyFile opens the file.
func copySQL(addr string, check bool) (orm.DB, error) {
	if check {
		return iamstore.Connect("sql", "", addr)
	}
	return openSQL("sql", addr)
}

// copyStores is RunCopy past opening the two stores.
func copyStores(ctx context.Context, src, dst orm.DB, from, to string, check, prune bool, out io.Writer) error {
	if !check {
		reports, err := iamstore.Copy(ctx, src, dst, prune)
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
		fmt.Fprintf(w, "kind\tsource\twritten\tsame\tremoved\t\n")
		for _, r := range reports {
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t\n", r.Kind, r.Source, r.Written, r.Same, r.Removed)
		}
		w.Flush()
		if err != nil {
			return err
		}
	}
	diffs, err := iamstore.Verify(ctx, src, dst)
	if err != nil {
		return err
	}
	if len(diffs) == 0 {
		fmt.Fprintf(out, "%s and %s hold the same identity records\n", from, to)
		return nil
	}
	for i, d := range diffs {
		if i == 20 {
			fmt.Fprintf(out, "... and %d more\n", len(diffs)-i)
			break
		}
		fmt.Fprintf(out, "%s %s/%s\n", d.What, d.Kind, d.ID)
	}
	return fmt.Errorf("iam copy: %d records differ between %s and %s", len(diffs), from, to)
}
