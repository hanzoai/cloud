package audit

// Replicas of one deployment append to ONE chain. These tests open the chain twice,
// each open its own pool as two replicas' would be, and contend for it.

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/namespace"
	sqlitedrv "github.com/hanzoai/sqlite"
)

// replicas opens the chain "audit" twice on one store, as two replicas do. A SQLite
// file without the C codec cannot be opened twice (the pure-Go envelope gives each
// opener a private copy, and the last close wins), and a file is one process's by
// construction, so there both names are the one recorder: the contention is its own
// goroutines', and the invariants asked are the same.
func replicas(t *testing.T) (a, b *Recorder) {
	t.Helper()
	dir := t.TempDir()
	open := func() *Recorder {
		rec, err := Open(dir, "audit", nil)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		return rec
	}
	a = open()
	if !a.d.Postgres() && !sqlitedrv.CodecLinked() {
		return a, a
	}
	return a, open()
}

// Appends from two replicas, made at once, land in one chain: every seq 0..N-1 once,
// every record linked to the one before it, and both replicas read the same head. On
// hanzo-sql no append may fail: the second replica waits for the first's transaction
// and links to what it wrote. SQLite is one process by construction, so two writers
// on a file are not a topology it serves; the property it owes is that the chain
// still never forks, which the primary key holds, and a loser fails closed.
func TestReplicasAppendToOneLinearChain(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		ctx := context.Background()

		const workers, per = 4, 40
		type result struct {
			seq uint64
			err error
		}
		var mu sync.Mutex
		var got []result
		var wg sync.WaitGroup
		for _, rec := range []*Recorder{a, b} {
			for range workers {
				wg.Go(func() {
					for range per {
						out, err := rec.Append(ctx, sampleRecord("POST /v1/admin/sync"))
						mu.Lock()
						got = append(got, result{out.Seq, err})
						mu.Unlock()
					}
				})
			}
		}
		wg.Wait()

		var seqs []uint64
		for _, r := range got {
			if r.err != nil {
				if a.d.Postgres() {
					t.Fatalf("an append failed beside another replica's: %v", r.err)
				}
				continue
			}
			seqs = append(seqs, r.seq)
		}
		slices.Sort(seqs)
		for i, s := range seqs {
			if s != uint64(i) {
				t.Fatalf("seqs are not a gapless run from 0 (a fork, a gap or a repeat): position %d holds %d", i, s)
			}
		}
		if a.d.Postgres() && len(seqs) != 2*workers*per {
			t.Fatalf("%d of %d appends landed", len(seqs), 2*workers*per)
		}
		for name, rec := range map[string]*Recorder{"a": a, "b": b} {
			iv, err := rec.Verify(ctx)
			if err != nil || iv.Verdict != Intact || iv.Count != uint64(len(seqs)) {
				t.Fatalf("replica %s verifies %+v, %v; want %d records intact", name, iv, err, len(seqs))
			}
		}
		ac, ah := a.Head()
		bc, bh := b.Head()
		if ac != bc || ah != bh || ac != uint64(len(seqs)) {
			t.Fatalf("heads differ: a (%d, %s) b (%d, %s), want %d records", ac, ah, bc, bh, len(seqs))
		}
	})
}

// A replica does not carry the head in its memory: after another replica appends it
// links to that replica's record, and the other links back.
func TestAReplicaLinksToTheOtherReplicasRecord(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, b := replicas(t)
		ctx := context.Background()
		var last Record
		for range 3 {
			out, err := a.Append(ctx, sampleRecord("POST /v1/a"))
			if err != nil {
				t.Fatal(err)
			}
			last = out
		}
		// b opened before any of this and has seen nothing since.
		fromB, err := b.Append(ctx, sampleRecord("POST /v1/b"))
		if err != nil || fromB.Seq != 3 || fromB.PrevHash != last.Hash {
			t.Fatalf("b appended seq %d prev %q, %v; want seq 3 linked to a's record %q", fromB.Seq, fromB.PrevHash, err, last.Hash)
		}
		backToA, err := a.Append(ctx, sampleRecord("POST /v1/a"))
		if err != nil || backToA.Seq != 4 || backToA.PrevHash != fromB.Hash {
			t.Fatalf("a appended seq %d prev %q, %v; want seq 4 linked to b's record %q", backToA.Seq, backToA.PrevHash, err, fromB.Hash)
		}
		if iv, err := a.Verify(ctx); err != nil || iv.Verdict != Intact || iv.Count != 5 {
			t.Fatalf("verify %+v, %v", iv, err)
		}

		if !a.d.Postgres() {
			return
		}
		// While one replica's transaction is open the other waits for it: the chain lock
		// is the database's. The open transaction writes the next record, as a replica
		// mid-commit would, and the waiting append links to it.
		tx, err := a.d.BeginWrite(ctx, a.db)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		next, head, err := a.head(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		held, err := seal(sampleRecord("POST /v1/held"), next, head)
		if err != nil {
			t.Fatal(err)
		}
		type answer struct {
			rec Record
			err error
		}
		done := make(chan answer, 1)
		go func() {
			out, err := b.Append(ctx, sampleRecord("POST /v1/waiting"))
			done <- answer{out, err}
		}()
		select {
		case out := <-done:
			t.Fatalf("an append finished while another replica held the chain: %+v, %v", out.rec, out.err)
		case <-time.After(300 * time.Millisecond):
		}
		if err := insert(ctx, tx, []Record{held}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case out := <-done:
			if out.err != nil || out.rec.Seq != held.Seq+1 || out.rec.PrevHash != held.Hash {
				t.Fatalf("the waiting append took seq %d prev %q, %v; want seq %d linked to %q", out.rec.Seq, out.rec.PrevHash, out.err, held.Seq+1, held.Hash)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the waiting append never proceeded after the lock was released")
		}
		if iv, err := b.Verify(ctx); err != nil || iv.Verdict != Intact || iv.Count != 7 {
			t.Fatalf("verify %+v, %v; want 7 records intact", iv, err)
		}
	})
}

// The family is found in the registry of stores, not by globbing a directory: every
// chain the system namespace keeps whose name is the family's, and nothing else.
func TestTrailFindsTheFamilyWhereverItIsKept(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		requireSharedStore(t) // a sibling is another opener of a file; hanzo-sql has no such limit
		dir := t.TempDir()
		ctx := context.Background()
		for _, name := range []string{"audit-iam", "audit-kms"} {
			old, err := Open(dir, name, nil)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if _, err := old.Append(ctx, sampleRecord("POST /v1/old")); err != nil {
					t.Fatal(err)
				}
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
		}
		rec, err := Open(dir, "audit", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rec.Close() })
		if _, err := rec.Append(ctx, sampleRecord("POST /v1/live")); err != nil {
			t.Fatal(err)
		}
		if locus.DSN() != "" {
			// Stores that share the prefix and are not chains, or are another entity's.
			other, err := locus.Open(namespace.System(), "auditlog", dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			acme, err := locus.Open(namespace.MustOrg("acme"), "audit", dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = acme.Close() })
		}

		tr, err := rec.Trail(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, c := range tr.Chains {
			names = append(names, c.Name)
		}
		if want := []string{"audit", "audit-iam", "audit-kms"}; !slices.Equal(names, want) {
			t.Fatalf("the family is %v, want %v", names, want)
		}
		if tr.Intact != 3 || tr.Broken != 0 || tr.Unread != 0 || tr.Records != 5 {
			t.Fatalf("trail %+v, want 3 chains intact over 5 records", tr)
		}
	})
}
