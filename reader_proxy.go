package cloud

// Reader edge — a transparent, always-ready reverse proxy to the single writer.
//
// WHY A PROXY, NOT A LOCAL-STORE REPLICA. The writer embeds an exclusive-lock
// ZapDB KMS store (a Badger fork). clients/kms.TestConcurrentOpen_LiveWriterStore-
// IsNotROShareable proves that opening that store READ-ONLY while the writer is
// live FAILS ("Log truncate required to run DB") — Badger's RO open replays the
// live memtable WAL and refuses to truncate it. So a reader CANNOT open the KMS
// store off the writer's PVC, even read-only, even same-node. (The audit SQLite
// store IS concurrently shareable — audit/shareability_probe_test.go — but the
// KMS store is the one that is not, and every mutation is audited on the writer
// anyway.) Rather than braid a partial local-read replica that must carefully
// route KMS + every audited verb to the writer, the reader is the SIMPLEST
// correct thing: it opens NO stores and forwards EVERY request to the writer.
//
// WHAT IT BUYS. The reader Deployment rolls RollingUpdate (maxUnavailable:0), so
// the edge Service always has a ready endpoint. During a writer roll the reader
// holds the client connection and RETRIES (dial-only) across the writer's brief
// handoff gap, so a `rollout restart` of the writer never surfaces a 502/refused
// at the edge — it surfaces as a little extra latency. This is what removes the
// ~30s console blip that Recreate/replicas:1 causes today.
//
// SAFETY OF RETRY. Retry fires ONLY on a dial failure — the connection to the
// writer was never established (no ready endpoint / connection refused), so the
// request was never delivered and re-sending it cannot double-execute a
// non-idempotent POST. Once bytes are on the wire to a writer, a failure is NOT
// retried (it is ambiguous). The request body is buffered (bounded) so a retried
// POST can be replayed.
//
// TRANSPARENCY / TRUST BOUNDARY. The reader forwards the request unchanged
// (method, path, query, headers, body) and preserves the inbound Host for the
// writer's host-based routing. It adds nothing to identity: the writer's
// SanitizeIdentity re-validates the JWT and re-derives X-Org-Id/X-User-Id exactly
// as if the gateway reached it directly, so the writer's trust boundary is
// unchanged by the extra hop. httputil.ReverseProxy strips hop-by-hop headers and
// appends X-Forwarded-For, and transparently proxies WebSocket/SSE upgrades.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	luxlog "github.com/luxfi/log"
)

// serveReaderProxy runs the reader edge: a reverse proxy to cfg.WriterURL on the
// public listener, plus the ops health listener, shutting down gracefully on
// SIGINT/SIGTERM. It opens no stores and never returns until shutdown or a bind
// error. Serve dispatches here when CLOUD_ROLE=reader, BEFORE BuildDeps, so a
// reader never opens the KMS/audit/per-org stores.
func serveReaderProxy(cfg *Config) error {
	log := luxlog.New("cloud").New("subsystem", "reader")

	rp, err := newReaderProxy(cfg, log)
	if err != nil {
		return err
	}

	mainSrv := &http.Server{Addr: cfg.ListenAddr, Handler: rp, ReadHeaderTimeout: 10 * time.Second}
	healthSrv := &http.Server{Addr: cfg.HealthListenAddr, Handler: healthMux(), ReadHeaderTimeout: 5 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenErr := make(chan error, 2)
	go func() {
		log.Info("reader health listening", "addr", cfg.HealthListenAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- fmt.Errorf("reader health listen: %w", err)
		}
	}()
	go func() {
		log.Info("reader edge listening", "addr", cfg.ListenAddr, "writer", cfg.WriterURL, "retry_budget", cfg.ReaderRetryBudget.String())
		if err := mainSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- fmt.Errorf("reader edge listen: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("reader shutdown requested")
	case err := <-listenErr:
		return fmt.Errorf("reader listen: %w", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = healthSrv.Shutdown(shutdownCtx)
	return mainSrv.Shutdown(shutdownCtx)
}

// newReaderProxy builds the transparent reverse proxy to cfg.WriterURL: dial-only
// retry transport, prompt flushing for SSE, and a 502 error handler once the
// retry budget is exhausted. It preserves the inbound Host for the writer's
// host-based routing. Extracted from serveReaderProxy so it is unit-testable.
func newReaderProxy(cfg *Config, log luxlog.Logger) (*httputil.ReverseProxy, error) {
	if cfg.WriterURL == "" {
		return nil, fmt.Errorf("reader role: CLOUD_WRITER_URL is required (the writer base URL to forward to, e.g. http://cloud-writer.hanzo.svc:8000)")
	}
	target, err := url.Parse(cfg.WriterURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("reader role: invalid CLOUD_WRITER_URL %q: %v", cfg.WriterURL, err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	// Flush promptly so SSE / chunked streams (chat completions) pass through with
	// no added buffering latency.
	rp.FlushInterval = 100 * time.Millisecond
	rp.Transport = newRetryTransport(cfg.ReaderRetryBudget, log)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		if log != nil {
			log.Warn("reader proxy failed", "path", r.URL.Path, "method", r.Method, "err", e)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"code":"writer_unavailable","message":"upstream writer unavailable"}}`))
	}
	return rp, nil
}

// retryTransport re-sends a request ONLY when the writer could not be dialed —
// the connection was never established, so the request never reached the writer
// and replay cannot double-execute it. It buffers the request body (bounded) so a
// retried POST can be replayed. All other failures (a delivered request whose
// response failed) are returned as-is: retrying them would be ambiguous.
type retryTransport struct {
	base            http.RoundTripper
	budget          time.Duration
	maxBufferedBody int64
	log             luxlog.Logger
}

func newRetryTransport(budget time.Duration, log luxlog.Logger) *retryTransport {
	if budget <= 0 {
		budget = 25 * time.Second
	}
	return &retryTransport{
		base:            http.DefaultTransport.(*http.Transport).Clone(),
		budget:          budget,
		maxBufferedBody: 8 << 20, // 8 MiB — above this a request is a single attempt (never buffered)
		log:             log,
	}
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Make the body replayable. If it is already rewindable (GetBody set by the
	// proxy for small bodies) use that; otherwise buffer up to the cap. A body
	// larger than the cap is sent once with no retry (never silently truncated).
	getBody := req.GetBody
	if getBody == nil && req.Body != nil && req.Body != http.NoBody {
		buf, err := io.ReadAll(io.LimitReader(req.Body, t.maxBufferedBody+1))
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		if int64(len(buf)) > t.maxBufferedBody {
			// Too large to safely rebuffer: single attempt with what we read.
			req.Body = io.NopCloser(bytes.NewReader(buf))
			req.ContentLength = int64(len(buf))
			return t.base.RoundTrip(req)
		}
		body := buf
		getBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		req.ContentLength = int64(len(buf))
	}

	deadline := time.Now().Add(t.budget)
	backoff := 100 * time.Millisecond
	attempts := 0
	for {
		attempts++
		if getBody != nil {
			b, err := getBody()
			if err != nil {
				return nil, err
			}
			req.Body = b
		}
		resp, err := t.base.RoundTrip(req)
		if err == nil {
			return resp, nil
		}
		// Only a dial failure (request never delivered) is safely retryable.
		if !isDialError(err) || time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// isDialError reports whether err means the connection to the writer was never
// established — a no-endpoint / connection-refused / dial-timeout condition
// during a writer roll — so re-sending the request cannot double-execute it.
func isDialError(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// "dial" is the phase before any byte is written to the writer.
		return opErr.Op == "dial"
	}
	return false
}
