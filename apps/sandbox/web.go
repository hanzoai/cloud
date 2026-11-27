package sandbox

// web.go — a port in a sandbox, in a browser.
//
//	POST /v1/sandbox/:id/preview {port}   a ticket, and the address it opens
//	*    https://<preview host>/...        the port, at an origin of its own
//
// THE ADDRESS IS AN ORIGIN OF ITS OWN (apps/sandbox/preview), so the app behind
// the port owns its whole path space and shares no origin with the API. The
// router rewrites a request for it onto /v1/sandbox/preview/<id>/<port>/..., and
// this refuses any request on that route whose Host is not the preview host it
// names: the route serves a sandbox's bytes under that origin and no other.
//
// THE TICKET OPENS IT ONCE, A COOKIE KEEPS IT OPEN. A browser fetches a page's
// scripts, images and sockets with no header a host page can add, so a preview is
// authorized by a cookie on its own origin. The ticket (terminal.go) is minted by
// the ordinary validated POST and names the door "<id>:<port>", so it opens that
// port and neither another port nor the terminal. `/_hanzo/enter?ticket=` spends
// it and sets a host-only, HttpOnly, partitioned cookie naming a pass this process
// holds for one org, one sandbox and one port, for passTTL. Every later request
// presents the pass, and the sandbox must still be the org's and running. Passes
// are replica-local and in memory, like tickets: a restart forgets them, and the
// preview answers 401 until it is opened again.
//
// THE BYTES CROSS THE ONE CHANNEL. A connection to the port is `socat` on the
// exec subresource, as the screen is, so nothing in the pod listens on the pod
// network. HTTP keeps those connections alive and reuses them; a WebSocket is
// relayed message by message over one.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/wsx"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/preview"
	"github.com/hanzoai/cloud/sdk/principal"
)

const (
	// passTTL is how long an opened preview stays open without being opened again.
	passTTL = 12 * time.Hour
	// passCookie names the pass. `__Host-` makes the browser refuse it unless it
	// is Secure, host-only and at Path=/.
	passCookie = "__Host-hanzo_preview"
	// enterPath is the preview's own door, on its own origin.
	enterPath = "/_hanzo/enter"
	// attendEvery bounds how often a preview's traffic stamps its sandbox as used.
	attendEvery = 30 * time.Second
	// tunnelsPer bounds the connections relayed into one sandbox at once, HTTP
	// and WebSocket together, and streamsPer the HTTP connections into one of
	// its ports; a request past streamsPer waits for one.
	tunnelsPer = 64
	streamsPer = 16
	// answerWithin bounds a wait for the app: a request's dial and headers all
	// together, and each silence of a body it streams.
	answerWithin = 5 * time.Minute
	// handshake bounds a WebSocket's opening, on each side.
	handshake = 30 * time.Second
)

// door is what a preview ticket opens: one port of one sandbox.
func door(id string, port int) string { return id + ":" + strconv.Itoa(port) }

// previewIn names a port to open.
type previewIn struct {
	// ID is the sandbox, from the path.
	ID string `json:"id"`
	// Port is the TCP port inside the sandbox to open, 1 to 65535. Whatever
	// listens on it on localhost is what the address serves.
	Port int `json:"port" url:"-"`
}

// previewGrant is where a port is opened.
type previewGrant struct {
	// URL opens the preview: its own origin, with a single-use ticket that sets
	// the preview's cookie and redirects to its root. Open it in a frame or a tab
	// within ExpiresIn seconds, and mint another to reopen.
	URL string `json:"url"`
	// Host is the preview's origin host, the same for every ticket for this port
	// of this sandbox, before and after the sandbox is parked and resumed.
	Host string `json:"host"`
	// Port is the port the preview serves.
	Port int `json:"port"`
	// ExpiresIn is how long the ticket in URL is good for, in seconds.
	ExpiresIn int `json:"expiresIn"`
}

// OpenPreview opens a port of a sandbox the caller holds in a browser.
//
// It answers a URL at an origin of the preview's own —
// https://sandbox-<id>-preview-<port>.<apex>/ — carrying a single-use ticket.
// Opening it sets a cookie on that origin and lands on its root, and from then on
// every request there is carried to the port inside the sandbox: pages, assets,
// APIs and WebSockets, as the app serves them on localhost. The preview stays open
// for twelve hours or until the sandbox stops running; a preview answering 401 is
// opened again by asking for another URL. A sandbox that is not running is 409.
func (o ops) preview(ctx context.Context, in *previewIn) (*previewGrant, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if in.Port < 1 || in.Port > 65535 {
		return nil, zip.ErrBadRequest("port must be 1 to 65535")
	}
	m, _, err := find(o.s, ctx, org, strings.TrimSpace(in.ID), "preview.ticket")
	if err != nil {
		return nil, err
	}
	if m.Status != statusRunning {
		return nil, zip.Errorf(http.StatusConflict, "sandbox is %s", m.Status)
	}
	tok, err := o.s.State.tickets.mint(time.Now(), org, door(m.ID, in.Port))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "ticket: %v", err)
	}
	host := preview.Host(strings.TrimPrefix(m.ID, IDPrefix), in.Port)
	return &previewGrant{
		URL:  "https://" + host + enterPath + "?ticket=" + tok,
		Host: host, Port: in.Port, ExpiresIn: int(ticketTTL / time.Second),
	}, nil
}

// pass is one opened preview.
type pass struct {
	org, door string
	expires   time.Time
}

// passes are the opened previews, by the cookie that names each.
type passes struct {
	mu   sync.Mutex
	live map[string]pass
}

func newPasses() *passes { return &passes{live: map[string]pass{}} }

// open issues a pass for org's door.
func (p *passes) open(now time.Time, org, door string) (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range p.live {
		if !now.Before(v.expires) {
			delete(p.live, k)
		}
	}
	p.live[tok] = pass{org: org, door: door, expires: now.Add(passTTL)}
	return tok, nil
}

// check answers the org a pass was issued to, when it opens door and is live.
func (p *passes) check(now time.Time, tok, door string) (string, bool) {
	if tok == "" {
		return "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.live[tok]
	if !ok || v.door != door || !now.Before(v.expires) {
		return "", false
	}
	return v.org, true
}

// web serves one request for a preview host.
func web(s *Service, c *zip.Ctx) error {
	hex := c.Param("id")
	port, err := strconv.Atoi(c.Param("port"))
	host := strings.ToLower(sites.Host(c))
	if h, p, ok := preview.Parse(host); err != nil || !ok || h != hex || p != port {
		return zip.ErrNotFound("not found")
	}
	// A preview is drawn in a frame on another origin: Dev's panel. The edge answers
	// every host under the sites apex with X-Frame-Options: SAMEORIGIN, which refuses
	// that frame, and a frame-ancestors policy is what a browser obeys in its place.
	// So every answer here names who may frame it — the surfaces that frame the
	// terminal and the screen (framers) — the door's redirect, the app's own pages
	// beside any policy the app sends, and the notices that tell the frame why.
	c.Fiber().Response().Header.Add("Content-Security-Policy", framers())
	id, d := IDPrefix+hex, door(IDPrefix+hex, port)
	rest := strings.TrimPrefix(string(c.Fiber().Request().URI().RequestURI()), preview.Prefix+hex+"/"+strconv.Itoa(port))
	if rest == "" || rest[0] != '/' {
		rest = "/" + rest
	}
	now := time.Now()

	if p, _, _ := strings.Cut(rest, "?"); p == enterPath {
		org, ok := s.State.tickets.redeem(now, c.Query("ticket"), d)
		if !ok {
			return notice(c, http.StatusUnauthorized, "This preview link has expired. Open the preview from its run again.")
		}
		if _, _, err := up(s, c.Context(), org, id); err != nil {
			return shown(c, err)
		}
		tok, err := s.State.passes.open(now, org, d)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "pass: %v", err)
		}
		c.Fiber().Response().Header.Add("Set-Cookie", passCookie+"="+tok+
			"; Path=/; Max-Age="+strconv.Itoa(int(passTTL/time.Second))+"; Secure; HttpOnly; SameSite=None; Partitioned")
		c.SetHeader("Cache-Control", "no-store")
		c.SetHeader("Referrer-Policy", "no-referrer")
		return c.Redirect(http.StatusSeeOther, "/")
	}

	if !kin(c, hex) {
		return notice(c, http.StatusForbidden, "This preview answers only its own pages.")
	}
	org, ok := s.State.passes.check(now, string(c.Fiber().Request().Header.Cookie(passCookie)), d)
	if !ok {
		return notice(c, http.StatusUnauthorized, "This preview is not open here. Open it from its run.")
	}
	m, store, err := up(s, c.Context(), org, id)
	if err != nil {
		return shown(c, err)
	}
	s.State.web.attend(store, m)
	t := target{m: m, port: port, host: host, rest: rest, until: leaseEnd(m, s.State.clk.absolute)}
	if websocket.FastHTTPIsWebSocketUpgrade(c.Fiber().RequestCtx()) {
		return s.State.web.socket(c, s.State.rt, t, store)
	}
	return s.State.web.page(c, s.State.rt, t)
}

// kin reports whether a request comes from a preview of the same sandbox, or
// is a navigation: a page elsewhere cannot drive a preview, or read its
// scripts, with its viewer's pass, whatever the browser does with the pass's
// partition. A request naming an Origin is judged by it; one naming none by
// Sec-Fetch-Site, which only a navigation may cross.
func kin(c *zip.Ctx, hex string) bool {
	h := &c.Fiber().Request().Header
	if o := string(h.Peek("Origin")); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Scheme != "https" {
			return false
		}
		x, _, ok := preview.Parse(strings.ToLower(u.Host))
		return ok && x == hex
	}
	switch string(h.Peek("Sec-Fetch-Site")) {
	case "same-site", "cross-site":
		return string(h.Peek("Sec-Fetch-Mode")) == "navigate"
	}
	return true
}

// up is the org's sandbox id, when it is running.
func up(s *Service, ctx context.Context, org, id string) (Sandbox, *Store, error) {
	m, store, err := lookup(s, ctx, org, id)
	if err != nil {
		return Sandbox{}, nil, err
	}
	if m.Status != statusRunning {
		return Sandbox{}, nil, zip.Errorf(http.StatusConflict, "This sandbox is %s. Resume it to see its preview.", m.Status)
	}
	return m, store, nil
}

// shown answers err as a page.
func shown(c *zip.Ctx, err error) error {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return notice(c, he.Status, he.Msg)
	}
	return notice(c, http.StatusBadGateway, err.Error())
}

// notice is a preview's own answer, a page, and a message to the frame holding
// it: {source: "hanzo-preview", status}.
func notice(c *zip.Ctx, status int, why string) error {
	c.SetHeader("Content-Type", "text/html; charset=utf-8")
	c.SetHeader("Cache-Control", "no-store")
	return c.String(status, `<!doctype html><meta charset="utf-8"><title>Preview</title>`+
		`<body style="font:14px system-ui;margin:2rem;color:#888">`+html.EscapeString(why)+
		`<script>parent.postMessage({source:"hanzo-preview",status:`+strconv.Itoa(status)+`},"*")</script>`)
}

// target is one request's destination.
type target struct {
	m     Sandbox
	port  int
	host  string // the preview host
	rest  string // the path and query asked for
	until time.Time
}

// origin is how the app inside knows itself.
func (t target) origin() string { return "localhost:" + strconv.Itoa(t.port) }

// relay carries previews into sandboxes: one HTTP client whose connections are
// exec streams, when each sandbox was last stamped as attended, and how many
// tunnels each has open.
type relay struct {
	client  *http.Client
	mu      sync.Mutex
	seen    map[string]time.Time
	tunnels map[string]int
}

// errFull is a sandbox holding tunnelsPer tunnels already.
var errFull = errors.New("sandbox: every tunnel is in use")

type dialKey struct{}

func newRelay(rt *runtime) *relay {
	r := &relay{seen: map[string]time.Time{}, tunnels: map[string]int{}}
	r.client = &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				t, _ := ctx.Value(dialKey{}).(target)
				return r.dial(rt, t)
			},
			MaxIdleConnsPerHost: 8,
			MaxConnsPerHost:     streamsPer,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return r
}

// dial opens a tunnel to t's port, one of the tunnelsPer its sandbox may hold.
func (r *relay) dial(rt *runtime, t target) (net.Conn, error) {
	if !r.take(t.m.ID) {
		return nil, errFull
	}
	return rt.join(t.m, "TCP:localhost:"+strconv.Itoa(t.port), t.until, func() { r.give(t.m.ID) })
}

// attend stamps a sandbox as used, at most once each attendEvery.
func (r *relay) attend(store *Store, m Sandbox) {
	now := time.Now()
	r.mu.Lock()
	due := now.Sub(r.seen[m.ID]) >= attendEvery
	if due {
		r.seen[m.ID] = now
	}
	for id, at := range r.seen {
		if now.Sub(at) > time.Hour {
			delete(r.seen, id)
		}
	}
	r.mu.Unlock()
	if due {
		touched(context.Background(), store, m)
	}
}

// hop are headers that describe one connection, never the request.
var hop = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-connection": true, "proxy-authenticate": true,
	"proxy-authorization": true, "te": true, "trailer": true, "transfer-encoding": true, "upgrade": true,
}

// platform are headers the platform speaks in, which an app inside is not handed.
func platform(k string) bool {
	k = strings.ToLower(k)
	return hop[k] || k == "host" || k == "content-length" || k == "x-real-ip" || k == "x-client-ip" ||
		strings.HasPrefix(k, "cf-") || strings.HasPrefix(k, "x-forwarded-") ||
		strings.HasPrefix(k, "x-user-") || strings.HasPrefix(k, "x-org-") || strings.HasPrefix(k, "x-hanzo-") ||
		strings.HasPrefix(k, "sec-websocket-")
}

// headers are the request's own headers, as the app inside is handed them: the
// pass taken out of its cookies, and the app named as the host it listens as.
func headers(c *zip.Ctx, t target) http.Header {
	h := http.Header{}
	for k, v := range c.Fiber().Request().Header.All() {
		name := string(k)
		if platform(name) {
			continue
		}
		val := string(v)
		if strings.EqualFold(name, "cookie") {
			if val = unpassed(val); val == "" {
				continue
			}
		}
		h.Add(name, val)
	}
	h.Set("X-Forwarded-Host", t.host)
	h.Set("X-Forwarded-Proto", "https")
	return h
}

// unpassed is a Cookie header without the preview's pass.
func unpassed(cookie string) string {
	var keep []string
	for part := range strings.SplitSeq(cookie, ";") {
		if name, _, _ := strings.Cut(strings.TrimSpace(part), "="); name != passCookie && strings.TrimSpace(part) != "" {
			keep = append(keep, strings.TrimSpace(part))
		}
	}
	return strings.Join(keep, "; ")
}

// page carries one HTTP request to the port and its response back, streamed.
func (r *relay) page(c *zip.Ctx, rt *runtime, t target) error {
	u, err := url.Parse("http://" + t.m.Pod + ":" + strconv.Itoa(t.port) + t.rest)
	if err != nil {
		return notice(c, http.StatusBadRequest, "That address is not one this preview can serve.")
	}
	ctx, cancel := context.WithDeadline(context.WithValue(context.Background(), dialKey{}, t), t.until)
	body := c.Body()
	req, err := http.NewRequestWithContext(ctx, c.Method(), u.String(), bytes.NewReader(body))
	if err != nil {
		cancel()
		return notice(c, http.StatusBadRequest, "That request is not one this preview can serve.")
	}
	req.ContentLength = int64(len(body))
	req.Header = headers(c, t)
	req.Host = t.origin()
	late := time.AfterFunc(answerWithin, cancel)
	resp, err := r.client.Do(req)
	if !late.Stop() && err == nil {
		_ = resp.Body.Close()
		err = context.DeadlineExceeded
	}
	if errors.Is(err, errFull) {
		cancel()
		return notice(c, http.StatusTooManyRequests, full)
	}
	if err != nil {
		cancel()
		return notice(c, http.StatusBadGateway, fmt.Sprintf("Nothing answered on port %d in this sandbox yet.", t.port))
	}
	out := &c.Fiber().Response().Header
	for k, vs := range resp.Header {
		if hop[strings.ToLower(k)] || strings.EqualFold(k, "content-length") {
			continue
		}
		for _, v := range vs {
			if strings.EqualFold(k, "location") {
				v = located(v, t)
			}
			if strings.EqualFold(k, "set-cookie") && strings.HasPrefix(strings.TrimSpace(v), passCookie+"=") {
				continue
			}
			out.Add(k, v)
		}
	}
	c.Status(resp.StatusCode)
	if c.Method() == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified {
		_ = resp.Body.Close()
		cancel()
		return nil
	}
	size := -1
	if resp.ContentLength >= 0 {
		size = int(resp.ContentLength)
	}
	c.Fiber().Response().SetBodyStream(streamed(resp.Body, cancel, answerWithin), size)
	return nil
}

// located is a redirect the app answered, with its own localhost address put
// back as the preview's origin.
func located(v string, t target) string {
	for _, own := range []string{"http://" + t.origin(), "http://127.0.0.1:" + strconv.Itoa(t.port)} {
		if rest, ok := strings.CutPrefix(v, own); ok && (rest == "" || rest[0] == '/' || rest[0] == '?') {
			return "https://" + t.host + rest
		}
	}
	return v
}

// finish ends a response's request when its body is done with, or has said
// nothing for wait.
type finish struct {
	io.ReadCloser
	cancel context.CancelFunc
	quiet  *time.Timer
	wait   time.Duration
}

func streamed(body io.ReadCloser, cancel context.CancelFunc, wait time.Duration) *finish {
	return &finish{ReadCloser: body, cancel: cancel, quiet: time.AfterFunc(wait, cancel), wait: wait}
}

func (c *finish) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	if n > 0 {
		c.quiet.Reset(c.wait)
	}
	return n, err
}

func (c *finish) Close() error {
	c.quiet.Stop()
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// full is a preview's answer when its sandbox holds every tunnel it may.
var full = fmt.Sprintf("This sandbox has %d connections open to its previews; close some first.", tunnelsPer)

// socket relays one WebSocket to the port, message by message: the app's
// subprotocol is the one the browser is answered with.
func (r *relay) socket(c *zip.Ctx, rt *runtime, t target, store *Store) error {
	var protos []string
	for p := range strings.SplitSeq(string(c.Fiber().Request().Header.Peek("Sec-WebSocket-Protocol")), ",") {
		if p = strings.TrimSpace(p); p != "" {
			protos = append(protos, p)
		}
	}
	h := headers(c, t)
	h.Set("Origin", "http://"+t.origin())
	d := websocket.Dialer{
		NetDialContext:   func(context.Context, string, string) (net.Conn, error) { return r.dial(rt, t) },
		Subprotocols:     protos,
		HandshakeTimeout: handshake,
	}
	ctx, cancel := context.WithDeadline(context.Background(), t.until)
	up, _, err := d.DialContext(ctx, "ws://"+t.origin()+t.rest, h)
	if errors.Is(err, errFull) {
		cancel()
		return notice(c, http.StatusTooManyRequests, full)
	}
	if err != nil {
		cancel()
		return notice(c, http.StatusBadGateway, fmt.Sprintf("Nothing took a WebSocket on port %d in this sandbox.", t.port))
	}
	var cfg wsx.Config
	if p := up.Subprotocol(); p != "" {
		cfg.Subprotocols = []string{p}
	}
	// The browser's side is taken over after this handler returns, and not at
	// all when its 101 cannot be written; a socket whose relay has not begun
	// within handshake is closed and its slot given back.
	var began atomic.Bool
	orphan := time.AfterFunc(handshake, func() {
		if began.CompareAndSwap(false, true) {
			_ = up.Close()
			cancel()
		}
	})
	serve := wsx.Upgrade(func(down *wsx.Conn) error {
		if !began.CompareAndSwap(false, true) {
			return nil
		}
		orphan.Stop()
		defer cancel()
		pipeWS(ctx, down, up, func() { r.attend(store, t.m) })
		return nil
	}, cfg)
	if err := serve(c); err != nil {
		orphan.Stop()
		_ = up.Close()
		cancel()
		return err
	}
	return nil
}

// take claims one of a sandbox's tunnels, give returns it.
func (r *relay) take(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tunnels[id] >= tunnelsPer {
		return false
	}
	r.tunnels[id]++
	return true
}

func (r *relay) give(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tunnels[id]--; r.tunnels[id] <= 0 {
		delete(r.tunnels, id)
	}
}

// pipeWS copies messages both ways until either side ends or ctx does, and
// returns only once neither copy is touching either socket. A message is
// streamed frame by frame, never held whole.
//
// Each socket has one writer: the copy reading the other. The browser's is a
// hijacked connection whose Close does nothing until the handler returns — and
// fasthttp hands it back to a pool the moment it does — so its reader is ended by
// a deadline, and both copies are waited for before returning.
func pipeWS(ctx context.Context, down, up *websocket.Conn, attend func()) {
	stop := make(chan struct{})
	var once sync.Once
	var wg sync.WaitGroup
	copyWS := func(from, to *websocket.Conn) {
		defer wg.Done()
		defer once.Do(func() { close(stop) })
		for {
			typ, msg, err := from.NextReader()
			if err != nil {
				code := websocket.CloseNormalClosure
				var ce *websocket.CloseError
				if errors.As(err, &ce) && ce.Code != websocket.CloseNoStatusReceived {
					code = ce.Code
				}
				_ = to.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, ""), time.Now().Add(time.Second))
				return
			}
			attend()
			w, err := to.NextWriter(typ)
			if err != nil {
				return
			}
			if _, err := io.Copy(w, msg); err != nil {
				_ = w.Close()
				return
			}
			if err := w.Close(); err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go copyWS(down, up)
	go copyWS(up, down)
	select {
	case <-stop:
	case <-ctx.Done():
	}
	_ = up.Close()
	_ = down.SetReadDeadline(time.Now())
	wg.Wait()
}

// join is one connection to `to` inside the sandbox — a socat address, TCP on
// `localhost` for a port or UNIX-CONNECT for a socket — carried over the exec
// channel by socat: `localhost` so an app listening on either loopback answers.
// It lasts until either end closes it or until the sandbox's lease end, and
// done is called once when it ends, however it ends.
func (r *runtime) join(m Sandbox, to string, until time.Time, done func()) (net.Conn, error) {
	ctx, cancel := context.WithDeadline(context.Background(), until)
	rt, err := r.on(ctx, m)
	if err != nil {
		cancel()
		done()
		return nil, err
	}
	if m.Status != statusRunning || m.Pod == "" {
		cancel()
		done()
		return nil, fmt.Errorf("sandbox is %s", m.Status)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t := &tunnel{in: inW, out: outR, cancel: cancel, done: done}
	go func() {
		var why capped
		err := rt.str.stream(ctx, rt.ns, m.Pod, []string{"socat", "-", to}, inR, outW, &why)
		if said := strings.TrimSpace(why.String()); err != nil && said != "" {
			err = errors.New(said)
		}
		if err == nil {
			err = io.EOF
		}
		_ = outW.CloseWithError(err)
		_ = inR.CloseWithError(err)
		t.end()
	}()
	return t, nil
}

// tunnel is a connection whose bytes are one exec stream's stdin and stdout.
type tunnel struct {
	in             *io.PipeWriter
	out            *io.PipeReader
	cancel         context.CancelFunc
	done           func()
	closing, ended sync.Once
}

// end lets the stream and its slot go, once: at Close, or when the stream ends.
func (t *tunnel) end() {
	t.ended.Do(func() {
		t.cancel()
		t.done()
	})
}

func (t *tunnel) Read(p []byte) (int, error)  { return t.out.Read(p) }
func (t *tunnel) Write(p []byte) (int, error) { return t.in.Write(p) }
func (t *tunnel) Close() error {
	t.closing.Do(func() {
		_ = t.in.Close()
		_ = t.out.Close()
	})
	t.end()
	return nil
}
func (t *tunnel) LocalAddr() net.Addr              { return tunnelAddr{} }
func (t *tunnel) RemoteAddr() net.Addr             { return tunnelAddr{} }
func (t *tunnel) SetDeadline(time.Time) error      { return nil }
func (t *tunnel) SetReadDeadline(time.Time) error  { return nil }
func (t *tunnel) SetWriteDeadline(time.Time) error { return nil }

type tunnelAddr struct{}

func (tunnelAddr) Network() string { return "exec" }
func (tunnelAddr) String() string  { return "sandbox" }

// screenPorts are the desktop's own loopback servers — VNC and noVNC — which the
// screen already shows and a preview does not list.
var screenPorts = map[int]bool{rfb: true, 6080: true}

// Ports answers the TCP ports something listens on in a sandbox the caller holds,
// read from the kernel's own socket tables, ascending.
func Ports(s *Service, ctx context.Context, org, id string) ([]client.Port, error) {
	m, store, err := find(s, ctx, org, strings.TrimSpace(id), "ports")
	if err != nil {
		return nil, err
	}
	if m.Status != statusRunning {
		return nil, zip.Errorf(http.StatusConflict, "sandbox is %s", m.Status)
	}
	r, err := s.State.rt.exec(ctx, m, []string{"sh", "-c", "cat /proc/net/tcp /proc/net/tcp6 2>/dev/null"}, nil, 10, nil)
	if err != nil {
		return nil, fault(s, ctx, store, m, "ports", err)
	}
	hex := strings.TrimPrefix(m.ID, IDPrefix)
	out := []client.Port{}
	for _, p := range listening(r.Stdout) {
		out = append(out, client.Port{Port: p, Host: preview.Host(hex, p)})
	}
	return out, nil
}

// listening reads /proc/net/tcp{,6}: the local port of every socket in state 0A,
// LISTEN, once each and ascending, leaving out the screen's own.
func listening(table string) []int {
	seen := map[int]bool{}
	for l := range strings.SplitSeq(table, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 || f[3] != "0A" {
			continue
		}
		_, hexPort, ok := strings.Cut(f[1], ":")
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if ok && err == nil && p > 0 && !screenPorts[int(p)] {
			seen[int(p)] = true
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// portsIn names the sandbox whose ports are listed.
type portsIn struct {
	// ID is the sandbox, from the path.
	ID string `json:"id"`
}

// ListPorts lists the TCP ports something listens on in a sandbox the caller
// holds, each with the preview host that serves it — what the sandbox's Browser
// can open. A dev server started in the sandbox appears here once it listens.
func (o ops) ports(ctx context.Context, in *portsIn) (*client.Ports, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := Ports(o.s, ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	return &client.Ports{Ports: ps}, nil
}

// planePorts is ListPorts for a peer.
func planePorts(ctx context.Context, in *client.PortsIn) (*client.Ports, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	ps, err := Ports(s, ctx, org, in.ID)
	if err != nil {
		return nil, err
	}
	return &client.Ports{Ports: ps}, nil
}
