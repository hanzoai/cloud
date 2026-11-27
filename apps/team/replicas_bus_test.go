package team

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
)

// TWO PODS, ONE DEPLOYMENT. A person's socket is on one pod and the write that
// concerns them on another; two editors of one document are on two. Every test
// here stands up hubs the way pods stand up theirs — each its own, sharing only
// the KV — on the in-process KV and on one that speaks the KV's protocol.

func eachKV(t *testing.T, f func(t *testing.T, pod func(id string) *coord.Coord)) {
	t.Helper()
	t.Run("in process", func(t *testing.T) { f(t, coordtest.Memory(t)) })
	t.Run("kv", func(t *testing.T) {
		pod := coordtest.Memory(t)
		f(t, pod)
	})
}

func got(t *testing.T, tp *tap) batch {
	t.Helper()
	select {
	case b, ok := <-tp.ch:
		if !ok {
			t.Fatal("the tap was cut")
		}
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("the broadcast never reached the tap")
	}
	return batch{}
}

func quiet(t *testing.T, tp *tap, what string) {
	t.Helper()
	select {
	case b := <-tp.ch:
		t.Fatalf("%s: %d txes arrived", what, len(b.txes))
	case <-time.After(100 * time.Millisecond):
	}
}

// A write on one pod reaches the event streams open on the other two, once, in the
// order it was made, and nobody who is not on the space hears it.
func TestABroadcastReachesTheStreamsOnEveryPod(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b, c := newHub(pod("cloud-0")), newHub(pod("cloud-1")), newHub(pod("cloud-2"))
		defer a.leave()
		defer b.leave()
		defer c.leave()
		ta, stopA, _ := a.listen("ada", []string{"ws"})
		tb, stopB, _ := b.listen("bob", []string{"ws"})
		tc, stopC, _ := c.listen("cat", []string{"ws"})
		tz, stopZ, _ := c.listen("zed", []string{"elsewhere"})
		defer stopA()
		defer stopB()
		defer stopC()
		defer stopZ()

		const n = 40
		for i := range n {
			a.broadcast("ws", []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"objectId":"m%d"}`, i))})
		}
		for _, tp := range []*tap{ta, tb, tc} {
			for i := range n {
				bt := got(t, tp)
				if want := fmt.Sprintf(`{"objectId":"m%d"}`, i); bt.space != "ws" || len(bt.txes) != 1 || string(bt.txes[0]) != want {
					t.Fatalf("broadcast %d arrived as %q in %q, want %q in ws", i, bt.txes, bt.space, want)
				}
			}
			quiet(t, tp, "a broadcast was delivered twice")
		}
		quiet(t, tz, "a stream on another space heard ws")
	})
}

// A pod that receives a broadcast delivers it and does not send it on: the other
// pods hear it once from the pod that wrote, not again from each that heard.
func TestAReceivedBroadcastIsNotSentOnAgain(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		pa, pb, spy := pod("cloud-0"), pod("cloud-1"), pod("cloud-2")
		a, b := newHub(pa), newHub(pb)
		defer a.leave()
		defer b.leave()
		var mu sync.Mutex
		seen := 0
		defer spy.Subscribe(hubSpread, func(string, []byte) { mu.Lock(); seen++; mu.Unlock() }, nil)()
		tb, stop, _ := b.listen("bob", []string{"ws"})
		defer stop()
		a.broadcast("ws", []json.RawMessage{json.RawMessage(`{"objectId":"m"}`)})
		got(t, tb)
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if seen != 1 {
			t.Fatalf("one write put %d messages on the bus, want 1", seen)
		}
	})
}

// Who is online is who is connected to the space on any pod, and a stream that
// closes stops counting.
func TestPresenceSpansPods(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		a, b, c := newHub(pod("cloud-0")), newHub(pod("cloud-1")), newHub(pod("cloud-2"))
		defer a.leave()
		defer b.leave()
		defer c.leave()
		ctx := context.Background()
		_, stopA, _ := a.listen("ada", []string{"ws"})
		defer stopA()
		_, stopB, _ := b.listen("bob", []string{"ws"})
		c.add(&session{account: "cat", space: "ws"})
		c.add(&session{account: "cat", space: "ws"}) // a second tab

		here := a.present(ctx, "ws")
		for _, who := range []string{"ada", "bob", "cat"} {
			if !here[who] {
				t.Fatalf("%s is connected to ws and not online: %v", who, here)
			}
		}
		if here["zed"] || len(a.present(ctx, "elsewhere")) != 0 {
			t.Fatal("somebody not connected is online")
		}
		users := b.users(ctx, "ws")
		var ids []string
		for _, u := range users {
			ids = append(ids, u.UserID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, []string{"cat", "cat"}) {
			t.Fatalf("sessions of ws seen from b: %v, want cat's two (streams are not sessions)", ids)
		}

		stopB()
		if a.present(ctx, "ws")["bob"] {
			t.Fatal("bob closed his stream and is still online")
		}
	})
}

// ----- Yjs rooms ------------------------------------------------------------

// pipe is the socket of an editor: a frame sink that can be read from another
// goroutine.
type pipe struct {
	mu     sync.Mutex
	frames [][]byte
}

func (p *pipe) write(frame []byte) error {
	p.mu.Lock()
	p.frames = append(p.frames, append([]byte(nil), frame...))
	p.mu.Unlock()
	return nil
}

// updates are the Y.js updates the editor has been sent for doc, in the order sent,
// from sync frames of either kind that carry one.
func (p *pipe) updates(doc string) [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]byte
	for _, f := range p.frames {
		r := &lreader{buf: f}
		name, err := r.varString()
		if err != nil || name != doc {
			continue
		}
		if typ, err := r.varUint(); err != nil || typ != hpSync {
			continue
		}
		if sub, err := r.varUint(); err != nil || (sub != yUpdate && sub != ySyncStep2) {
			continue
		}
		if u, err := r.varBytes(); err == nil {
			out = append(out, u)
		}
	}
	return out
}

func (p *pipe) has(frame []byte) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.frames {
		if bytes.Equal(f, frame) {
			return true
		}
	}
	return false
}

func (p *pipe) hasUpdate(doc string, u []byte) bool {
	for _, got := range p.updates(doc) {
		if bytes.Equal(got, u) {
			return true
		}
	}
	return false
}

// editor is one person's socket on a pod, authenticated onto one document: the
// frames it sends go through the pod's real frame handler.
type editor struct {
	hub  *collabHub
	cc   *collabConn
	pipe *pipe
	doc  string
	room *collabRoom
	peer *collabPeer
}

const (
	yOrg = "acme"
	yDoc = "ws-1|document:class:Document|doc-1|content"
)

func open(t *testing.T, hub *collabHub) *editor {
	t.Helper()
	d, err := decodeCollabDoc(yDoc)
	if err != nil {
		t.Fatal(err)
	}
	pp := &pipe{}
	peer := &collabPeer{sink: pp}
	room, _, err := hub.join(context.Background(), yOrg, d.space, yDoc, d, peer)
	if err != nil {
		t.Fatal(err)
	}
	cc := newCollabConn(&collabService{hub: hub}, pp)
	cc.sessions[yDoc] = &collabDocSession{name: yDoc, org: yOrg, space: d.space, room: room, peer: peer}
	return &editor{hub: hub, cc: cc, pipe: pp, doc: yDoc, room: room, peer: peer}
}

func (e *editor) update(u []byte) {
	e.cc.frame(context.Background(), clientSyncFrame(e.doc, yUpdate, u))
}
func (e *editor) state(u []byte) {
	e.cc.frame(context.Background(), clientSyncFrame(e.doc, ySyncStep2, u))
}
func (e *editor) leave() { e.cc.close(context.Background()) }
func (e *editor) logs() [][]byte {
	e.room.mu.Lock()
	defer e.room.mu.Unlock()
	return slices.Clone(e.room.log)
}
func (e *editor) otherPods() int {
	e.room.mu.Lock()
	defer e.room.mu.Unlock()
	return len(e.room.others)
}
func (e *editor) awareness(b []byte) []byte {
	f := clientAwarenessFrame(e.doc, b)
	e.cc.frame(context.Background(), f)
	return f
}

func holds(log [][]byte, u []byte) bool {
	return slices.ContainsFunc(log, func(x []byte) bool { return bytes.Equal(x, u) })
}

// Two editors of one document on two pods see each other's edits, hold the same log,
// and what is stored is the union.
func TestEditorsOnDifferentPodsConverge(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		vfs := newMemVFS()
		ha, hb := newCollabHub(vfs, pod("cloud-0")), newCollabHub(vfs, pod("cloud-1"))
		ea, eb := open(t, ha), open(t, hb)
		u1, u2 := []byte{1, 1, 1}, []byte{2, 2, 2}

		ea.update(u1)
		eb.update(u2)
		eventually := func(what string, ok func() bool) {
			t.Helper()
			deadline := time.Now().Add(5 * time.Second)
			for !ok() {
				if time.Now().After(deadline) {
					t.Fatal(what)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		eventually("a's editor never saw b's edit", func() bool { return ea.pipe.hasUpdate(yDoc, u2) })
		eventually("b's editor never saw a's edit", func() bool { return eb.pipe.hasUpdate(yDoc, u1) })
		eventually("the logs differ", func() bool {
			return holds(ea.logs(), u1) && holds(ea.logs(), u2) && holds(eb.logs(), u1) && holds(eb.logs(), u2)
		})

		// Awareness (cursors) crosses too, verbatim, and the sender still gets its echo.
		aw := ea.awareness([]byte{9, 9})
		eventually("b's editor never saw a's cursor", func() bool { return eb.pipe.has(aw) })
		if !ea.pipe.has(aw) {
			t.Fatal("the sender's own awareness echo is its keepalive and went missing")
		}

		// Both pods leave; what is stored is every update, whichever pod flushed last.
		ea.leave()
		eb.leave()
		blob, err := vfs.Get(context.Background(), ea.room.blobKey)
		if err != nil {
			t.Fatal(err)
		}
		stored := unmarshalYLog(blob)
		if !holds(stored, u1) || !holds(stored, u2) {
			t.Fatalf("stored log holds %d updates and lacks one of the two edits", len(stored))
		}
	})
}

// A pod that opens a room reads a log missing what the others have not flushed yet
// (the flush is debounced). It still gets those edits, and so does its editor.
func TestARoomOpenedLateGetsWhatIsNotYetStored(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		vfs := newMemVFS()
		ha, hb := newCollabHub(vfs, pod("cloud-0")), newCollabHub(vfs, pod("cloud-1"))
		spy := pod("cloud-2")
		ea := open(t, ha)
		sent := make(chan struct{}, 1)
		defer spy.Subscribe(ea.room.subject, func(_ string, p []byte) {
			if len(p) > 0 && p[0] == roomUpdate {
				sent <- struct{}{}
			}
		}, nil)()
		unflushed := []byte{7, 7, 7}
		ea.update(unflushed)
		if _, err := vfs.Get(context.Background(), ea.room.blobKey); err == nil {
			t.Fatal("the edit was already stored: the test would prove nothing")
		}
		select {
		case <-sent: // it is out, and nobody on pod b was there to hear it
		case <-time.After(5 * time.Second):
			t.Fatal("the edit was never published")
		}

		eb := open(t, hb)
		deadline := time.Now().Add(5 * time.Second)
		for !eb.pipe.hasUpdate(yDoc, unflushed) || !holds(eb.logs(), unflushed) {
			if time.Now().After(deadline) {
				t.Fatalf("the pod that opened the room never got the edit the other holds: log %d, editor saw %d",
					len(eb.logs()), len(eb.pipe.updates(yDoc)))
			}
			time.Sleep(5 * time.Millisecond)
		}
		ea.leave()
		eb.leave()
	})
}

// A lone editor's full state replaces the log — its compaction — only when no other
// pod holds the room: another pod's editors have edits this one has not applied.
func TestAFullStateCompactsOnlyWhereTheRoomIsAlone(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		vfs := newMemVFS()
		ha, hb := newCollabHub(vfs, pod("cloud-0")), newCollabHub(vfs, pod("cloud-1"))
		ea, eb := open(t, ha), open(t, hb)
		deadline := time.Now().Add(5 * time.Second)
		for ea.otherPods() != 1 || eb.otherPods() != 1 {
			if time.Now().After(deadline) {
				t.Fatal("the two pods never learned of each other")
			}
			time.Sleep(5 * time.Millisecond)
		}
		for i := range 3 {
			ea.update([]byte{byte(i + 1)})
		}
		ea.state([]byte{0xAA})
		if n := len(ea.logs()); n != 4 {
			t.Fatalf("a full state with another pod in the room left a log of %d, want the 3 edits and the state appended", n)
		}

		eb.leave()
		for ea.otherPods() != 0 {
			if time.Now().After(deadline) {
				t.Fatal("the pod that left is still counted")
			}
			time.Sleep(5 * time.Millisecond)
		}
		ea.state([]byte{0xBB})
		if log := ea.logs(); len(log) != 1 || !bytes.Equal(log[0], []byte{0xBB}) {
			t.Fatalf("alone in the room, a full state left %v, want it to replace the log", log)
		}
		ea.leave()
	})
}

// A pod told it may have missed messages asks the others for what they hold.
func TestAGapIsAskedAgain(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		vfs := newMemVFS()
		ha, hb := newCollabHub(vfs, pod("cloud-0")), newCollabHub(vfs, pod("cloud-1"))
		ea, eb := open(t, ha), open(t, hb)
		deadline := time.Now().Add(5 * time.Second)
		for ea.otherPods() != 1 || eb.otherPods() != 1 {
			if time.Now().After(deadline) {
				t.Fatal("the two pods never learned of each other")
			}
			time.Sleep(5 * time.Millisecond)
		}
		// An edit b never heard: put in a's log without relaying it, as a message lost
		// on the way leaves it.
		lost := []byte{4, 4, 4}
		ea.room.mu.Lock()
		ea.room.log = append(ea.room.log, lost)
		ea.room.known[sha256.Sum256(lost)] = struct{}{}
		ea.room.mu.Unlock()
		if holds(eb.logs(), lost) {
			t.Fatal("b already holds the edit")
		}

		eb.room.hello() // what a pod told of a gap does
		for !holds(eb.logs(), lost) || !eb.pipe.hasUpdate(yDoc, lost) {
			if time.Now().After(deadline) {
				t.Fatal("b never recovered the edit it missed")
			}
			time.Sleep(5 * time.Millisecond)
		}
		ea.leave()
		eb.leave()
	})
}

// One edit is one message on the bus: the pod that hears it applies it and does not
// say it again.
func TestAnEditIsOneMessage(t *testing.T) {
	eachKV(t, func(t *testing.T, pod func(string) *coord.Coord) {
		vfs := newMemVFS()
		ha, hb := newCollabHub(vfs, pod("cloud-0")), newCollabHub(vfs, pod("cloud-1"))
		spy := pod("cloud-2")
		ea, eb := open(t, ha), open(t, hb)
		var mu sync.Mutex
		updates := 0
		defer spy.Subscribe(ea.room.subject, func(_ string, p []byte) {
			if len(p) > 0 && p[0] == roomUpdate {
				mu.Lock()
				updates++
				mu.Unlock()
			}
		}, nil)()
		ea.update([]byte{5, 5})
		deadline := time.Now().Add(5 * time.Second)
		for !eb.pipe.hasUpdate(yDoc, []byte{5, 5}) {
			if time.Now().After(deadline) {
				t.Fatal("the edit never arrived")
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(150 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if updates != 1 {
			t.Fatalf("one edit put %d update messages on the bus", updates)
		}
		ea.leave()
		eb.leave()
	})
}
