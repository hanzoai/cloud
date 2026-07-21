package team

import (
	"bytes"
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/team/token"
)

// bufSink captures frames a collabConn writes — the test double for the
// per-connection serialized writer.
type bufSink struct{ frames [][]byte }

func (b *bufSink) write(frame []byte) error {
	cp := make([]byte, len(frame))
	copy(cp, frame)
	b.frames = append(b.frames, cp)
	return nil
}

func (b *bufSink) reset() { b.frames = nil }

// ── frame builders (the client side of the hocuspocus wire) ──────────────────

func clientAuthFrame(doc, tok string) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpAuth)
	w.varUint(authToken)
	w.varString(tok)
	return w.buf
}

func clientSyncFrame(doc string, sub uint64, payload []byte) []byte {
	return hpSyncFrame(doc, sub, payload)
}

func clientAwarenessFrame(doc string, payload []byte) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpAwareness)
	w.varBytes(payload)
	return w.buf
}

// parseFrame decodes docName + type and leaves the reader at the payload.
func parseFrame(t *testing.T, frame []byte) (string, uint64, *lreader) {
	t.Helper()
	r := &lreader{buf: frame}
	doc, err := r.varString()
	if err != nil {
		t.Fatalf("frame docName: %v", err)
	}
	typ, err := r.varUint()
	if err != nil {
		t.Fatalf("frame type: %v", err)
	}
	return doc, typ, r
}

// collabHarness mounts team, seeds one workspace, and hands back the live
// collabService plus a member token and a valid documentName.
func collabHarness(t *testing.T) (svc *collabService, docName, memberTok string) {
	t.Helper()
	vfs := newMemVFS()
	_ = mountTeamVFS(t, vfs)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	svc = &collabService{vfs: vfs, accounts: mounted.State.accounts, secret: testSecret, hub: newCollabHub(vfs)}
	docName = ws.UUID + "|document:class:Document|doc-1|content"
	return svc, docName, tok
}

// TestCollabCodecRoundTrip pins the lib0 encoding the whole wire depends on,
// including multi-byte varUints and the exact empty-SV / empty-update bytes.
func TestCollabCodecRoundTrip(t *testing.T) {
	w := &lwriter{}
	w.varUint(0)
	w.varUint(127)
	w.varUint(128)
	w.varUint(300)
	w.varString("hello|world")
	w.varBytes([]byte{1, 2, 3})
	r := &lreader{buf: w.buf}
	for _, want := range []uint64{0, 127, 300} {
		got, err := r.varUint()
		if err != nil {
			t.Fatal(err)
		}
		if want == 300 {
			// consumed 0,127 then 128 — read it before 300
			if got != 128 {
				t.Fatalf("varUint = %d, want 128", got)
			}
			got, err = r.varUint()
			if err != nil || got != 300 {
				t.Fatalf("varUint = %d, %v, want 300", got, err)
			}
			break
		}
		if got != want {
			t.Fatalf("varUint = %d, want %d", got, want)
		}
	}
	s, err := r.varString()
	if err != nil || s != "hello|world" {
		t.Fatalf("varString = %q, %v", s, err)
	}
	b, err := r.varBytes()
	if err != nil || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("varBytes = %v, %v", b, err)
	}
	// Truncated input fails closed, never panics.
	if _, err := (&lreader{buf: []byte{0x85}}).varUint(); err == nil {
		t.Fatal("truncated varUint must error")
	}
	if _, err := (&lreader{buf: []byte{0x05, 0x01}}).varBytes(); err == nil {
		t.Fatal("truncated varBytes must error")
	}
}

// TestCollabWSHandshake proves the full hocuspocus handshake against a live
// account store: Auth(token) → Authenticated("read-write"), then SyncStep1 →
// empty-diff SyncStep2 + server SyncStep1 (the provider flips synced and
// answers with its full state), then an Update → persisted + SyncStatus ack.
func TestCollabWSHandshake(t *testing.T) {
	svc, doc, tok := collabHarness(t)
	ctx := context.Background()
	sink := &bufSink{}
	cc := newCollabConn(svc, sink)
	defer cc.close(ctx)

	cc.frame(ctx, clientAuthFrame(doc, tok))
	if len(sink.frames) != 1 {
		t.Fatalf("auth frames = %d, want 1", len(sink.frames))
	}
	d, typ, r := parseFrame(t, sink.frames[0])
	if d != doc || typ != hpAuth {
		t.Fatalf("auth reply doc=%q type=%d", d, typ)
	}
	if sub, _ := r.varUint(); sub != authAuthenticated {
		t.Fatalf("auth sub = %d, want Authenticated", sub)
	}
	if scope, _ := r.varString(); scope != "read-write" {
		t.Fatalf("scope = %q", scope)
	}

	// SyncStep1 on an empty doc: exactly empty Step2 + server Step1.
	sink.reset()
	cc.frame(ctx, clientSyncFrame(doc, ySyncStep1, yEmptySV))
	if len(sink.frames) != 2 {
		t.Fatalf("step1 replies = %d, want 2 (step2 + server step1)", len(sink.frames))
	}
	_, typ, r = parseFrame(t, sink.frames[0])
	if typ != hpSync {
		t.Fatalf("reply 0 type = %d", typ)
	}
	if sub, _ := r.varUint(); sub != ySyncStep2 {
		t.Fatalf("reply 0 sub = %d, want Step2", sub)
	}
	if u, _ := r.varBytes(); !bytes.Equal(u, yEmptyUpdate) {
		t.Fatalf("empty-doc step2 payload = %v", u)
	}
	_, typ, r = parseFrame(t, sink.frames[1])
	if typ != hpSync {
		t.Fatalf("reply 1 type = %d", typ)
	}
	if sub, _ := r.varUint(); sub != ySyncStep1 {
		t.Fatalf("reply 1 sub = %d, want server Step1", sub)
	}
	if sv, _ := r.varBytes(); !bytes.Equal(sv, yEmptySV) {
		t.Fatalf("server step1 sv = %v", sv)
	}

	// A client Update is acked with SyncStatus(applied).
	sink.reset()
	update := []byte{9, 9, 9, 1}
	cc.frame(ctx, clientSyncFrame(doc, yUpdate, update))
	if len(sink.frames) != 1 {
		t.Fatalf("update replies = %d, want 1", len(sink.frames))
	}
	_, typ, r = parseFrame(t, sink.frames[0])
	if typ != hpSyncStatus {
		t.Fatalf("ack type = %d, want SyncStatus", typ)
	}
	if v, _ := r.varUint(); v != 1 {
		t.Fatalf("SyncStatus payload = %d, want 1 (applied)", v)
	}
}

// TestCollabWSRelayAndPersist is the two-client contract: client B (a second
// connection) receives A's update live, a fresh join replays the persisted
// log after the room is gone, and awareness echoes to BOTH (the sender echo is
// the client's keepalive).
func TestCollabWSRelayAndPersist(t *testing.T) {
	svc, doc, tok := collabHarness(t)
	ctx := context.Background()
	sinkA, sinkB := &bufSink{}, &bufSink{}
	a, b := newCollabConn(svc, sinkA), newCollabConn(svc, sinkB)

	a.frame(ctx, clientAuthFrame(doc, tok))
	a.frame(ctx, clientSyncFrame(doc, ySyncStep1, yEmptySV))
	b.frame(ctx, clientAuthFrame(doc, tok))
	b.frame(ctx, clientSyncFrame(doc, ySyncStep1, yEmptySV))

	// A types: B gets the exact Sync/Update frame; A gets only the ack.
	sinkA.reset()
	sinkB.reset()
	update := []byte{7, 7, 7}
	frame := clientSyncFrame(doc, yUpdate, update)
	a.frame(ctx, frame)
	if len(sinkB.frames) != 1 || !bytes.Equal(sinkB.frames[0], frame) {
		t.Fatalf("B frames = %v, want the relayed update frame", sinkB.frames)
	}
	if len(sinkA.frames) != 1 {
		t.Fatalf("A frames = %d, want 1 (SyncStatus only)", len(sinkA.frames))
	}

	// Awareness echoes to BOTH peers, including the sender.
	sinkA.reset()
	sinkB.reset()
	aw := clientAwarenessFrame(doc, []byte{1})
	a.frame(ctx, aw)
	if len(sinkA.frames) != 1 || len(sinkB.frames) != 1 {
		t.Fatalf("awareness fanout A=%d B=%d, want 1/1", len(sinkA.frames), len(sinkB.frames))
	}

	// Both leave → flush; a brand-new connection replays the persisted update.
	a.close(ctx)
	b.close(ctx)
	sinkC := &bufSink{}
	c := newCollabConn(svc, sinkC)
	defer c.close(ctx)
	c.frame(ctx, clientAuthFrame(doc, tok))
	sinkC.reset()
	c.frame(ctx, clientSyncFrame(doc, ySyncStep1, yEmptySV))
	if len(sinkC.frames) != 3 { // replayed update + step2 + server step1
		t.Fatalf("replay frames = %d, want 3", len(sinkC.frames))
	}
	_, typ, r := parseFrame(t, sinkC.frames[0])
	if typ != hpSync {
		t.Fatalf("replay type = %d", typ)
	}
	if sub, _ := r.varUint(); sub != yUpdate {
		t.Fatalf("replay sub = %d, want Update", sub)
	}
	if u, _ := r.varBytes(); !bytes.Equal(u, update) {
		t.Fatalf("replayed update = %v, want %v", u, update)
	}
}

// TestCollabWSCompaction: a lone peer's full-state SyncStep2 (the reply to the
// server's Step1) REPLACES the log; with a second peer attached it appends.
func TestCollabWSCompaction(t *testing.T) {
	svc, doc, tok := collabHarness(t)
	ctx := context.Background()
	sinkA := &bufSink{}
	a := newCollabConn(svc, sinkA)
	defer a.close(ctx)

	a.frame(ctx, clientAuthFrame(doc, tok))
	a.frame(ctx, clientSyncFrame(doc, ySyncStep1, yEmptySV))
	a.frame(ctx, clientSyncFrame(doc, yUpdate, []byte{1}))
	a.frame(ctx, clientSyncFrame(doc, yUpdate, []byte{2}))
	room := a.sessions[doc].room
	room.mu.Lock()
	n := len(room.log)
	room.mu.Unlock()
	if n != 2 {
		t.Fatalf("log = %d, want 2", n)
	}

	// Lone peer full-state Step2 → the log collapses to that one snapshot.
	snapshot := []byte{42, 42}
	a.frame(ctx, clientSyncFrame(doc, ySyncStep2, snapshot))
	room.mu.Lock()
	n, first := len(room.log), room.log[0]
	room.mu.Unlock()
	if n != 1 || !bytes.Equal(first, snapshot) {
		t.Fatalf("after lone-peer step2: log=%d first=%v", n, first)
	}

	// With a second peer in the room, Step2 appends (no replace).
	sinkB := &bufSink{}
	b := newCollabConn(svc, sinkB)
	defer b.close(ctx)
	b.frame(ctx, clientAuthFrame(doc, tok))
	a.frame(ctx, clientSyncFrame(doc, ySyncStep2, []byte{43}))
	room.mu.Lock()
	n = len(room.log)
	room.mu.Unlock()
	if n != 2 {
		t.Fatalf("two-peer step2 must append: log=%d, want 2", n)
	}
}

// TestCollabWSTenancy is the red bar mirrored from the RPC lane: a bad token,
// a foreign org, a workspace-pinned token naming another workspace, and any
// pre-auth traffic are ALL refused with PermissionDenied — and never joined.
func TestCollabWSTenancy(t *testing.T) {
	svc, doc, _ := collabHarness(t)
	ctx := context.Background()

	deny := func(name string, frame []byte) {
		t.Helper()
		sink := &bufSink{}
		cc := newCollabConn(svc, sink)
		defer cc.close(ctx)
		cc.frame(ctx, frame)
		if len(sink.frames) != 1 {
			t.Fatalf("%s: frames = %d, want 1", name, len(sink.frames))
		}
		_, typ, r := parseFrame(t, sink.frames[0])
		if typ != hpAuth {
			t.Fatalf("%s: type = %d, want Auth", name, typ)
		}
		if sub, _ := r.varUint(); sub != authPermissionDenied {
			t.Fatalf("%s: sub = %d, want PermissionDenied", name, sub)
		}
		if len(cc.sessions) != 0 {
			t.Fatalf("%s: session must not exist", name)
		}
	}

	deny("garbage token", clientAuthFrame(doc, "not-a-token"))

	foreign, err := token.Generate("bbbbbbbb-0000-4000-8000-00000000000b", "",
		map[string]any{"org": "org-b"}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	deny("foreign org", clientAuthFrame(doc, foreign))

	pinned, err := token.Generate("550e8400-e29b-41d4-a716-446655440000",
		"00000000-0000-4000-8000-00000000dead", map[string]any{"org": "acme"},
		expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	deny("workspace pin mismatch", clientAuthFrame(doc, pinned))

	deny("pre-auth sync", clientSyncFrame(doc, ySyncStep1, yEmptySV))
}

// TestCollabYLogMarshal pins the persisted log format and its truncation
// tolerance (a torn tail loses only the torn entry, never the file).
func TestCollabYLogMarshal(t *testing.T) {
	log := [][]byte{{1}, {2, 3}, {}}
	got := unmarshalYLog(marshalYLog(log))
	if len(got) != 3 || !bytes.Equal(got[0], []byte{1}) || !bytes.Equal(got[1], []byte{2, 3}) || len(got[2]) != 0 {
		t.Fatalf("round trip = %v", got)
	}
	torn := append(marshalYLog(log), 0x7f, 0x01) // claims 127 bytes, has 1
	if got = unmarshalYLog(torn); len(got) != 3 {
		t.Fatalf("torn tail = %d entries, want 3", len(got))
	}
	if unmarshalYLog(nil) != nil {
		t.Fatal("empty blob must be an empty log")
	}
}
