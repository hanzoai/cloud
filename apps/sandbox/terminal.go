package sandbox

// terminal.go — the INTERACTIVE way into a sandbox, and the one credential a
// browser can carry through a WebSocket handshake.
//
// Every other route here is a request/response: the caller presents a bearer, the
// identity boundary mints X-User-Id, and principal.Org turns that into the org
// whose store may be read. A WebSocket cannot do that. The browser's WebSocket
// constructor takes a URL and nothing else — no Authorization header, no way to
// add one — so a socket authenticated the way exec is authenticated is a socket
// no browser can open.
//
// The usual answers to that are both wrong. Putting the bearer in the query
// string writes a long-lived credential into every access log, proxy buffer and
// browser history entry on the path. Trusting the session cookie makes the socket
// a CSRF target: no same-origin policy applies to a WebSocket, so any page the
// user visits could open one against their session.
//
// So the credential for the socket is MINTED for the socket. A ticket is a
// crypto-random token bound to ONE org and ONE sandbox, valid for thirty seconds,
// and spent the first time it is presented. Minting one requires the ordinary
// validated principal on the ordinary POST; presenting one grants exactly one
// terminal in exactly one sandbox and then no longer exists. A page that could
// somehow open the socket still has nothing to present, which is why the origin
// is not checked here — the ticket IS the check, and an origin allowlist beside
// it would be a second gate answering a question the first one already closed.
//
// THE TICKETS LIVE IN MEMORY, which is a decision with a stated bound rather than
// a shortcut. A thirty-second secret written to storage is a secret that can be
// read from storage for far longer than it is worth, so this one is never written
// anywhere. The cost is that a ticket is REPLICA-LOCAL: the socket has to reach
// the process that minted it, and cloud-api runs one replica (universe:
// charts/app/values/hanzo/cloud.yaml, replicas: 1), so today it always does.
//
// The day that number changes, this is what changes with it, and it fails LOUDLY
// — a 401 on the socket, saying the ticket is unknown — rather than quietly
// serving the wrong tenant. That is the property worth having in a gate: when its
// assumption stops holding, it refuses.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"

	"k8s.io/client-go/tools/remotecommand"
)

// errClosed is what a write attempted after the terminal is over returns. The
// session is already ending when it appears, so it is a stop signal for the
// heartbeat and never a failure anybody reports.
var errClosed = errors.New("sandbox: terminal closed")

// ticketTTL is how long a ticket is worth anything. It is a handshake window and
// nothing more: the console mints one and dials immediately, so thirty seconds is
// slack for a slow network rather than a lifetime anybody is meant to hold.
const ticketTTL = 30 * time.Second

// login is what a terminal runs. It asks for bash and settles for sh, because the
// three sandbox images are not one image and a shell that must exist is a shell
// that will one day not — the exec class is a stock node image today. Whatever
// tools the image carries, the hanzo CLI included, are commands the user types;
// none of them is a requirement to get a prompt.
var login = []string{"/bin/sh", "-lc", "exec bash -l 2>/dev/null || exec sh -l"}

// keystrokes bounds one inbound frame. A terminal's input is keys and pastes, so
// this is generous for a paste and far below anything a socket could be used to
// push into our memory.
const keystrokes = 1 << 16

// beat is how often the server pings, and idle is how long it waits to hear
// anything back. A terminal sits untouched for long stretches on purpose — that
// is what a shell IS — so the socket is kept alive by the heartbeat rather than
// by the user, and what the idle deadline detects is a client that has gone away
// without closing, not a user who is thinking.
const (
	beat = 30 * time.Second
	idle = 3 * time.Minute
)

// ─────────────────────────────────────────────────────────────────────────────
// The ticket
// ─────────────────────────────────────────────────────────────────────────────

// ticket is one permission to open one terminal: which org it was minted for,
// which sandbox it opens, and when it stops being worth anything.
type ticket struct {
	org     string
	sandbox string
	expires time.Time
}

// tickets is the live set. Small by construction — a ticket lives thirty seconds
// and every mint sweeps — so this is a map and a mutex rather than a cache.
type tickets struct {
	mu   sync.Mutex
	live map[string]ticket
}

func newTickets() *tickets { return &tickets{live: map[string]ticket{}} }

// mint issues a ticket for one org's sandbox. now is a parameter rather than a
// call to time.Now so that expiry is a fact a test can state instead of one it
// has to wait for.
func (t *tickets) mint(now time.Time, org, sandbox string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(now)
	t.live[tok] = ticket{org: org, sandbox: sandbox, expires: now.Add(ticketTTL)}
	return tok, nil
}

// redeem spends a ticket for one sandbox and answers the org it was minted for.
//
// The token is REMOVED the moment it is presented, before anything about it is
// checked. Spending it only on success would leave a rejected ticket live for the
// rest of its window — so a caller who presented it against the wrong sandbox
// could simply try again with the right one, which is the whole property
// single-use is supposed to remove.
func (t *tickets) redeem(now time.Time, tok, sandbox string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweep(now)
	k, ok := t.live[tok]
	if !ok {
		return "", false
	}
	delete(t.live, tok)
	if k.sandbox != sandbox || !now.Before(k.expires) {
		return "", false
	}
	return k.org, true
}

// sweep drops what has expired. Called under the lock by both operations, so an
// unspent ticket cannot accumulate: nothing here is reachable without a mint, and
// every mint clears the ones before it.
func (t *tickets) sweep(now time.Time) {
	for tok, k := range t.live {
		if !now.Before(k.expires) {
			delete(t.live, tok)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// The routes
// ─────────────────────────────────────────────────────────────────────────────

// open mints the ticket for one terminal. Gated exactly like its siblings — a
// validated principal, resolved to the org whose sandboxes may be addressed —
// and it resolves the sandbox before minting, so a ticket never names a sandbox
// the caller does not own or one that is not running.
func open(s *Service, c *zip.Ctx) error {
	o, ok := orgOf(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := idParam(c)
	m, _, err := find(s, c.Context(), o, id)
	if err != nil {
		return err
	}
	if m.Status != "running" {
		return zip.Errorf(http.StatusConflict, "sandbox is %s", firstNonEmpty(m.Status, "unknown"))
	}
	tok, err := s.State.tickets.mint(time.Now(), o, m.ID)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "ticket: %v", err)
	}
	return c.JSON(http.StatusCreated, map[string]any{
		"ticket": tok,
		// The PATH, not a URL. Which host this address wears in public is the
		// edge's answer and not ours — behind the gateway this process only ever
		// sees an internal name — so handing back an absolute URL would hand back
		// a guess. The client already knows the host it is talking to.
		"url": "/v1/sandboxes/" + m.ID + "/terminal/ws?ticket=" + tok,
	})
}

// attach serves one terminal. The ticket is spent BEFORE the upgrade, so a
// request that presents nothing gets an ordinary 401 with a body a client can
// read, rather than a socket that opens and immediately closes for reasons the
// browser will not tell it.
func attach(s *Service, c *zip.Ctx) error {
	id := idParam(c)
	org, ok := s.State.tickets.redeem(time.Now(), c.Query("ticket"), id)
	if !ok {
		return zip.ErrUnauthorized("terminal ticket is missing, expired or already spent")
	}
	m, store, err := find(s, c.Context(), org, id)
	if err != nil {
		return err
	}
	if m.Status != "running" {
		return zip.Errorf(http.StatusConflict, "sandbox is %s", firstNonEmpty(m.Status, "unknown"))
	}
	touched(c.Context(), store, m)

	// The session's own context. Background, not the request's: the request is
	// over the instant the connection is hijacked, and a socket bounded by it
	// would be closed before the first keystroke. What bounds it instead is the
	// LEASE — the same expiry the reaper enforces — so a terminal cannot outlive
	// the sandbox it is attached to even if the reaper is behind.
	ctx, stop := context.WithDeadline(context.Background(), leaseEnd(m))
	log := s.Log
	// The session runs AFTER this handler returns: the upgrade hands fasthttp a
	// hijack callback and answers immediately. So the cancel is deferred inside
	// the callback, where the session actually ends — deferring it here would
	// cancel the terminal before its first keystroke — and called by hand on the
	// one path where the callback never runs at all.
	serve := wsx.Upgrade(func(conn *wsx.Conn) error {
		defer stop()
		if err := bridge(ctx, conn, func(ctx context.Context, in *pipe, out *frames, w *window) error {
			return s.State.rt.tty(ctx, m, login, in, out, w)
		}); err != nil {
			log.Debug("terminal ended", "sandbox", m.ID, "err", err)
		}
		return nil
	})
	if err := serve(c); err != nil {
		stop()
		return err
	}
	return nil
}

// leaseEnd is when this terminal must be over: the sandbox's own expiry, or the
// longest lease anything here may hold when the row carries none. A row with no
// expiry is a row written before the lease was, not permission to run forever.
func leaseEnd(m Sandbox) time.Time {
	if m.ExpiresAt > 0 {
		return time.Unix(m.ExpiresAt, 0)
	}
	return time.Now().Add(maxTTL * time.Second)
}

// ─────────────────────────────────────────────────────────────────────────────
// The socket, seen as a terminal
// ─────────────────────────────────────────────────────────────────────────────

// bridge runs one terminal session over one socket, start to finish.
//
// THE WIRE, both directions, once:
//
//	client → server  a text frame is stdin, unless it is the one JSON object
//	                 {"resize":{"cols":N,"rows":M}}, which is a resize. A binary
//	                 frame is always stdin.
//	server → client  a BINARY frame is stdout.
//
// stdout is binary and not text for a reason that is not a preference. A text
// frame must be valid UTF-8 or the browser fails the connection, and what comes
// back from a pty is arbitrary bytes cut at arbitrary offsets — a multi-byte rune
// straddling two reads makes both frames individually invalid, so a terminal that
// sent text would die the first time anyone printed an emoji. Binary frames carry
// the bytes as they are and the decoder on the far side is the one that already
// knows how to carry a partial rune between writes.
func bridge(ctx context.Context, conn *wsx.Conn, run func(context.Context, *pipe, *frames, *window) error) error {
	// The session's cancel lives HERE, with the socket, because the socket is what
	// ends first. A client that closes its tab leaves a shell sitting at a prompt
	// with nothing to read and nothing to print, and EOF on stdin is a hint a pty
	// is free to ignore — so the end of the socket cancels the stream outright
	// rather than waiting for the far end to agree.
	ctx, stop := context.WithCancel(ctx)
	defer stop()

	in := newPipe()
	out := &frames{conn: conn}
	w := newWindow()

	conn.SetReadLimit(keystrokes)
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(idle))
	})

	done := make(chan struct{})
	defer close(done)
	go heartbeat(out, done)

	// The read pump owns everything the far side can affect: stdin's bytes and the
	// window's size. It is the ONE writer to each, so neither needs a lock of its
	// own — and when it returns, the far side is gone and the session goes with it.
	go func() {
		defer stop()
		defer w.close()
		defer in.eof()
		for {
			if err := conn.SetReadDeadline(time.Now().Add(idle)); err != nil {
				return
			}
			typ, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if typ != wsx.TextMessage && typ != wsx.BinaryMessage {
				continue
			}
			if cols, rows, ok := resize(typ, msg); ok {
				w.to(cols, rows)
				continue
			}
			if _, err := in.Write(msg); err != nil {
				return
			}
		}
	}()

	err := run(ctx, in, out, w)
	// The far side is told the session is over rather than left holding a socket
	// that has simply stopped answering. shut is a barrier as well as a notice:
	// once it returns no goroutine is inside a write, which matters because
	// fasthttp hands this connection's write buffer back to a shared pool the
	// moment this handler returns.
	out.shut(err)
	in.drop()
	return err
}

// heartbeat keeps an idle terminal open. A shell that nobody has typed into for
// an hour is a working shell, so the socket has to stay up without traffic — and
// the ping is also how a client that vanished without closing is noticed, since
// the read deadline is only ever extended by a pong.
func heartbeat(out *frames, done <-chan struct{}) {
	t := time.NewTicker(beat)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := out.ping(); err != nil {
				return
			}
		}
	}
}

// resize reads the one control frame. It is recognised only in a TEXT frame that
// is a JSON object carrying a plausible window — a binary frame is stdin whatever
// it contains, and a text frame that is not this exact shape is stdin too.
func resize(typ int, msg []byte) (uint16, uint16, bool) {
	if typ != wsx.TextMessage || len(msg) == 0 || msg[0] != '{' {
		return 0, 0, false
	}
	var ctl struct {
		Resize *struct {
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		} `json:"resize"`
	}
	if json.Unmarshal(msg, &ctl) != nil || ctl.Resize == nil {
		return 0, 0, false
	}
	if ctl.Resize.Cols == 0 || ctl.Resize.Rows == 0 {
		return 0, 0, false
	}
	return ctl.Resize.Cols, ctl.Resize.Rows, true
}

// pipe is stdin: what the read pump writes and the exec stream reads.
//
// Its two ends are retired by two different events and so they are two methods.
// Both are idempotent and safe from any goroutine — io.Pipe closes each end
// through a sync.Once of its own — which is what lets the pump and the session
// end in either order without either checking on the other.
type pipe struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func newPipe() *pipe {
	r, w := io.Pipe()
	return &pipe{r: r, w: w}
}

func (p *pipe) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipe) Write(b []byte) (int, error) { return p.w.Write(b) }

// eof ends stdin. The reader sees io.EOF, which is what tells a shell its input
// is over — a pty whose stdin merely stops producing bytes waits forever.
func (p *pipe) eof() { _ = p.w.Close() }

// drop retires the READER, and it is not the same act. It unblocks a read pump
// that is mid-write when the shell exits: an io.Pipe write waits for a reader,
// and after the stream is gone no reader is ever coming — so without this the
// pump's goroutine waits for the life of the process holding one keystroke.
func (p *pipe) drop() { _ = p.r.Close() }

// frames is stdout as WebSocket frames, serialized. fasthttp/websocket permits
// exactly one writer at a time and there are three here — the shell's output, the
// heartbeat and the closing notice — so they go through one lock or they corrupt
// each other's frames.
type frames struct {
	mu   sync.Mutex
	conn *wsx.Conn
	done bool
}

func (f *frames) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return 0, errClosed
	}
	if err := f.conn.SetWriteDeadline(time.Now().Add(beat)); err != nil {
		return 0, err
	}
	if err := f.conn.WriteMessage(wsx.BinaryMessage, b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (f *frames) ping() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return errClosed
	}
	return f.conn.WriteControl(wsx.PingMessage, nil, time.Now().Add(beat))
}

// shut says why the terminal ended and then bars every later write. Taking the
// same lock every write takes is what makes it a barrier rather than a flag: a
// write already inside finishes first, and none can start after.
func (f *frames) shut(cause error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return
	}
	f.done = true
	reason := "terminal closed"
	if cause != nil {
		reason = cause.Error()
	}
	_ = f.conn.WriteControl(wsx.CloseMessage, closing(reason), time.Now().Add(beat))
	_ = f.conn.Close()
}

// closing builds the close payload: the status code big-endian, then the reason.
// Written here because wsx re-exports the message TYPES and not the helper, and
// reaching past it to the socket library for two bytes would give this package a
// direct dependency on the transport the framework exists to own.
//
// A control frame carries at most 125 bytes and two of them are the code, so the
// reason is cut at 123 — over that the frame is not merely long, it is invalid.
func closing(reason string) []byte {
	if len(reason) > 123 {
		reason = reason[:123]
	}
	b := make([]byte, 2, 2+len(reason))
	binary.BigEndian.PutUint16(b, 1000) // normal closure
	return append(b, reason...)
}

// window is the terminal's size, in the shape the exec stream reads one: Next
// blocks until the size CHANGES and answers nil once the session is over.
//
// Only the LATEST size is kept. A resize that arrives while the previous one is
// still unread replaces it, because the intermediate widths of a window somebody
// is dragging are sizes nobody needs to see and a queue of them is a queue the
// shell would redraw its way through.
type window struct {
	mu   sync.Mutex
	size remotecommand.TerminalSize
	wake chan struct{}
	over chan struct{}
	once sync.Once
}

func newWindow() *window {
	return &window{wake: make(chan struct{}, 1), over: make(chan struct{})}
}

func (w *window) to(cols, rows uint16) {
	w.mu.Lock()
	w.size = remotecommand.TerminalSize{Width: cols, Height: rows}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default: // one is already pending and it will read the size just written
	}
}

// close retires the window, which is what lets the goroutine the exec stream
// runs Next in return instead of blocking on a socket nobody is reading.
func (w *window) close() { w.once.Do(func() { close(w.over) }) }

func (w *window) Next() *remotecommand.TerminalSize {
	select {
	case <-w.over:
		return nil
	case <-w.wake:
		w.mu.Lock()
		defer w.mu.Unlock()
		size := w.size
		return &size
	}
}

// terminal registers the interactive pair on the group that already owns the
// member routes. One function and not two lines in Routes, so that what a
// terminal needs — a ticket door and a socket door, never one without the other
// — cannot be half registered.
func terminal(g zip.Router, s *Service) {
	g.Post("/:id/terminal", cloud.Handle(s, open))
	g.Get("/:id/terminal/ws", cloud.Handle(s, attach))
}
