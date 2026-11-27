package dev

// adapter.go — a wasm session's ZAP stream, answered by the Dev frame adapter
// (hanzoai/dev go/frame): the Dev environment protocol for chat mode, with the loop
// behind it. The adapter owns the conversation and the core; this supplies what it asks
// the world for — the model, a cell for a command, and a place to keep its state — and
// decides what a request that needs a workspace becomes: a cell, and the session
// upgraded to the real Dev server there.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	dev "github.com/hanzoai/dev/go"
	"github.com/hanzoai/dev/go/frame"
	luxlog "github.com/luxfi/log"
	"zap-proto.io/go/rpc"
	"zap-proto.io/go/transport/quic"

	"github.com/hanzoai/cloud/apps/dev/loop"
	"github.com/hanzoai/cloud/sdk"
)

// effects is what a frame adapter asks of cloud for one session.
type effects struct {
	h    *host
	held *loop.Held
}

// Model asks the gateway as the person, and hands the client each increment of the
// reply as the model writes it.
func (e *effects) Model(ctx context.Context, ask dev.Ask, delta func(string)) (dev.Reply, error) {
	return e.h.Stream(ctx, ask, delta)
}

// Perform does an action in the person's cell, leasing it the first time. The action
// is claimed in the session's journal under its thread and id first, as a loop turn's
// is, so it is done at most once and only while this connection holds the session.
func (e *effects) Perform(ctx context.Context, thread string, act dev.Action) (dev.Output, error) {
	return e.held.Perform(ctx, thread, act, e.h)
}

// Save keeps the adapter's state with the session.
func (e *effects) Save(_ context.Context, state []byte) error { return e.held.Save(state) }

// wasm handles one frame of a wasm session, after the caller has proved who they are.
func (b *bridge) wasm(call rpc.Call, env []byte) ([]byte, error) {
	refuse := func(status uint32, why string) ([]byte, error) {
		return rpc.BuildResponse(status, call.PromiseID, []byte(why)), nil
	}
	if b.ad == nil && b.cell == nil {
		if len(call.Cap) == 0 {
			// Before the bearer, the one thing a client may ask is what environment this is.
			if call.PromiseID&callBit != 0 && call.Method == frame.MethodDescriptor {
				return b.describe(call)
			}
			return refuse(rpc.StatusUnauthorized, "the first envelope carries the IAM bearer")
		}
		who, err := b.valid.Validate(string(call.Cap))
		if err != nil || who.Subject == "" || who.Owner == "" {
			return refuse(rpc.StatusUnauthorized, "the bearer is not valid")
		}
		rt := running.Load()
		if rt == nil {
			return refuse(http.StatusNotImplemented, "the wasm runtime is not linked into this deployment")
		}
		b.bearer = append([]byte(nil), call.Cap...)
		if err := b.attach(rt, who); err != nil {
			status := uint32(rpc.StatusInternal)
			switch {
			case errors.Is(err, loop.ErrNoSession):
				status = rpc.StatusNotFound
			case errors.Is(err, loop.ErrBusy), errors.Is(err, loop.ErrSuspended):
				status = http.StatusConflict
			}
			return refuse(status, err.Error())
		}
	}
	if b.cell != nil { // upgraded: the real Dev server answers from here on
		return nil, quic.WriteEnvelope(b.cell, env)
	}
	if call.PromiseID&callBit != 0 {
		// A call the client used to make over HTTP, answered with the route's JSON.
		body, err := b.ad.Call(int(call.Method), call.Payload)
		switch {
		case errors.Is(err, frame.ErrMethod):
			return refuse(rpc.StatusNotFound, err.Error())
		case err != nil:
			return refuse(rpc.StatusInternal, err.Error())
		}
		return rpc.BuildResponse(rpc.StatusOK, call.PromiseID, body), nil
	}
	b.promise.Store(call.PromiseID)
	err := b.ad.Handle(context.Background(), call.Payload)
	var need *frame.NeedsSandbox
	if errors.As(err, &need) {
		return b.upgrade(need, call, env)
	}
	if err != nil {
		return refuse(rpc.StatusBadRequest, err.Error())
	}
	return nil, nil
}

// importPromise is the promise id of the envelope that carries a session's chat
// threads into its cell, and importRequestID the request id inside it. Neither is
// one a client sends: its calls set callBit and its frames' promises are its own
// request ids, which count up from zero. The cell's answer to it is dropped on the
// way back.
const (
	importPromise   = 1 << 31
	importRequestID = 1<<31 - 1
)

// callBit marks an envelope's promise id as a call — the descriptor, the session or the
// shell snapshot — and not a frame of the environment protocol.
const callBit = 1 << 30

// describe answers the descriptor for a client that has not sent its bearer yet. It is
// the same answer the session's adapter gives, from an adapter that holds nothing.
func (b *bridge) describe(call rpc.Call) ([]byte, error) {
	rt := running.Load()
	if rt == nil {
		return rpc.BuildResponse(http.StatusNotImplemented, call.PromiseID, []byte("the wasm runtime is not linked into this deployment")), nil
	}
	ad, err := frame.New(frame.Config{
		Core: rt.loop.Core, Effects: nothing{}, Send: func([]byte) error { return nil },
		Environment: frame.Environment{ID: b.id, Label: "Hanzo cloud (chat)"},
	})
	if err != nil {
		return rpc.BuildResponse(rpc.StatusInternal, call.PromiseID, []byte(err.Error())), nil
	}
	defer func() { _ = ad.Close(context.Background()) }()
	body, err := ad.Call(frame.MethodDescriptor, call.Payload)
	if err != nil {
		return rpc.BuildResponse(rpc.StatusInternal, call.PromiseID, []byte(err.Error())), nil
	}
	return rpc.BuildResponse(rpc.StatusOK, call.PromiseID, body), nil
}

// nothing is an Effects for an adapter that answers the descriptor and does no work.
type nothing struct{}

func (nothing) Model(context.Context, dev.Ask, func(string)) (dev.Reply, error) {
	return dev.Reply{}, errors.New("no model")
}
func (nothing) Perform(context.Context, string, dev.Action) (dev.Output, error) {
	return dev.Output{}, errors.New("no cell")
}
func (nothing) Save(context.Context, []byte) error { return nil }

// attach holds the session and starts the frame adapter over it.
func (b *bridge) attach(rt *Runtime, who cloud.VerifiedIdentity) error {
	held, err := rt.loop.Hold(context.Background(), who.Owner, who.Subject, b.id)
	if err != nil {
		return err
	}
	push := b.push
	if push == nil {
		push = b.sess.Push
	}
	ad, err := frame.New(frame.Config{
		Core:    rt.loop.Core,
		Effects: &effects{h: rt.hostFor(who.Owner, caller(who), b.id), held: held},
		Send: func(f []byte) error {
			return push(b.promise.Load(), rpc.StatusOK, f)
		},
		Environment:    frame.Environment{ID: b.id, Label: "Hanzo cloud (chat)"},
		SessionExpires: who.Expiry,
		Query:          b.query,
		State:          held.Frames,
	})
	if err != nil {
		held.Release()
		return err
	}
	b.who, b.rt, b.held, b.ad = who, rt, held, ad
	go func() {
		<-b.sess.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.mu.Lock()
		ad := b.ad
		b.mu.Unlock()
		if ad != nil {
			_ = ad.Close(ctx)
		}
		held.Release()
	}()
	return nil
}

// upgrade answers a request that needs a workspace: lease the person's cell, signed in
// with the bearer this stream was verified on, start the real Dev server in it, and
// carry the request there; every later frame goes the same way. A cell the sandbox
// will not give is a refusal in the client's own terms.
func (b *bridge) upgrade(need *frame.NeedsSandbox, call rpc.Call, env []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(actingAs(b.who.Owner, caller(b.who)), 90*time.Second)
	defer cancel()
	cell, err := takeCell(ctx, b.who.Owner+"/"+b.who.Username, b.who.Issuer, string(b.bearer), b.who.Subject, "")
	if err == nil {
		var conn net.Conn
		if conn, err = b.open(b.who, cell.ID); err == nil {
			if werr := b.held.Cell(cell.ID); werr != nil {
				_ = conn.Close()
				return nil, werr
			}
			ad := b.ad
			b.cell, b.ad = conn, nil
			go b.back(conn)
			// The chat threads go first, so the request that needed the cell finds them
			// there under the ids the client holds. The import carries the bearer, as the
			// first envelope a Dev server reads must; its answer is cloud's, not the client's.
			if imp, ierr := ad.ImportRequest(importRequestID); ierr == nil && imp != nil {
				err = quic.WriteEnvelope(conn, rpc.BuildRequest(rpc.Call{PromiseID: importPromise, Cap: b.bearer, Payload: imp}))
			} else if ierr != nil {
				err = ierr
			}
			go func() { _ = ad.Close(context.Background()) }()
			if err != nil {
				return nil, err
			}
			return nil, quic.WriteEnvelope(conn, env)
		}
	}
	b.refused(need, err)
	if rerr := b.ad.Refuse(need); rerr != nil {
		return nil, rerr
	}
	return nil, nil
}

// refused says why a request that needed a workspace got no cell. The client is told
// only that the session has no workspace (frame's refusal names no cause), so the cause
// is logged here, where an operator can read it: a lease the sandbox refused, a Dev
// server that did not start in the cell, a stream that would not open (#908). It used
// to be dropped.
func (b *bridge) refused(need *frame.NeedsSandbox, err error) {
	log := luxlog.Default()
	if b.rt != nil && b.rt.logf != nil {
		log = b.rt.logf
	}
	log.Error("dev: no cell for a workspace request", "session", b.id, "method", need.Method, "error", err)
}
