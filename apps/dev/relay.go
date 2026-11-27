package dev

// relay.go — a person's own Mac, reached from their phone.
//
//	https://zap.hanzo.ai/v1/dev/zap/<source id>
//
// Both ends dial out to the same path and the cloud joins them; neither accepts a
// connection. The desktop's first envelope is methodHost: it registers as the server end
// of that source. A phone's first envelope is any other call: it is joined to the
// desktop that holds the id. Each carries the person's IAM bearer, and the source's
// owner is the only person either may be. What crosses is the ZAP framing that already
// runs a cell's stream: the phone's request envelopes reach the desktop unchanged, and
// the desktop's response envelopes reach the phone unchanged.
//
// A WebTransport session's server end writes only response envelopes, so the desktop
// receives the phone's requests as pushes under its attach promise, each body
// [u32 LE channel][request envelope], one channel per phone session. It answers with a
// methodReply request whose payload is [u32 LE channel][response envelope]. A push
// of only the four-byte channel says that phone session ended. The attach is answered
// with an empty 200 once the desktop is registered; any other status is a refusal.

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"sync"
	"time"

	"zap-proto.io/go/rpc"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// sourcePrefix marks a linked source's id (apps/source), so the stream knows who answers it.
const sourcePrefix = "src_"

// Methods of the desktop's session, above every Dev frame method.
const (
	// methodHost registers the desktop as the server end of its source.
	methodHost uint32 = 0xFFFF0001
	// methodReply carries one of the desktop's response envelopes to a phone.
	methodReply uint32 = 0xFFFF0002
)

// push writes one response envelope to a session; quic.Session.Push is one.
type push func(promiseID, status uint32, body []byte) error

// desktops are the Macs attached now, by source id.
var desktops = &hub{hosts: map[string]*attached{}}

type hub struct {
	mu    sync.Mutex
	hosts map[string]*attached
}

// attached is one desktop's session and the phone sessions joined to it.
type attached struct {
	who     cloud.VerifiedIdentity
	push    push
	promise uint32

	mu    sync.Mutex
	next  uint32
	phone map[uint32]push
	gone  bool
}

// attach registers h as the desktop of id, replacing a stale one.
func (u *hub) attach(id string, h *attached) {
	u.mu.Lock()
	old := u.hosts[id]
	u.hosts[id] = h
	u.mu.Unlock()
	if old != nil {
		old.end()
	}
}

// detach removes h, if it is still the desktop of id, and tells its phones.
func (u *hub) detach(id string, h *attached) {
	u.mu.Lock()
	if u.hosts[id] == h {
		delete(u.hosts, id)
	}
	u.mu.Unlock()
	h.end()
}

func (u *hub) get(id string) *attached {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hosts[id]
}

// end marks the desktop gone and tells every joined phone.
func (h *attached) end() {
	h.mu.Lock()
	h.gone = true
	phones := h.phone
	h.phone = nil
	h.mu.Unlock()
	for _, p := range phones {
		_ = p(0, http.StatusConflict, []byte("Mac offline"))
	}
}

// join gives a phone session its channel, or false when the desktop is gone.
func (h *attached) join(p push) (uint32, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.gone {
		return 0, false
	}
	h.next++
	if h.phone == nil {
		h.phone = map[uint32]push{}
	}
	h.phone[h.next] = p
	return h.next, true
}

// leave ends a phone's channel and tells the desktop.
func (h *attached) leave(ch uint32) {
	h.mu.Lock()
	delete(h.phone, ch)
	gone := h.gone
	h.mu.Unlock()
	if !gone {
		_ = h.push(h.promise, http.StatusOK, channel(ch, nil))
	}
}

// forward writes a phone's envelope to the desktop on its channel.
func (h *attached) forward(ch uint32, env []byte) error {
	return h.push(h.promise, http.StatusOK, channel(ch, env))
}

// reply routes one of the desktop's response envelopes to its phone.
func (h *attached) reply(payload []byte) {
	if len(payload) <= 4 {
		return
	}
	ch := binary.LittleEndian.Uint32(payload)
	r, err := rpc.ParseResponse(payload[4:])
	if err != nil {
		return
	}
	h.mu.Lock()
	p := h.phone[ch]
	h.mu.Unlock()
	if p != nil {
		_ = p(r.PromiseID, r.Status, r.Body)
	}
}

func channel(ch uint32, env []byte) []byte {
	out := make([]byte, 4, 4+len(env))
	binary.LittleEndian.PutUint32(out, ch)
	return append(out, env...)
}

// relay is a source session's state: which end it is.
type relay struct {
	host *attached // set on a desktop's session
	peer *attached // the desktop a phone's session is joined to
	chnl uint32
}

func refusal(call rpc.Call, status uint32, msg string) []byte {
	return rpc.BuildResponse(status, call.PromiseID, []byte(msg))
}

// owned is the registry's ownership rule; a test replaces it.
var owned = func(ctx context.Context, org, subject, id string) error {
	// The registry is the sources app's, served by its own binary: ask it on the plane.
	out, err := cloud.Ask[client.SourceOwnsIn, client.SourceOwned](
		zip.WithCaller(ctx, zip.Caller{Org: org, User: subject}), "sources", client.SourcesOwns,
		&client.SourceOwnsIn{ID: id, Subject: subject})
	if err != nil {
		return err
	}
	if !out.Owned {
		return errors.New("source: not owned")
	}
	return nil
}

// source serves one envelope of a linked source's session. b.mu is held.
func (b *bridge) source(call rpc.Call, env []byte) ([]byte, error) {
	if b.rel == nil {
		if len(call.Cap) == 0 {
			return refusal(call, rpc.StatusUnauthorized, "the first envelope carries the IAM bearer"), nil
		}
		who, err := b.valid.Validate(string(call.Cap))
		if err != nil || who.Subject == "" || who.Owner == "" {
			return refusal(call, rpc.StatusUnauthorized, "the bearer is not valid"), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if owned(ctx, who.Owner, who.Subject, b.id) != nil {
			return refusal(call, http.StatusForbidden, "this source is not yours"), nil
		}
		if call.Method == methodHost {
			h := &attached{who: who, push: b.pusher(), promise: call.PromiseID}
			desktops.attach(b.id, h)
			b.rel = &relay{host: h}
			go func() { <-b.sess.Done(); desktops.detach(b.id, h) }()
			return rpc.BuildResponse(http.StatusOK, call.PromiseID, nil), nil
		}
		h := desktops.get(b.id)
		if h == nil {
			return refusal(call, http.StatusConflict, "Mac offline"), nil
		}
		if h.who.Owner != who.Owner || h.who.Subject != who.Subject {
			return refusal(call, http.StatusForbidden, "this source is not yours"), nil
		}
		ch, ok := h.join(b.pusher())
		if !ok {
			return refusal(call, http.StatusConflict, "Mac offline"), nil
		}
		b.rel = &relay{peer: h, chnl: ch}
		go func() { <-b.sess.Done(); h.leave(ch) }()
	}
	if h := b.rel.host; h != nil {
		if call.Method == methodReply {
			h.reply(call.Payload)
		}
		return nil, nil
	}
	if err := b.rel.peer.forward(b.rel.chnl, env); err != nil {
		return refusal(call, http.StatusConflict, "Mac offline"), nil
	}
	return nil, nil
}

// pusher is how this session is written to: the test's hook, or the stream itself.
func (b *bridge) pusher() push {
	if b.push != nil {
		return b.push
	}
	return b.sess.Push
}
