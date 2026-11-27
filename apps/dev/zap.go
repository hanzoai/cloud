package dev

// zap.go — a browser's Dev session, over WebTransport, piped to the cell.
//
//	https://zap.hanzo.ai/v1/dev/zap/<cell>   one bidirectional stream of ZAP envelopes
//
// The browser's stream and the cell's Dev server speak the same framing —
// [u32 LE length][router envelope] — so this holds the two ends together and reads
// neither: the envelope that goes in at one end comes out of the other as it went
// in. What it does decide is who may. The first envelope carries the caller's IAM
// bearer as its cap; it is verified here, by the verifier the identity boundary
// uses, before a cell is touched, and the stream into the cell is then opened on
// the sandbox plane as that person, so the sandbox's own ownership rule decides
// whether the cell is theirs. The envelope is forwarded unchanged, bearer
// included: Dev pins the session to the account it was started for and checks the
// same token itself.
//
// A dispatch that returns nil answers nothing; Dev's replies and events come back
// on their own and are pushed to the browser as they arrive.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/dev/go/frame"
	"zap-proto.io/go/rpc"
	"zap-proto.io/go/transport"
	"zap-proto.io/go/transport/quic"

	"crypto/tls"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/dev/loop"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
)

// Path is where the browser's session is served; the cell's id follows.
const Path = "/v1/dev/zap/"

// Listen starts the browser listener the deployment configures: CLOUD_ZAP_WT_LISTEN
// is the UDP address and CLOUD_ZAP_WT_CERT and CLOUD_ZAP_WT_KEY the public
// certificate. Unset, there is no listener. Set without a certificate it refuses
// to bind and says so, because a WebTransport origin accepts no other.
func Listen() (*quic.WebTransportServer, error) {
	addr := env.Or("CLOUD_ZAP_WT_LISTEN", "")
	if addr == "" {
		return nil, nil
	}
	return Serve(addr, env.Or("CLOUD_ZAP_WT_CERT", ""), env.Or("CLOUD_ZAP_WT_KEY", ""))
}

// Serve binds UDP addr and serves Dev sessions. cert and key are the public
// certificate for the host a browser opens, which a WebTransport origin requires.
func Serve(addr, cert, key string) (*quic.WebTransportServer, error) {
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return serve(addr, &tls.Config{Certificates: []tls.Certificate{pair}},
		cloud.NewTokenValidator(cloud.DiscoveryURL(), nil))
}

func serve(addr string, conf *tls.Config, valid verifier) (*quic.WebTransportServer, error) {
	return quic.ListenWebTransport(addr, conf, Path,
		func(r *http.Request, sess *quic.Session) transport.Dispatch {
			id := strings.TrimPrefix(r.URL.Path, Path)
			if id == "" || strings.Contains(id, "/") {
				return nil
			}
			crossOrigin(r)
			return (&bridge{id: id, sess: sess, valid: valid, query: r.URL.Query()}).dispatch
		})
}

// crossOrigin admits a page served from another origin, which is every page: nothing
// is served from zap.<brand>. The WebTransport library's default rule refuses a CONNECT
// whose Origin names any host but the one it was sent to, so every browser session was
// answered 400 before it existed (Chromium: "Opening handshake failed"), and only a
// client that sends no Origin (Go, the desktop) got through. That rule guards a
// credential the browser attaches on its own; a WebTransport CONNECT carries none
// (WebTransport: credentials mode "omit"). A session's one credential is the bearer the
// page puts in its first envelope, verified in dispatch, so the Origin decides nothing
// here and is dropped before the upgrade reads it.
func crossOrigin(r *http.Request) { r.Header.Del("Origin") }

// verifier is the identity boundary's verdict on a bearer: cloud.TokenValidator.
type verifier interface {
	Validate(raw string) (cloud.VerifiedIdentity, error)
}

// caller is a verified bearer as the plane states its person (actingAs): who they are,
// and who pays for what is done for them — the token's billing account, as the boundary
// reads it for an HTTP request.
func caller(who cloud.VerifiedIdentity) zip.Caller {
	return zip.Caller{User: who.Subject, Name: who.Username, Owner: who.Home(), Admin: who.Super, Account: who.BillingAccount}
}

// bridge is one browser session and, once it has proved who it is, its stream.
type bridge struct {
	id    string
	sess  *quic.Session
	valid verifier

	query url.Values
	push  func(promiseID, status uint32, body []byte) error // sess.Push unless a test says otherwise

	mu      sync.Mutex
	cell    net.Conn      // a sandbox session's stream, or a wasm session's once upgraded
	promise atomic.Uint32 // the request being answered, which the frames of its reply carry

	// A wasm session's own: who it is for, the runtime, the hold on its row, and the
	// frame adapter that answers it until a request needs a workspace.
	who    cloud.VerifiedIdentity
	bearer []byte // the cap the session was proved with: relayed into the cell's lease and shown to its Dev server first
	rt     *Runtime
	held   *loop.Held
	ad     *frame.Adapter

	// A linked source's session: which end of the relay it is (relay.go).
	rel *relay
}

// dispatch forwards each envelope to the cell, opening the stream on the first.
func (b *bridge) dispatch(env []byte) ([]byte, error) {
	call, err := rpc.ParseRequest(env)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if strings.HasPrefix(b.id, wasmPrefix) {
		return b.wasm(call, env)
	}
	if strings.HasPrefix(b.id, sourcePrefix) {
		return b.source(call, env)
	}
	if b.cell == nil {
		if len(call.Cap) == 0 {
			// The descriptor is the one thing asked before the bearer, and cloud answers it
			// itself on every runtime, so the client sees one behavior wherever it lands.
			if call.PromiseID&callBit != 0 && call.Method == frame.MethodDescriptor && running.Load() != nil {
				return b.describe(call)
			}
			return rpc.BuildResponse(rpc.StatusUnauthorized, call.PromiseID, []byte("the first envelope carries the IAM bearer")), nil
		}
		who, err := b.valid.Validate(string(call.Cap))
		if err != nil || who.Subject == "" || who.Owner == "" {
			return rpc.BuildResponse(rpc.StatusUnauthorized, call.PromiseID, []byte("the bearer is not valid")), nil
		}
		conn, err := b.open(who, b.id)
		if err != nil {
			var he *zip.HTTPError
			status := rpc.StatusInternal
			if errors.As(err, &he) {
				status = uint32(he.Status)
			}
			return rpc.BuildResponse(status, call.PromiseID, []byte(err.Error())), nil
		}
		b.cell = conn
		go b.back(conn)
	}
	return nil, quic.WriteEnvelope(b.cell, env)
}

// open asks the sandbox plane, as the person, for a stream to the cell's Dev socket.
func (b *bridge) open(who cloud.VerifiedIdentity, cell string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(acting(who.Owner, who.Subject), 30*time.Second)
	defer cancel()
	out, err := sandboxpeer.SandboxStream(ctx, &client.StreamIn{ID: cell, Path: Socket})
	if err != nil {
		return nil, err
	}
	return net.DialTimeout("unix", out.Socket, 5*time.Second)
}

// back pushes everything the cell sends to the browser, until either ends.
func (b *bridge) back(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	go func() { <-b.sess.Done(); _ = conn.Close() }()
	for {
		env, err := quic.ReadEnvelope(conn)
		if err != nil {
			return
		}
		r, err := rpc.ParseResponse(env)
		if err != nil {
			return
		}
		if r.PromiseID == importPromise {
			continue // the answer to cloud's own import of the chat threads
		}
		push := b.push
		if push == nil {
			push = b.sess.Push
		}
		if push(r.PromiseID, r.Status, r.Body) != nil {
			return
		}
	}
}
