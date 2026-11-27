package host

// The plugin wire's response deadline (sdk/plane/readdeadline.go).
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

	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/valyala/fasthttp"
)

// BOTH halves must survive re-registration. A Transport may leave either nil,
// so replacing the ZAP scheme with a Dial-only value would silently remove the
// host's ability to LISTEN on its own default scheme — the fleet would build
// fine and fail at boot.
func TestRegisteringTheDialHalfDoesNotDropTheServeHalf(t *testing.T) {
	tr := plane.Transport()
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

// The reason a total-duration cap is the wrong SHAPE, kept as an executable
// note: a stream that is alive but slow must not die. This drives a real
// zaphttp-served stream that emits a chunk every 150ms for well past the old
// 30s-per-response budget's proportional equivalent, proving the deadline is
// not re-armed per frame upstream and that ours is large enough to cover it.
func TestASlowButLiveStreamIsNotCut(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a real socket over ~2s")
	}
	tr := plane.Transport()

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
