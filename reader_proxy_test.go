package cloud

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// TestRetryTransport_AbsorbsDialGapThenSucceeds proves the core zero-downtime
// property: while the writer is un-dialable (its endpoint is down mid-roll), the
// reader retries and, once the writer comes back, the request SUCCEEDS — no
// 502/refused surfaces at the edge. This is what turns the writer's roll gap into
// a little latency instead of a blip.
func TestRetryTransport_AbsorbsDialGapThenSucceeds(t *testing.T) {
	var up atomic.Bool
	// A stub upstream that only exists while up==true; when down, dials are refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close() // free the port; nothing listens until we bring it up

	srvErr := make(chan error, 1)
	bringUp := func() {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			srvErr <- err
			return
		}
		up.Store(true)
		s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok:" + string(b)))
		})}
		_ = s.Serve(l)
	}

	// Bring the upstream up after 600ms — simulating the writer handoff gap.
	go func() {
		time.Sleep(600 * time.Millisecond)
		bringUp()
	}()

	rt := newRetryTransport(10*time.Second, luxlog.NewNoOpLogger())
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/v1/chat/completions", strings.NewReader(`{"x":1}`))

	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip failed despite retry budget: %v (up=%v)", err, up.Load())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `ok:{"x":1}` {
		t.Fatalf("body = %q, want replayed POST body echoed", body)
	}
	if elapsed := time.Since(start); elapsed < 500*time.Millisecond {
		t.Fatalf("succeeded in %s — expected to have waited through the ~600ms gap", elapsed)
	}
	select {
	case e := <-srvErr:
		t.Fatalf("bringUp failed: %v", e)
	default:
	}
}

// TestRetryTransport_GivesUpAfterBudget proves the retry is BOUNDED: if the
// writer never returns, the reader stops retrying at the budget and surfaces the
// dial error (which the proxy renders as 502), rather than hanging forever.
func TestRetryTransport_GivesUpAfterBudget(t *testing.T) {
	rt := newRetryTransport(300*time.Millisecond, luxlog.NewNoOpLogger())
	// 127.0.0.1:1 is reserved/unbound → connection refused (a dial error).
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/v1/models", nil)
	start := time.Now()
	_, err := rt.RoundTrip(req)
	if err == nil {
		t.Fatal("expected a dial error after the budget elapsed")
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Fatalf("gave up in %s — should have retried until ~300ms budget", elapsed)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s — retry budget was not honored", elapsed)
	}
}

// TestIsDialError_OnlyRetriesUndeliveredRequests pins the safety invariant: only
// a dial failure (request NEVER delivered) is retryable, so a non-idempotent POST
// is never double-executed after it reached the writer.
func TestIsDialError_OnlyRetriesUndeliveredRequests(t *testing.T) {
	if !isDialError(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}) {
		t.Fatal("a dial connection-refused must be retryable")
	}
	if !isDialError(syscall.ECONNREFUSED) {
		t.Fatal("bare ECONNREFUSED must be retryable")
	}
	// A read failure AFTER the request was written is NOT a dial error — ambiguous,
	// must not be retried.
	if isDialError(&net.OpError{Op: "read", Err: io.ErrUnexpectedEOF}) {
		t.Fatal("a post-delivery read error must NOT be retried (ambiguous)")
	}
	if isDialError(errors.New("some upstream 500")) {
		t.Fatal("a generic error must NOT be retried")
	}
}

// TestServeReaderProxy_RequiresWriterURL proves a reader fails loud without a
// target rather than silently serving nothing.
func TestServeReaderProxy_RequiresWriterURL(t *testing.T) {
	cfg := &Config{WriterURL: ""}
	if err := serveReaderProxy(cfg); err == nil {
		t.Fatal("reader with no CLOUD_WRITER_URL must fail closed")
	}
}

// TestReaderProxy_EndToEndTransparent proves the assembled proxy forwards method,
// path, body, and a header to the writer and streams the response back — the
// transparent-forwarder contract, exercised through httptest.
func TestReaderProxy_EndToEndTransparent(t *testing.T) {
	var gotPath, gotOrg, gotMethod string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotOrg = r.URL.Path, r.Method, r.Header.Get("X-Org-Id")
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(append([]byte("echo:"), b...))
	}))
	defer upstream.Close()

	cfg := &Config{WriterURL: upstream.URL, ListenAddr: "127.0.0.1:0", HealthListenAddr: "127.0.0.1:0", ReaderRetryBudget: 2 * time.Second}
	// Build the proxy handler the same way serveReaderProxy does, and drive it via
	// httptest so we assert forwarding without binding real ports.
	proxy, err := newReaderProxy(cfg, luxlog.NewNoOpLogger())
	if err != nil {
		t.Fatalf("newReaderProxy: %v", err)
	}
	edge := httptest.NewServer(proxy)
	defer edge.Close()

	req, _ := http.NewRequest(http.MethodPut, edge.URL+"/v1/prompts/foo", strings.NewReader("BODY"))
	req.Header.Set("X-Org-Id", "acme")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("edge request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if gotMethod != http.MethodPut || gotPath != "/v1/prompts/foo" || gotOrg != "acme" {
		t.Fatalf("writer saw method=%q path=%q org=%q — not transparently forwarded", gotMethod, gotPath, gotOrg)
	}
	if resp.StatusCode != http.StatusCreated || string(body) != "echo:BODY" {
		t.Fatalf("edge returned status=%d body=%q — response not passed through", resp.StatusCode, body)
	}
}
