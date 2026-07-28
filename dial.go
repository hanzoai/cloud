package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// It is plain HTTP over both, deliberately. ZAP ops are zip handlers, so they
// already speak request/response; swapping only net.Conn keeps ONE protocol,
// one router and one set of typed ops. A second wire format for local calls
// would be a second way to do the same thing.

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

// Peer is another app, reachable. Get/Post carry the caller's principal so the
// callee applies its OWN authorization — a peer call is never implicitly
// privileged.
type Peer struct {
	app   string
	base  string
	local bool
	c     *http.Client
	who   map[string]string // forwarded principal; see As
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
		return &Peer{app: app, base: "http://" + app, local: true, c: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", s)
			}},
		}}
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
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("peer %s: encode: %w", p.app, err)
		}
		body = bytes.NewReader(b)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, body)
	if err != nil {
		return fmt.Errorf("peer %s: request: %w", p.app, err)
	}
	for h, v := range p.who {
		req.Header.Set(h, v)
	}
	// An explicit org overrides the delegated one: a background job has no
	// request to delegate from and must still name the tenant it acts for.
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.c.Do(req)
	if err != nil {
		return fmt.Errorf("peer %s (%s): %w", p.app, p.where(), err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// The status is carried verbatim: a caller distinguishing 402 from 404
		// from 503 is the difference between "unfunded", "no such thing" and
		// "that app is down", and collapsing them would make every board lie the
		// same way the in-process reads did.
		return fmt.Errorf("peer %s: %s %s: status %d", p.app, method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("peer %s: decode: %w", p.app, err)
	}
	return nil
}

// where names the transport for an error message, so a failure says whether it
// could not reach a socket or could not reach the internet.
func (p *Peer) where() string {
	if p.local {
		return "uds " + sock(p.app)
	}
	return p.base
}
