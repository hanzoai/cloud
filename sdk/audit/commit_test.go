package audit

import (
	"context"
	"github.com/hanzoai/cloud/internal/sqltest"
	"sync"
	"testing"
	"time"
)

// A burst larger than one transaction lands whole: every append returns its own
// sealed record, the seqs are a gapless run, and the chain verifies.
func TestGroupCommit_BurstLandsWholeAndInOrder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		ctx := context.Background()
		const n = 3*maxBatch + 7
		seqs := make([]uint64, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				out, err := rec.Append(ctx, sampleRecord("POST /v1/event"))
				seqs[i], errs[i] = out.Seq, err
			})
		}
		wg.Wait()
		seen := make(map[uint64]bool, n)
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("append %d: %v", i, errs[i])
			}
			if seen[seqs[i]] {
				t.Fatalf("seq %d answered twice", seqs[i])
			}
			seen[seqs[i]] = true
		}
		for s := range uint64(n) {
			if !seen[s] {
				t.Fatalf("seq %d answered to nobody: a gap", s)
			}
		}
		iv, err := rec.Verify(ctx)
		if err != nil || iv.Verdict != Intact || iv.Count != n {
			t.Fatalf("verify: %+v, %v", iv, err)
		}
	})
}

// A transaction that fails refuses every record in it and moves nothing: the next
// append takes the same seq, so the chain never gaps.
func TestGroupCommit_FailedBatchMovesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		ctx := context.Background()
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/a")); err != nil {
			t.Fatal(err)
		}
		count, head := rec.Head()

		// The database refuses the second record of the batch, after the first is in the
		// transaction: the engine's own refusal, which no check of ours anticipates.
		if rec.d.Postgres() {
			if _, err := rec.db.Exec(`CREATE FUNCTION refuse_c() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'refused'; END $$;
CREATE TRIGGER refuse_c BEFORE INSERT ON audit_log FOR EACH ROW WHEN (NEW.action = 'POST /v1/c') EXECUTE FUNCTION refuse_c()`); err != nil {
				t.Fatal(err)
			}
		} else if _, err := rec.db.Exec(`CREATE TRIGGER refuse_c BEFORE INSERT ON audit_log WHEN NEW.action = 'POST /v1/c' BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
			t.Fatal(err)
		}
		rec.mu.Lock()
		ok := &waiter{rec: sampleRecord("POST /v1/b"), at: time.Now(), done: make(chan written, 1)}
		clash := &waiter{rec: sampleRecord("POST /v1/c"), at: time.Now(), done: make(chan written, 1)}
		rec.commit(ctx, []*waiter{ok, clash})
		rec.mu.Unlock()
		for _, w := range []*waiter{ok, clash} {
			if out := <-w.done; out.err == nil {
				t.Fatal("a record in a failed transaction was answered as written")
			}
		}
		if c, h := rec.Head(); c != count || h != head {
			t.Fatalf("a failed batch moved the head: %d %s, want %d %s", c, h, count, head)
		}
		if n := rows(t, rec); n != int(count) {
			t.Fatalf("the failed transaction left %d rows, want %d", n, count)
		}
		// And the chain goes on from where it was.
		next, err := rec.Append(ctx, sampleRecord("POST /v1/d"))
		if err != nil || next.Seq != count || next.PrevHash != head {
			t.Fatalf("append after the failed batch: seq %d prev %q, %v; want seq %d prev %q", next.Seq, next.PrevHash, err, count, head)
		}
	})
}

func rows(t *testing.T, r *Recorder) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A read never holds the writer: with the C codec the chain is read through handles of
// its own, so an append lands while a read is open on the chain. Before, GET
// /v1/admin/audit counted the whole chain on the writer's one connection and every
// append in the deployment waited behind it for minutes.
func TestReadsDoNotHoldTheWriter(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		ctx := context.Background()
		for range 50 {
			if _, err := rec.Append(ctx, sampleRecord("POST /v1/a")); err != nil {
				t.Fatal(err)
			}
		}
		if rec.read == rec.db && !rec.d.Postgres() {
			t.Skip("no C codec: the pure-Go envelope cannot share its file, so reads share the writer's handle")
		}
		// Hold a read open, mid-result, on the read handle.
		rs, err := rec.read.QueryContext(ctx, `SELECT seq FROM audit_log ORDER BY seq`)
		if err != nil {
			t.Fatal(err)
		}
		defer rs.Close()
		if !rs.Next() {
			t.Fatal("no rows")
		}
		done := make(chan error, 1)
		go func() { _, err := rec.Append(ctx, sampleRecord("POST /v1/b")); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("append beside an open read: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("an append waited on a read")
		}
	})
}

// The unfiltered total is the head, read without counting the chain.
func TestQueryTotalIsTheHead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		ctx := context.Background()
		for range 7 {
			if _, err := rec.Append(ctx, sampleRecord("POST /v1/a")); err != nil {
				t.Fatal(err)
			}
		}
		rows, total, err := rec.Query(ctx, Filter{Limit: 3})
		if err != nil || total != 7 || len(rows) != 3 || rows[0].Seq != 6 {
			t.Fatalf("query: %d rows, total %d, %v", len(rows), total, err)
		}
		if _, total, _ := rec.Query(ctx, Filter{Action: "POST /v1/none"}); total != 0 {
			t.Fatalf("a filtered total still counts its matches, got %d", total)
		}
	})
}

// Behind names how long the oldest waiting append has waited, and zero when none waits.
func TestBehindNamesTheOldestWait(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rec, _ := openTemp(t)
		if b := rec.Behind(); b != 0 {
			t.Fatalf("an idle trail is %v behind", b)
		}
		rec.mu.Lock() // a commit in progress: appends queue behind it
		go func() { _, _ = rec.Append(context.Background(), sampleRecord("POST /v1/a")) }()
		time.Sleep(50 * time.Millisecond)
		if b := rec.Behind(); b < 40*time.Millisecond {
			t.Fatalf("a waiting append reads %v behind", b)
		}
		rec.mu.Unlock()
		deadline := time.Now().Add(2 * time.Second)
		for rec.Behind() != 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if b := rec.Behind(); b != 0 {
			t.Fatalf("a written trail still reads %v behind", b)
		}
	})
}
