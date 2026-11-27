package host

// The shipped cmd/cloud router does not link the cloud package (nor any of its
// database-backed plugins). The reader therefore lives at this boundary, before
// the writer lease, data directory or plugin runtime is opened.
//
// A reader is deliberately a STATELESS edge, not a replica of tenant SQLite.
// It preserves the request Host and delegates authentication, authorization and
// every mutation to the writer. During a Recreate writer roll it retries ONLY
// requests whose connection could not be established; an ambiguous delivered
// request is never replayed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	readerEdgeDefaultBudget = 25 * time.Second
	readerEdgeMaxReplayBody = 8 << 20
	readerEdgeShutdownGrace = 45 * time.Second
)

// serveReaderEdge is invoked by cmd/cloud.run before any writer resources open.
// It binds only the product HTTP listener; probes use that same listener, not
// the historic :9090 ops port that the shipped router no longer starts.
func serveReaderEdge(addr string) error {
	budget := readerEdgeDefaultBudget
	if raw := strings.TrimSpace(os.Getenv("CLOUD_READER_RETRY_BUDGET")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("reader: CLOUD_READER_RETRY_BUDGET %q must be a positive Go duration", raw)
		}
		budget = parsed
	}

	draining := new(atomic.Bool)
	handler, err := readerEdgeHandler(os.Getenv("CLOUD_WRITER_URL"), budget, draining)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: MCP/SSE/streaming requests can legitimately be long-lived.
	}
	stopped := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			stopped <- err
			return
		}
		stopped <- nil
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-stopped:
		if err == nil {
			return fmt.Errorf("reader: listener closed unexpectedly")
		}
		return fmt.Errorf("reader: listen on %s: %w", addr, err)
	case <-ctx.Done():
		// Kubelet already removes terminating endpoints. Flip readiness first,
		// then allow a few seconds for endpoint propagation before closing the
		// listener. A preStop sleep can provide additional propagation time.
		draining.Store(true)
		time.Sleep(3 * time.Second)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), readerEdgeShutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("reader: graceful shutdown: %w", err)
	}
	return nil
}

func readerEdgeHandler(rawWriterURL string, budget time.Duration, draining *atomic.Bool) (http.Handler, error) {
	rawWriterURL = strings.TrimSpace(rawWriterURL)
	if rawWriterURL == "" {
		return nil, fmt.Errorf("reader: CLOUD_WRITER_URL must name the internal writer Service")
	}
	target, err := url.Parse(rawWriterURL)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil || target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
		return nil, fmt.Errorf("reader: invalid CLOUD_WRITER_URL %q (want http(s)://writer-service:port)", rawWriterURL)
	}
	if budget <= 0 || draining == nil {
		return nil, fmt.Errorf("reader: positive retry budget and drain signal required")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = 100 * time.Millisecond
	proxy.Transport = newReaderEdgeRetryTransport(budget)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, `{"error":{"code":"writer_unavailable","message":"upstream writer unavailable"}}`)
	}

	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "role": "reader"})
	}
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/health", health)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if draining.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"status":"draining","role":"reader"}`)
			return
		}
		// Remain in rotation while the *writer* rolls: the reader is what keeps
		// the client connection alive for the bounded retry interval.
		_, _ = io.WriteString(w, `{"status":"ok","role":"reader"}`)
	})
	mux.Handle("/", proxy)
	return mux, nil
}

// readerEdgeRetryTransport safely replays only requests that never connected to
// the writer. Bodies up to 8MiB are buffered; larger or streamed bodies are
// forwarded *in full* once, without retries. In particular, never read a body
// prefix and silently truncate the remainder.
type readerEdgeRetryTransport struct {
	base          http.RoundTripper
	budget        time.Duration
	maxReplayBody int64
}

func newReaderEdgeRetryTransport(budget time.Duration) *readerEdgeRetryTransport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.DialContext = (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	return &readerEdgeRetryTransport{base: base, budget: budget, maxReplayBody: readerEdgeMaxReplayBody}
}

type readerEdgeBody struct {
	io.Reader
	io.Closer
}

func (t *readerEdgeRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	request := req.Clone(req.Context())
	bodyFactory := req.GetBody
	replayable := req.Body == nil || req.Body == http.NoBody || bodyFactory != nil

	if !replayable && req.Body != nil {
		// Known huge payload: leave the body untouched and try exactly once.
		if req.ContentLength > t.maxReplayBody {
			return t.base.RoundTrip(req)
		}
		prefix, err := io.ReadAll(io.LimitReader(req.Body, t.maxReplayBody+1))
		if err != nil {
			_ = req.Body.Close()
			return nil, err
		}
		if int64(len(prefix)) > t.maxReplayBody {
			// Restore the entire stream, not only the buffered prefix.
			request.Body = &readerEdgeBody{Reader: io.MultiReader(bytes.NewReader(prefix), req.Body), Closer: req.Body}
			return t.base.RoundTrip(request)
		}
		_ = req.Body.Close()
		bodyFactory = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(prefix)), nil }
		request.ContentLength = int64(len(prefix))
		replayable = true
	}
	if !replayable {
		return t.base.RoundTrip(req)
	}

	deadline := time.Now().Add(t.budget)
	delay := 100 * time.Millisecond
	for {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		if bodyFactory != nil {
			body, err := bodyFactory()
			if err != nil {
				return nil, err
			}
			request.Body = body
		}
		response, err := t.base.RoundTrip(request)
		if err == nil {
			return response, nil
		}
		if !readerEdgeDialError(err) || time.Now().After(deadline) {
			return nil, err
		}
		sleep := delay
		if left := time.Until(deadline); left < sleep {
			sleep = left
		}
		if sleep <= 0 {
			return nil, err
		}
		timer := time.NewTimer(sleep)
		select {
		case <-req.Context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, req.Context().Err()
		case <-timer.C:
		}
		if delay < 2*time.Second {
			delay *= 2
		}
	}
}

func readerEdgeDialError(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}
