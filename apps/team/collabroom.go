package team

// collabroom.go — a live document's room across pods.
//
// The peers of a document are not all on one pod, and a pod's room holds only its
// own peers and the log it has seen. So the pods holding a room talk on the room's
// subject (the deployment's coord, sdk/coord/bus.go), and a message is one of:
//
//	roomUpdate  one Y.js update a peer of the sender's sent: append it, hand it to
//	            this pod's peers
//	roomRaw     an awareness frame, verbatim: hand it to this pod's peers
//	roomHello   the sender opened the room, or may have missed messages: answer with
//	            what you hold
//	roomHave    some of what the sender holds (an empty one says only that it holds
//	            the room): append what this pod lacks and hand it to its peers
//	roomBye     the sender's last peer left
//
// WHY A RELAY AND NOT AN OWNER. The server never reads an update (collabws.go), so a
// room needs nothing to be one place: updates commute and apply twice as once, and a
// pod appends what it lacks, whoever it came from. Sending a room to one pod would
// have asked the edge to route a WebSocket by document, which one socket multiplexing
// several documents cannot be.
//
// WHAT THE STORE DOES NOT HOLD. A pod flushes its log on a debounce, so a pod that
// opens a room reads a log that is missing what the others have not yet flushed. The
// hello is how it gets it: every pod holding the room answers, and each update it
// lacks reaches its peers as an update. A pod that was deaf for a moment (gap) asks
// the same way.
//
// ONE BLOB, MANY WRITERS. Every pod holding the room flushes its log to the room's
// one blob. The logs converge, so the last write is as good as any; and a flush that
// lands before a pod has caught up is repaired by that pod's next one, because
// applying an update marks the log dirty.
//
// WHAT IT CANNOT PROMISE. A message lost on the way (the KV was out of reach when it
// was sent) is lost to the pods that did not hear it, until a peer's full state or a
// hello brings it back. The editors' own Y.js documents hold every update they
// applied, and a peer that reconnects sends its full state.

import (
	"crypto/sha256"
)

const (
	roomUpdate byte = 1
	roomRaw    byte = 2
	roomHello  byte = 3
	roomHave   byte = 4
	roomBye    byte = 5
)

// roomBatch bounds what one answer carries: a log of thousands of small updates is
// sent in a few messages, not one per update and not one of its whole size.
const roomBatch = 1 << 20

// setLog replaces the log, and the digests of it. The caller holds rm.mu.
func (rm *collabRoom) setLog(log [][]byte) {
	rm.log = log
	rm.known = make(map[[sha256.Size]byte]struct{}, len(log))
	for _, u := range log {
		rm.known[sha256.Sum256(u)] = struct{}{}
	}
}

// relay tells the other pods holding the room. A peer's write does not wait on the
// bus.
func (rm *collabRoom) relay(kind byte, body []byte) {
	if rm.bus == nil {
		return
	}
	m := make([]byte, 0, 1+len(body))
	m = append(m, kind)
	m = append(m, body...)
	_ = rm.bus.Publish(rm.subject, m)
}

// hello asks every pod holding the room for what it holds.
func (rm *collabRoom) hello() { rm.relay(roomHello, nil) }

// leaveBus is a room's last act: say so, and stop listening.
func (rm *collabRoom) leaveBus() {
	if rm.bus == nil {
		return
	}
	rm.relay(roomBye, nil)
	if rm.unbus != nil {
		rm.unbus()
	}
}

// hear is a message from another pod holding the room.
func (rm *collabRoom) hear(from string, p []byte) {
	if len(p) == 0 {
		return
	}
	select {
	case <-rm.stop:
		return // the room is gone from this pod
	default:
	}
	kind, body := p[0], p[1:]
	rm.mu.Lock()
	if kind == roomBye {
		delete(rm.others, from)
	} else {
		rm.others[from] = struct{}{}
	}
	rm.mu.Unlock()
	switch kind {
	case roomUpdate:
		rm.apply([][]byte{body})
	case roomRaw:
		rm.broadcast(body, nil)
	case roomHello:
		rm.answer()
	case roomHave:
		rm.apply(unmarshalYLog(body))
	}
}

// apply appends the updates this pod lacks and hands each to its peers. The caller's
// slices are shared with other subscribers, so what is kept is a copy.
func (rm *collabRoom) apply(updates [][]byte) {
	var fresh [][]byte
	rm.mu.Lock()
	for _, u := range updates {
		d := sha256.Sum256(u)
		if _, ok := rm.known[d]; ok {
			continue
		}
		cp := append([]byte(nil), u...)
		rm.known[d] = struct{}{}
		rm.log = append(rm.log, cp)
		fresh = append(fresh, cp)
	}
	if len(fresh) > 0 {
		rm.dirty = true
	}
	rm.mu.Unlock()
	for _, u := range fresh {
		rm.broadcast(hpSyncFrame(rm.docName, yUpdate, u), nil)
	}
}

// answer tells the pods holding the room what this pod holds, in batches. An empty
// room still answers, so a pod that opened the room learns it is not alone.
func (rm *collabRoom) answer() {
	rm.mu.Lock()
	log := make([][]byte, len(rm.log))
	copy(log, rm.log)
	rm.mu.Unlock()
	var batch [][]byte
	size := 0
	for _, u := range log {
		if size+len(u) > roomBatch && len(batch) > 0 {
			rm.relay(roomHave, marshalYLog(batch))
			batch, size = nil, 0
		}
		batch = append(batch, u)
		size += len(u)
	}
	rm.relay(roomHave, marshalYLog(batch))
}
