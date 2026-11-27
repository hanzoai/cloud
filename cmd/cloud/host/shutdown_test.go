package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/webui/release"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// A client holding a stream open does not decide when the host exits, and the
// children stop with it. zip's own Shutdown drains every open connection before
// its hooks stop a child, so a console tab left open held a SIGTERMed host, and
// every child under it, until the tab closed.
func TestShutdownOutlastsAnOpenStream(t *testing.T) {
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()

	const name = "held"
	app := zip.New(zip.Config{AppName: "cloud", Logger: luxlog.New("test")})
	leaf, err := zip.Load(zip.Plugin{
		Name: name, Path: os.Args[0],
		Env:   []string{wakeChildEnv + "=" + name, "ZIP_RUNTIME_DIR=" + dir},
		Start: 60 * time.Second,
	}, "/v1/"+name)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	app.Use(leaf)
	app.Raw(http.MethodGet, "/stream", func(c *zip.Ctx) error {
		return c.SendStreamWriter(func(w *bufio.Writer) {
			for {
				if _, err := w.WriteString(": ping\n\n"); err != nil || w.Flush() != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	})

	at := filepath.Join(dir, "h.sock")
	go func() { _ = app.Listen("http://" + at) }()
	waitFor(t, at)

	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", at)
		},
	}}
	resp, err := hc.Get("http://cloud/stream")
	if err != nil {
		t.Fatalf("open the stream: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatalf("read the stream: %v", err)
	}
	if n := running(t); n != 1 {
		t.Fatalf("%d children running before shutdown, want 1", n)
	}

	const grace = 100 * time.Millisecond
	returned := make(chan struct{})
	go func() { shutdown(app, grace); close(returned) }()
	select {
	case <-returned:
	case <-time.After(20 * grace):
		t.Fatalf("shutdown still waiting on an open stream after %s", 20*grace)
	}
	for range 100 {
		if running(t) == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%d children outlived the host's shutdown", running(t))
}

// TestHaltOnce_AttributesFirstInitiatorOnly verifies every shutdown path logs a
// structured cause before teardown and subsequent calls (e.g. deferred
// run_return) neither re-run teardown nor overwrite the initiating reason.
func TestHaltOnce_AttributesFirstInitiatorOnly(t *testing.T) {
	cases := []struct {
		name      string
		reason    string
		cause     error
		wantCause string
	}{
		{"sigterm", "signal:" + syscall.SIGTERM.String(), nil, ""},
		{"boot_failure", "boot_failure", errors.New("required app failed"), "required app failed"},
		{"coordinator", "deployment_coordinator", coord.ErrDuplicate, coord.ErrDuplicate.Error()},
		{"listener_error", "listener_error", errors.New("bind: address already in use"), "address already in use"},
		{"listener_return", "listener_return", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			var calls atomic.Int32
			var gotReason string
			var gotCause error

			halt := haltOnce(luxlog.NewWriter(&buf), func(reason string, cause error) {
				calls.Add(1)
				gotReason = reason
				gotCause = cause
			})

			halt(tc.reason, tc.cause)
			halt("run_return", errors.New("later deferred noise"))

			if calls.Load() != 1 {
				t.Fatalf("stop executed %d times, want 1", calls.Load())
			}
			if gotReason != tc.reason {
				t.Fatalf("reason = %q, want %q", gotReason, tc.reason)
			}
			if !errors.Is(gotCause, tc.cause) {
				t.Fatalf("cause = %v, want %v", gotCause, tc.cause)
			}
			logged := buf.String()
			if !strings.Contains(logged, "host shutdown initiated") {
				t.Fatalf("expected structured shutdown log, got: %s", logged)
			}
			if !strings.Contains(logged, tc.reason) {
				t.Fatalf("expected reason %q in log, got: %s", tc.reason, logged)
			}
			if tc.wantCause != "" && !strings.Contains(logged, tc.wantCause) {
				t.Fatalf("expected cause %q in log, got: %s", tc.wantCause, logged)
			}
			if strings.Contains(logged, "run_return") {
				t.Fatalf("second halt call leaked into log: %s", logged)
			}
		})
	}
}

// TestWatchDeployment_IgnoresTransientKVAndHaltsOnDuplicate verifies that
// transient KV outages never halt the host, a verified duplicate pod ID halts
// with reason="deployment_coordinator" and the real coord error, and cancelling
// ctx exits the watcher goroutine cleanly without leaking its ticker.
func TestWatchDeployment_IgnoresTransientKVAndHaltsOnDuplicate(t *testing.T) {
	// 1. Transient KV outage: Start + watchDeployment must NOT halt the host.
	downCoord := coordtest.Down(coord.Config{
		Self:     coord.Member{ID: "cloud-dev-0", Addr: "127.0.0.1:9443"},
		Root:     true,
		Declared: 2,
		Log:      luxlog.NewNoOpLogger(),
	})
	downCoord.Start(context.Background())
	t.Cleanup(func() { downCoord.Close(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	var halted atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDeploymentEvery(ctx, downCoord, 10*time.Millisecond, luxlog.NewNoOpLogger(), func(string, error) {
			halted.Store(true)
		})
	}()
	time.Sleep(60 * time.Millisecond)
	if halted.Load() {
		t.Fatal("transient KV outage halted the host; coordinator must tolerate unreachable KV")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchDeployment did not exit when ctx was cancelled (goroutine leak)")
	}

	// 2. Verified duplicate pod ID: watchDeployment MUST halt with the error.
	dupCoord := coord.Broken(coord.ErrDuplicate)
	var mu sync.Mutex
	var gotReason string
	var gotErr error
	triggered := make(chan struct{})
	go watchDeploymentEvery(context.Background(), dupCoord, 10*time.Millisecond, luxlog.NewNoOpLogger(), func(reason string, cause error) {
		mu.Lock()
		gotReason = reason
		gotErr = cause
		mu.Unlock()
		close(triggered)
	})
	select {
	case <-triggered:
	case <-time.After(time.Second):
		t.Fatal("watchDeployment did not halt on verified duplicate pod ID")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotReason != "deployment_coordinator" {
		t.Fatalf("reason = %q, want deployment_coordinator", gotReason)
	}
	if !errors.Is(gotErr, coord.ErrDuplicate) {
		t.Fatalf("cause = %v, want %v", gotErr, coord.ErrDuplicate)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestBoot_ProjectFailureDegradesConsoleWithoutHaltingHost replicates a failed
// `project` child during boot. Previously boot() returned an error and killed
// the entire API host; now it logs loudly and keeps serving the API and healthy
// siblings while the console degrades to 503 until project recovers.
func TestBoot_ProjectFailureDegradesConsoleWithoutHaltingHost(t *testing.T) {
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()

	var logs syncBuffer
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, Logger: luxlog.NewWriter(&logs)})
	t.Cleanup(func() { _ = app.Shutdown() })

	proj := dead(t, projectApp, "/v1/project")
	proj.Eager = true
	if err := mount(app, proj, true); err != nil {
		t.Fatalf("mount project: %v", err)
	}
	if err := mount(app, good(t, "flags", "/v1/flag", `{"ok":true}`), false); err != nil {
		t.Fatalf("mount sibling: %v", err)
	}
	health(app)

	at := filepath.Join(dir, "boot.sock")
	go func() { _ = app.Listen("http://" + at) }()
	waitFor(t, at)

	consoleSrc := release.New(release.Config{}, luxlog.NewWriter(&logs))
	if err := boot(context.Background(), app, at, []manifest.App{proj}, consoleSrc); err != nil {
		t.Fatalf("boot() failed on project start error (would halt the entire host): %v", err)
	}
	if !strings.Contains(logs.String(), "serving the API without it") {
		t.Fatalf("expected loud console degradation log, got: %s", logs.String())
	}

	// Host stays alive and healthy siblings continue serving.
	code, _, body := do(t, app, "/healthz")
	if code != 200 || !strings.Contains(body, projectApp) {
		t.Fatalf("GET /healthz = %d %s, want 200 reporting absent project", code, body)
	}
	code, _, body = do(t, app, "/v1/flag")
	if code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("GET /v1/flag = %d %s, want healthy sibling still serving", code, body)
	}
}

// TestBoot_RequiredFailureStillAbortsHost verifies an app explicitly marked
// Required still returns an error from boot so halt("boot_failure", err) fires.
func TestBoot_RequiredFailureStillAbortsHost(t *testing.T) {
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()

	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, Logger: luxlog.NewNoOpLogger()})
	t.Cleanup(func() { _ = app.Shutdown() })

	req := dead(t, "critical", "/v1/critical")
	req.Eager = true
	req.Required = true
	if err := mount(app, req, true); err != nil {
		t.Fatalf("mount critical: %v", err)
	}

	at := filepath.Join(dir, "req.sock")
	go func() { _ = app.Listen("http://" + at) }()
	waitFor(t, at)

	err := boot(context.Background(), app, at, []manifest.App{req}, nil)
	if err == nil || !strings.Contains(err.Error(), "critical is required") {
		t.Fatalf("boot() error = %v, want required-app failure", err)
	}
}

// TestFaultIsolation_PluginCrashAndStreamEOFDoNotHaltHost is the architectural
// acceptance test: abrupt stream EOFs, a killed plugin child (SIGKILL), and a
// broken lazy plugin must NEVER bring down the host or disturb healthy siblings,
// and the crashed plugin must recover on its own.
func TestFaultIsolation_PluginCrashAndStreamEOFDoNotHaltHost(t *testing.T) {
	dir := kit.Sockets(t)
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	cloud.ResetPlane()

	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true, Logger: luxlog.NewNoOpLogger()})
	t.Cleanup(func() { _ = app.Shutdown() })

	for _, name := range []string{"alpha", "beta"} {
		leaf, err := zip.Load(zip.Plugin{
			Name:  name,
			Path:  os.Args[0],
			Lazy:  true,
			Env:   []string{wakeChildEnv + "=" + name, "ZIP_RUNTIME_DIR=" + dir},
			Start: 60 * time.Second,
		}, "/v1/"+name)
		if err != nil {
			t.Fatalf("Load %s: %v", name, err)
		}
		app.Use(leaf)
	}
	if err := mount(app, lazyDead(t, "broken", "/v1/broken"), false); err != nil {
		t.Fatalf("mount broken: %v", err)
	}
	app.Raw(http.MethodGet, "/stream", func(c *zip.Ctx) error {
		return c.SendStreamWriter(func(w *bufio.Writer) {
			for range 20 {
				if _, err := w.WriteString("data: tick\n\n"); err != nil || w.Flush() != nil {
					return
				}
				time.Sleep(15 * time.Millisecond)
			}
		})
	})
	health(app)

	at := filepath.Join(dir, "iso.sock")
	listenDone := make(chan error, 1)
	go func() { listenDone <- app.Listen("http://" + at) }()
	waitFor(t, at)

	hc := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", at)
			},
		},
	}
	get := func(path string) (int, string) {
		t.Helper()
		resp, err := hc.Get("http://cloud" + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, strings.TrimSpace(string(b))
	}

	// 1. Abrupt client stream EOF must close only the stream, never the host.
	resp, err := hc.Get("http://cloud/stream")
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	if _, err := bufio.NewReader(resp.Body).ReadString('\n'); err != nil {
		t.Fatalf("read stream line: %v", err)
	}
	_ = resp.Body.Close() // abrupt EOF

	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("GET /healthz after stream EOF = %d, want 200", code)
	}

	// 2. Start both plugins, then SIGKILL alpha. Host and beta must stay healthy,
	// and alpha must automatically recover with a new PID.
	code, alphaPIDStr := get("/v1/alpha/pid")
	if code != 200 {
		t.Fatalf("GET /v1/alpha/pid = %d %q, want 200", code, alphaPIDStr)
	}
	alphaPID, err := strconv.Atoi(alphaPIDStr)
	if err != nil || alphaPID <= 0 {
		t.Fatalf("invalid alpha pid %q: %v", alphaPIDStr, err)
	}
	code, betaPIDBefore := get("/v1/beta/pid")
	if code != 200 {
		t.Fatalf("GET /v1/beta/pid = %d %q, want 200", code, betaPIDBefore)
	}

	if err := syscall.Kill(alphaPID, syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL alpha (%d): %v", alphaPID, err)
	}

	// Host listener must still be running and sibling beta untouched.
	select {
	case err := <-listenDone:
		t.Fatalf("host listener exited after plugin crash: %v", err)
	default:
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("GET /healthz after plugin SIGKILL = %d, want 200", code)
	}
	if code, betaPIDAfter := get("/v1/beta/pid"); code != 200 || betaPIDAfter != betaPIDBefore {
		t.Fatalf("sibling beta disrupted by alpha crash: code=%d pid=%s (was %s)", code, betaPIDAfter, betaPIDBefore)
	}

	// Wait for zip's supervisor to restart ONLY alpha with a new PID.
	deadline := time.Now().Add(15 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		resp, err := hc.Get("http://cloud/v1/alpha/pid")
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			newPID := strings.TrimSpace(string(b))
			if resp.StatusCode == 200 && newPID != "" && newPID != alphaPIDStr {
				recovered = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("crashed plugin alpha (old pid %d) did not auto-recover", alphaPID)
	}

	// 3. A broken lazy plugin returns 503 and is reported absent without halting the host.
	if code, _ := get("/v1/broken/check"); code != 503 {
		t.Fatalf("GET /v1/broken/check = %d, want 503", code)
	}
	if code, body := get("/healthz"); code != 200 || !strings.Contains(body, "broken") {
		t.Fatalf("GET /healthz after broken plugin = %d %s, want 200 naming broken", code, body)
	}
	if code, betaPIDAfter := get("/v1/beta/pid"); code != 200 || betaPIDAfter != betaPIDBefore {
		t.Fatalf("sibling beta disrupted by broken plugin: code=%d pid=%s", code, betaPIDAfter)
	}
	select {
	case err := <-listenDone:
		t.Fatalf("host listener exited during fault injection: %v", err)
	default:
	}
}
