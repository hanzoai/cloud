package host

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReaderEdgeRejectsBadWriterURL(t *testing.T) {
	for _, raw := range []string{"", "cloud.hanzo.svc:8000", "file:///etc/passwd", "https://user:pass@cloud:8000", "http://cloud:8000/base", "http://cloud:8000/?token=x"} {
		if _, err := readerEdgeHandler(raw, time.Second, new(atomic.Bool)); err == nil {
			t.Errorf("accepted unsafe writer URL %q", raw)
		}
	}
}

func TestReaderEdgePreservesHostAndApprovalBoundary(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.hanzo.ai" || r.URL.Path != "/v1/build" || r.Header.Get("Authorization") != "Bearer demo" {
			t.Errorf("writer got host=%q path=%q authorization=%q", r.Host, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "passed")
	}))
	defer upstream.Close()
	drain := new(atomic.Bool)
	h, err := readerEdgeHandler(upstream.URL, time.Second, drain)
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewServer(h)
	defer edge.Close()
	req, _ := http.NewRequest(http.MethodPost, edge.URL+"/v1/build", strings.NewReader("payload"))
	req.Host = "api.hanzo.ai"
	req.Header.Set("Authorization", "Bearer demo")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("unexpected status %d", res.StatusCode)
	}
	for _, p := range []string{"/healthz", "/readyz"} {
		response, err := http.Get(edge.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("%s unexpectedly %d", p, response.StatusCode)
		}
	}
	drain.Store(true)
	res, err = http.Get(edge.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 503 {
		t.Fatalf("draining readiness = %d, want 503", res.StatusCode)
	}
	res, err = http.Get(edge.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("draining liveness = %d, want 200", res.StatusCode)
	}
}

func TestReaderEdgeRetryOnlyBeforeConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	var delivered atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			delivered.Add(1)
			b, _ := io.ReadAll(r.Body)
			_, _ = w.Write(b)
		})}
		go func() { time.Sleep(600 * time.Millisecond); _ = server.Shutdown(context.Background()) }()
		_ = server.Serve(l)
	}()
	rt := newReaderEdgeRetryTransport(2 * time.Second)
	request, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/test", strings.NewReader("once"))
	request.GetBody = nil // exercise proxy-originated POST, not just rewindable client bodies
	res, err := rt.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(data) != "once" {
		t.Fatalf("bad body %q", data)
	}
	if got := delivered.Load(); got != 1 {
		t.Fatalf("delivered %d times", got)
	}
	<-done
}

func TestReaderEdgeNoReplayAfterWriterAcceptedConnection(t *testing.T) {
	var delivered atomic.Int32
	writer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered.Add(1)
		h, ok := w.(http.Hijacker)
		if !ok {
			t.Error("no hijack")
			return
		}
		c, _, err := h.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = c.Close()
	}))
	defer writer.Close()
	h, err := readerEdgeHandler(writer.URL, 2*time.Second, new(atomic.Bool))
	if err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewServer(h)
	defer edge.Close()
	req, _ := http.NewRequest(http.MethodPost, edge.URL+"/v1/pay", strings.NewReader("do not replay"))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %d", res.StatusCode)
	}
	if got := delivered.Load(); got != 1 {
		t.Fatalf("ambiguous non-idempotent POST delivered %d times", got)
	}
}

func TestReaderEdgeNeverTruncatesLargeBody(t *testing.T) {
	var n atomic.Int64
	writer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nBytes, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			t.Error(err)
		}
		n.Store(nBytes)
		w.WriteHeader(204)
	}))
	defer writer.Close()
	payload := bytes.Repeat([]byte("x"), readerEdgeMaxReplayBody+10)
	req, _ := http.NewRequest(http.MethodPost, writer.URL, bytes.NewReader(payload))
	req.GetBody = nil
	req.ContentLength = -1 // exercise unknown streaming body length
	rt := newReaderEdgeRetryTransport(time.Second)
	response, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got := n.Load(); got != int64(len(payload)) {
		t.Fatalf("writer received %d bytes instead of %d", got, len(payload))
	}
}
