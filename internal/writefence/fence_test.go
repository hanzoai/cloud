package writefence

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func mustPush(t *testing.T, f *Fence, plugin, shard, writer string, epoch uint64, payload string) {
	t.Helper()
	if err := f.Push(context.Background(), plugin, shard, writer, epoch, []byte(payload)); err != nil {
		t.Fatalf("push(%s,%s,writer=%s,epoch=%d): unexpected error: %v", plugin, shard, writer, epoch, err)
	}
}

func latest(t *testing.T, f *Fence, plugin, shard string) (uint64, string, string) {
	t.Helper()
	epoch, writer, payload, err := f.Latest(context.Background(), plugin, shard)
	if err != nil {
		t.Fatalf("latest(%s,%s): %v", plugin, shard, err)
	}
	return epoch, writer, string(payload)
}

// (a) STRICT `>` rejects a same-epoch stale-writer push. This is the direct
// proof that closing "<=" to ">" closes the same-epoch double-write: a
// deposed/partitioned writer (or anyone else) retrying at the SAME epoch
// that already landed must be refused, with the mirror left untouched.
func TestPushRejectsSameEpochStaleWriter(t *testing.T) {
	f := New(newFakeStore())
	mustPush(t, f, "kms", "shard-0", "writer-A", 5, "v1")

	err := f.Push(context.Background(), "kms", "shard-0", "writer-A", 5, []byte("v1-stale-retry"))
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("same-epoch retry: got %v, want ErrStaleEpoch", err)
	}

	epoch, writer, payload := latest(t, f, "kms", "shard-0")
	if epoch != 5 || writer != "writer-A" || payload != "v1" {
		t.Fatalf("mirror corrupted by rejected retry: epoch=%d writer=%q payload=%q, want epoch=5 writer=writer-A payload=v1", epoch, writer, payload)
	}
}

// A distinct writer at the same epoch as an already-landed push (e.g. a
// misconfigured second process handed the same lease epoch) must ALSO be
// rejected — strictness is per-shard-epoch, not per-writer-identity.
func TestPushRejectsSameEpochDifferentWriter(t *testing.T) {
	f := New(newFakeStore())
	mustPush(t, f, "kms", "shard-0", "writer-A", 5, "v1")

	err := f.Push(context.Background(), "kms", "shard-0", "writer-B", 5, []byte("v-imposter"))
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("same-epoch different writer: got %v, want ErrStaleEpoch", err)
	}
}

// (b) The atomic CAS rejects the loser of two concurrent same-epoch pushes.
//
// First, a direct, single-goroutine proof of the raw CAS semantics: two
// PutIfVersion calls conditioned on the SAME expectVersion (both having read
// the same prior state) — only the first may succeed.
func TestConditionalStoreRejectsLoserOfConcurrentCAS(t *testing.T) {
	store := newFakeStore()
	ctx := context.Background()

	// Both "readers" observe the same absent-key state (expectVersion "").
	_, v1, err := store.Get(ctx, "k")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("initial get: got %v, want ErrNotFound", err)
	}
	_, v2, err := store.Get(ctx, "k")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("initial get: got %v, want ErrNotFound", err)
	}
	if v1 != v2 {
		t.Fatalf("two reads of an absent key returned different versions: %q vs %q", v1, v2)
	}

	if _, err := store.PutIfVersion(ctx, "k", []byte("winner"), v1); err != nil {
		t.Fatalf("winner's CAS: unexpected error: %v", err)
	}
	if _, err := store.PutIfVersion(ctx, "k", []byte("loser"), v2); !errors.Is(err, ErrConflict) {
		t.Fatalf("loser's CAS: got %v, want ErrConflict", err)
	}

	data, _, err := store.Get(ctx, "k")
	if err != nil || string(data) != "winner" {
		t.Fatalf("store state after race: data=%q err=%v, want %q", data, err, "winner")
	}
}

// End-to-end: two goroutines race Fence.Push at the SAME candidate epoch
// against an empty mirror, forced (via the fake store's beforeCAS barrier)
// to both complete their read before either's write is attempted — the
// interleaving a real object store would only expose under specific network
// timing, made deterministic. Exactly one must be admitted; the mirror must
// end up self-consistent with whichever one won, never a mix of both, and
// never both "succeeding".
func TestFencePushRejectsConcurrentSameEpochRace(t *testing.T) {
	store := newFakeStore()
	store.beforeCAS = barrierAt2()
	f := New(store)

	const epoch = uint64(5)
	results := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			defer wg.Done()
			writer := fmt.Sprintf("writer-%d", i)
			results[i] = f.Push(context.Background(), "kms", "shard-0", writer, epoch, []byte("payload-"+writer))
		}()
	}
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
			continue
		}
		// The loser must be rejected as stale/conflicting — never any other
		// class of error (e.g. a corrupted-record decode error, which would
		// indicate the winner's write and the loser's write both landed and
		// clobbered each other).
		if !errors.Is(err, ErrStaleEpoch) && !errors.Is(err, ErrConflict) {
			t.Fatalf("loser's error is neither ErrStaleEpoch nor ErrConflict: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful concurrent pushes at the same epoch, want exactly 1 (double-write NOT closed)", successes)
	}

	// The mirror holds exactly one writer's payload, uncorrupted — not a
	// byte-mix of the two concurrent attempts.
	gotEpoch, gotWriter, gotPayload := latest(t, f, "kms", "shard-0")
	if gotEpoch != epoch {
		t.Fatalf("mirror epoch = %d, want %d", gotEpoch, epoch)
	}
	if gotPayload != "payload-"+gotWriter {
		t.Fatalf("mirror payload %q does not match its own recorded writer %q — corrupted by the race", gotPayload, gotWriter)
	}
}

// (c) A legitimately higher epoch from a new writer is admitted.
func TestPushAdmitsStrictlyHigherEpoch(t *testing.T) {
	f := New(newFakeStore())
	mustPush(t, f, "kms", "shard-0", "writer-A", 5, "v1")
	mustPush(t, f, "kms", "shard-0", "writer-B", 6, "v2") // new writer, higher epoch — admitted.

	epoch, writer, payload := latest(t, f, "kms", "shard-0")
	if epoch != 6 || writer != "writer-B" || payload != "v2" {
		t.Fatalf("got epoch=%d writer=%q payload=%q, want epoch=6 writer=writer-B payload=v2", epoch, writer, payload)
	}
}

// (d) A stale LOWER epoch is rejected — e.g. a writer that was granted an
// epoch before a failover, waking up after the new writer already pushed at
// a higher one.
func TestPushRejectsLowerEpoch(t *testing.T) {
	f := New(newFakeStore())
	mustPush(t, f, "kms", "shard-0", "writer-B", 6, "v2")

	err := f.Push(context.Background(), "kms", "shard-0", "writer-A-woke-up-late", 3, []byte("stale"))
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("lower epoch: got %v, want ErrStaleEpoch", err)
	}

	epoch, writer, payload := latest(t, f, "kms", "shard-0")
	if epoch != 6 || writer != "writer-B" || payload != "v2" {
		t.Fatalf("mirror corrupted by rejected lower-epoch push: epoch=%d writer=%q payload=%q", epoch, writer, payload)
	}
}

// Separate shards are independent: a push (at any epoch) to one shard must
// never be admitted or refused based on another shard's recorded epoch.
func TestPushIsScopedPerShard(t *testing.T) {
	f := New(newFakeStore())
	mustPush(t, f, "kms", "shard-0", "writer-A", 9, "v-a")
	// shard-1 has never been pushed; epoch 1 must be admitted even though
	// shard-0 is already at epoch 9.
	mustPush(t, f, "kms", "shard-1", "writer-B", 1, "v-b")

	e0, _, _ := latest(t, f, "kms", "shard-0")
	e1, _, _ := latest(t, f, "kms", "shard-1")
	if e0 != 9 || e1 != 1 {
		t.Fatalf("shard scoping broken: shard-0 epoch=%d (want 9), shard-1 epoch=%d (want 1)", e0, e1)
	}
}

// EpochSource composability: the fence takes an epoch as a plain argument —
// it has no idea where it came from. This proves a real EpochSource (here,
// the trivial StaticEpochSource; the controlplane-graduated one drops in
// with the same call shape) drives Push end-to-end.
func TestEpochSourceDrivesPush(t *testing.T) {
	var src EpochSource = StaticEpochSource(7)
	f := New(newFakeStore())
	ctx := context.Background()

	epoch, err := src.Epoch(ctx, "kms", "shard-0")
	if err != nil {
		t.Fatalf("epoch source: %v", err)
	}
	if err := f.Push(ctx, "kms", "shard-0", "writer-A", epoch, []byte("v1")); err != nil {
		t.Fatalf("push at source-provided epoch: %v", err)
	}

	gotEpoch, _, _ := latest(t, f, "kms", "shard-0")
	if gotEpoch != 7 {
		t.Fatalf("got epoch %d, want 7", gotEpoch)
	}
}

// TestMirrorKeyInjective_NoCrossShardAliasing — regression for a HIGH red-team
// finding: a slash-joined mirror key is not injective, so two logically
// distinct shards whose (plugin,shard) split differently around a '/' collide
// on one object, letting a push framed as one shard overwrite another's
// epoch/writer/payload. Real shard scopes carry '/' (vfs replica.DBPath yields
// "projects/site"), so this is reachable. The length-prefixed key must keep the
// two records fully independent.
func TestMirrorKeyInjective_NoCrossShardAliasing(t *testing.T) {
	// The classic collision pair: "kms/tenant-a"+"secrets" vs "kms"+"tenant-a/secrets".
	if k1, k2 := mirrorKey("kms/tenant-a", "secrets"), mirrorKey("kms", "tenant-a/secrets"); k1 == k2 {
		t.Fatalf("mirrorKey not injective: %q collides across distinct shards", k1)
	}

	f := New(newFakeStore())
	mustPush(t, f, "kms/tenant-a", "secrets", "writer-A", 5, "payload-A")
	// If the keys aliased, this push (lower-per-shard but a different shard)
	// would be seen as epoch 9 on the SAME record and clobber writer-A.
	mustPush(t, f, "kms", "tenant-a/secrets", "writer-B", 9, "payload-B")

	eA, wA, pA := latest(t, f, "kms/tenant-a", "secrets")
	eB, wB, pB := latest(t, f, "kms", "tenant-a/secrets")
	if eA != 5 || wA != "writer-A" || pA != "payload-A" {
		t.Fatalf("shard A corrupted by aliasing: epoch=%d writer=%q payload=%q", eA, wA, pA)
	}
	if eB != 9 || wB != "writer-B" || pB != "payload-B" {
		t.Fatalf("shard B wrong: epoch=%d writer=%q payload=%q", eB, wB, pB)
	}
}
