package audit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// BenchmarkMoveOnlineRate is the rate a live chain moves at, and what the move costs
// the appends made meanwhile: the megabytes of file a second, how long the chain was
// held still for the tail, and the slowest append. Run it as
//
//	CLOUD_TEST_SQL_DSN=... go test -tags "libsqlite3 sqlite_fts5 sqlite_math_functions" \
//	    -run '^$' -bench MoveOnlineRate/1024MB -benchtime 1x ./audit/
//
// The rows are 4 KB of incompressible text each (a record with its before and after),
// written by SQL in one transaction, so the file is the size asked for without the
// hours it takes to append that many records one at a time. The server is the local
// one on a unix socket, so the rate is the mover's ceiling: hanzo-sql across a network
// with a synchronous standby is slower.
func BenchmarkMoveOnlineRate(b *testing.B) {
	for _, mb := range []int{64, 1024} {
		b.Run(fmt.Sprintf("%dMB", mb), func(b *testing.B) {
			if !sqlitedrv.CodecLinked() {
				b.Skip("no C codec linked: the move reads the live chain while it is appended to")
			}
			for range b.N {
				b.StopTimer()
				b.Setenv(locus.Env, "")
				dir := b.TempDir()
				rec, err := Open(dir, "audit", nil)
				if err != nil {
					b.Fatal(err)
				}
				rows := mb * 1024 * 1024 / 4200
				ctx := context.Background()
				if _, err := rec.db.ExecContext(ctx, fmt.Sprintf(`WITH RECURSIVE c(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM c WHERE i + 1 < %d)
					INSERT INTO audit_log (seq, ts, action, result, path, before, after, prev_hash, hash)
					SELECT i, '2026-11-13T00:00:00Z', 'bench', 'success', '/v1/bench/' || i, hex(randomblob(500)), hex(randomblob(500)), hex(randomblob(32)), hex(randomblob(32)) FROM c`, rows)); err != nil {
					b.Fatal(err)
				}
				if _, err := rec.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
					b.Fatal(err)
				}
				path, _ := namespace.Path(dir, namespace.System(), "audit")
				st, err := os.Stat(path)
				if err != nil {
					b.Fatal(err)
				}
				sqltest.Server(b)

				on := rec.online()
				var held time.Duration
				freeze := on.Freeze
				on.Freeze = func(ctx context.Context, fn func(locus.Querier) error) error {
					at := time.Now()
					defer func() { held = time.Since(at) }()
					return freeze(ctx, fn)
				}
				// Appends at about 200 a second, as a busy fleet makes them, with the
				// slowest one kept. They continue the bench's rows, so the chain is not
				// a valid hash chain; the mover does not read hashes, and the head is
				// held equal whole.
				var slowest, appended atomic.Int64
				stop := make(chan struct{})
				var wg sync.WaitGroup
				wg.Go(func() {
					for {
						select {
						case <-stop:
							return
						case <-time.After(5 * time.Millisecond):
						}
						at := time.Now()
						if _, err := rec.Append(ctx, sampleRecord("during")); err != nil {
							b.Errorf("append: %v", err)
							return
						}
						appended.Add(1)
						if d := time.Since(at).Nanoseconds(); d > slowest.Load() {
							slowest.Store(d)
						}
					}
				})
				b.StartTimer()
				rep, err := rec.moveOnline(ctx, nil, on)
				b.StopTimer()
				close(stop)
				wg.Wait()
				if err != nil {
					b.Fatal(err)
				}
				mbs := float64(st.Size()) / 1e6
				b.ReportMetric(mbs/b.Elapsed().Seconds(), "MB/s")
				b.ReportMetric(float64(held.Milliseconds()), "held-ms")
				b.ReportMetric(float64(slowest.Load())/1e6, "slowest-append-ms")
				b.Logf("moved %d rows, file %.0f MB, took %v, held the chain %v for the tail, %d appends meanwhile, slowest %v",
					rep.Rows, mbs, rep.Took, held, appended.Load(), time.Duration(slowest.Load()))
				_ = rec.Close()
			}
		})
	}
}
