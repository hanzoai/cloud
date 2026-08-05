package lsp

// server.go is a JSON-RPC 2.0 client speaking the Language Server Protocol over
// a language server's stdio: Content-Length framing, one multiplexed connection,
// initialize → didOpen → ask.
//
// It is the testable core of this app, so it knows nothing about orgs, HTTP,
// billing or git. It is given a reader, a writer and a Lang; everything else is
// the caller's. [newConn] is the seam that makes that true — [Start] spawns a
// real server and hands it here, and a test hands it an in-process pipe, and
// both drive the identical code.
//
// The one structural decision: a SINGLE reader goroutine owns the stdout side
// and demultiplexes it. LSP is not request/response — a server interleaves
// responses, its own requests, and unsolicited notifications on the same stream,
// and diagnostics are only ever the third kind. Reading inline from Call (which
// is what the Python tool does) drops every message that is not the response
// being waited for, which is why that tool cannot report diagnostics without a
// second read path. One reader, three destinations, no second path.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// maxFrame bounds one inbound message. A language server is a subprocess we
// spawned, but it is parsing a tenant's checkout, and a hostile input that makes
// it emit an enormous frame must not become an allocation the pod dies on.
const maxFrame = 32 << 20 // 32 MiB

// handshake, request and shutdown budgets. A cold rust-analyzer or gopls indexes
// before it answers, so initialize is generous where a point query is not.
const (
	initWait  = 90 * time.Second
	callWait  = 30 * time.Second
	closeWait = 3 * time.Second
)

// Conn is one live language server: a process (or, in a test, a pipe) plus the
// bookkeeping to route its stream. Safe for concurrent use — Call may be entered
// from several requests against the same warm workspace.
type Conn struct {
	w    io.WriteCloser
	stop func() // releases the transport (kills the process)

	wmu sync.Mutex // serializes frame writes; a torn frame desynchronizes the stream
	seq atomic.Int64

	mu   sync.Mutex
	wait map[int64]chan msg

	dmu  sync.Mutex
	diag map[string][]Diagnostic

	done chan struct{} // closed when the reader stops
	err  error         // why it stopped; read only after done
	once sync.Once
}

// msg is any JSON-RPC frame in either direction. Which of the four kinds it is
// follows from which fields are present, which is what [Conn.read] switches on.
type msg struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("lsp: rpc %d: %s", e.Code, e.Message) }

// Start spawns l's language server rooted at root and completes the handshake.
//
// The process is deliberately NOT tied to ctx: a Conn outlives the request that
// warmed it (that is the entire point of the pool), so binding it to the
// request's context would kill the server the moment the caller got its answer.
// [Conn.Close] is what ends it.
func Start(ctx context.Context, l Lang, root string) (*Conn, error) {
	if len(l.Start) == 0 {
		return nil, fmt.Errorf("lsp: language %q has no server", l.Name)
	}
	cmd := exec.Command(l.Start[0], l.Start[1:]...)
	cmd.Dir = root
	cmd.Env = env(l)
	// The server's stderr is its own log, not ours to relay: it can be chatty and
	// it can echo tenant source. Dropping it keeps both out of our logs.
	cmd.Stderr = io.Discard

	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdin: %w", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %s: %w", l.Start[0], err)
	}

	c := newConn(in, out, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	if err := c.handshake(ctx, l, root); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// newConn wires a Conn onto an already-open transport and starts its reader.
// Start uses it for a real process; a test uses it for a pipe.
func newConn(w io.WriteCloser, r io.Reader, stop func()) *Conn {
	c := &Conn{
		w:    w,
		stop: stop,
		wait: make(map[int64]chan msg),
		diag: make(map[string][]Diagnostic),
		done: make(chan struct{}),
	}
	go c.read(bufio.NewReaderSize(r, 64<<10))
	return c
}

// handshake performs initialize → initialized. Ported from the Python tool's
// _initialize_lsp, with initializationOptions added (langs.go: Init) because
// that is where rust-analyzer's build scripts get turned off.
func (c *Conn) handshake(ctx context.Context, l Lang, root string) error {
	ctx, cancel := context.WithTimeout(ctx, initWait)
	defer cancel()

	uri := pathURI(root)
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   uri,
		"rootPath":  root,
		"capabilities": map[string]any{
			"workspace": map[string]any{"workspaceFolders": true, "applyEdit": false},
			"textDocument": map[string]any{
				"synchronization":    map[string]any{"dynamicRegistration": true, "didSave": true},
				"completion":         map[string]any{"completionItem": map[string]any{"snippetSupport": true}},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"definition":         map[string]any{"dynamicRegistration": true, "linkSupport": true},
				"references":         map[string]any{"dynamicRegistration": true},
				"publishDiagnostics": map[string]any{"relatedInformation": false},
			},
		},
		"workspaceFolders": []map[string]any{{"uri": uri, "name": filepath.Base(root)}},
	}
	if l.Init != nil {
		params["initializationOptions"] = l.Init
	}
	if _, err := c.Call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("lsp: initialize: %w", err)
	}
	return c.Notify("initialized", map[string]any{})
}

// Call issues a request and waits for the response with that id.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.seq.Add(1)
	ch := make(chan msg, 1)

	c.mu.Lock()
	c.wait[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.wait, id)
		c.mu.Unlock()
	}()

	if err := c.send(map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	}); err != nil {
		return nil, err
	}

	select {
	case m := <-ch:
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-c.done:
		return nil, c.stopped()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Notify sends a notification — no id, so no response is expected or waited for.
func (c *Conn) Notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// Open sends textDocument/didOpen for a file already inside the workspace, which
// is what makes the server willing to answer about it. Content is read from
// disk: the checkout is the source of truth, and accepting caller-supplied text
// would let one request answer about a file the tenant's repo does not have.
func (c *Conn) Open(l Lang, abs string) (string, error) {
	text, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("lsp: read %s: %w", filepath.Base(abs), err)
	}
	uri := pathURI(abs)
	return uri, c.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": l.ID(abs), "version": 1, "text": string(text),
		},
	})
}

// Diagnostics collects what the server published for uri.
//
// LSP has no "diagnostics complete" signal — publishDiagnostics is unsolicited
// and a server may publish several times as analysis deepens. So this waits for a
// first publication, then for a settle window in which nothing new arrives, and
// reports what it has. A server that publishes nothing (a clean file) is reported
// as clean at the deadline, which is the honest reading.
func (c *Conn) Diagnostics(ctx context.Context, uri string, settle time.Duration) []Diagnostic {
	const tick = 50 * time.Millisecond
	var last int
	var quiet time.Duration
	for {
		select {
		case <-ctx.Done():
			return c.published(uri)
		case <-c.done:
			return c.published(uri)
		case <-time.After(tick):
		}
		got := c.published(uri)
		if len(got) != last {
			last, quiet = len(got), 0
			continue
		}
		if last > 0 {
			if quiet += tick; quiet >= settle {
				return got
			}
		}
	}
}

func (c *Conn) published(uri string) []Diagnostic {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	if d, ok := c.diag[uri]; ok {
		return append([]Diagnostic(nil), d...)
	}
	return nil
}

// Close shuts the server down politely, then unconditionally. Idempotent.
//
// Polite first because a server asked to exit flushes and releases its own
// children. But the polite phase can BLOCK: a server that has stopped reading its
// stdin — crashed, or wedged mid-index — leaves our write with nowhere to go, and
// Close would then never return. It would hold a pool slot, a process handle
// and, at Shutdown, the whole binary.
//
// So the courtesy runs off to the side and this waits on a clock, never on the
// server. Closing the writer afterwards is what releases that goroutine: a
// blocked write to a closed pipe returns rather than waits.
func (c *Conn) Close() {
	c.once.Do(func() {
		select {
		case <-c.done:
			// Already dead. There is nobody to say goodbye to, and saying it
			// anyway is exactly how this used to hang.
		default:
			polite := make(chan struct{})
			go func() {
				defer close(polite)
				ctx, cancel := context.WithTimeout(context.Background(), closeWait)
				defer cancel()
				_, _ = c.Call(ctx, "shutdown", nil)
				_ = c.Notify("exit", nil)
			}()
			select {
			case <-polite:
			case <-time.After(closeWait):
			}
		}

		_ = c.w.Close()
		select {
		case <-c.done:
		case <-time.After(closeWait):
		}
		if c.stop != nil {
			c.stop()
		}
	})
}

// send frames one message. The write lock spans header and body: two goroutines
// interleaving there would produce a frame whose length does not match its
// payload, and the stream never recovers from that.
func (c *Conn) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("lsp: encode: %w", err)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := fmt.Fprintf(c.w, "Content-Length: %d\r\n\r\n", len(b)); err != nil {
		return fmt.Errorf("lsp: write header: %w", err)
	}
	if _, err := c.w.Write(b); err != nil {
		return fmt.Errorf("lsp: write body: %w", err)
	}
	return nil
}

// read is the sole owner of the inbound stream. Every frame is exactly one of
// four kinds, and each has exactly one destination.
func (c *Conn) read(r *bufio.Reader) {
	defer close(c.done)
	for {
		body, err := readFrame(r)
		if err != nil {
			c.err = err
			return
		}
		var m msg
		if json.Unmarshal(body, &m) != nil {
			continue // a frame we cannot parse is not a reason to drop the session
		}

		switch {
		case m.Method != "" && len(m.ID) > 0:
			// A server→client REQUEST (workspace/configuration,
			// client/registerCapability). It BLOCKS the server until answered,
			// so silence here is a hang, not a no-op. A null result is a valid
			// answer to every one of them and commits us to nothing.
			_ = c.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": nil})

		case m.Method != "" && len(m.ID) == 0:
			if m.Method == "textDocument/publishDiagnostics" {
				c.publish(m.Params)
			}

		case len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue // a response to an id we never issued
			}
			c.mu.Lock()
			ch := c.wait[id]
			c.mu.Unlock()
			if ch != nil {
				ch <- m // buffered, and the waiter is the only receiver
			}
		}
	}
}

func (c *Conn) publish(params json.RawMessage) {
	var p struct {
		URI         string       `json:"uri"`
		Diagnostics []Diagnostic `json:"diagnostics"`
	}
	if json.Unmarshal(params, &p) != nil || p.URI == "" {
		return
	}
	c.dmu.Lock()
	c.diag[p.URI] = p.Diagnostics
	c.dmu.Unlock()
}

func (c *Conn) stopped() error {
	if c.err != nil && !errors.Is(c.err, io.EOF) {
		return fmt.Errorf("lsp: server stopped: %w", c.err)
	}
	return errors.New("lsp: server stopped")
}

// readFrame reads one Content-Length-framed message.
//
// Content-Length is REQUIRED and is the only header that matters; Content-Type is
// accepted and ignored, as the spec allows. A frame is refused rather than
// truncated when it exceeds maxFrame, because a truncated read leaves the stream
// pointing at the middle of a message.
func readFrame(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			continue
		}
		if n, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return nil, fmt.Errorf("lsp: bad Content-Length %q", v)
		}
	}
	if n < 0 {
		return nil, errors.New("lsp: frame without Content-Length")
	}
	if n > maxFrame {
		return nil, fmt.Errorf("lsp: frame of %d bytes exceeds %d", n, maxFrame)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

// env is the environment a language server and its dependency fetch run under.
//
// It is BUILT, never inherited. The cloud process holds gateway credentials, KMS
// addresses and cluster tokens in its own environment, and a language server is a
// third-party binary parsing tenant source — the two must not meet. PATH and HOME
// are what a toolchain needs to find itself and its caches; nothing else is
// passed, and the deployment adds proxy settings through Lang.Env.
func env(l Lang) []string {
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}
	return append(base, l.Env...)
}

// pathURI renders an absolute path as a file: URI. url.URL does the escaping, so
// a path with a space or a percent survives the round trip.
func pathURI(p string) string {
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// uriPath is pathURI's inverse for a file: URI, and the empty string for anything
// else — a server may cite a definition inside a jar: or zipfile: URI, which
// names no path on our disk.
func uriPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" {
		return ""
	}
	return u.Path
}
