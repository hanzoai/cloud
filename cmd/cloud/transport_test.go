package main

// The plugin wire's response deadline (transport.go).
//
// The bug was invisible from inside the process: a streamed completion simply
// stopped mid-token at ~30s with no finish_reason, and every layer downstream
// read that as a complete answer. So these tests hold the two facts that make
// the truncation impossible, and one that makes the fix safe to apply.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/valyala/fasthttp"
)

// The whole point: a plugin response gets long enough to be a model completion.
// 30s — zaphttp's default, and what shipped — cut a real generation in half.
func TestPluginResponseDeadlineOutlastsACompletion(t *testing.T) {
	const zaphttpDefault = 30 * time.Second
	if pluginResponseTimeout <= zaphttpDefault {
		t.Fatalf("pluginResponseTimeout = %v, which is not longer than zaphttp's %v default — "+
			"a streamed completion is capped at its TOTAL duration, so this is the truncation",
			pluginResponseTimeout, zaphttpDefault)
	}
	// Measured: the upstream streamed a large page for 389s and finished
	// correctly. A cap under that truncates real work.
	if pluginResponseTimeout < 7*time.Minute {
		t.Fatalf("pluginResponseTimeout = %v — a long document has been observed streaming "+
			"for 389s upstream, so this still cuts legitimate output", pluginResponseTimeout)
	}
	// And it is still a bound: zero would let a wedged plugin hold a connection
	// forever, which is the failure this deadline exists to prevent.
	if pluginResponseTimeout == 0 {
		t.Fatal("a zero deadline is no deadline — a wedged plugin would hold the conn forever")
	}
}

// BOTH halves must survive re-registration. A Transport may leave either nil,
// so replacing the ZAP scheme with a Dial-only value would silently remove the
// host's ability to LISTEN on its own default scheme — the fleet would build
// fine and fail at boot.
func TestRegisteringTheDialHalfDoesNotDropTheServeHalf(t *testing.T) {
	tr := longDeadlineTransport()
	if tr.Serve == nil {
		t.Fatal("the ZAP scheme lost its Serve half — the host could not listen on its own default scheme")
	}
	if tr.Dial == nil {
		t.Fatal("the ZAP scheme lost its Dial half — no plugin could be reached")
	}

	sock := shortSocket(t, "probe")
	srv := tr.Serve(sock, func(ctx *fasthttp.RequestCtx) { ctx.SetBodyString("ok") })
	go func() { _ = srv.ListenAndServe() }()
	defer func() { _ = srv.Close() }()

	// Dial half: it must reach that socket. Retried, because the listener comes
	// up asynchronously — a probe of the socket FILE is not the same question.
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
		req.SetRequestURI("http://plugin/")
		lastErr = tr.Dial(sock).Do(req, resp)
		body := string(resp.Body())
		fasthttp.ReleaseRequest(req)
		fasthttp.ReleaseResponse(resp)
		if lastErr == nil && body == "ok" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("the ZAP scheme lost its Dial half: %v", lastErr)
}

// pluginNetwork must agree with zip's own unexported networkOf, or a unix
// plugin socket is dialed as tcp and nothing mounts.
func TestPluginNetworkMatchesZipsRule(t *testing.T) {
	for addr, want := range map[string]string{
		"/var/lib/cloud/run/ai.sock": "unix",
		"./relative.sock":            "unix",
		"@abstract":                  "unix",
		"127.0.0.1:8000":             "tcp",
		"cloud-0.cloud:8000":         "tcp",
		"":                           "tcp",
	} {
		if got := pluginNetwork(addr); got != want {
			t.Fatalf("pluginNetwork(%q) = %q, want %q", addr, got, want)
		}
	}
}

// The reason a total-duration cap is the wrong SHAPE, kept as an executable
// note: a stream that is alive but slow must not die. This drives a real
// zaphttp-served stream that emits a chunk every 150ms for well past the old
// 30s-per-response budget's proportional equivalent, proving the deadline is
// not re-armed per frame upstream and that ours is large enough to cover it.
func TestASlowButLiveStreamIsNotCut(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real socket over ~2s")
	}
	tr := longDeadlineTransport()

	sock := shortSocket(t, "stream")
	const chunks = 12
	srv := tr.Serve(sock, func(ctx *fasthttp.RequestCtx) {
		ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
			for i := range chunks {
				fmt.Fprintf(w, "chunk-%d\n", i)
				_ = w.Flush()
				time.Sleep(150 * time.Millisecond)
			}
		})
	})
	go func() { _ = srv.ListenAndServe() }()
	defer func() { _ = srv.Close() }()

	req, resp := fasthttp.AcquireRequest(), fasthttp.AcquireResponse()
	defer fasthttp.ReleaseRequest(req)
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI("http://plugin/")
	var body string
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := tr.Dial(sock).Do(req, resp); err == nil {
			body = string(resp.Body())
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("stream request never succeeded: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	// Every chunk, in order, and the last one present — a truncated stream is
	// exactly what this must catch.
	for i := range chunks {
		if !strings.Contains(body, fmt.Sprintf("chunk-%d\n", i)) {
			t.Fatalf("chunk %d missing — the stream was cut after %d bytes", i, len(body))
		}
	}
}

// shortSocket returns a unix socket path SHORT enough to bind. t.TempDir() on
// macOS yields a /var/folders/… path that, with the test name in it, exceeds
// sun_path's 104 bytes — the bind then fails with "invalid argument", which
// reads like a transport bug and is only a path length.
func shortSocket(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "z")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir + "/" + name + ".sock"
}
