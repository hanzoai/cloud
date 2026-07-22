package team

import (
	"bytes"
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/types"
)

// TestCollabHubConcurrentJoinLeaveNoPanicNoLeak is the regression guard for the
// room-resurrection race: join used to register its peer AFTER releasing h.mu, so a
// concurrent leave of the last old peer could GC the room in the gap — closing
// rm.stop, then panicking "close of closed channel" on the resurrected room's next
// leave, and evicting live rooms (split-brain). This hammers join+leave on ONE doc
// from many goroutines (the "reconnect by the sole editor" shape). With the fix it
// runs clean under -race AND leaves NO leaked room; without it, it panics/leaks.
func TestCollabHubConcurrentJoinLeaveNoPanicNoLeak(t *testing.T) {
	vfs := newMemVFS()
	hub := newCollabHub(vfs)
	ctx := context.Background()
	const org, ws, docName = "acme", "ws-1", "ws-1|document:class:Document|doc-1|content"
	d, err := decodeCollabDoc(docName)
	if err != nil {
		t.Fatalf("decode doc: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			peer := &collabPeer{sink: &bufSink{}}
			room, _, err := hub.join(ctx, org, ws, docName, d, peer)
			if err != nil {
				return
			}
			hub.leave(ctx, org, ws, docName, room, peer)
		}()
	}
	wg.Wait()

	// Every peer left, so the room must be GC'd — a leftover entry means join and
	// leave disagreed about the room's membership (the resurrection bug leaks rooms
	// and their flusher goroutines).
	hub.mu.Lock()
	n := len(hub.rooms)
	hub.mu.Unlock()
	if n != 0 {
		t.Fatalf("after all peers left, rooms must be GC'd (no leak), got %d live", n)
	}
}

// TestCollabSeedNeverClobbersLiveLog is the deterministic regression guard for the
// seedYLog TOCTOU: createContent seeded a brand-new doc's update log with a bare
// Get(miss)-then-Put, so a first live edit that landed in the gap was overwritten.
// The seed now routes through the hub and applies ONLY when the room's log is empty.
// Here a live editor has already appended an edit; the racing seed must be dropped,
// never clobber it.
func TestCollabSeedNeverClobbersLiveLog(t *testing.T) {
	vfs := newMemVFS()
	hub := newCollabHub(vfs)
	ctx := context.Background()
	const org, ws, docName = "acme", "ws-1", "ws-1|document:class:Document|doc-1|content"
	d, err := decodeCollabDoc(docName)
	if err != nil {
		t.Fatalf("decode doc: %v", err)
	}

	// A live editor joins and appends a real edit; it stays connected (authoritative).
	editor := &collabPeer{sink: nopSink{}}
	room, _, err := hub.join(ctx, org, ws, docName, d, editor)
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	live := []byte{0xAA, 0xBB, 0xCC}
	room.append(ctx, vfs, live, false)

	// A createContent seed for the SAME field races in — it must NOT clobber the live log.
	if err := hub.seedIfAbsent(ctx, org, ws, docName, d, []byte{0x11, 0x22}); err != nil {
		t.Fatalf("seedIfAbsent: %v", err)
	}

	room.mu.Lock()
	got := append([][]byte(nil), room.log...)
	room.mu.Unlock()
	if len(got) != 1 || !bytes.Equal(got[0], live) {
		t.Fatalf("seed clobbered the live log: got %v, want just the live edit %v", got, live)
	}
	hub.leave(ctx, org, ws, docName, room, editor)
}

// TestCollabSeedRaceKeepsLiveEdit hammers the seed-vs-first-edit race on a BRAND-NEW
// doc: a live editor's first append and a createContent seed fire concurrently on the
// SAME field. Whichever wins the room lock, the final persisted log must always CONTAIN
// the live edit (never dropped) and must never be the seed alone (a clobber). Run under
// -race, it also proves the two lanes share one lock. No leaked room afterwards.
func TestCollabSeedRaceKeepsLiveEdit(t *testing.T) {
	ctx := context.Background()
	live := []byte{0xAA, 0xBB, 0xCC}
	seed := []byte{0x11, 0x22}
	for iter := 0; iter < 60; iter++ {
		vfs := newMemVFS()
		hub := newCollabHub(vfs)
		const org, ws, docName = "acme", "ws-1", "ws-1|document:class:Document|doc-1|content"
		d, _ := decodeCollabDoc(docName)

		var wg sync.WaitGroup
		wg.Add(2)
		start := make(chan struct{})
		go func() { // the live editor: join, then race its first append against the seed
			defer wg.Done()
			peer := &collabPeer{sink: nopSink{}}
			room, _, err := hub.join(ctx, org, ws, docName, d, peer)
			if err != nil {
				return
			}
			<-start
			room.append(ctx, vfs, live, false)
			hub.leave(ctx, org, ws, docName, room, peer)
		}()
		go func() { // the createContent seed
			defer wg.Done()
			<-start
			_ = hub.seedIfAbsent(ctx, org, ws, docName, d, seed)
		}()
		close(start)
		wg.Wait()

		// All peers left → room GC'd and the last leaver flushed the final log.
		blob, err := vfs.Get(ctx, blobKey(org, ws, yLogBlobID(d)))
		if err != nil {
			t.Fatalf("iter %d: final log not persisted: %v", iter, err)
		}
		log := unmarshalYLog(blob)
		found := false
		for _, u := range log {
			if bytes.Equal(u, live) {
				found = true
			}
		}
		if !found {
			t.Fatalf("iter %d: live edit lost — final log %v does not contain it", iter, log)
		}
		if len(log) == 1 && bytes.Equal(log[0], seed) {
			t.Fatalf("iter %d: seed clobbered the live edit (log is seed-only)", iter)
		}
		hub.mu.Lock()
		n := len(hub.rooms)
		hub.mu.Unlock()
		if n != 0 {
			t.Fatalf("iter %d: room leaked after all peers left, got %d", iter, n)
		}
	}
}

// flakyVFS is a VFSClient whose Put fails failN times before it starts persisting.
// Get always misses (empty backend); Delete is a no-op. It records what it stored so
// a test can assert nothing lands on a failed write.
type flakyVFS struct {
	mu     sync.Mutex
	failN  int
	puts   int
	stored map[string][]byte
}

func (v *flakyVFS) Put(_ context.Context, key string, payload []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.puts++
	if v.failN > 0 {
		v.failN--
		return fmt.Errorf("flakyVFS: transient put failure")
	}
	cp := make([]byte, len(payload))
	copy(cp, payload)
	if v.stored == nil {
		v.stored = map[string][]byte{}
	}
	v.stored[key] = cp
	return nil
}

func (v *flakyVFS) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, types.ErrBlobNotFound
}

func (v *flakyVFS) Delete(_ context.Context, _ string) error { return nil }

func (v *flakyVFS) get(key string) ([]byte, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.stored[key]
	return b, ok
}

// TestCollabFlushRetriesAfterPutError is the regression guard for the flush
// durability bug: flush used to clear dirty BEFORE the Put and discard the Put error,
// so a transient VFS failure lost the buffered updates silently (dirty already false
// ⇒ neither the ticker nor last-leave retried). The fix clears dirty only on a
// SUCCESSFUL Put and re-arms it on failure.
func TestCollabFlushRetriesAfterPutError(t *testing.T) {
	ctx := context.Background()
	vfs := &flakyVFS{failN: 1, stored: map[string][]byte{}}
	rm := &collabRoom{
		blobKey: "blob-key",
		stop:    make(chan struct{}),
		peers:   map[*collabPeer]struct{}{},
		loaded:  true,
	}
	rm.mu.Lock()
	rm.log = [][]byte{{1, 2, 3}}
	rm.dirty = true
	rm.mu.Unlock()

	// First flush: the Put fails. The room MUST stay dirty (the window is retained,
	// never lost) and NOTHING may be persisted.
	rm.flush(ctx, vfs, true)
	rm.mu.Lock()
	stillDirty := rm.dirty
	rm.mu.Unlock()
	if !stillDirty {
		t.Fatal("a failed Put must keep the room dirty so the next flush retries — not drop the buffered updates")
	}
	if _, ok := vfs.get("blob-key"); ok {
		t.Fatal("nothing may be persisted after a failed Put")
	}

	// Second flush: the Put succeeds. Now the log persists and dirty clears.
	rm.flush(ctx, vfs, true)
	rm.mu.Lock()
	nowClean := !rm.dirty
	rm.mu.Unlock()
	if !nowClean {
		t.Fatal("a successful Put must clear dirty")
	}
	got, ok := vfs.get("blob-key")
	if !ok || !bytes.Equal(got, marshalYLog([][]byte{{1, 2, 3}})) {
		t.Fatalf("persisted blob = %v, want the marshalled log", got)
	}
}
