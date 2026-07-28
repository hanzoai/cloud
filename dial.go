package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
	zaphttp "github.com/zap-proto/http"
	"github.com/zap-proto/zip"
)

// dial.go — how one app calls another. The ONLY way.
//
// Apps are separate binaries now, so a Go import can no longer reach another
// app's data: `treasury.ReserveCents()` compiles inside apps/admin and returns
// the zero value, because treasury never mounts there. Every cross-app read was
// silently blank, and each one cost admin an entire dependency graph to be
// blank. This replaces all of it — the import, the `mounted` package globals,
// and the Register* seams that only fire when two apps share a binary.
//
// LOCAL IS A SOCKET, REMOTE IS TLS, AND THE CALLER NEVER SAYS WHICH. Dial picks
// by whether the callee's socket exists. A plugin can move hosts and no call
// site changes. That is the whole reason transport is resolved here and not
// spelled out at each use.
//
// WHY A SOCKET WINS LOCALLY. The kernel proves who is calling: credz already
// authenticates its peers with SO_PEERCRED rather than a token, and a 0600
// socket is the entire authorization boundary. Reaching a process on the same
// disk over TLS would mean minting a credential, rotating it, terminating a
// handshake and discovering a port — to cross a boundary the kernel already
// enforces for free. The socket is also not reachable from the network at all,
// so the blast radius is the filesystem.
//
// THE INNER WIRE IS ZAP. A co-located call frames ZAP over the socket
// (zaphttp.Transport, the same transport the gateway and ingress speak), not
// HTTP. ZAP ops are zip handlers either way, so there is still ONE router and
// one set of typed ops — what changes is only the framing underneath them, and
// the caller still never says which.
//
// The remote leg stays HTTPS because it is not an inner call: it crosses the
// public boundary, where TLS is the requirement and zaphttp carries no TLS. The
// split is a real boundary — inside the deployment ZAP over a unix socket,
// outside it TLS — rather than two spellings of the same hop.

// runDirEnv overrides where app sockets live. Default: {DataDir}/run, the same
// data root credz puts its broker socket in.
const runDirEnv = "CLOUD_RUN_DIR"

// remoteEnv overrides the base URL used when an app has no local socket.
// "%s" is replaced by the app name; default is the public API host, so an app
// that has not been co-located still resolves.
const remoteEnv = "CLOUD_PEER_URL"

// runDir is where per-app sockets live.
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

// PeerSocket is the canonical path an app serves on so its peers can reach it,
// and it is the SAME path Dial looks for — one definition, so a server and its
// callers cannot disagree about where an app lives.
//
// A ZAP listener takes a PATH here, not a port: there is nothing to allocate, no
// clash between co-located apps, and nothing bound to a network interface. An app
// binary adds it to the addresses it already serves, alongside its HTTP/WS/SSE
// listener — zip serves every address in parallel over the one route surface, so
// adding the socket takes nothing away from the edge.
//
//	app.Listen(cloud.PeerSocket("tasks"), "http://"+httpAddr)
//
// A bare path is ZAP by zip's own convention; only an "http://" prefix selects
// the HTTP framing. That is why this returns a path and not a URL.
func PeerSocket(app string) string { return sock(app) }

// Peer is another app, reachable. Get/Post carry the caller's principal so the
// callee applies its OWN authorization — a peer call is never implicitly
// privileged.
type Peer struct {
	app   string
	base  string
	local bool
	zap   *zaphttp.Transport // inner: ZAP frames over the callee's unix socket
	c     *http.Client       // outer: TLS to the public edge
	who   map[string]string  // forwarded principal; see As
}

// identity is the set of headers the edge mints from a validated IAM JWT
// (middleware_identity.go). A peer call forwards them UNCHANGED so the callee
// re-applies its own rules to the SAME principal — admin reading the treasury
// as the SuperAdmin who asked, not as "admin the service".
var identity = []string{"X-Org-Id", "X-User-Id", "X-User-Email", "X-User-IsAdmin", "X-Project-Id"}

// As delegates the caller's principal to the peer, taken from the request being
// served. This is DELEGATION, not escalation: the headers were minted at the
// edge from a validated token, the callee still runs its own check, and a
// caller can only ever pass on authority it already holds.
//
// It is sound over the socket because the kernel proves the peer is one of our
// own processes (credz authenticates with SO_PEERCRED, and the socket is 0600),
// so a forwarded claim cannot originate outside the deployment.
//
// It is NOT sufficient over the network: the gateway mints identity from a JWT
// and deliberately ignores inbound identity headers, so a remote peer call
// carrying only these will be treated as anonymous and fail closed. That is the
// safe direction, and it is the remaining gap — a remote peer needs a real
// service credential, not a forwarded header.
func (p *Peer) As(c *zip.Ctx) *Peer {
	if c == nil {
		return p
	}
	who := make(map[string]string, len(identity))
	for _, h := range identity {
		if v := strings.TrimSpace(c.Header(h)); v != "" {
			who[h] = v
		}
	}
	cp := *p
	cp.who = who
	return &cp
}

// Dial resolves how to reach app. A socket on disk means it is co-located, so
// the call never touches the network; otherwise it goes out over TLS.
//
// Resolution happens per Dial rather than once at boot, so an app that starts
// later is picked up without a restart — and a Peer is cheap, so callers may
// hold one or dial per request.
func Dial(app string) *Peer {
	if s := sock(app); sockExists(s) {
		// zaphttp dials lazily, so constructing this opens nothing — a Peer stays
		// cheap enough to build per request, which is what makes late-starting apps
		// resolve without a restart.
		t := zaphttp.Dial("unix", s)
		t.SetDialTimeout(5 * time.Second)
		t.SetReadTimeout(20 * time.Second)
		return &Peer{app: app, base: "http://" + app, local: true, zap: t}
	}
	base := strings.TrimSuffix(peerURL(app), "/")
	return &Peer{app: app, base: base, c: &http.Client{Timeout: 30 * time.Second}}
}

// Local reports whether this peer resolved to a socket. Callers use it for
// diagnostics, never to choose a code path — there is only one.
func (p *Peer) Local() bool { return p.local }

// peerURL is the remote base for an app when it is not co-located.
func peerURL(app string) string {
	if v := strings.TrimSpace(os.Getenv(remoteEnv)); v != "" {
		if strings.Contains(v, "%s") {
			return fmt.Sprintf(v, app)
		}
		return v
	}
	return "https://api.hanzo.ai"
}

func sockExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Get reads path (e.g. "/v1/treasury/reserve") into out. org is the tenant the
// call is made ON BEHALF OF and is forwarded as X-Org-Id, so the callee scopes
// the answer itself rather than trusting the caller to have scoped it.
func (p *Peer) Get(ctx context.Context, org, path string, out any) error {
	return p.do(ctx, http.MethodGet, org, path, nil, out)
}

// Post sends in and decodes the reply into out. out may be nil to discard it.
func (p *Peer) Post(ctx context.Context, org, path string, in, out any) error {
	return p.do(ctx, http.MethodPost, org, path, in, out)
}

func (p *Peer) do(ctx context.Context, method, org, path string, in, out any) error {
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("peer %s: encode: %w", p.app, err)
		}
		body = b
	}

	head := make(map[string]string, len(p.who)+2)
	for h, v := range p.who {
		head[h] = v
	}
	// An explicit org overrides the delegated one: a background job has no
	// request to delegate from and must still name the tenant it acts for.
	if org != "" {
		head["X-Org-Id"] = org
	}
	if in != nil {
		head["Content-Type"] = "application/json"
	}

	status, reply, err := p.send(ctx, method, path, head, body)
	if err != nil {
		return fmt.Errorf("peer %s (%s): %w", p.app, p.where(), err)
	}
	if status < 200 || status > 299 {
		// The status is carried verbatim: a caller distinguishing 402 from 404
		// from 503 is the difference between "unfunded", "no such thing" and
		// "that app is down", and collapsing them would make every board lie the
		// same way the in-process reads did.
		return fmt.Errorf("peer %s: %s %s: status %d", p.app, method, path, status)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(reply, out); err != nil {
		return fmt.Errorf("peer %s: decode: %w", p.app, err)
	}
	return nil
}

// send performs one exchange over whichever wire this peer resolved to. It is
// the ONLY place the two framings differ, and nothing above it knows which ran:
// do builds one request description, send frames it.
func (p *Peer) send(ctx context.Context, method, path string, head map[string]string, body []byte) (int, []byte, error) {
	if p.zap != nil {
		return p.sendZAP(ctx, method, path, head, body)
	}
	return p.sendTLS(ctx, method, path, head, body)
}

// sendZAP frames the call as ZAP over the callee's unix socket.
//
// zaphttp.Do has no context parameter — it bounds itself with the dial and read
// timeouts set in Dial. So a cancelled context is honoured before the exchange
// starts rather than during it; the timeouts, not the caller, bound a call that
// is already in flight. That is acceptable on a socket to a process on the same
// disk, where there is no network to hang on, and it is why the timeouts are set
// tighter here than on the TLS leg.
func (p *Peer) sendZAP(ctx context.Context, method, path string, head map[string]string, body []byte) (int, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	req.SetRequestURI(p.base + path)
	req.Header.SetMethod(method)
	for h, v := range head {
		req.Header.Set(h, v)
	}
	if body != nil {
		req.SetBody(body)
	}
	if err := p.zap.Do(req, resp); err != nil {
		return 0, nil, err
	}
	// The response body is owned by the pooled response, so copy it out before
	// ReleaseResponse hands the buffer back — returning the slice itself would
	// hand the caller memory that is about to be reused under it.
	return resp.StatusCode(), append([]byte(nil), resp.Body()...), nil
}

// sendTLS is the outer leg: ordinary HTTPS to the public edge.
func (p *Peer) sendTLS(ctx context.Context, method, path string, head map[string]string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, fmt.Errorf("request: %w", err)
	}
	for h, v := range head {
		req.Header.Set(h, v)
	}
	resp, err := p.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read: %w", err)
	}
	return resp.StatusCode, reply, nil
}

// where names the transport for an error message, so a failure says whether it
// could not reach a socket or could not reach the internet.
func (p *Peer) where() string {
	if p.local {
		return "zap over uds " + sock(p.app)
	}
	return p.base
}
