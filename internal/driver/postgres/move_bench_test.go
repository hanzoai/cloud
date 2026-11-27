package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// openBulk is a store whose rows weigh something: a blob and a paragraph each, as the
// stores that hold repositories and documents do.
func openBulk(db *sql.DB) (io.Closer, error) {
	d := dialect.Of(db)
	err := d.Migrate(context.Background(), db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS objects (
		  id `+d.Identity()+`,
		  org TEXT NOT NULL,
		  body `+d.Blob()+` NOT NULL,
		  note TEXT NOT NULL,
		  at BIGINT NOT NULL
		)`)
		return err
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// BenchmarkMoveRate is the rate a store moves at, as the megabytes a second its rows
// weigh: copy, identity resync and the read-back that checks every row. Run it as
//
//	scripts/postgres.sh go test -run '^$' -bench MoveRate/512MB -benchtime 1x ./internal/driver/postgres/
//
// The server is the throwaway one on a unix socket, so the number is the mover's own
// ceiling: a hanzo-sql across a network, with a synchronous standby, is slower.
func BenchmarkMoveRate(b *testing.B) {
	for _, size := range []int{16, 128, 512} {
		b.Run(fmt.Sprintf("%dMB", size), func(b *testing.B) {
			for range b.N {
				b.StopTimer()
				dir := b.TempDir()
				ns := namespace.MustOrg("acme")
				b.Setenv(locus.Env, "")
				db, err := locus.Open(ns, "bulk", dir)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := openBulk(db); err != nil {
					b.Fatal(err)
				}
				rows := size * 1024 * 1024 / 4600
				tx, err := db.Begin()
				if err != nil {
					b.Fatal(err)
				}
				stmt, err := tx.Prepare(`INSERT INTO objects (org, body, note, at) VALUES ($1, $2, $3, $4)`)
				if err != nil {
					b.Fatal(err)
				}
				blob := make([]byte, 4096)
				note := strings.Repeat("a paragraph of text ", 25)
				for i := range rows {
					if i%64 == 0 {
						if _, err := rand.Read(blob); err != nil { // incompressible, as packed objects are
							b.Fatal(err)
						}
					}
					if _, err := stmt.Exec(fmt.Sprintf("org%d", i%50), blob, note, time.Now().Unix()); err != nil {
						b.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				_ = db.Close()
				sqltest.Server(b)
				b.Setenv(locus.MoveEnv, "lazy")
				b.StartTimer()

				st, err := locus.Use(context.Background(), nil, ns, "bulk", dir, func(db *sql.DB) (io.Closer, error) {
					return openBulk(db)
				})
				if err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				_ = st.Close()
				place, err := locus.Where(ns, "bulk", dir)
				if err != nil || place != locus.OnServer {
					b.Fatalf("the store is at %v, %v", place, err)
				}
				b.ReportMetric(float64(size)/b.Elapsed().Seconds(), "MB/s")
			}
		})
	}
}
