package sandbox

// web_test.go — a port in a sandbox opens at an origin of its own, for the
// sandbox's holder, and for nobody else: not another member, not another org, not
// another port, not under any other host, and not once the sandbox stops running.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/preview"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	"k8s.io/client-go/tools/remotecommand"
)

// socat is an exec channel that answers `socat - TCP:localhost:<port>` by joining
// the stream to that port on this machine, and hands every other command on.
type socat struct {
	streamer
	mu    sync.Mutex
	dials int
}

func (s *socat) stream(ctx context.Context, ns, pod string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(argv) != 3 || argv[0] != "socat" {
		return s.streamer.stream(ctx, ns, pod, argv, stdin, stdout, stderr)
	}
	s.mu.Lock()
	s.dials++
	s.mu.Unlock()
	network, addr := "tcp", "127.0.0.1:"+strings.TrimPrefix(argv[2], "TCP:localhost:")
	if path, ok := strings.CutPrefix(argv[2], "UNIX-CONNECT:"); ok {
		network, addr = "unix", path
	}
	conn, err := net.Dial(network, addr)
	if err != nil {
		_, _ = io.WriteString(stderr, "Connection refused")
		return fmt.Errorf("exit 1")
	}
	defer func() { _ = conn.Close() }()
	go func() { _, _ = io.Copy(conn, stdin); _ = conn.(interface{ CloseWrite() error }).CloseWrite() }()
	_, err = io.Copy(stdout, conn)
	return err
}

func (s *socat) tty(context.Context, string, string, []string, io.Reader, io.Writer, remotecommand.TerminalSizeQueue) error {
	return nil
}

// app is what listens inside the sandbox: it says what it was asked, and as whom.
func app(t *testing.T) (int, *http.Header) {
	t.Helper()
	var seen http.Header
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Clone()
		seen.Set("Host", r.Host)
		mu.Unlock()
		if r.URL.Path == "/away" {
			http.Redirect(w, r, "http://"+r.Host+"/next?q=1", http.StatusFound)
			return
		}
		if r.URL.Path == "/policy" {
			w.Header().Set("Content-Security-Policy", appPolicy)
		}
		w.Header().Set("X-App", "yes")
		_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.RequestURI())
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return port, &seen
}

// previewing is the sandbox surface behind the router's rewrite, over a cluster
// whose pods come up at once and whose exec channel reaches this machine's ports.
func previewing(t *testing.T) (*zip.App, *Service) {
	t.Helper()
	_, s := owning(t)
	s.State.rt.str = &socat{streamer: s.State.rt.str}
	s.State.web = newRelay(s.State.rt)
	a := zip.New(zip.Config{Logger: luxlog.New("web"), DisableStartupMessage: true})
	// Twice, as production composes it: the router's, then the sandbox process's own.
	a.Use(preview.Middleware())
	a.Use(preview.Middleware())
	a.Use(cloud.Bridge())
	Routes(a, s)
	return a, s
}

// visit is one browser request to a preview host.
func visit(t *testing.T, a *zip.App, host, path, cookie string) *http.Response {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	resp, err := a.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("GET %s%s: %v", host, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func text(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

// opened mints a preview of port as m and answers the grant.
func opened(t *testing.T, a *zip.App, m member, id string, port int) (int, previewGrant) {
	t.Helper()
	code, body := as(t, a, m, http.MethodPost, "/v1/sandbox/"+id+"/preview", `{"port":`+strconv.Itoa(port)+`}`)
	var g previewGrant
	if code == http.StatusCreated {
		if err := json.Unmarshal(body, &g); err != nil {
			t.Fatalf("grant %s: %v", body, err)
		}
	}
	return code, g
}

// enter spends a grant's ticket and answers the pass cookie it set.
func enter(t *testing.T, a *zip.App, g previewGrant) string {
	t.Helper()
	u, _ := url.Parse(g.URL)
	r := visit(t, a, u.Host, u.RequestURI(), "")
	if r.StatusCode != http.StatusSeeOther || r.Header.Get("Location") != "/" {
		t.Fatalf("enter = %d Location %q: %s", r.StatusCode, r.Header.Get("Location"), text(t, r))
	}
	if got := r.Header.Get("Content-Security-Policy"); got != framers() {
		t.Fatalf("the door answers policy %q, want %q: it is opened in Dev's frame", got, framers())
	}
	set := r.Header.Get("Set-Cookie")
	for _, attr := range []string{passCookie + "=", "Path=/", "Secure", "HttpOnly", "SameSite=None", "Partitioned"} {
		if !strings.Contains(set, attr) {
			t.Fatalf("the pass cookie lacks %q: %s", attr, set)
		}
	}
	if strings.Contains(strings.ToLower(set), "domain=") {
		t.Fatalf("the pass cookie names a domain, so it reaches other hosts: %s", set)
	}
	pass, _, _ := strings.Cut(set, ";")
	return pass
}

func TestAPreviewServesThePortToItsHolder(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, _ := previewing(t)
		port, seen := app(t)
		m := leaseAs(t, a, alice)

		code, g := opened(t, a, alice, m.ID, port)
		if code != http.StatusCreated {
			t.Fatalf("open = %d", code)
		}
		host := preview.Host(strings.TrimPrefix(m.ID, IDPrefix), port)
		if g.Host != host || !strings.HasPrefix(g.URL, "https://"+host+enterPath+"?ticket=") {
			t.Fatalf("grant %+v, want host %s", g, host)
		}
		pass := enter(t, a, g)

		r := visit(t, a, host, "/a/b?c=d", pass+"; theirs=kept")
		if body := text(t, r); r.StatusCode != http.StatusOK || body != "GET /a/b?c=d" || r.Header.Get("X-App") != "yes" {
			t.Fatalf("preview = %d %q", r.StatusCode, body)
		}
		if got := seen.Get("Host"); got != "localhost:"+strconv.Itoa(port) {
			t.Errorf("the app was asked as %q, not as the localhost it listens on", got)
		}
		if got := seen.Get("X-Forwarded-Host"); got != host {
			t.Errorf("X-Forwarded-Host = %q, want %q", got, host)
		}
		if got := seen.Get("Cookie"); strings.Contains(got, passCookie) || got != "theirs=kept" {
			t.Errorf("the app was handed cookies %q: the pass must stay ours, its own must reach it", got)
		}

		r = visit(t, a, host, "/away", pass)
		if loc := r.Header.Get("Location"); r.StatusCode != http.StatusFound || loc != "https://"+host+"/next?q=1" {
			t.Errorf("a redirect to the app's own localhost = %d %q, want the preview's origin", r.StatusCode, loc)
		}
	})
}

func TestAPreviewOpensForNobodyElse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, s := previewing(t)
		port, _ := app(t)
		m := leaseAs(t, a, alice)

		if code, _ := opened(t, a, bob, m.ID, port); code != http.StatusNotFound {
			t.Errorf("another member opened alice's preview: %d", code)
		}
		if code, _ := opened(t, a, alice, m.ID, 0); code != http.StatusBadRequest {
			t.Errorf("port 0 = %d", code)
		}

		_, g := opened(t, a, alice, m.ID, port)
		host := g.Host
		u, _ := url.Parse(g.URL)
		pass := enter(t, a, g)
		if r := visit(t, a, host, u.RequestURI(), ""); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("a spent ticket opened the preview again: %d", r.StatusCode)
		}
		if r := visit(t, a, host, "/", ""); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("no pass = %d", r.StatusCode)
		}
		if r := visit(t, a, host, "/", passCookie+"=forged"); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("a forged pass = %d", r.StatusCode)
		}

		other := preview.Host(strings.TrimPrefix(m.ID, IDPrefix), port+1)
		if r := visit(t, a, other, "/", pass); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("a pass for port %d opened port %d: %d", port, port+1, r.StatusCode)
		}

		// The route itself, under any host but the preview's, serves nothing: its
		// bytes are the sandbox's, and no other origin may carry them.
		hex := strings.TrimPrefix(m.ID, IDPrefix)
		for _, h := range []string{"api.hanzo.ai", "hanzo.ai", preview.Host(hex, port) + ".evil.com"} {
			r := visit(t, a, h, preview.Prefix+hex+"/"+strconv.Itoa(port)+"/", pass)
			if r.StatusCode != http.StatusNotFound {
				t.Errorf("the preview route answered under %s: %d", h, r.StatusCode)
			}
		}

		// Another org's pass names another org's store, where this sandbox is not.
		st, _ := storeFor(s, "acme")
		row, _ := st.Get(context.Background(), "acme", m.ID)
		forged, _ := s.State.passes.open(time.Now(), "globex", door(row.ID, port))
		if r := visit(t, a, host, "/", passCookie+"="+forged); r.StatusCode == http.StatusOK {
			t.Errorf("another org's pass opened acme's sandbox: %d", r.StatusCode)
		}
	})
}

func TestAPreviewClosesWhenItsSandboxIsParked(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, s := previewing(t)
		port, _ := app(t)
		m := leaseAs(t, a, alice)
		_, g := opened(t, a, alice, m.ID, port)
		pass := enter(t, a, g)
		if _, err := Park(s, cloud.For(context.Background(), "acme"), "acme", m.ID); err == nil {
			t.Fatal("park by a caller who holds nothing succeeded")
		}
		st, _ := storeFor(s, "acme")
		row, _ := st.Get(context.Background(), "acme", m.ID)
		if _, err := park(s, context.Background(), st, row, ""); err != nil {
			t.Fatalf("park: %v", err)
		}
		r := visit(t, a, g.Host, "/", pass)
		if r.StatusCode != http.StatusConflict || !strings.Contains(text(t, r), "parked") {
			t.Fatalf("a parked sandbox's preview = %d", r.StatusCode)
		}
		if code, _ := opened(t, a, alice, m.ID, port); code != http.StatusConflict {
			t.Errorf("a parked sandbox minted a preview: %d", code)
		}
	})
}

// appPolicy is a policy the app inside sends of its own.
const appPolicy = "default-src 'self'"

// A preview is drawn in Dev's panel, a frame on another origin, and the edge
// answers every host under the sites apex with X-Frame-Options: SAMEORIGIN. A
// frame-ancestors policy is what a browser obeys in its place, so every answer a
// preview host gives names who may frame it, as the terminal does: the app's own
// pages, with the app's own policy still beside it, and the notices that tell
// the frame why there is nothing to show.
func TestAPreviewMayBeFramedWhereTheTerminalIs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, _ := previewing(t)
		port, _ := app(t)
		m := leaseAs(t, a, alice)
		_, g := opened(t, a, alice, m.ID, port)
		pass := enter(t, a, g)

		r := visit(t, a, g.Host, "/policy", pass)
		got := r.Header.Values("Content-Security-Policy")
		if r.StatusCode != http.StatusOK || !slices.Contains(got, framers()) {
			t.Fatalf("an app's page = %d with policies %q, want %q among them", r.StatusCode, got, framers())
		}
		if !slices.Contains(got, appPolicy) {
			t.Errorf("the app's own policy was dropped: %q", got)
		}

		r = visit(t, a, g.Host, "/", "")
		if got := r.Header.Get("Content-Security-Policy"); r.StatusCode != http.StatusUnauthorized || got != framers() {
			t.Errorf("a notice = %d with policy %q, want 401 with %q: its frame reads why from it", r.StatusCode, got, framers())
		}
	})
}

func TestAPreviewSaysWhenNothingListens(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, _ := previewing(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		m := leaseAs(t, a, alice)
		_, g := opened(t, a, alice, m.ID, port)
		pass := enter(t, a, g)
		r := visit(t, a, g.Host, "/", pass)
		if r.StatusCode != http.StatusBadGateway || !strings.Contains(text(t, r), "Nothing answered on port") {
			t.Fatalf("an empty port = %d", r.StatusCode)
		}
	})
}

// A preview host is longer than a project slug may be (40), so no published site
// can claim one, and it reads back as the sandbox and port it names.
func TestAPreviewHostIsNeverASite(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		hex := strings.Repeat("a", 24)
		for _, port := range []int{1, 80, 3000, 65535} {
			h := preview.Host(hex, port)
			label, _, _ := strings.Cut(h, ".")
			if len(label) <= 40 {
				t.Errorf("preview label %q is %d long, which a project slug can be", label, len(label))
			}
			if gh, gp, ok := preview.Parse(h); !ok || gh != hex || gp != port {
				t.Errorf("Parse(%q) = %q %d %v", h, gh, gp, ok)
			}
		}
		for _, h := range []string{
			"sandbox-" + hex + "-preview-0." + preview.Apex(),
			"sandbox-" + hex + "-preview-65536." + preview.Apex(),
			"sandbox-" + hex + "-preview-3000.evil.com",
			"sandbox-" + hex[:23] + "-preview-3000." + preview.Apex(),
			"x.sandbox-" + hex + "-preview-3000." + preview.Apex(),
		} {
			if _, _, ok := preview.Parse(h); ok {
				t.Errorf("Parse(%q) read a preview host", h)
			}
		}
	})
}

// A preview answers its own pages, a navigation into it, and a request from a
// preview of the same sandbox; a page elsewhere can neither drive it nor load
// its scripts with the viewer's pass.
func TestAPreviewTakesNoOrdersFromElsewhere(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, _ := previewing(t)
		port, _ := app(t)
		m := leaseAs(t, a, alice)
		_, g := opened(t, a, alice, m.ID, port)
		pass := enter(t, a, g)
		hex := strings.TrimPrefix(m.ID, IDPrefix)
		other := preview.Host(strings.Repeat("0", 24), port)
		for _, tc := range []struct {
			method, origin, site, mode string
			want                       int
		}{
			{"POST", "", "", "", http.StatusOK},
			{"POST", "https://" + g.Host, "same-origin", "cors", http.StatusOK},
			{"POST", "https://" + preview.Host(hex, 5173), "same-site", "cors", http.StatusOK},
			{"GET", "", "same-origin", "no-cors", http.StatusOK},
			{"GET", "", "cross-site", "navigate", http.StatusOK},
			{"GET", "", "none", "navigate", http.StatusOK},
			{"POST", "https://evil.example", "cross-site", "cors", http.StatusForbidden},
			{"POST", "null", "cross-site", "navigate", http.StatusForbidden},
			{"POST", "http://" + g.Host, "", "", http.StatusForbidden},
			{"POST", "https://" + other, "same-site", "cors", http.StatusForbidden},
			{"GET", "", "same-site", "no-cors", http.StatusForbidden},
			{"GET", "", "cross-site", "no-cors", http.StatusForbidden},
		} {
			r := httptest.NewRequest(tc.method, "https://"+g.Host+"/form", strings.NewReader("a=1"))
			r.Header.Set("Cookie", pass)
			for k, v := range map[string]string{"Origin": tc.origin, "Sec-Fetch-Site": tc.site, "Sec-Fetch-Mode": tc.mode} {
				if v != "" {
					r.Header.Set(k, v)
				}
			}
			resp, err := a.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s from origin %q, site %q, mode %q = %d, want %d", tc.method, tc.origin, tc.site, tc.mode, resp.StatusCode, tc.want)
			}
		}
	})
}

// A WebSocket on a preview reaches the app's own socket and speaks its
// subprotocol, message for message: a dev server's HMR channel is exactly this.
// A message larger than any buffer streams through, a socket from another
// origin is refused, and a sandbox holds at most tunnelsPer tunnels at once.
func TestAPreviewRelaysAWebSocket(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a, s := previewing(t)
		var origin string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin = r.Header.Get("Origin")
			c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"vite-hmr"}})
			if err != nil {
				return
			}
			c.SetReadLimit(-1)
			defer func() { _ = c.CloseNow() }()
			for {
				typ, msg, err := c.Read(r.Context())
				if err != nil {
					return
				}
				if err := c.Write(r.Context(), typ, append([]byte("echo "), msg...)); err != nil {
					return
				}
			}
		}))
		t.Cleanup(srv.Close)
		u, _ := url.Parse(srv.URL)
		port, _ := strconv.Atoi(u.Port())

		m := leaseAs(t, a, alice)
		_, g := opened(t, a, alice, m.ID, port)
		pass := enter(t, a, g)

		f := a.Fiber()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = f.Listener(ln) }()
		t.Cleanup(func() { _ = f.ShutdownWithTimeout(2 * time.Second) })

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c, resp, err := websocket.Dial(ctx, "ws://"+ln.Addr().String()+"/hmr?token=x", &websocket.DialOptions{
			Host: g.Host, Subprotocols: []string{"vite-hmr"}, HTTPHeader: http.Header{"Cookie": {pass}},
		})
		if err != nil {
			t.Fatalf("dial: %v (%v)", err, resp)
		}
		defer func() { _ = c.CloseNow() }()
		if c.Subprotocol() != "vite-hmr" {
			t.Errorf("subprotocol %q, want the app's", c.Subprotocol())
		}
		if err := c.Write(ctx, websocket.MessageText, []byte("ping")); err != nil {
			t.Fatal(err)
		}
		_, msg, err := c.Read(ctx)
		if err != nil || string(msg) != "echo ping" {
			t.Fatalf("read %q %v", msg, err)
		}
		if origin != "http://localhost:"+strconv.Itoa(port) {
			t.Errorf("the app's socket was opened from %q, not its own origin", origin)
		}

		c.SetReadLimit(-1)
		big := []byte(strings.Repeat("0123456789abcdef", 1<<18)) // 4 MiB
		if err := c.Write(ctx, websocket.MessageBinary, big); err != nil {
			t.Fatal(err)
		}
		if _, msg, err := c.Read(ctx); err != nil || len(msg) != len(big)+5 || string(msg[5:]) != string(big) {
			t.Fatalf("a 4 MiB message came back as %d bytes, %v", len(msg), err)
		}

		dial := func(h http.Header) int {
			h.Set("Cookie", pass)
			c, resp, err := websocket.Dial(ctx, "ws://"+ln.Addr().String()+"/hmr", &websocket.DialOptions{Host: g.Host, HTTPHeader: h})
			if err == nil {
				_ = c.CloseNow()
				return http.StatusSwitchingProtocols
			}
			if resp == nil {
				t.Fatalf("dial: %v", err)
			}
			return resp.StatusCode
		}
		if code := dial(http.Header{"Origin": {"https://evil.example"}}); code != http.StatusForbidden {
			t.Errorf("a socket from another site = %d, want 403", code)
		}
		if code := dial(http.Header{"Origin": {"https://" + g.Host}}); code != http.StatusSwitchingProtocols {
			t.Errorf("a socket from the preview's own page = %d, want 101", code)
		}
		open := func(n int) bool {
			s.State.web.mu.Lock()
			defer s.State.web.mu.Unlock()
			if s.State.web.tunnels[m.ID] != 1 {
				return false
			}
			s.State.web.tunnels[m.ID] = n
			return true
		}
		for end := time.Now().Add(5 * time.Second); !open(tunnelsPer); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("the closed sockets were never given back")
			}
		}
		if code := dial(http.Header{}); code != http.StatusTooManyRequests {
			t.Errorf("a socket past tunnelsPer = %d, want 429", code)
		}
		if r := visit(t, a, g.Host, "/", pass); r.StatusCode != http.StatusTooManyRequests {
			t.Errorf("a page past tunnelsPer = %d, want 429", r.StatusCode)
		}
	})
}

// A relayed body that says nothing for its wait is ended; one that keeps
// talking is not: five bytes spaced a quarter wait apart all arrive, then the
// silence ends it.
func TestAQuietBodyIsEnded(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		pr, pw := io.Pipe()
		wait := 400 * time.Millisecond
		f := streamed(pr, func() { _ = pw.CloseWithError(context.Canceled) }, wait)
		defer func() { _ = f.Close() }()
		go func() {
			for range 5 {
				time.Sleep(wait / 4)
				if _, err := pw.Write([]byte("x")); err != nil {
					return
				}
			}
		}()
		b, err := io.ReadAll(f)
		if string(b) != "xxxxx" || !errors.Is(err, context.Canceled) {
			t.Fatalf("read %q, %v; want every byte said, then the end", b, err)
		}
	})
}

// What listens is read off the kernel's own tables: LISTEN sockets only, both
// families, once each, without the screen's own servers.
func TestListeningReadsTheKernelsTables(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		table := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:170C 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1
   1: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1
   2: 0100007F:0BB8 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 3 1
   3: 0100007F:17C0 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4 1
  sl  local_address                         remote_address                        st
   0: 00000000000000000000000001000000:1435 00000000000000000000000000000000:0000 0A
   1: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A
`
		got := listening(table)
		if want := []int{3000, 5173}; !slices.Equal(got, want) {
			t.Fatalf("listening = %v, want %v", got, want)
		}
	})
}
