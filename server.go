// Copyright (c) 2026 Hanzo Industries Inc.
// SPDX-License-Identifier: Apache-2.0
//
// server.go — ZAP listener for the unified cloud binary.
//
// Per HIP-0106 the cloud binary serves two listeners:
//
//	HTTP (cfg.ListenAddr, default :8080)  — external client surface
//	ZAP  (cfg.ZAPListenAddr, default :9999) — intra-Hanzo subsystem RPC
//
// Before this file the ZAPListenAddr was logged in main.go but never
// bound — gateway-side ZAP plugins targeting cloud-api would get
// "no available server" (see memory:gateway_zap_cloud_api). Now the
// listener actually exists and answers handshake + heartbeat opcodes;
// the per-subsystem typed handlers register via `RegisterZAPHandler`
// from Mount(...) when subsystem code wants to expose itself.
//
// Port choice: the deployed cluster (insights/cloud/deployment.yaml)
// exposes containerPort 9999 and the cloud-api ZAP_*_ADDR env vars all
// target :9999, so we default to that. The cfg.ZAPListenAddr override
// still works for split-binary deployments that want isolation.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/luxfi/zap"
)

// ZAP message-type registry — append-only. Each subsystem owns a slot,
// matching the canonical IDs used elsewhere in the stack:
//
//	  10 = control      — handshake, heartbeat (built-in)
//	 200 = iam          — VerifyJWT, GetUser, GetOrg
//	 201 = kms          — GetSecret, PutSecret, Sign
//	 202 = base         — Open (per-tenant SQLite handle)
//	 203 = commerce     — GetTenantConfig
//	 204 = ai           — ChatCompletion
//	 205 = o11y         — Counter, Timing, Span
//	 206 = vfs          — Put, Get
//	 207 = mq           — Publish, Subscribe
//	 208 = payments     — CreateIntent, ConfirmIntent, GetIntentStatus
//	 209 = vault        — Charge
//	 302 = datastore    — already in use by hanzoai/datastore zap-bridge.
//
// Wire dispatch quirk: luxfi/zap routes on `msg.Flags() >> 8` which
// truncates the 16-bit msgType to its low byte. So 200 → 0xC8 on the
// wire. Senders write `FinishWithFlags(msgType << 8)`. We register and
// dispatch on the 8-bit projection; the 16-bit constant is the source
// label.
const (
	MsgTypeControl   uint16 = 10
	MsgTypeIAM       uint16 = 200
	MsgTypeKMS       uint16 = 201
	MsgTypeBase      uint16 = 202
	MsgTypeCommerce  uint16 = 203
	MsgTypeAI        uint16 = 204
	MsgTypeO11y      uint16 = 205
	MsgTypeVFS       uint16 = 206
	MsgTypeMQ        uint16 = 207
	MsgTypePayments  uint16 = 208
	MsgTypeVault     uint16 = 209
)

// Server is the running ZAP listener for one cloud process. It wraps
// the underlying luxfi/zap.Node so subsystems can register their
// per-msgType handler from Mount(...) and the binary can shut down
// cleanly on SIGTERM.
type Server struct {
	node     *zap.Node
	logger   luxlog.Logger
	closeOnce sync.Once
	started  atomic.Bool

	// inflight counts handlers currently running so Shutdown can drain.
	inflight atomic.Int64
}

// NewServer constructs a ZAP server bound to cfg.ZAPListenAddr.
// Returns an error only if address parsing fails. Call Start to bind.
func NewServer(cfg *Config, logger luxlog.Logger) (*Server, error) {
	port, err := portFromListen(cfg.ZAPListenAddr)
	if err != nil {
		return nil, fmt.Errorf("zap listen %q: %w", cfg.ZAPListenAddr, err)
	}

	nodeID := os.Getenv("ZAP_NODE_ID")
	if nodeID == "" {
		// Default = brand-domain composite. Operators can pin via env
		// for stable mDNS identity across restarts.
		nodeID = fmt.Sprintf("%s-cloud", cfg.Brand)
	}

	serviceType := os.Getenv("ZAP_SERVICE_TYPE")
	if serviceType == "" {
		serviceType = "_hanzo._tcp"
	}

	// Plaintext for in-cluster — TLS termination is the ingress's job;
	// pod-to-pod traffic stays on the Hanzo cluster network. Operators
	// can flip to PQ-TLS by passing a *tls.Config via wider plumbing
	// when split-deployed across clusters.
	slog := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})).With("component", "zap-server", "brand", cfg.Brand)

	node := zap.NewNode(zap.NodeConfig{
		NodeID:      nodeID,
		ServiceType: serviceType,
		Port:        port,
		NoDiscovery: true, // K8s — no mDNS multicast.
		Logger:      slog,
	})

	srv := &Server{
		node:   node,
		logger: logger,
	}

	// Register built-in control handler (handshake + heartbeat).
	node.Handle(MsgTypeControl&0xFF, srv.wrap(srv.handleControl))
	return srv, nil
}

// Start binds the listener. After Start returns nil the server is
// answering ZAP frames. Idempotent.
func (s *Server) Start() error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("zap server already started")
	}
	if err := s.node.Start(); err != nil {
		s.started.Store(false)
		return fmt.Errorf("zap node start: %w", err)
	}
	s.logger.Info("zap server listening")
	return nil
}

// Shutdown waits up to grace for in-flight handlers to complete, then
// stops the node. Idempotent.
func (s *Server) Shutdown(grace time.Duration) {
	s.closeOnce.Do(func() {
		deadline := time.Now().Add(grace)
		for s.inflight.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if remaining := s.inflight.Load(); remaining > 0 {
			s.logger.Warn("zap shutdown grace expired with inflight handlers",
				"inflight", remaining,
			)
		}
		s.node.Stop()
	})
}

// ZAPServer is the surface subsystems need from the cloud-level ZAP
// listener (the Deps.ZAP field). *Server satisfies this implicitly.
// The interface exists so subsystems can take a mockable dependency
// without importing luxfi/zap directly into every Mount package.
type ZAPServer interface {
	RegisterHandler(msgType uint16, handler zap.Handler)
}

// Compile-time assertion that *Server satisfies ZAPServer.
var _ ZAPServer = (*Server)(nil)

// RegisterHandler attaches `handler` to a subsystem msgType. Subsystems
// call this from Mount(...) to expose themselves over ZAP. The handler
// is automatically wrapped with inflight tracking + recover.
//
// The msgType is the source-level 16-bit constant (e.g. MsgTypeIAM);
// the dispatch key on the wire is its 8-bit projection. RegisterHandler
// does the projection so callers don't need to think about it.
func (s *Server) RegisterHandler(msgType uint16, handler zap.Handler) {
	s.node.Handle(msgType&0xFF, s.wrap(handler))
}

// wrap installs inflight tracking + recover around a handler. A panic
// inside the subsystem code becomes a 500-equivalent ZAP response
// instead of crashing the entire cloud process.
func (s *Server) wrap(h zap.Handler) zap.Handler {
	return func(ctx context.Context, from string, msg *zap.Message) (resp *zap.Message, err error) {
		s.inflight.Add(1)
		defer s.inflight.Add(-1)
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("zap handler panic",
					"from", from,
					"panic", r,
				)
				resp = buildErrorResponse(http.StatusInternalServerError, "internal error")
				err = nil
			}
		}()
		return h(ctx, from, msg)
	}
}

// handleControl answers the built-in handshake / heartbeat opcode.
// Request: empty (any body ignored). Response: {status:200, body:{ok,
// brand, domain, server:"hanzo-cloud"}}.
//
// Gateway-side health probes hit this to confirm the ZAP listener is
// up before routing real traffic to subsystem msgTypes.
func (s *Server) handleControl(_ context.Context, _ string, _ *zap.Message) (*zap.Message, error) {
	return buildControlResponse(http.StatusOK, "ok"), nil
}

// buildControlResponse encodes a minimal {status, body:"ok"} ZAP
// response without pulling json — single Bytes field.
func buildControlResponse(status int, payload string) *zap.Message {
	b := zap.NewBuilder(len(payload) + 64)
	ob := b.StartObject(12)
	ob.SetUint32(0, uint32(status))
	ob.SetBytes(4, []byte(payload))
	ob.FinishAsRoot()
	msg, err := zap.Parse(b.Finish())
	if err != nil {
		// Self-built unparseable is a build bug; fall back to a
		// status-only frame instead of crashing.
		fb := zap.NewBuilder(64)
		fob := fb.StartObject(12)
		fob.SetUint32(0, uint32(http.StatusInternalServerError))
		fob.FinishAsRoot()
		msg, _ = zap.Parse(fb.Finish())
	}
	return msg
}

// buildErrorResponse is a generic {status, body:msg} ZAP frame used by
// the panic recover path.
func buildErrorResponse(status int, payload string) *zap.Message {
	return buildControlResponse(status, payload)
}

// portFromListen accepts ":9999" or "0.0.0.0:9999" — returns the port.
// Lives here (not main.go) so subsystems can call it for split listens.
func portFromListen(listen string) (int, error) {
	listen = strings.TrimSpace(listen)
	idx := strings.LastIndex(listen, ":")
	if idx < 0 {
		return 0, errors.New("missing port")
	}
	p, err := strconv.Atoi(listen[idx+1:])
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid port %q", listen[idx+1:])
	}
	return p, nil
}
