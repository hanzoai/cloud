package team

// collabws.go — the LIVE collaborative-editing lane: a hocuspocus-protocol
// (v2.15) WebSocket server at GET /collaborator, the counterpart of collab.go's
// snapshot RPC lane. The Team front's @hocuspocus/provider connects to
// COLLABORATOR_URL (wss://<host>/collaborator) and speaks the hocuspocus wire:
// every binary frame is varString(documentName) + varUint(messageType) + payload,
// with the documentName the SAME encodeDocumentId shape the RPC lane decodes
// ("<workspaceUuid>|<objectClass>|<objectId>|<objectAttr>") and the token the
// SAME HS256 session/workspace token every other team route verifies — carried
// IN-BAND in the Auth message (the browser WS API cannot set headers).
//
// The server is a relay + update log, NOT a CRDT engine. Y.js updates are
// commutative and idempotent under applyUpdate (out-of-order arrivals park as
// pending until their causal deps land), so persisting the ordered update log
// and replaying it to every joining peer converges exactly; the server never
// needs to parse update payloads. The one Y-aware move it makes is protocol-
// shaped, not payload-shaped: it answers a client SyncStep1 with the replay,
// an empty-diff SyncStep2 (flips the provider's `synced`), and its own
// SyncStep1 carrying the empty state vector — soliciting a full-state
// SyncStep2 back, which both pushes edits the client made before connecting
// and, when the client is alone in the room, REPLACES the log (free
// compaction: a lone peer has provably applied everything the log held).
//
// Mirrored server behaviors the provider's accounting depends on (verified
// against @hocuspocus/server 2.15.3): SyncStatus(applied) acks for every
// incoming SyncStep2/Update (startSync primes unsyncedChanges=1 expecting the
// Step2 ack), and awareness frames echoed to ALL peers INCLUDING the sender —
// the echo is the keepalive that feeds the provider's 30s messageReconnectTimeout
// (the client renews awareness every ~15s).
//
// TENANT ISOLATION: identical to collab.go's RPC lane — org is the VERIFIED
// token claim, the documentId's workspace must be the token's workspace (when
// pinned) AND the caller must be a member; rooms are keyed org+workspace-first
// and the persisted blob key embeds org+workspace, so a foreign documentId can
// neither join a room nor resolve a blob.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"

	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
)

// hocuspocus message types (@hocuspocus/provider 2.15.3 MessageType).
const (
	hpSync           = 0
	hpAwareness      = 1
	hpAuth           = 2
	hpQueryAwareness = 3
	hpStateless      = 5
	hpClose          = 7
	hpSyncStatus     = 8
)

// Auth submessage types (@hocuspocus/common AuthMessageType).
const (
	authToken            = 0
	authPermissionDenied = 1
	authAuthenticated    = 2
)

// y-protocols/sync submessage types.
const (
	ySyncStep1 = 0
	ySyncStep2 = 1
	yUpdate    = 2
)

const (
	// collabReadLimit caps one WS frame; a single Y.js update for a huge paste
	// is well under this, and the RPC lane's markup cap is 10 MiB.
	collabReadLimit = 16 << 20
	// collabIdleTimeout closes sockets with no inbound traffic — data frames
	// (the provider renews awareness every ~15s) or pong replies to our pings.
	collabIdleTimeout = 60 * time.Second
	// collabPingEvery keeps healthy-but-quiet sockets alive: backgrounded tabs
	// throttle JS timers (no awareness renewals) but the browser's network
	// stack still auto-pongs WS pings, which extend the read deadline. Without
	// this, a backgrounded tab dies at the idle deadline with an abrupt close
	// the provider reports as 1006 — the exact "cannot connect" error banner
	// this lane exists to kill.
	collabPingEvery = 20 * time.Second
	// collabWriteTimeout bounds one frame write so a dead peer cannot wedge a
	// broadcaster.
	collabWriteTimeout = 10 * time.Second
	// collabFlushEvery is the persistence debounce: a dirty room is flushed to
	// VFS at most this often (and always when its last peer leaves).
	collabFlushEvery = 2 * time.Second
)

// ── lib0 codec (the encoding @hocuspocus and y-protocols share) ──────────────

var errCollabCodec = errors.New("collab: malformed frame")

type lreader struct {
	buf []byte
	pos int
}

func (r *lreader) varUint() (uint64, error) {
	var v uint64
	var shift uint
	for {
		if r.pos >= len(r.buf) {
			return 0, errCollabCodec
		}
		b := r.buf[r.pos]
		r.pos++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v, nil
		}
		shift += 7
		if shift > 63 {
			return 0, errCollabCodec
		}
	}
}

func (r *lreader) varBytes() ([]byte, error) {
	n, err := r.varUint()
	if err != nil {
		return nil, err
	}
	if n > uint64(len(r.buf)-r.pos) {
		return nil, errCollabCodec
	}
	b := r.buf[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return b, nil
}

func (r *lreader) varString() (string, error) {
	b, err := r.varBytes()
	return string(b), err
}

type lwriter struct{ buf []byte }

func (w *lwriter) varUint(v uint64) {
	for v >= 0x80 {
		w.buf = append(w.buf, byte(v)|0x80)
		v >>= 7
	}
	w.buf = append(w.buf, byte(v))
}

func (w *lwriter) varBytes(b []byte) {
	w.varUint(uint64(len(b)))
	w.buf = append(w.buf, b...)
}

func (w *lwriter) varString(s string) { w.varBytes([]byte(s)) }

// ── outgoing frames ──────────────────────────────────────────────────────────

// yEmptyUpdate is the canonical empty Y.js update (0 struct clients, 0 delete
// sets) — applyUpdate treats it as a no-op; sending it as SyncStep2 flips the
// provider's synced flag without touching the document.
var yEmptyUpdate = []byte{0x00, 0x00}

// yEmptySV is the empty Y.js state vector (0 entries) — a SyncStep1 carrying it
// asks the peer for its FULL state.
var yEmptySV = []byte{0x00}

func hpAuthOKFrame(doc string) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpAuth)
	w.varUint(authAuthenticated)
	w.varString("read-write")
	return w.buf
}

func hpAuthDeniedFrame(doc, reason string) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpAuth)
	w.varUint(authPermissionDenied)
	w.varString(reason)
	return w.buf
}

func hpSyncFrame(doc string, sub uint64, payload []byte) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpSync)
	w.varUint(sub)
	w.varBytes(payload)
	return w.buf
}

func hpSyncStatusFrame(doc string, applied bool) []byte {
	w := &lwriter{}
	w.varString(doc)
	w.varUint(hpSyncStatus)
	if applied {
		// lib0 writeVarInt(1) — single byte for small non-negative values.
		w.buf = append(w.buf, 0x01)
	} else {
		w.buf = append(w.buf, 0x00)
	}
	return w.buf
}

// ── persistence: length-prefixed update log in one VFS blob per doc ──────────

func marshalYLog(log [][]byte) []byte {
	w := &lwriter{}
	for _, u := range log {
		w.varBytes(u)
	}
	return w.buf
}

func unmarshalYLog(b []byte) [][]byte {
	r := &lreader{buf: b}
	var log [][]byte
	for r.pos < len(r.buf) {
		u, err := r.varBytes()
		if err != nil {
			break // tolerate a truncated tail; everything before it is intact
		}
		cp := make([]byte, len(u))
		copy(cp, u)
		log = append(log, cp)
	}
	return log
}

// yLogBlobID names the persisted update log, class-free like the markup
// snapshot ids ("<objectId>-<field>-<ms>") so the two lanes sit side by side
// under the same tenant-scoped prefix.
func yLogBlobID(d collabDoc) string {
	return "ydoc-" + d.objectID + "-" + d.objectAttr
}

// ── rooms ────────────────────────────────────────────────────────────────────

// frameSink is where outgoing frames go — the per-connection serialized writer
// in production, a buffer in tests.
type frameSink interface {
	write(frame []byte) error
}

// collabWriter serializes writes onto one WS connection (fasthttp/websocket
// permits a single concurrent writer) with a per-frame deadline.
type collabWriter struct {
	mu   sync.Mutex
	conn *wsx.Conn
}

func (w *collabWriter) write(frame []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(collabWriteTimeout))
	return w.conn.WriteMessage(wsx.BinaryMessage, frame)
}

// collabPeer is one authenticated (connection, document) attachment.
type collabPeer struct {
	sink frameSink
}

// collabRoom is the in-memory hub of one live document: its peers and the
// persisted update log. Correct because cloud runs single-replica (the CR pins
// replicas=1) — every session for a doc lands in this process.
type collabRoom struct {
	blobKey string
	stop    chan struct{} // closes when the room is GC'd; ends the flusher

	// flushMu serializes the persist step so two concurrent flushes cannot reorder
	// their Puts (an earlier, staler snapshot landing after a newer one). Held ACROSS
	// the Put but never with rm.mu (rm.mu is released before the Put), so appends and
	// broadcasts are not blocked by a slow write. Order is flushMu → mu, never mu →
	// flushMu.
	flushMu sync.Mutex

	mu        sync.Mutex
	peers     map[*collabPeer]struct{}
	log       [][]byte
	loaded    bool
	dirty     bool
	lastFlush time.Time
}

// flusher persists a dirty room on the debounce clock even when no further
// appends arrive (burst-then-idle must not sit in memory until the last peer
// leaves). Ends when the room is GC'd.
func (rm *collabRoom) flusher(vfs types.VFSClient) {
	t := time.NewTicker(collabFlushEvery)
	defer t.Stop()
	for {
		select {
		case <-rm.stop:
			return
		case <-t.C:
			rm.flush(context.Background(), vfs, false)
		}
	}
}

type collabHub struct {
	vfs types.VFSClient

	mu    sync.Mutex
	rooms map[string]*collabRoom
}

func newCollabHub(vfs types.VFSClient) *collabHub {
	return &collabHub{vfs: vfs, rooms: map[string]*collabRoom{}}
}

// join attaches peer to the (org, workspace, doc) room, lazily loading the
// persisted log on first join, and returns a snapshot of the log to replay.
func (h *collabHub) join(ctx context.Context, org, workspace, docName string, d collabDoc, peer *collabPeer) (*collabRoom, [][]byte, error) {
	key := org + "\x00" + workspace + "\x00" + docName
	h.mu.Lock()
	rm, ok := h.rooms[key]
	if !ok {
		rm = &collabRoom{
			blobKey: blobKey(org, workspace, yLogBlobID(d)),
			stop:    make(chan struct{}),
			peers:   map[*collabPeer]struct{}{},
		}
		h.rooms[key] = rm
		go rm.flusher(h.vfs)
	}
	// Register the peer while STILL holding h.mu, BEFORE the (slow) VFS load, so a
	// concurrent leave of the last old peer cannot observe this room as empty in the
	// gap and GC it out from under this join — which would close(rm.stop) on an
	// already-closed channel (panic) and evict a live room (split-brain). Lock order
	// stays h→rm, matching leave, so no inversion.
	rm.mu.Lock()
	rm.peers[peer] = struct{}{}
	h.mu.Unlock()

	if !rm.loaded {
		data, err := h.vfs.Get(ctx, rm.blobKey)
		if err != nil && !errors.Is(err, types.ErrBlobNotFound) {
			// Undo this join so a failed first-open leaks NEITHER the peer NOR the
			// room+flusher: drop rm.mu, then leave() removes the peer and GCs the room
			// if it is now empty (flush is a no-op here — nothing was appended).
			rm.mu.Unlock()
			h.leave(ctx, org, workspace, docName, rm, peer)
			return nil, nil, fmt.Errorf("collab: load %s: %w", rm.blobKey, err)
		}
		rm.log = unmarshalYLog(data)
		rm.loaded = true
		rm.lastFlush = time.Now()
	}
	snap := make([][]byte, len(rm.log))
	copy(snap, rm.log)
	rm.mu.Unlock()
	return rm, snap, nil
}

// nopSink is a frameSink that discards. The hub's seed path attaches as a peer to
// reuse a room's lifecycle and lock (so the seed serializes against live flushes) but
// never sends frames of its own.
type nopSink struct{}

func (nopSink) write([]byte) error { return nil }

// seedIfAbsent seeds a brand-new doc field's update log from a dialog-authored Y.js
// update, but ONLY when the room's log is still empty. It runs THROUGH the SAME room
// the live WS lane uses (identical docName ⇒ one room, one flushMu/mu), so the check
// (empty?) and the set happen atomically under the room lock: a concurrent first edit
// is never clobbered and the seed never overwrites a live log. This replaces the old
// get-then-put in collab.go, whose gap between the Get(miss) and the Put let a racing
// live edit be overwritten. Joins with a discard sink, seeds when empty, persists via
// the room's own flush, then leaves (last-leaver GC unchanged).
func (h *collabHub) seedIfAbsent(ctx context.Context, org, workspace, docName string, d collabDoc, update []byte) error {
	peer := &collabPeer{sink: nopSink{}}
	rm, _, err := h.join(ctx, org, workspace, docName, d, peer)
	if err != nil {
		return err
	}
	defer h.leave(ctx, org, workspace, docName, rm, peer)
	rm.mu.Lock()
	if len(rm.log) != 0 {
		rm.mu.Unlock()
		return nil // a live (or already-seeded) log exists — never clobber it
	}
	cp := make([]byte, len(update))
	copy(cp, update)
	rm.log = [][]byte{cp}
	rm.dirty = true
	rm.mu.Unlock()
	rm.flush(ctx, h.vfs, true)
	return nil
}

// leave detaches peer; the last leaver flushes and GCs the room.
func (h *collabHub) leave(ctx context.Context, org, workspace, docName string, rm *collabRoom, peer *collabPeer) {
	key := org + "\x00" + workspace + "\x00" + docName
	h.mu.Lock()
	rm.mu.Lock()
	delete(rm.peers, peer)
	empty := len(rm.peers) == 0
	if empty {
		delete(h.rooms, key)
		close(rm.stop)
	}
	rm.mu.Unlock()
	h.mu.Unlock()
	if empty {
		rm.flush(ctx, h.vfs, true)
	}
}

// append records one update (or, for a lone peer's full-state SyncStep2,
// REPLACES the log — see the file header) and flushes on the debounce clock.
func (rm *collabRoom) append(ctx context.Context, vfs types.VFSClient, update []byte, replace bool) {
	cp := make([]byte, len(update))
	copy(cp, update)
	rm.mu.Lock()
	if replace && len(rm.peers) == 1 {
		rm.log = [][]byte{cp}
	} else {
		rm.log = append(rm.log, cp)
	}
	rm.dirty = true
	due := time.Since(rm.lastFlush) >= collabFlushEvery
	rm.mu.Unlock()
	if due {
		rm.flush(ctx, vfs, false)
	}
}

// flush persists the log when dirty. force skips the debounce (last-leave). The
// persist is serialized (flushMu) so overlapping flushes cannot reorder their Puts,
// and dirty is cleared only on a SUCCESSFUL Put — a failed write keeps the room
// dirty so the next tick / last-leave retries, instead of silently dropping the
// buffered window (the old code cleared dirty before the Put and discarded its
// error, losing updates on any transient VFS failure).
func (rm *collabRoom) flush(ctx context.Context, vfs types.VFSClient, force bool) {
	rm.flushMu.Lock()
	defer rm.flushMu.Unlock()
	rm.mu.Lock()
	if !rm.dirty || (!force && time.Since(rm.lastFlush) < collabFlushEvery) {
		rm.mu.Unlock()
		return
	}
	data := marshalYLog(rm.log)
	rm.dirty = false
	rm.lastFlush = time.Now()
	rm.mu.Unlock()
	if err := vfs.Put(ctx, rm.blobKey, data); err != nil {
		// Persist failed — re-arm dirty so the window is retried, never lost. An
		// append that raced in during the Put has already set dirty=true; this is a
		// no-op in that case (still dirty), which is correct.
		rm.mu.Lock()
		rm.dirty = true
		rm.mu.Unlock()
	}
}

// broadcast relays frame to the room's peers; skip omits one (the sender).
func (rm *collabRoom) broadcast(frame []byte, skip *collabPeer) {
	rm.mu.Lock()
	targets := make([]*collabPeer, 0, len(rm.peers))
	for p := range rm.peers {
		if p == skip {
			continue
		}
		targets = append(targets, p)
	}
	rm.mu.Unlock()
	for _, p := range targets {
		_ = p.sink.write(frame)
	}
}

// ── per-connection protocol engine ───────────────────────────────────────────

// collabDocSession is one authenticated document on one connection.
type collabDocSession struct {
	name      string // raw documentName (the provider's routing key)
	org       string
	workspace string
	room      *collabRoom
	peer      *collabPeer
}

// collabConn is the per-WS state: the serialized writer and the authenticated
// documents multiplexed on this socket (docName is in every frame).
type collabConn struct {
	svc      *collabService
	sink     frameSink
	sessions map[string]*collabDocSession
	denied   map[string]bool // docNames already denied — don't re-spam
}

func newCollabConn(svc *collabService, sink frameSink) *collabConn {
	return &collabConn{svc: svc, sink: sink, sessions: map[string]*collabDocSession{}, denied: map[string]bool{}}
}

// close detaches every document session (socket gone).
func (cc *collabConn) close(ctx context.Context) {
	for name, ds := range cc.sessions {
		cc.svc.hub.leave(ctx, ds.org, ds.workspace, ds.name, ds.room, ds.peer)
		delete(cc.sessions, name)
	}
}

// frame handles ONE inbound binary frame. Malformed frames are dropped (the
// codec fails closed); protocol errors answer in-band per the hocuspocus wire.
func (cc *collabConn) frame(ctx context.Context, data []byte) {
	r := &lreader{buf: data}
	docName, err := r.varString()
	if err != nil || docName == "" {
		return
	}
	typ, err := r.varUint()
	if err != nil {
		return
	}

	if typ == hpAuth {
		cc.auth(ctx, docName, r)
		return
	}

	ds, authed := cc.sessions[docName]
	if !authed {
		// Fail secure: everything but Auth on an unauthenticated document is
		// refused once (the provider disconnects on PermissionDenied).
		if !cc.denied[docName] {
			cc.denied[docName] = true
			_ = cc.sink.write(hpAuthDeniedFrame(docName, "auth required"))
		}
		return
	}

	switch typ {
	case hpSync:
		cc.sync(ctx, ds, r, data)
	case hpAwareness:
		// Echo to ALL peers including the sender — the sender's echo is the
		// keepalive its 30s messageReconnectTimeout counts on.
		ds.room.broadcast(data, nil)
	case hpQueryAwareness:
		// Relay to the other peers; each answers with an Awareness frame the
		// server broadcasts back (presence without server-side awareness state).
		ds.room.broadcast(data, ds.peer)
	case hpClose:
		cc.svc.hub.leave(ctx, ds.org, ds.workspace, ds.name, ds.room, ds.peer)
		delete(cc.sessions, docName)
	case hpStateless, hpSyncStatus:
		// Not part of this lane's contract; ignore.
	}
}

// auth verifies the in-band token exactly like the RPC lane (same token, same
// workspace pin, same membership gate) and on success joins the room.
func (cc *collabConn) auth(ctx context.Context, docName string, r *lreader) {
	if _, ok := cc.sessions[docName]; ok {
		return // duplicate Auth for a live session — idempotent
	}
	deny := func(reason string) {
		cc.denied[docName] = true
		_ = cc.sink.write(hpAuthDeniedFrame(docName, reason))
	}
	sub, err := r.varUint()
	if err != nil || sub != authToken {
		deny("malformed auth")
		return
	}
	raw, err := r.varString()
	if err != nil {
		deny("malformed auth")
		return
	}
	t, err := token.Decode(raw, cc.svc.secret, true)
	if err != nil || t.Account == "" {
		deny("invalid session token")
		return
	}
	org, _ := t.Extra["org"].(string)
	if strings.TrimSpace(org) == "" {
		deny("invalid session token")
		return
	}
	doc, err := decodeCollabDoc(docName)
	if err != nil {
		deny("malformed documentId")
		return
	}
	if t.Workspace != "" && t.Workspace != doc.workspace {
		deny("document not found")
		return
	}
	if cc.svc.accounts == nil {
		deny("collaborator unavailable")
		return
	}
	w, err := cc.svc.accounts.WorkspaceByUUID(ctx, org, doc.workspace)
	if err != nil {
		deny("document not found")
		return
	}
	if _, ok := cc.svc.accounts.Membership(ctx, w.ID, t.Account); !ok {
		deny("document not found")
		return
	}

	peer := &collabPeer{sink: cc.sink}
	room, _, err := cc.svc.hub.join(ctx, org, doc.workspace, docName, doc, peer)
	if err != nil {
		deny("storage unavailable")
		return
	}
	cc.sessions[docName] = &collabDocSession{
		name: docName, org: org, workspace: doc.workspace, room: room, peer: peer,
	}
	delete(cc.denied, docName)
	_ = cc.sink.write(hpAuthOKFrame(docName))
}

// sync handles one y-protocols sync submessage. raw is the full inbound frame,
// relayed verbatim to peers (it is already a valid hocuspocus Sync frame).
func (cc *collabConn) sync(ctx context.Context, ds *collabDocSession, r *lreader, raw []byte) {
	sub, err := r.varUint()
	if err != nil {
		return
	}
	switch sub {
	case ySyncStep1:
		// The client asks for the server state: replay the persisted log, close
		// the handshake with an empty-diff Step2 (flips `synced` after the
		// replay applied), then ask for the client's full state with our own
		// Step1 — the reply pushes offline edits and feeds compaction.
		ds.room.mu.Lock()
		snap := make([][]byte, len(ds.room.log))
		copy(snap, ds.room.log)
		ds.room.mu.Unlock()
		for _, u := range snap {
			_ = ds.peer.sink.write(hpSyncFrame(ds.name, yUpdate, u))
		}
		_ = ds.peer.sink.write(hpSyncFrame(ds.name, ySyncStep2, yEmptyUpdate))
		_ = ds.peer.sink.write(hpSyncFrame(ds.name, ySyncStep1, yEmptySV))
	case ySyncStep2, yUpdate:
		u, err := r.varBytes()
		if err != nil {
			return
		}
		ds.room.append(ctx, cc.svc.hub.vfs, u, sub == ySyncStep2)
		ds.room.broadcast(raw, ds.peer)
		_ = ds.peer.sink.write(hpSyncStatusFrame(ds.name, true))
	}
}

// ── HTTP surface ─────────────────────────────────────────────────────────────

// ws upgrades GET /collaborator and runs the frame loop. Auth is IN-BAND (per
// document, inside the hocuspocus Auth message) — the upgrade itself only
// gates on browser Origin, exactly like the transactor.
func (s *collabService) ws(c *zip.Ctx) error {
	return wsx.Upgrade(func(conn *wsx.Conn) error {
		conn.SetReadLimit(collabReadLimit)
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(collabIdleTimeout))
		})
		stop := make(chan struct{})
		defer close(stop)
		go func() { // keepalive: WriteControl is safe alongside WriteMessage
			t := time.NewTicker(collabPingEvery)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if conn.WriteControl(wsx.PingMessage, nil, time.Now().Add(collabWriteTimeout)) != nil {
						return
					}
				}
			}
		}()
		ctx := context.Background()
		cc := newCollabConn(s, &collabWriter{conn: conn})
		defer cc.close(ctx)
		for {
			_ = conn.SetReadDeadline(time.Now().Add(collabIdleTimeout))
			typ, data, err := conn.ReadMessage()
			if err != nil {
				return nil
			}
			if typ != wsx.BinaryMessage {
				continue
			}
			cc.frame(ctx, data)
		}
	}, wsx.Config{CheckOrigin: wsOriginOK})(c)
}
