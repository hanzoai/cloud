// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Server exposes a Node's P2P discovery, DHT, routing, and sovereignty control
// surface over both github.com/zap-proto/zip typed operations and standard-library
// net/http.
type Server struct {
	Node *Node
	Now  func() int64
}

// NewServer wraps node in a dual-surface HTTP control server.
func NewServer(node *Node) (*Server, error) {
	if node == nil {
		return nil, errors.New("p2pnet: nil node")
	}
	return &Server{
		Node: node,
		Now:  func() int64 { return time.Now().Unix() },
	}, nil
}

func (s *Server) nowUnix() int64 {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().Unix()
}

// noIn is the input of a typed operation that accepts no parameters or body.
type noIn struct{}

// healthResponse describes the local P2P node's health and sovereignty state.
type healthResponse struct {
	// Service names the subsystem answering — always "p2pnet".
	Service string `json:"service"`
	// Status is "ok" while the P2P node is mounted and serving.
	Status string `json:"status"`
	// ProtocolVersion is the active p2pnet wire and schema version.
	ProtocolVersion string `json:"protocolVersion"`
	// PeerID is the node's self-certifying Ed25519 multihash peer identifier.
	PeerID PeerID `json:"peerId"`
	// NATMode is the node's reachability mode (direct, nat_relayed, or relay_server).
	NATMode NATReachability `json:"natMode"`
	// Role is the operational role of this node in the decentralized mesh.
	Role NodeRole `json:"role"`
	// RoutingTableSize is the number of peers currently tracked in Kademlia k-buckets.
	RoutingTableSize int `json:"routingTableSize"`
	// VerifiedAnnouncements is the count of currently valid announcements in the local DHT store.
	VerifiedAnnouncements int `json:"verifiedAnnouncements"`
	// HanzoCalls is the count of centralized Hanzo control-plane calls made (0 in P2P mode).
	HanzoCalls int64 `json:"hanzoCalls"`
	// HanzoRegistryDisabled reports whether centralized registry lookups are disabled.
	HanzoRegistryDisabled bool `json:"hanzoRegistryDisabled"`
}

// peersResponse lists the local peer ID and every peer in the Kademlia routing table.
type peersResponse struct {
	// PeerID is this node's self-certifying Ed25519 peer identifier.
	PeerID PeerID `json:"peerId"`
	// Peers is the deterministic PeerID-sorted snapshot of known routing-table peers.
	Peers []PeerInfo `json:"peers"`
}

// announcementsIn filters verified capability announcements by optional model ID.
type announcementsIn struct {
	// Model optionally restricts returned announcements to providers serving this model ID.
	Model string `json:"model,omitempty"`
}

// announcementsResponse wraps the list of verified capability announcements.
type announcementsResponse struct {
	// Data contains verified, non-expired capability announcements sorted by PeerID.
	Data []CapabilityAnnouncement `json:"data"`
}

// submitAnnouncementIn carries a signed CapabilityAnnouncement body plus the
// optional relaying sender peer ID from the X-P2P-Sender header.
type submitAnnouncementIn struct {
	// Sender optionally identifies the relaying peer from the X-P2P-Sender header.
	Sender string `json:"sender,omitempty" header:"X-P2P-Sender" url:"-"`
	// ProtocolVersion is the p2pnet protocol version signed into the announcement.
	ProtocolVersion string `json:"protocol_version" url:"-"`
	// PeerID is the self-certifying PeerID of the announcing peer.
	PeerID PeerID `json:"peer_id" url:"-"`
	// PublicKey is the hex-encoded 32-byte Ed25519 public key bound to PeerID.
	PublicKey string `json:"public_key" url:"-"`
	// Addrs lists direct TCP and UDP/QUIC-v1 multiaddrs bound to PeerID.
	Addrs []string `json:"addrs,omitempty" url:"-"`
	// RelayAddrs lists circuit-relay v2 multiaddrs targeting PeerID.
	RelayAddrs []string `json:"relay_addrs,omitempty" url:"-"`
	// NATMode declares direct, nat_relayed, or relay_server reachability.
	NATMode NATReachability `json:"nat_mode" url:"-"`
	// Role declares the peer's mesh role.
	Role NodeRole `json:"role" url:"-"`
	// Models lists the AI models served by this peer with pricing and latency metadata.
	Models []ModelCapability `json:"models,omitempty" url:"-"`
	// Hardware describes self-declared compute and optional hardware attestation metadata.
	Hardware HardwareMetadata `json:"hardware" url:"-"`
	// Seq is the strictly monotonic per-identity announcement sequence number.
	Seq uint64 `json:"seq" url:"-"`
	// Nonce is the unique anti-replay cryptographic nonce.
	Nonce string `json:"nonce" url:"-"`
	// IssuedAt is the Unix timestamp (seconds) when the announcement was signed.
	IssuedAt int64 `json:"issued_at" url:"-"`
	// ExpiresAt is the Unix timestamp (seconds) after which the announcement expires.
	ExpiresAt int64 `json:"expires_at" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the canonical announcement payload.
	Signature string `json:"signature" url:"-"`
	// VerificationClass states the proof class of the announcement.
	VerificationClass VerificationClass `json:"verification_class" url:"-"`
}

func (in *submitAnnouncementIn) toAnnouncement() CapabilityAnnouncement {
	return CapabilityAnnouncement{
		ProtocolVersion:   in.ProtocolVersion,
		PeerID:            in.PeerID,
		PublicKey:         in.PublicKey,
		Addrs:             in.Addrs,
		RelayAddrs:        in.RelayAddrs,
		NATMode:           in.NATMode,
		Role:              in.Role,
		Models:            in.Models,
		Hardware:          in.Hardware,
		Seq:               in.Seq,
		Nonce:             in.Nonce,
		IssuedAt:          in.IssuedAt,
		ExpiresAt:         in.ExpiresAt,
		Signature:         in.Signature,
		VerificationClass: in.VerificationClass,
	}
}

// providersIn queries the Kademlia DHT for verified providers of a model.
type providersIn struct {
	// Model is the required model identifier to discover verified providers for.
	Model string `json:"model"`
}

// providersResponse returns verified providers discovered for a model.
type providersResponse struct {
	// Model is the queried model identifier.
	Model string `json:"model"`
	// Providers lists verified, non-expired announcements advertising Model.
	Providers []CapabilityAnnouncement `json:"providers"`
}

// routeInferenceIn carries an inference routing request over the P2P mesh.
type routeInferenceIn struct {
	// RequestID optionally pins a caller-chosen request identifier.
	RequestID string `json:"request_id" url:"-"`
	// ModelID names the target AI model to route inference to.
	ModelID string `json:"model_id" url:"-"`
	// PromptTokens is the input prompt token count for cost accounting.
	PromptTokens int `json:"prompt_tokens" url:"-"`
	// MaxOutputTokens bounds generated output tokens for the inference call.
	MaxOutputTokens int `json:"max_output_tokens" url:"-"`
	// PromptDigest is a caller-supplied digest representing the prompt payload.
	PromptDigest string `json:"prompt_digest" url:"-"`
	// ClientPeerID optionally echoes the originating client peer ID.
	ClientPeerID PeerID `json:"client_peer_id" url:"-"`
	// Timestamp is the optional Unix timestamp (seconds) of the request.
	Timestamp int64 `json:"timestamp" url:"-"`
}

// verifyBundleIn carries a signed MeshExportBundle for offline cryptographic verification.
type verifyBundleIn struct {
	// ProtocolVersion is the p2pnet schema version recorded in the bundle.
	ProtocolVersion string `json:"protocol_version" url:"-"`
	// ExportedAt is the Unix timestamp (seconds) when the bundle was signed.
	ExportedAt int64 `json:"exported_at" url:"-"`
	// ExporterPeerID is the self-certifying PeerID of the exporting node.
	ExporterPeerID PeerID `json:"exporter_peer_id" url:"-"`
	// ExporterPublicKey is the hex-encoded Ed25519 public key of the exporter.
	ExporterPublicKey string `json:"exporter_public_key" url:"-"`
	// BootstrapPeers lists direct seed peers exported in the bundle.
	BootstrapPeers []PeerInfo `json:"bootstrap_peers" url:"-"`
	// RoutingTable lists the exported Kademlia routing table snapshot.
	RoutingTable []PeerInfo `json:"routing_table" url:"-"`
	// Announcements lists the exported verified capability announcements.
	Announcements []CapabilityAnnouncement `json:"announcements" url:"-"`
	// StateDigest is the SHA-256 hex digest of the canonical bundle state payload.
	StateDigest string `json:"state_digest" url:"-"`
	// Signature is the hex-encoded Ed25519 signature over the bundle digest envelope.
	Signature string `json:"signature" url:"-"`
	// VerificationClass states the proof class of the exported bundle.
	VerificationClass VerificationClass `json:"verification_class" url:"-"`
}

func (in *verifyBundleIn) toBundle() MeshExportBundle {
	return MeshExportBundle{
		ProtocolVersion:   in.ProtocolVersion,
		ExportedAt:        in.ExportedAt,
		ExporterPeerID:    in.ExporterPeerID,
		ExporterPublicKey: in.ExporterPublicKey,
		BootstrapPeers:    in.BootstrapPeers,
		RoutingTable:      in.RoutingTable,
		Announcements:     in.Announcements,
		StateDigest:       in.StateDigest,
		Signature:         in.Signature,
		VerificationClass: in.VerificationClass,
	}
}

// verifyBundleResponse reports the outcome of verifying a signed MeshExportBundle.
type verifyBundleResponse struct {
	// Verified is true when the bundle digest, signature, and every announcement verified.
	Verified bool `json:"verified"`
	// ExporterPeerID is the authenticated PeerID that signed the bundle.
	ExporterPeerID PeerID `json:"exporterPeerId"`
	// StateDigest is the verified SHA-256 hex state digest of the bundle.
	StateDigest string `json:"stateDigest"`
	// AnnouncementCount is the number of verified capability announcements inside the bundle.
	AnnouncementCount int `json:"announcementCount"`
	// VerificationClass is the cryptographic verification class of the bundle.
	VerificationClass VerificationClass `json:"verificationClass"`
}

func (s *Server) buildHealth() healthResponse {
	now := s.nowUnix()
	return healthResponse{
		Service:               "p2pnet",
		Status:                "ok",
		ProtocolVersion:       ProtocolVersion,
		PeerID:                s.Node.ID.PeerID,
		NATMode:               s.Node.Config.NATMode,
		Role:                  s.Node.Config.Role,
		RoutingTableSize:      s.Node.Table.Size(),
		VerifiedAnnouncements: len(s.Node.Store.ListAnnouncements(now)),
		HanzoCalls:            s.Node.HanzoCalls(),
		HanzoRegistryDisabled: s.Node.Config.HanzoRegistryDisabled,
	}
}

func statusForError(err error) int {
	switch {
	case err == nil:
		return http.StatusOK
	case errors.Is(err, ErrInvalidSignature), errors.Is(err, ErrPeerIDMismatch), errors.Is(err, ErrInvalidPublicKey), errors.Is(err, ErrBundleTampered), errors.Is(err, ErrResponseMismatch):
		return http.StatusForbidden
	case errors.Is(err, ErrSequenceRollback), errors.Is(err, ErrReplayNonce):
		return http.StatusConflict
	case errors.Is(err, ErrSybilFloodDetected):
		return http.StatusTooManyRequests
	case errors.Is(err, ErrNoRouteFound), errors.Is(err, ErrRelayUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadRequest
	}
}

// ops binds Server to zip typed operation methods so every /v1/p2p/* endpoint
// enters zip's typed-operation registry and projects into OpenAPI, MCP, CLI,
// and generated SDKs.
type ops struct{ srv *Server }

// health reports liveness, peer identity, NAT mode, routing table size, and
// zero-registry sovereignty counters for the mounted P2P node.
func (o ops) health(_ context.Context, _ *noIn) (*healthResponse, error) {
	h := o.srv.buildHealth()
	return &h, nil
}

// peers returns this node's self-certifying PeerID alongside every peer
// currently tracked in its 256-bucket XOR Kademlia routing table.
func (o ops) peers(_ context.Context, _ *noIn) (*peersResponse, error) {
	return &peersResponse{
		PeerID: o.srv.Node.ID.PeerID,
		Peers:  o.srv.Node.Table.AllPeers(),
	}, nil
}

// listAnnouncements returns verified, non-expired capability announcements from
// the local DHT store, optionally filtered to providers advertising a model.
func (o ops) listAnnouncements(_ context.Context, in *announcementsIn) (*announcementsResponse, error) {
	model := strings.TrimSpace(in.Model)
	now := o.srv.nowUnix()
	var data []CapabilityAnnouncement
	if model != "" {
		data = o.srv.Node.Store.ProvidersForModel(model, now)
	} else {
		data = o.srv.Node.Store.ListAnnouncements(now)
	}
	return &announcementsResponse{Data: data}, nil
}

// submitAnnouncement verifies and ingests a signed CapabilityAnnouncement
// fail-closed, enforcing Ed25519 signature checks, anti-replay nonces,
// monotonic sequences, and live mutual-TLS 1.3 probes for unverified newcomers.
func (o ops) submitAnnouncement(ctx context.Context, in *submitAnnouncementIn) (*CapabilityAnnouncement, error) {
	ann := in.toAnnouncement()
	sender := PeerID(strings.TrimSpace(in.Sender))
	if err := o.srv.Node.SubmitAnnouncement(ctx, ann, sender); err != nil {
		return nil, zip.Errorf(statusForError(err), "%s", err.Error()).With(map[string]any{
			"reason": RejectReason(err),
		})
	}
	return &ann, nil
}

// findProviders executes an iterative Kademlia FIND_PROVIDERS lookup for a
// model and returns every locally verified, non-expired provider announcement.
func (o ops) findProviders(ctx context.Context, in *providersIn) (*providersResponse, error) {
	model := strings.TrimSpace(in.Model)
	if model == "" {
		return nil, zip.ErrBadRequest("model query parameter required")
	}
	provs := o.srv.Node.FindProviders(ctx, model)
	return &providersResponse{Model: model, Providers: provs}, nil
}

// routeInference discovers verified providers for the requested model via DHT
// and dispatches the inference request end-to-end with automatic failover.
func (o ops) routeInference(ctx context.Context, in *routeInferenceIn) (*RouteResult, error) {
	req := InferenceRequest{
		RequestID:       in.RequestID,
		ModelID:         in.ModelID,
		PromptTokens:    in.PromptTokens,
		MaxOutputTokens: in.MaxOutputTokens,
		PromptDigest:    in.PromptDigest,
		ClientPeerID:    in.ClientPeerID,
		Timestamp:       in.Timestamp,
	}
	res, err := o.srv.Node.RouteInference(ctx, req)
	if err != nil {
		return nil, zip.Errorf(statusForError(err), "%s", err.Error())
	}
	return &res, nil
}

// exportBundle creates an Ed25519-signed MeshExportBundle snapshot of the
// node's routing table, bootstrap seeds, and verified announcements for
// sovereign offline verification and zero-Hanzo cold-start forking.
func (o ops) exportBundle(_ context.Context, _ *noIn) (*MeshExportBundle, error) {
	bundle, err := o.srv.Node.ExportMeshBundle(o.srv.nowUnix())
	if err != nil {
		return nil, zip.ErrInternal(err.Error())
	}
	return &bundle, nil
}

// verifyBundle performs strict offline cryptographic verification of a signed
// MeshExportBundle's exporter PeerID binding, state digest, signature, and
// every contained capability announcement.
func (o ops) verifyBundle(_ context.Context, in *verifyBundleIn) (*verifyBundleResponse, error) {
	bundle := in.toBundle()
	if err := VerifyMeshBundle(bundle, o.srv.nowUnix(), o.srv.Node.Config.Policy); err != nil {
		return nil, zip.Errorf(statusForError(err), "%s", err.Error()).With(map[string]any{
			"verified": false,
		})
	}
	return &verifyBundleResponse{
		Verified:          true,
		ExporterPeerID:    bundle.ExporterPeerID,
		StateDigest:       bundle.StateDigest,
		AnnouncementCount: len(bundle.Announcements),
		VerificationClass: bundle.VerificationClass,
	}, nil
}

// telemetry returns a thread-safe snapshot of networking, gossip, relay,
// failover, and cryptographic verification counters for this P2P node.
func (o ops) telemetry(_ context.Context, _ *noIn) (*NodeTelemetry, error) {
	tel := o.srv.Node.Telemetry()
	return &tel, nil
}

// Mount attaches the HIP-1350 P2P discovery & routing API onto a *zip.App
// under /v1/p2p as typed operations with zero host dependencies.
func Mount(app *zip.App, _ luxlog.Logger, srv *Server) error {
	if app == nil {
		return errors.New("p2pnet: nil app")
	}
	if srv == nil || srv.Node == nil {
		return errors.New("p2pnet: nil server or node")
	}

	o := ops{srv: srv}
	g := app.Group("/v1/p2p").Tag("p2p")
	g.Get("/health", o.health)
	g.Get("/peers", o.peers)
	g.Get("/announcements", o.listAnnouncements)
	g.Post("/announcements", o.submitAnnouncement, zip.WithStatus(http.StatusCreated))
	g.Get("/providers", o.findProviders)
	g.Post("/route", o.routeInference)
	g.Get("/export", o.exportBundle)
	g.Post("/verify-bundle", o.verifyBundle)
	g.Get("/telemetry", o.telemetry)
	return nil
}

// Handler returns a standalone standard-library net/http handler serving the
// exact same /v1/p2p/* control surface for operators running without zip.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/p2p/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, s.buildHealth())
	})
	mux.HandleFunc("/v1/p2p/peers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, peersResponse{
			PeerID: s.Node.ID.PeerID,
			Peers:  s.Node.Table.AllPeers(),
		})
	})
	mux.HandleFunc("/v1/p2p/announcements", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			model := strings.TrimSpace(r.URL.Query().Get("model"))
			now := s.nowUnix()
			var data []CapabilityAnnouncement
			if model != "" {
				data = s.Node.Store.ProvidersForModel(model, now)
			} else {
				data = s.Node.Store.ListAnnouncements(now)
			}
			writeJSON(w, http.StatusOK, announcementsResponse{Data: data})
		case http.MethodPost:
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			var ann CapabilityAnnouncement
			if err := json.Unmarshal(body, &ann); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			sender := PeerID(strings.TrimSpace(r.Header.Get("X-P2P-Sender")))
			if err := s.Node.SubmitAnnouncement(r.Context(), ann, sender); err != nil {
				writeJSON(w, statusForError(err), map[string]string{"error": err.Error(), "reason": RejectReason(err)})
				return
			}
			writeJSON(w, http.StatusCreated, ann)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})
	mux.HandleFunc("/v1/p2p/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		model := strings.TrimSpace(r.URL.Query().Get("model"))
		if model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "model query parameter required"})
			return
		}
		provs := s.Node.FindProviders(r.Context(), model)
		writeJSON(w, http.StatusOK, providersResponse{Model: model, Providers: provs})
	})
	mux.HandleFunc("/v1/p2p/route", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var req InferenceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		res, err := s.Node.RouteInference(r.Context(), req)
		if err != nil {
			writeJSON(w, statusForError(err), map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, res)
	})
	mux.HandleFunc("/v1/p2p/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		bundle, err := s.Node.ExportMeshBundle(s.nowUnix())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, bundle)
	})
	mux.HandleFunc("/v1/p2p/verify-bundle", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		var bundle MeshExportBundle
		if err := json.Unmarshal(body, &bundle); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := VerifyMeshBundle(bundle, s.nowUnix(), s.Node.Config.Policy); err != nil {
			writeJSON(w, statusForError(err), map[string]any{"verified": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, verifyBundleResponse{
			Verified:          true,
			ExporterPeerID:    bundle.ExporterPeerID,
			StateDigest:       bundle.StateDigest,
			AnnouncementCount: len(bundle.Announcements),
			VerificationClass: bundle.VerificationClass,
		})
	})
	mux.HandleFunc("/v1/p2p/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, s.Node.Telemetry())
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
