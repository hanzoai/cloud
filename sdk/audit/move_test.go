package audit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
)

// A chain is moved while it is written, so the file has to be shared between the
// appender and the mover's reads: the C codec's behaviour, as the shipped image has it.
const needsCodec = "the move reads the live chain while it is appended to"

// liveChain is a recorder on a SQLite file holding n records, with hanzo-sql reachable
// and the file not yet moved.
func liveChain(t *testing.T, n int) (*Recorder, string) {
	t.Helper()
	kit.Codec(t, needsCodec)
	t.Setenv(locus.Env, "")
	rec, dir := openTemp(t)
	for i := range n {
		if _, err := rec.Append(context.Background(), sampleRecord(fmt.Sprintf("seed.%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	sqltest.Server(t)
	return rec, dir
}

func moved(t *testing.T, name, dir string) locus.Place {
	t.Helper()
	place, err := locus.Where(namespace.System(), name, dir)
	if err != nil {
		t.Fatal(err)
	}
	return place
}

// intact walks the chain the recorder reads now and requires every record to be there,
// linked and unaltered.
func intact(t *testing.T, rec *Recorder, want int) {
	t.Helper()
	iv, err := rec.Verify(context.Background())
	if err != nil || iv.Verdict != Intact || int(iv.Count) != want {
		t.Fatalf("the chain is %+v, %v; want %d records intact", iv, err, want)
	}
	if count, _ := rec.Head(); int(count) != want {
		t.Fatalf("the head counts %d records, want %d", count, want)
	}
}

// Appends continue while the history is copied, every one of them arrives, and the
// chain verifies end to end on hanzo-sql.
func TestAppendsDuringTheCopyAheadArrive(t *testing.T) {
	rec, dir := liveChain(t, 600)
	on := rec.online()
	on.Batch = 25

	var appended atomic.Int64
	var before int64 // appended when the freeze began
	freeze := on.Freeze
	on.Freeze = func(ctx context.Context, fn func(locus.Querier) error) error {
		before = appended.Load()
		return freeze(ctx, fn)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := rec.Append(context.Background(), sampleRecord("during")); err != nil {
				t.Errorf("an append failed while the chain moved: %v", err)
				return
			}
			appended.Add(1)
		}
	})
	rep, err := rec.moveOnline(context.Background(), nil, on)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if before == 0 {
		t.Fatal("no append landed during the copy ahead, so it proved nothing")
	}
	total := 600 + int(appended.Load())
	if int(rep.Rows) < 600+int(before) {
		t.Fatalf("the report counts %d rows, the file held at least %d", rep.Rows, 600+before)
	}
	if moved(t, rec.name, dir) != locus.OnServer {
		t.Fatal("the chain is not on hanzo-sql")
	}
	if _, d := rec.view(); !d.Postgres() {
		t.Fatal("the recorder still writes to the file")
	}
	intact(t, rec, total)

	// The next append lands on hanzo-sql and extends the same chain.
	out, err := rec.Append(context.Background(), sampleRecord("after"))
	if err != nil || int(out.Seq) != total {
		t.Fatalf("append after the move: seq %d, %v; want %d", out.Seq, err, total)
	}
	intact(t, rec, total+1)

	// A restart opens the chain where it lives now.
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir, "audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = again.Close() })
	intact(t, again, total+1)
}

// A process killed between the copy ahead and the tail leaves the file authoritative,
// writes continue on it, and the next attempt completes the move.
func TestAMoveStoppedBetweenPhasesLeavesTheFile(t *testing.T) {
	rec, dir := liveChain(t, 120)
	ctx := context.Background()

	on := rec.online()
	on.Batch = 10
	stopped, cancel := context.WithCancel(ctx)
	on.Freeze = func(context.Context, func(locus.Querier) error) error {
		cancel() // killed after phase 1, before phase 2 holds anything
		return stopped.Err()
	}
	if _, err := rec.moveOnline(stopped, nil, on); !errors.Is(err, context.Canceled) {
		t.Fatalf("move: %v", err)
	}
	if moved(t, rec.name, dir) != locus.OnFile {
		t.Fatal("a stopped move left the chain somewhere other than its file")
	}
	if _, d := rec.view(); d.Postgres() {
		t.Fatal("the recorder was switched by a move that did not finish")
	}
	if exists, err := locus.Exists(namespace.System(), rec.name); err != nil || exists {
		t.Fatalf("a schema was left behind: %v, %v", exists, err)
	}
	for i := range 5 {
		if _, err := rec.Append(ctx, sampleRecord(fmt.Sprintf("between.%d", i))); err != nil {
			t.Fatalf("the file stopped serving: %v", err)
		}
	}
	intact(t, rec, 125)

	// The retry copies it all again, the rows appended meanwhile included.
	if _, err := rec.MoveOnline(ctx, nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if moved(t, rec.name, dir) != locus.OnServer {
		t.Fatal("the retry did not move the chain")
	}
	intact(t, rec, 125)
}

// A move that fails says why, is not retried at once, and the chain keeps its file.
func TestAMoveThatFailsKeepsTheFile(t *testing.T) {
	rec, dir := liveChain(t, 40)
	on := rec.online()
	boom := errors.New("the tail could not be held")
	on.Freeze = func(context.Context, func(locus.Querier) error) error { return boom }
	if _, err := rec.moveOnline(context.Background(), nil, on); !errors.Is(err, boom) {
		t.Fatalf("move: %v", err)
	}
	if _, err := rec.MoveOnline(context.Background(), nil); !errors.Is(err, locus.ErrBackoff) {
		t.Fatalf("a failed move was tried again at once: %v", err)
	}
	if moved(t, rec.name, dir) != locus.OnFile {
		t.Fatal("the chain left its file")
	}
	if _, err := rec.Append(context.Background(), sampleRecord("still")); err != nil {
		t.Fatal(err)
	}
	intact(t, rec, 41)
}

// An append that arrives while the tail is copied waits for it, and lands on hanzo-sql.
func TestAnAppendDuringTheTailWaitsAndLandsOnTheServer(t *testing.T) {
	rec, dir := liveChain(t, 80)
	on := rec.online()
	on.Batch = 20

	freeze := on.Freeze
	var landed chan Record
	var early atomic.Bool
	on.Freeze = func(ctx context.Context, fn func(locus.Querier) error) error {
		return freeze(ctx, func(q locus.Querier) error {
			landed = make(chan Record, 1)
			go func() {
				out, err := rec.Append(context.Background(), sampleRecord("held"))
				if err != nil {
					t.Errorf("the held append failed: %v", err)
				}
				landed <- out
			}()
			select {
			case <-landed:
				early.Store(true)
			case <-time.After(300 * time.Millisecond):
			}
			return fn(q)
		})
	}
	if _, err := rec.moveOnline(context.Background(), nil, on); err != nil {
		t.Fatalf("move: %v", err)
	}
	if early.Load() {
		t.Fatal("an append was written while the chain was held still")
	}
	var out Record
	select {
	case out = <-landed:
	case <-time.After(10 * time.Second):
		t.Fatal("the held append never returned")
	}
	if out.Seq != 80 {
		t.Fatalf("the held append took seq %d, want 80", out.Seq)
	}
	if moved(t, rec.name, dir) != locus.OnServer {
		t.Fatal("the chain is not on hanzo-sql")
	}
	// It is in the schema, and nowhere else the chain reads.
	rows, total, err := rec.Query(context.Background(), Filter{Action: "held"})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Seq != 80 {
		t.Fatalf("the held append is not on hanzo-sql: %v rows of %d, %v", len(rows), total, err)
	}
	intact(t, rec, 81)
}

// The chains the fleet wrote before one holder appended move too, and the trail reads
// each wherever it is.
func TestTheHistoryChainsMoveAndTheTrailReadsThemWhereverTheyAre(t *testing.T) {
	kit.Codec(t, needsCodec)
	t.Setenv(locus.Env, "")
	rec, dir := openTemp(t)
	ctx := context.Background()
	for i := range 30 {
		if _, err := rec.Append(ctx, sampleRecord(fmt.Sprintf("live.%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"audit-iam", "audit-kms"} {
		old, err := Open(dir, name, nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 12 {
			if _, err := old.Append(ctx, sampleRecord(fmt.Sprintf("old.%d", i))); err != nil {
				t.Fatal(err)
			}
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
	}
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, "sweep")

	// Before anything moves, the trail reads the files.
	tr, err := rec.Trail(ctx)
	if err != nil || tr.Intact != 3 || tr.Records != 54 {
		t.Fatalf("trail before: %+v, %v", tr, err)
	}
	if err := rec.moveAll(ctx, nil); err != nil {
		t.Fatalf("move all: %v", err)
	}
	for _, name := range []string{"audit", "audit-iam", "audit-kms"} {
		if moved(t, name, dir) != locus.OnServer {
			t.Fatalf("%s is still a file", name)
		}
	}
	tr, err = rec.Trail(ctx)
	if err != nil || tr.Intact != 3 || tr.Broken != 0 || tr.Unread != 0 || tr.Records != 54 {
		t.Fatalf("trail after: %+v, %v", tr, err)
	}
	// A second pass has nothing to do.
	if err := rec.moveAll(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
