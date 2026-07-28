package cloud

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	zaprpc "github.com/zap-proto/go/rpc"
)

// dial.go — the client half of the internal plane (rpc.go is the server half).
//
// Apps are separate binaries, so a Go import cannot reach another app's data:
// treasury.ReserveCents() compiled inside apps/admin and returned zero forever,
// because treasury never mounts there. Dial replaces that — the imports, the
// `mounted` package globals read across apps, and the Register* seams that only
// fire when two apps share a binary.
//
// One transport: the callee's unix socket. No HTTP, no fallback. A missing
// socket is the answer "that app is not running here", reported as an error the
// caller can show — never a zero value, which is exactly the lie the imports
// told.

// runDirEnv overrides where app sockets live. Default: {CLOUD_DATA_DIR}/run,
// the same data root credz keeps its broker socket in.
const runDirEnv = "CLOUD_RUN_DIR"

func runDir() string {
	if v := strings.TrimSpace(os.Getenv(runDirEnv)); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("CLOUD_DATA_DIR")); v != "" {
		return filepath.Join(v, "run")
	}
	return "/run/hanzo"
}

// sock is the well-known socket path for one app.
func sock(app string) string { return filepath.Join(runDir(), app+".sock") }

// PeerSocket is the canonical path an app SERVES on, and it is the same path
// Call resolves — one definition, so a server and its callers cannot disagree
// about where an app lives. serve.go binds it for every app a process mounts;
// without that nothing creates the file and every peer call is the error "that
// app is not running here".
//
// A ZAP listener takes a PATH, not a port: nothing to allocate, no clash between
// co-located apps, and nothing bound to a network interface.
func PeerSocket(app string) string { return sock(app) }

// Headers is the sliver of a request Dial needs to delegate its principal —
// satisfied by *zip.Ctx, and by any test stub. Taking the sliver instead of the
// concrete context keeps the transport free of the router.
type Headers interface{ Header(string) string }

// Peer is another app. Cheap: Dial records the name, Call does the work, so a
// caller may hold one or dial per call.
type Peer struct {
	app string
	who Ident
}

// Dial names the app to call. Resolution happens per Call, not here, so an app
// that starts later is picked up without a restart.
func Dial(app string) *Peer { return &Peer{app: app} }

// As delegates the caller's principal: the identity headers the edge minted
// from a validated token, re-read by the callee as its own authorization input.
// A caller can only pass on authority it already holds, and the callee still
// decides — delegation, never escalation. Sound here because the socket is the
// transport: only our own processes can present anything at all.
func (p *Peer) As(h Headers) *Peer {
	if h == nil {
		return p
	}
	cp := *p
	cp.who = Ident{
		Org:     strings.TrimSpace(h.Header("X-Org-Id")),
		User:    strings.TrimSpace(h.Header("X-User-Id")),
		Email:   strings.TrimSpace(h.Header("X-User-Email")),
		Project: strings.TrimSpace(h.Header("X-Project-Id")),
		Admin:   strings.TrimSpace(h.Header("X-User-IsAdmin")) == "true",
	}
	return &cp
}

// For names the tenant a call acts FOR when there is no request to delegate —
// the background reconcile case. A loop acting for org X has no user attached,
// but it must still name X so the callee scopes the answer; applied after As,
// the explicit tenant wins, which is also the old internal-call contract.
func (p *Peer) For(org string) *Peer {
	cp := *p
	cp.who.Org = strings.TrimSpace(org)
	return &cp
}

// Call invokes one method on the peer and returns its reply payload, verbatim.
// The request payload is opaque to the transport: bytes in, bytes out, nothing
// re-encoded in between.
//
// One frame out, one frame in, on a connection dialed for this call — a unix
// connect is microseconds, and no pool means no pool to get wrong. The ctx
// deadline bounds the whole exchange.
func (p *Peer) Call(ctx context.Context, method string, req []byte) ([]byte, error) {
	path := sock(p.app)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %s: dial %s: %w", p.app, method, path, err)
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(callTimeout)
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	frame := zaprpc.BuildRequest(zaprpc.Call{
		Method:    methodID(method),
		PromiseID: 1, // one call, one answer; nothing to correlate
		Cap:       packIdent(p.who),
		Payload:   req,
	})
	if err := writeFrame(conn, frame); err != nil {
		return nil, fmt.Errorf("peer %s: %s: send: %w", p.app, method, err)
	}
	reply, err := readFrame(conn)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %s: recv: %w", p.app, method, err)
	}
	resp, err := zaprpc.ParseResponse(reply)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %s: bad frame: %w", p.app, method, err)
	}
	if resp.PromiseID != 1 {
		return nil, fmt.Errorf("peer %s: %s: promise %d for 1", p.app, method, resp.PromiseID)
	}
	if resp.Status < 200 || resp.Status > 299 {
		// The status arrives exactly as the method chose it: 402 vs 404 vs 503 is
		// "unfunded" vs "no such thing" vs "not ready", and the body is the
		// method's own words.
		return nil, fmt.Errorf("peer %s: %s: status %d: %s", p.app, method, resp.Status, resp.Body)
	}
	return resp.Body, nil
}
