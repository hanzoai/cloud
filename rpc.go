package cloud

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"os"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	zap "github.com/zap-proto/go"
	zaprpc "github.com/zap-proto/go/rpc"
)

// rpc.go — the internal plane: native ZAP over a unix socket.
//
// This is how one app calls another. Not HTTP, and not zapface — zapface is
// the BROWSER adaptor, and its job is translation: frame → SuperJSON → JSON →
// a rebuilt http.Request replayed through the router. Sending an internal call
// down that path would serialize the same value four times to cross one kernel
// boundary. Here there is no translation layer at all: the envelope and the
// payload are built once, in wire layout, and the bytes on the socket ARE the
// bytes in memory on both sides. That is what the format is for.
//
// The envelope is zaprpc.Call/Response — method id, promise id, capability,
// payload — exactly the frames the rest of the ZAP world speaks. The payload
// is a ZAP message owned by the method's two ends; until zapc generates typed
// codecs per subsystem (deps.go names that future), scalar helpers below cover
// the simple cases without ever touching JSON.
//
// TRANSPORT IS THE SOCKET, ONLY. {runDir}/<app>.sock, directory 0700, socket
// 0600: the filesystem is the reachable surface and the kernel enforces who
// may connect — the same boundary credz already trusts with credentials. There
// is deliberately NO network fallback. A missing socket means "that app is not
// running here", which is an answer, not a condition to paper over with a
// second transport; one mechanism cannot drift against itself.
//
// AUTHORITY RIDES AS A CAPABILITY. Call.Cap carries the caller's principal
// (Ident) — the same identity the edge minted from a validated token. The
// callee re-applies its own rules to it: delegation, never escalation, and
// sound here because only our own processes can reach the socket at all.

// Ident is the principal a call acts FOR — the identity the gateway minted at
// the edge (middleware_identity.go), carried in the envelope's capability slot
// so the callee applies its OWN authorization to the SAME principal. A zero
// Ident is anonymous, and a method that needs authority refuses it.
//
// TWO ADMIN SCOPES, TWO FIELDS. Admin is platform sudo (owner == the reserved
// admin org, X-User-IsAdmin); OrgAdmin is admin OF ONE'S OWN org (the IAM
// isAdmin bit, X-User-IsOrgAdmin). apps/principal calls conflating them a
// privilege escalation, so the capability carries them apart — a callee that
// admits an org admin can say so without having to read "holds an org" as
// "administers it". Both headers are stripped on ingress and re-minted only
// from validated claims, so neither is forgeable off the gateway.
type Ident struct {
	Org      string
	User     string
	Email    string
	Project  string
	Admin    bool
	OrgAdmin bool
}

// Ident wire layout. Both pack and parse live in this file, so the two halves
// cannot drift — the same discipline zapface keeps for its own frames.
//
// The bools sit in the padding the four text pointers already round up to, so
// identFixed stays 40 and the frame does not grow. A peer built before OrgAdmin
// existed sends that byte as zero and reads it as false — an older caller is
// never mistaken for an org admin, and an older callee simply does not ask.
const (
	identOrgOff      = 0
	identUserOff     = 8
	identEmailOff    = 16
	identProjectOff  = 24
	identAdminOff    = 32
	identOrgAdminOff = 33
	identFixed       = 40
)

func packIdent(id Ident) []byte {
	if id == (Ident{}) {
		return nil // anonymous: no capability at all, not an empty claim
	}
	b := zap.NewBuilder(len(id.Org) + len(id.User) + len(id.Email) + len(id.Project) + identFixed + 64)
	ob := b.StartObject(identFixed)
	ob.SetText(identOrgOff, id.Org)
	ob.SetText(identUserOff, id.User)
	ob.SetText(identEmailOff, id.Email)
	ob.SetText(identProjectOff, id.Project)
	ob.SetBool(identAdminOff, id.Admin)
	ob.SetBool(identOrgAdminOff, id.OrgAdmin)
	ob.FinishAsRoot()
	return b.Finish()
}

func parseIdent(b []byte) Ident {
	if len(b) == 0 {
		return Ident{}
	}
	m, err := zap.Parse(b)
	if err != nil {
		return Ident{} // an unreadable claim is no claim
	}
	r := m.Root()
	return Ident{
		Org:      r.Text(identOrgOff),
		User:     r.Text(identUserOff),
		Email:    r.Text(identEmailOff),
		Project:  r.Text(identProjectOff),
		Admin:    r.Bool(identAdminOff),
		OrgAdmin: r.Bool(identOrgAdminOff),
	}
}

// Method is one exposed procedure: it receives the delegated principal and the
// request payload, and returns the reply payload. Payloads are opaque to the
// transport — it moves bytes and never re-encodes them.
type Method func(ctx context.Context, who Ident, req []byte) ([]byte, error)

// methods is the process-wide registry. The wire carries fnv32a(name), so the
// envelope stays fixed-size and no string travels per call; a hash collision
// between two DIFFERENT names panics at Expose — boot time, loud — rather than
// silently routing one method's calls to another.
var methods = struct {
	sync.RWMutex
	byID map[uint32]Method
	name map[uint32]string
}{byID: map[uint32]Method{}, name: map[uint32]string{}}

func methodID(name string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	return h.Sum32()
}

// Expose publishes one method on the internal plane, by convention named
// "<app>.<verb>" ("treasury.reserve"). Re-exposing the SAME name replaces the
// handler — Mount runs again in tests and on remount, and that must not be a
// crash — but two different names hashing together is a routing fault and
// panics immediately.
func Expose(name string, m Method) {
	id := methodID(name)
	methods.Lock()
	defer methods.Unlock()
	if prev, ok := methods.name[id]; ok && prev != name {
		panic(fmt.Sprintf("rpc: method id collision: %q and %q — rename one", prev, name))
	}
	methods.byID[id] = m
	methods.name[id] = name
}

func lookup(id uint32) Method {
	methods.RLock()
	defer methods.RUnlock()
	return methods.byID[id]
}

// fault is a refusal with a status a peer can act on. Anything else a method
// returns is a 500 — an accident, not an answer.
type fault struct {
	status uint32
	msg    string
}

func (f fault) Error() string { return fmt.Sprintf("%d: %s", f.status, f.msg) }

// Fault refuses a call with a specific status. 402 vs 403 vs 404 vs 503 is
// "unfunded" vs "not yours" vs "no such thing" vs "not ready", and the caller
// receives exactly the code the method chose — collapsing them is how boards
// end up lying.
func Fault(status int, msg string) error { return fault{uint32(status), msg} }

// Frames on the stream: 4-byte big-endian length, then one zaprpc envelope.
// Bounded so a peer cannot make this process hold an unbounded buffer.
const frameMax = 4 << 20

func writeFrame(w io.Writer, b []byte) error {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(b)))
	if _, err := w.Write(l[:]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var l [4]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(l[:])
	if n == 0 || n > frameMax {
		return nil, fmt.Errorf("rpc: frame of %d bytes refused", n)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// callTimeout bounds one method invocation server-side; idleTimeout is how
// long a quiet connection is kept before it is closed.
const (
	callTimeout = 30 * time.Second
	idleTimeout = 2 * time.Minute
)

// Listen serves this process's exposed methods at {runDir}/<app>.sock. It is
// called by Serve for every mounted app name, so Dial(app) resolving a socket
// always means "the app is up" — and an up app answering 404 for a method
// means version skew, a different, separately diagnosable fact.
//
// A stale socket left by a dead process is taken over: connect first, and only
// an unanswered socket is unlinked — removing a LIVE listener's socket would
// orphan every peer that has not yet dialed (the credz rule).
func Listen(app string, log luxlog.Logger) (io.Closer, error) {
	dir := runDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("rpc: run dir: %w", err)
	}
	path := sock(app)
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("rpc: %s already served", path)
		}
		_ = os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("rpc: listen %s: %w", path, err)
	}
	_ = os.Chmod(path, 0o600)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go serveConn(conn, log)
		}
	}()
	if log != nil {
		log.Info("rpc listening", "app", app, "sock", path)
	}
	return closer(func() error {
		err := ln.Close()
		_ = os.Remove(path)
		return err
	}), nil
}

type closer func() error

func (c closer) Close() error { return c() }

// serveConn answers frames one at a time. A malformed frame ends the
// connection — there is no promise id to answer garbage with — and every
// well-formed call is answered, even the refused ones.
func serveConn(conn net.Conn, log luxlog.Logger) {
	defer func() { _ = conn.Close() }()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		frame, err := readFrame(conn)
		if err != nil {
			return
		}
		call, err := zaprpc.ParseRequest(frame)
		if err != nil {
			if log != nil {
				log.Warn("rpc: bad frame", "err", err)
			}
			return
		}
		status, body := answer(call)
		_ = conn.SetWriteDeadline(time.Now().Add(callTimeout))
		if err := writeFrame(conn, zaprpc.BuildResponse(status, call.PromiseID, body)); err != nil {
			return
		}
	}
}

// answer invokes one method. On success the body is the reply payload,
// verbatim; on refusal it is the message, and the status says why.
func answer(call zaprpc.Call) (uint32, []byte) {
	m := lookup(call.Method)
	if m == nil {
		return zaprpc.StatusNotFound, []byte("unknown method")
	}
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	out, err := m(ctx, parseIdent(call.Cap), call.Payload)
	if err != nil {
		var f fault
		if errors.As(err, &f) {
			return f.status, []byte(f.msg)
		}
		return zaprpc.StatusInternal, []byte(err.Error())
	}
	return zaprpc.StatusOK, out
}

// ---- scalar payloads ----
//
// Until zapc generates typed codecs, the simple methods carry one scalar. These
// keep the ONE payload rule — a payload is a ZAP message — without a schema,
// and without JSON.

const scalarOff = 0

// PutI64 packs one int64 as a reply payload.
func PutI64(v int64) []byte {
	b := zap.NewBuilder(8 + 64)
	ob := b.StartObject(8)
	ob.SetInt64(scalarOff, v)
	ob.FinishAsRoot()
	return b.Finish()
}

// I64 reads the int64 a scalar payload carries.
func I64(payload []byte) (int64, error) {
	m, err := zap.Parse(payload)
	if err != nil {
		return 0, fmt.Errorf("rpc: scalar: %w", err)
	}
	return m.Root().Int64(scalarOff), nil
}
