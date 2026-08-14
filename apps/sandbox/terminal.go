package sandbox

// terminal.go — the INTERACTIVE way into a sandbox, and the one credential a
// browser can carry into a socket or a frame.
//
// Three addresses, one mechanism:
//
//	POST  /:id/terminal/ticket   the credential
//	GET   /:id/terminal          the terminal, as a page (page.go)
//	GET   /:id/terminal/ws       the terminal, as a socket
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
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/apps/principal"
	"io"
	"net/http"
	"strings"
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

// shell is what a terminal runs, and everything it needs is `/bin/sh`.
//
// It asks for zsh, settles for bash, and settles again for sh, because the
// sandbox images are not one image and a shell that must exist is a shell that
// will one day not — the exec class is a stock node image today and only the
// admin image carries zsh. Whatever tools the image carries, the hanzo CLI
// included, are commands the user types; none is a requirement for a prompt.
//
// A PREFERENCE AND NOT A REQUIREMENT, all the way down. Asking the image what
// it has costs nothing when the answer is no — the next `exec` in the chain
// simply runs — whereas naming one shell would make the terminal a feature of
// the image rather than of the sandbox.
//
// A NAMED session is the same shell under tmux: `new -A` attaches to the session
// if it is there and creates it if it is not, which is what lets ONE sandbox hold
// many terminals — a host opening four panes opens four names, and each reattaches
// to what it left. tmux is asked for and not required: an image without it gets
// the plain shell rather than an error, because a missing multiplexer should cost
// a caller its session names, not its terminal.
func shell(session string) []string {
	if session == "" {
		return []string{"/bin/sh", "-lc", term + plain}
	}
	return []string{"/bin/sh", "-lc",
		term + "command -v tmux >/dev/null 2>&1 && exec tmux new -A -s " + shellQuote(session) + "; " + plain}
}

// term names the terminal to the programs running in it, and NOTHING ELSE DOES.
//
// The exec subresource opens a pty and stops there — Kubernetes sets no
// environment on it, so TERM arrives unset, and a pty whose type is unknown is
// one a full-screen program refuses to draw on. tmux says so exactly: "open
// terminal failed: terminal does not support clear", and then exits.
//
// That single missing variable took the whole terminal down rather than costing
// it tmux, because the fallback beside it cannot run: `exec` has already replaced
// the shell, so a tmux that STARTS and fails leaves nothing behind to fall back
// to and the socket closes. The named session was never created, every reconnect
// repeated it, and what a person saw was a terminal that opened and immediately
// said the connection had closed.
//
// xterm-256color is the truth about the other end: every surface frames the same
// xterm.js page. `:-` and not a bare assignment, so a caller that has already
// said which terminal it is keeps its answer.
const term = "export TERM=${TERM:-xterm-256color}; "

const plain = "exec zsh -l 2>/dev/null || exec bash -l 2>/dev/null || exec sh -l"

// sessionOK is what a session name may be. It is an allowlist and not an escape,
// because a name reaches a command line: `-` would be read by tmux as a flag and
// anything outside this set has no business naming a session in the first place.
// A name that does not fit is refused rather than repaired — silently renaming
// somebody's session hands them a different shell than the one they asked for.
func sessionOK(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return name[0] != '-'
}

// session reads the terminal's session name off the request. `arg` is the name
// the framing host already uses for it.
func session(c *zip.Ctx) (string, error) {
	name := strings.TrimSpace(c.Query("arg"))
	if name == "" {
		return "", nil
	}
	if !sessionOK(name) {
		return "", zip.ErrBadRequest("arg must be 1-64 characters of letters, digits, - or _, " +
			"and may not begin with -")
	}
	return name, nil
}

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

// open mints the ticket for one DOOR. Gated exactly like its siblings — a
// validated principal, resolved to the org whose sandboxes may be addressed —
// and it resolves the sandbox before minting, so a ticket never names a sandbox
// the caller does not own or one that is not running.
//
// THE DOOR IS THE ADDRESS AND NOT THE GRANT. A ticket says which org and which
// sandbox, and the terminal and the screen are two views of that one machine —
// a caller holding the authority to type in a sandbox holds the authority to
// look at it. Binding the door into the token would be a second gate answering
// a question the first one already closed, and a gate that decides nothing is
// one somebody later has to reason about anyway. What the door decides is the
// URL a caller is handed back, which is the only part that differs.
func open(door string) func(*Service, *zip.Ctx) error {
	return func(s *Service, c *zip.Ctx) error {
		o, ok := orgOf(c)
		if !ok {
			return principal.Refused(c)
		}
		id := idParam(c)
		m, _, err := find(s, c.Context(), o, id)
		if err != nil {
			return err
		}
		if m.Status != "running" {
			return zip.Errorf(http.StatusConflict, "sandbox is %s", cmp.Or(m.Status, "unknown"))
		}
		tok, err := s.State.tickets.mint(time.Now(), o, m.ID)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "ticket: %v", err)
		}
		return c.JSON(http.StatusCreated, map[string]any{
			"ticket":    tok,
			"expiresIn": int(ticketTTL / time.Second),
			// The PATH, not a URL. Which host this address wears in public is the
			// edge's answer and not ours — behind the gateway this process only ever
			// sees an internal name — so handing back an absolute URL would hand back
			// a guess. The client already knows the host it is talking to.
			//
			// It names the PAGE, because that is what a caller embeds; the page finds
			// its own socket. A caller that wants the raw socket adds `/ws`, which is
			// exactly what the page does.
			"url": "/v1/sandboxes/" + m.ID + "/" + door + "?ticket=" + tok,
		})
	}
}

// pty serves one terminal: a shell on a pseudo-terminal, for as long as
// somebody is typing.
func pty(s *Service, c *zip.Ctx) error {
	name, err := session(c)
	if err != nil {
		return err
	}
	return attach(s, c, func(ctx context.Context, m Sandbox, in *pipe, out *frames, w *window) error {
		return s.State.rt.tty(ctx, m, shell(name), in, out, w)
	})
}

// attach serves one interactive session over one socket, whatever the session
// shows. The ticket is spent BEFORE the upgrade, so a request that presents
// nothing gets an ordinary 401 with a body a client can read, rather than a
// socket that opens and immediately closes for reasons the browser will not
// tell it.
//
// EVERYTHING BUT `run` IS THE SAME FOR EVERY SESSION — the ticket, the sandbox,
// the lease that bounds it, the attention that keeps it from being reaped, the
// upgrade — so it is written once. The screen and the terminal differ in what
// runs inside the socket and in nothing else, and a second copy of this
// lifecycle is a second place for a session to outlive its lease.
func attach(s *Service, c *zip.Ctx, run func(context.Context, Sandbox, *pipe, *frames, *window) error) error {
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
		return zip.Errorf(http.StatusConflict, "sandbox is %s", cmp.Or(m.Status, "unknown"))
	}
	touched(c.Context(), store, m)

	// The session's own context. Background, not the request's: the request is
	// over the instant the connection is hijacked, and a socket bounded by it
	// would be closed before the first keystroke. What bounds it instead is the
	// LEASE — the same expiry the reaper enforces — so a terminal cannot outlive
	// the sandbox it is attached to even if the reaper is behind.
	ctx, stop := context.WithDeadline(context.Background(), leaseEnd(m))
	// ATTENTION, for as long as somebody is typing. The reaper ends a sandbox
	// that has gone an hour untouched, and attention is stamped by exec and fs
	// calls — which a terminal makes none of. Without this, a session somebody is
	// sitting in reads as abandoned and the pod is taken out from under it at the
	// hour mark, in the middle of a command.
	attend := func() { touched(ctx, store, m) }
	log := s.Log
	// The session runs AFTER this handler returns: the upgrade hands fasthttp a
	// hijack callback and answers immediately. So the cancel is deferred inside
	// the callback, where the session actually ends — deferring it here would
	// cancel the terminal before its first keystroke — and called by hand on the
	// one path where the callback never runs at all.
	serve := wsx.Upgrade(func(conn *wsx.Conn) error {
		defer stop()
		if err := bridge(ctx, conn, attend, func(ctx context.Context, in *pipe, out *frames, w *window) error {
			return run(ctx, m, in, out, w)
		}); err != nil {
			log.Debug("session ended", "sandbox", m.ID, "err", err)
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
func bridge(ctx context.Context, conn *wsx.Conn, attend func(), run func(context.Context, *pipe, *frames, *window) error) error {
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
	go alive(out, attend, done)

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

// alive says this session is still here, to the two things that have to be told.
//
// THE CLIENT is told with a ping. A shell nobody has typed into for an hour is a
// working shell, so the socket has to stay up without traffic — and the ping is
// also how a client that vanished without closing is noticed, since the read
// deadline is only ever extended by a pong.
//
// THE REAPER is told with a touch. It ends a sandbox that has gone an hour
// untouched, and the fields it reads are stamped by exec and fs calls, which a
// terminal makes none of. One loop says both, because they are one fact.
func alive(out *frames, attend func(), done <-chan struct{}) {
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
			attend()
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

// terminal registers the three doors, on the group that already owns the member
// routes. One function and not three lines in Routes, so that what a terminal
// needs — a credential, a page and a socket — cannot be half registered.
//
//	POST  /:id/terminal/ticket   the credential a socket can carry
//	GET   /:id/terminal          the page, for anything with an iframe
//	GET   /:id/terminal/ws       the socket, for anything with its own emulator
//
// The page and the socket are two addresses and not one, because the ticket is
// spent ONCE: a page that redeemed it would be a page holding a credential that
// no longer opens anything.
func terminal(g zip.Router, s *Service) {
	g.Post("/:id/terminal/ticket", cloud.Handle(s, open("terminal")))
	g.Get("/:id/terminal", cloud.Handle(s, serve(document)))
	g.Get("/:id/terminal/ws", cloud.Handle(s, pty))
}
