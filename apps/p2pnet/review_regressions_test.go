// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Finding 1 regression: unsigned PeerInfo injected via GOSSIP_SYNC.Peers (even
// with spoofed LivenessVerified=true) MUST NOT bypass Sybil admission limits or
// SubmitAnnouncement's live mutual-TLS probe.
func TestGossipPeersCannotBypassSybilAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	victim, err := NewNode(NodeConfig{
		Role:                  RoleRouter,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := victim.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer victim.Shutdown()

	attacker, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := attacker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer attacker.Shutdown()

	const sybilCount = 53
	now := time.Now().Unix()
	fakePeers := make([]PeerInfo, 0, sybilCount)
	fakeAnns := make([]CapabilityAnnouncement, 0, sybilCount)
	for i := 0; i < sybilCount; i++ {
		id, err := GenerateIdentity(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		// Unreachable loopback port 1.
		addr := FormatMultiaddr("tcp", "127.0.0.1", 1, id.PeerID)
		fakePeers = append(fakePeers, PeerInfo{
			PeerID:           id.PeerID,
			PublicKey:        id.PublicKeyHex(),
			KeyHash:          id.KeyHash,
			Addrs:            []string{addr},
			NATMode:          NATDirect,
			Role:             RoleProvider,
			LastSeen:         now,
			LivenessVerified: true, // Spoofed wire flag!
		})
		ann, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
			Addrs:     []string{addr},
			NATMode:   NATDirect,
			Role:      RoleProvider,
			Models:    []ModelCapability{{ModelID: "sybil-model", PriceMicroUSDPer1MTokens: 100}},
			Seq:       1,
			Nonce:     fmt.Sprintf("sybil-gossip-%d", i),
			IssuedAt:  now,
			ExpiresAt: now + 300,
		})
		if err != nil {
			t.Fatal(err)
		}
		fakeAnns = append(fakeAnns, ann)
	}

	// Step 1: Poison victim's routing table via GOSSIP_SYNC.Peers carrying 53 unreachable identities.
	poisonPayload, _ := json.Marshal(GossipSyncRequest{Peers: fakePeers})
	resp, _, _, err := attacker.callPeer(ctx, victim.PeerInfo(), RPCEnvelope{
		Type:    RPCGossipSync,
		Payload: poisonPayload,
	})
	if err != nil || resp.Error != "" {
		t.Fatalf("gossip poison rpc failed: err=%v respErr=%s", err, resp.Error)
	}

	// Every gossiped peer in victim.Table MUST have LivenessVerified == false.
	for _, fp := range fakePeers {
		if got, ok := victim.Table.GetPeer(fp.PeerID); ok && got.LivenessVerified {
			t.Fatalf("gossiped unreachable peer %s wrongly marked LivenessVerified=true", fp.PeerID)
		}
	}

	// Step 2a: SubmitAnnouncement MUST still probe and reject all 53 unreachable identities.
	submitRejected := 0
	for _, ann := range fakeAnns {
		if err := victim.SubmitAnnouncement(ctx, ann, attacker.ID.PeerID); errors.Is(err, ErrSybilFloodDetected) {
			submitRejected++
		} else {
			t.Fatalf("expected ErrSybilFloodDetected for unreachable gossiped peer %s, got %v", ann.PeerID, err)
		}
	}
	if submitRejected != sybilCount {
		t.Fatalf("expected all %d unreachable identities rejected by SubmitAnnouncement, got %d", sybilCount, submitRejected)
	}

	// Step 2b: Sending all 53 via GOSSIP_SYNC.Announcements MUST enforce the per-sender admission cap.
	annPayload, _ := json.Marshal(GossipSyncRequest{Announcements: fakeAnns})
	resp, _, _, err = attacker.callPeer(ctx, victim.PeerInfo(), RPCEnvelope{
		Type:    RPCGossipSync,
		Payload: annPayload,
	})
	if err != nil || resp.Error != "" {
		t.Fatalf("gossip announcements rpc failed: err=%v respErr=%s", err, resp.Error)
	}
	var gresp GossipSyncResponse
	if err := json.Unmarshal(resp.Payload, &gresp); err != nil {
		t.Fatalf("decode gossip response: %v", err)
	}
	wantMinReject := sybilCount - DefaultMaxNewPeersPerSenderWindow
	if gresp.AcceptedCount > DefaultMaxNewPeersPerSenderWindow || gresp.RejectedCount < wantMinReject {
		t.Fatalf("Sybil gossip burst bypassed cap: accepted=%d rejected=%d (cap=%d)",
			gresp.AcceptedCount, gresp.RejectedCount, DefaultMaxNewPeersPerSenderWindow)
	}
}

// Finding 2 regression: a TLS-authenticated untrusted circuit relay replaying a
// previously captured provider-signed InferenceResponse for another request or
// model MUST be rejected under strict verification, and RouteInference MUST
// fail over to an honest relay when available.
func TestCircuitRelayReplayAttackRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Unit-level request binding checks.
	provID, _ := GenerateIdentity(rand.Reader)
	signedOld, err := SignInferenceResponse(provID, InferenceResponse{
		RequestID:       "old-req-1",
		ModelID:         "model-alpha",
		TokensGenerated: 16,
		TTFTMs:          0.2,
		TotalLatencyMs:  0.5,
		CostMicroUSD:    10,
		OutputDigest:    "deadbeef",
		Timestamp:       time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyInferenceResponseForRequest(signedOld, InferenceRequest{RequestID: "new-req-2", ModelID: "model-alpha"}, provID.PeerID); !errors.Is(err, ErrResponseMismatch) {
		t.Fatalf("expected ErrResponseMismatch on RequestID mismatch, got %v", err)
	}
	if err := VerifyInferenceResponseForRequest(signedOld, InferenceRequest{RequestID: "old-req-1", ModelID: "model-beta"}, provID.PeerID); !errors.Is(err, ErrResponseMismatch) {
		t.Fatalf("expected ErrResponseMismatch on ModelID mismatch, got %v", err)
	}

	// End-to-end TLS-authenticated malicious relay replay reproduction.
	honestRelay, err := NewNode(NodeConfig{
		Role:                  RoleRelay,
		NATMode:               NATRelayServer,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := honestRelay.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer honestRelay.Shutdown()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATRelayed,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{
			{ModelID: "model-alpha", MaxContextTokens: 8192, EstTokPerSec: 100, BaseLatencyMs: 5, PriceMicroUSDPer1MTokens: 100000},
			{ModelID: "model-beta", MaxContextTokens: 8192, EstTokPerSec: 100, BaseLatencyMs: 5, PriceMicroUSDPer1MTokens: 100000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	// Capture a genuine provider-signed response for model-alpha / old-req-1.
	capturedResp, err := provider.executeInference(InferenceRequest{
		RequestID:       "captured-req-1",
		ModelID:         "model-alpha",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "old-prompt",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	capturedPayload, _ := json.Marshal(capturedResp)

	// Start a real mutual-TLS 1.3 malicious relay that replays capturedResp on every RPCRelayForward.
	malRelayID, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	malCert, err := GeneratePeerTLSCert(malRelayID)
	if err != nil {
		t.Fatal(err)
	}
	malLn, err := tls.Listen("tcp4", "127.0.0.1:0", ServerTLSConfig(malCert))
	if err != nil {
		t.Fatal(err)
	}
	defer malLn.Close()

	var malWg sync.WaitGroup
	malWg.Add(1)
	go func() {
		defer malWg.Done()
		for {
			conn, err := malLn.Accept()
			if err != nil {
				return
			}
			go func(c *tls.Conn) {
				defer c.Close()
				if err := c.Handshake(); err != nil {
					return
				}
				req, _, err := readFrame(c)
				if err != nil {
					return
				}
				innerResp := RPCEnvelope{
					RequestID: req.RequestID,
					Type:      RPCInferRequest,
					FromPeer:  provider.ID.PeerID,
					ToPeer:    req.FromPeer,
					Payload:   capturedPayload,
				}
				outPayload, _ := json.Marshal(innerResp)
				_, _ = writeFrame(c, RPCEnvelope{
					RequestID: req.RequestID,
					Type:      RPCRelayForward,
					FromPeer:  malRelayID.PeerID,
					ToPeer:    req.FromPeer,
					Payload:   outPayload,
				})
			}(conn.(*tls.Conn))
		}
	}()
	defer func() {
		_ = malLn.Close()
		malWg.Wait()
	}()

	var malPort int
	_, _ = fmt.Sscanf(strings.Split(malLn.Addr().String(), ":")[1], "%d", &malPort)
	malRelayAddr := FormatMultiaddr("tcp", "127.0.0.1", malPort, malRelayID.PeerID)
	malRelayPeer := PeerInfo{
		PeerID:    malRelayID.PeerID,
		PublicKey: malRelayID.PublicKeyHex(),
		KeyHash:   malRelayID.KeyHash,
		Addrs:     []string{malRelayAddr},
		NATMode:   NATRelayServer,
		Role:      RoleRelay,
		LastSeen:  time.Now().Unix(),
	}

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	// Case 1: Only the malicious relay is advertised -> replay MUST be rejected with ErrNoRouteFound.
	client.Table.UpsertPeer(malRelayPeer)
	now := time.Now().Unix()
	malOnlyAnn, err := SignCapabilityAnnouncement(provider.ID, CapabilityAnnouncement{
		RelayAddrs: []string{FormatCircuitMultiaddr(malRelayID.PeerID, provider.ID.PeerID)},
		NATMode:    NATRelayed,
		Role:       RoleProvider,
		Models:     provider.Config.Models,
		Seq:        1,
		Nonce:      "mal-only-1",
		IssuedAt:   now,
		ExpiresAt:  now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.IngestAnnouncement(malOnlyAnn, "", true); err != nil {
		t.Fatal(err)
	}

	if _, err := client.RouteInference(ctx, InferenceRequest{
		RequestID:       "fresh-req-beta",
		ModelID:         "model-beta",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "new-prompt",
	}); !errors.Is(err, ErrNoRouteFound) {
		t.Fatalf("expected replayed relay response rejected with ErrNoRouteFound, got %v", err)
	}
	// Provider announcement MUST still be retained (Finding 4 synergy).
	if _, ok := client.Store.GetAnnouncement(provider.ID.PeerID, now); !ok {
		t.Fatal("malicious relay replay should not permanently delete honest provider's announcement")
	}

	// Case 2: Provider also advertises an honest relay -> client rejects malicious relay and fails over to honest relay!
	if _, err := provider.RegisterWithRelay(ctx, honestRelay.PeerInfo()); err != nil {
		t.Fatal(err)
	}
	client.Table.UpsertPeer(honestRelay.PeerInfo())
	bothRelaysAnn, err := SignCapabilityAnnouncement(provider.ID, CapabilityAnnouncement{
		RelayAddrs: []string{
			FormatCircuitMultiaddr(malRelayID.PeerID, provider.ID.PeerID),
			FormatCircuitMultiaddr(honestRelay.ID.PeerID, provider.ID.PeerID),
		},
		NATMode:   NATRelayed,
		Role:      RoleProvider,
		Models:    provider.Config.Models,
		Seq:       2,
		Nonce:     "both-relays-2",
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.IngestAnnouncement(bothRelaysAnn, "", true); err != nil {
		t.Fatal(err)
	}

	req := InferenceRequest{
		RequestID:       "fresh-req-beta-failover",
		ModelID:         "model-beta",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "new-prompt-2",
	}
	res, err := client.RouteInference(ctx, req)
	if err != nil {
		t.Fatalf("expected failover to honest relay to succeed, got %v", err)
	}
	if res.RelayPeerID != honestRelay.ID.PeerID {
		t.Fatalf("expected honest relay %s, got %s", honestRelay.ID.PeerID, res.RelayPeerID)
	}
	if res.Response.RequestID != req.RequestID || res.Response.ModelID != req.ModelID {
		t.Fatalf("response mismatch: got req=%s model=%s", res.Response.RequestID, res.Response.ModelID)
	}
}

// Finding 3 regression: a live provider MUST renew and republish its
// CapabilityAnnouncement before TTL expires so routing does not fail after the
// initial 300-second TTL elapses.
func TestProviderAnnouncementAutoRenewalBeforeTTLExpiry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var fakeNow atomic.Int64
	fakeNow.Store(1_800_000_000)
	nowFn := func() int64 { return fakeNow.Load() }

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		AnnouncementTTLSec:    300,
		MaintenanceInterval:   25 * time.Millisecond,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Now:                   nowFn,
		Models: []ModelCapability{{
			ModelID:                  "ttl-renewal-model",
			MaxContextTokens:         8192,
			EstTokPerSec:             100,
			BaseLatencyMs:            5,
			PriceMicroUSDPer1MTokens: 100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		AnnouncementTTLSec:    300,
		MaintenanceInterval:   25 * time.Millisecond,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Now:                   nowFn,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	if err := client.Bootstrap(ctx, []PeerInfo{provider.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	initialSeq := provider.SelfAnnouncement().Seq

	// Confirm initial routing works.
	if _, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:         "ttl-renewal-model",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "before-ttl",
	}); err != nil {
		t.Fatalf("pre-expiry RouteInference failed: %v", err)
	}

	// Advance clock strictly beyond the 300s TTL.
	fakeNow.Add(350)

	// Wait for the provider's lifecycle maintenance loop to automatically renew and republish.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ann, ok := client.Store.GetAnnouncement(provider.ID.PeerID, fakeNow.Load())
		if ok && ann.Seq > initialSeq {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if gotSeq := provider.SelfAnnouncement().Seq; gotSeq <= initialSeq {
		t.Fatalf("expected provider to renew announcement sequence past %d, still at %d", initialSeq, gotSeq)
	}
	if _, err := client.Ping(ctx, provider.PeerInfo()); err != nil {
		t.Fatalf("Ping after TTL failed: %v", err)
	}
	res, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:         "ttl-renewal-model",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "after-ttl",
	})
	if err != nil {
		t.Fatalf("post-TTL RouteInference failed (should have auto-renewed): %v", err)
	}
	if res.Response.ServedByPeerID != provider.ID.PeerID {
		t.Fatalf("unexpected provider %s", res.Response.ServedByPeerID)
	}
}

// Finding 4 regression: a cancelled request or transient RPC failure MUST NOT
// permanently suppress a reachable provider until a higher sequence is published,
// and RemoveAnnouncement MUST allow safe rehydration of an identical verified
// record while still rejecting rollbacks and nonce replays.
func TestTransientFailureAndCancellationDoNotPermanentlySuppressProvider(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{{
			ModelID:                  "transient-model",
			MaxContextTokens:         8192,
			EstTokPerSec:             100,
			BaseLatencyMs:            5,
			PriceMicroUSDPer1MTokens: 100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	seed, err := NewNode(NodeConfig{
		Role:                  RoleBootstrap,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer seed.Shutdown()

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	if err := provider.Bootstrap(ctx, []PeerInfo{seed.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	if err := client.Bootstrap(ctx, []PeerInfo{seed.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	initialSeq := provider.SelfAnnouncement().Seq

	// 1. Cancelled RouteInference must NOT evict the provider's announcement.
	canceledCtx, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err := client.RouteInference(canceledCtx, InferenceRequest{
		ModelID:      "transient-model",
		PromptDigest: "cancelled",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, ok := client.Store.GetAnnouncement(provider.ID.PeerID, time.Now().Unix()); !ok {
		t.Fatal("cancelled request removed provider announcement from store")
	}

	// Subsequent normal RouteInference MUST succeed on the exact same sequence!
	if _, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:         "transient-model",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "post-cancel",
	}); err != nil {
		t.Fatalf("RouteInference failed after cancellation: %v", err)
	}
	if provider.SelfAnnouncement().Seq != initialSeq {
		t.Fatalf("expected same sequence %d without requiring republish, got %d", initialSeq, provider.SelfAnnouncement().Seq)
	}

	// 2. Explicit RemoveAnnouncement + gossip/discovery rehydration of identical verified record.
	origAnn := provider.SelfAnnouncement()
	client.Store.RemoveAnnouncement(provider.ID.PeerID)
	if client.Store.HasDigest(origAnn) {
		t.Fatal("HasDigest must be false after RemoveAnnouncement so gossip/discovery can rehydrate")
	}
	// Re-discover from seed holding the exact same seq=1 announcement.
	provs := client.FindProviders(ctx, "transient-model")
	if len(provs) == 0 {
		t.Fatal("FindProviders failed to rehydrate identical verified announcement after RemoveAnnouncement")
	}
	if _, err := client.Ping(ctx, provider.PeerInfo()); err != nil {
		t.Fatalf("Ping provider failed: %v", err)
	}
	if _, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:         "transient-model",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "post-rehydrate",
	}); err != nil {
		t.Fatalf("RouteInference failed after rehydration: %v", err)
	}

	// 3. Anti-replay and rollback protection MUST still reject any different announcement at <= Seq or reused Nonce.
	now := time.Now().Unix()
	diffAtSameSeq, err := SignCapabilityAnnouncement(provider.ID, CapabilityAnnouncement{
		Addrs:     provider.Multiaddrs(),
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Models:    provider.Config.Models,
		Seq:       origAnn.Seq,
		Nonce:     "different-nonce-same-seq",
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	client.Store.RemoveAnnouncement(provider.ID.PeerID)
	if err := client.Store.PutAnnouncement(diffAtSameSeq, seed.ID.PeerID, true, now); !errors.Is(err, ErrSequenceRollback) {
		t.Fatalf("expected ErrSequenceRollback for different announcement at same seq after removal, got %v", err)
	}
	reusedNonceHigherSeq, err := SignCapabilityAnnouncement(provider.ID, CapabilityAnnouncement{
		Addrs:     provider.Multiaddrs(),
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Models:    provider.Config.Models,
		Seq:       origAnn.Seq + 10,
		Nonce:     origAnn.Nonce,
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Store.PutAnnouncement(reusedNonceHigherSeq, seed.ID.PeerID, true, now); !errors.Is(err, ErrReplayNonce) {
		t.Fatalf("expected ErrReplayNonce for reused nonce with different announcement, got %v", err)
	}
}

// Finding 5 regression: Mount MUST register all 9 /v1/p2p/* JSON endpoints as
// zip typed operations so they appear in OpenAPI and MCP projections with full
// schemas, descriptions, header parameters, status codes, and url:"-" isolation.
func TestZipMountRegistersTypedOpsInOpenAPIAndMCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{{
			ModelID:                  "typed-op-model",
			MaxContextTokens:         8192,
			EstTokPerSec:             100,
			BaseLatencyMs:            5,
			PriceMicroUSDPer1MTokens: 100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	srv, err := NewServer(provider)
	if err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{AppName: "p2pnet-test", DisableStartupMessage: true})
	if err := Mount(app, luxlog.New("test"), srv); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	// 1. Inspect projected OpenAPI 3.1 document from the live router.
	rawSpec, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal OpenAPISpec: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rawSpec, &doc); err != nil {
		t.Fatalf("decode OpenAPISpec: %v", err)
	}

	expectedOps := []string{
		"GET /v1/p2p/health",
		"GET /v1/p2p/peers",
		"GET /v1/p2p/announcements",
		"POST /v1/p2p/announcements",
		"GET /v1/p2p/providers",
		"POST /v1/p2p/route",
		"GET /v1/p2p/export",
		"POST /v1/p2p/verify-bundle",
		"GET /v1/p2p/telemetry",
	}
	served := make(map[string]map[string]any)
	for p, item := range doc.Paths {
		if !strings.HasPrefix(p, "/v1/p2p") {
			continue
		}
		for method, op := range item {
			served[strings.ToUpper(method)+" "+p] = op
		}
	}
	if len(served) != len(expectedOps) {
		t.Fatalf("expected %d typed OpenAPI ops under /v1/p2p, got %d: %v", len(expectedOps), len(served), served)
	}
	for _, key := range expectedOps {
		op, ok := served[key]
		if !ok {
			t.Fatalf("missing typed OpenAPI op %s", key)
		}
		if desc, _ := op["description"].(string); strings.TrimSpace(desc) == "" {
			t.Errorf("typed op %s has empty OpenAPI description (regenerate zipdoc_gen.go)", key)
		}
	}

	// Verify POST /v1/p2p/announcements declares 201 Created and X-P2P-Sender header.
	postAnn := served["POST /v1/p2p/announcements"]
	responses, _ := postAnn["responses"].(map[string]any)
	if _, ok := responses["201"]; !ok {
		t.Fatalf("POST /v1/p2p/announcements must declare 201 response in OpenAPI, got %v", responses)
	}
	rawParams, _ := json.Marshal(postAnn["parameters"])
	if !strings.Contains(string(rawParams), `"X-P2P-Sender"`) {
		t.Fatalf("POST /v1/p2p/announcements must declare X-P2P-Sender header parameter, got %s", rawParams)
	}

	// 2. Inspect projected MCP tools via JSON-RPC 2.0 tools/list on /mcp.
	mcpReq := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	mcpReq.Header.Set("Content-Type", "application/json")
	mcpResp, err := app.Test(mcpReq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST /mcp tools/list: %v", err)
	}
	defer mcpResp.Body.Close()
	mcpBody, _ := io.ReadAll(mcpResp.Body)
	if mcpResp.StatusCode != http.StatusOK {
		t.Fatalf("MCP tools/list status=%d body=%s", mcpResp.StatusCode, mcpBody)
	}
	var mcpOut struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(mcpBody, &mcpOut); err != nil {
		t.Fatalf("decode MCP tools/list: %v (%s)", err, mcpBody)
	}
	if len(mcpOut.Result.Tools) != len(expectedOps) {
		t.Fatalf("expected %d MCP tools projected from typed ops, got %d (%s)", len(expectedOps), len(mcpOut.Result.Tools), mcpBody)
	}
	for _, tool := range mcpOut.Result.Tools {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("MCP tool %q has empty description", tool.Name)
		}
	}

	// 3. Query string MUST NOT override POST body fields (url:"-").
	routeBody, _ := json.Marshal(InferenceRequest{
		ModelID:         "typed-op-model",
		PromptTokens:    16,
		MaxOutputTokens: 8,
		PromptDigest:    "url-isolation",
	})
	routeReq := httptest.NewRequest(http.MethodPost, "/v1/p2p/route?model_id=hijacked-model", bytes.NewReader(routeBody))
	routeReq.Header.Set("Content-Type", "application/json")
	routeResp, err := app.Test(routeReq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST /v1/p2p/route: %v", err)
	}
	defer routeResp.Body.Close()
	if routeResp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(routeResp.Body)
		t.Fatalf("query string redirected POST /v1/p2p/route: status=%d body=%s", routeResp.StatusCode, rb)
	}

	// 4. Valid POST /v1/p2p/announcements returns 201 Created.
	newProv, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models:                provider.Config.Models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := newProv.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer newProv.Shutdown()
	annBody, _ := json.Marshal(newProv.SelfAnnouncement())
	postReq := httptest.NewRequest(http.MethodPost, "/v1/p2p/announcements", bytes.NewReader(annBody))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("X-P2P-Sender", string(newProv.ID.PeerID))
	postResp, err := app.Test(postReq, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST /v1/p2p/announcements: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != http.StatusCreated {
		pb, _ := io.ReadAll(postResp.Body)
		t.Fatalf("expected 201 Created for valid announcement, got %d (%s)", postResp.StatusCode, pb)
	}
}

// Finding 6 regression: reusing NodeConfig.Identity across a restart (in-process,
// via durable FileSequenceStore, or via cold mesh catch-up) MUST preserve
// monotonic announcement sequences so existing DHT stores accept the restarted
// node's replacement announcement and newly bound socket addresses.
func TestIdentityReusePreservesAnnouncementSequenceAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	provID, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seqStore := NewFileSequenceStore(filepath.Join(t.TempDir(), "p2p_sequences.json"))
	models := []ModelCapability{{
		ModelID:                  "restart-model",
		MaxContextTokens:         8192,
		EstTokPerSec:             100,
		BaseLatencyMs:            5,
		PriceMicroUSDPer1MTokens: 100000,
	}}

	// Boot 1: initial provider binds port 1 and publishes Seq 1.
	prov1, err := NewNode(NodeConfig{
		Identity:              provID,
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		SequenceStore:         seqStore,
		Models:                models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prov1.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Bootstrap(ctx, []PeerInfo{prov1.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	seq1 := prov1.SelfAnnouncement().Seq
	addr1 := prov1.Multiaddrs()[0]
	if _, err := client.RouteInference(ctx, InferenceRequest{ModelID: "restart-model", PromptDigest: "boot1"}); err != nil {
		t.Fatalf("boot1 RouteInference failed: %v", err)
	}
	prov1.Shutdown()

	// Boot 2: in-process restart reusing the exact same *Identity (binds a brand-new ephemeral port).
	prov2, err := NewNode(NodeConfig{
		Identity:              provID,
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models:                models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prov2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	seq2 := prov2.SelfAnnouncement().Seq
	addr2 := prov2.Multiaddrs()[0]
	if seq2 <= seq1 {
		t.Fatalf("expected restarted node reusing Identity to advance seq beyond %d, got %d", seq1, seq2)
	}
	if addr2 == addr1 {
		t.Fatalf("expected restarted node to bind a fresh ephemeral port, got same %s", addr2)
	}
	if err := prov2.Bootstrap(ctx, []PeerInfo{client.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	prov2.PublishAnnouncement(ctx)
	if _, err := client.Ping(ctx, prov2.PeerInfo()); err != nil {
		t.Fatalf("Ping prov2 failed: %v", err)
	}
	if _, err := client.RouteInference(ctx, InferenceRequest{ModelID: "restart-model", PromptDigest: "boot2"}); err != nil {
		t.Fatalf("boot2 RouteInference failed after identity reuse restart: %v", err)
	}
	prov2.Shutdown()

	// Boot 3: cross-process restart using a freshly reconstructed *Identity + FileSequenceStore.
	reconstructedID3 := &Identity{
		PeerID:     provID.PeerID,
		PublicKey:  provID.PublicKey,
		PrivateKey: provID.PrivateKey,
		KeyHash:    provID.KeyHash,
	}
	// Persist seq2 first to simulate prov2 also running with seqStore.
	if err := seqStore.SaveSequence(provID.PeerID, seq2); err != nil {
		t.Fatal(err)
	}
	prov3, err := NewNode(NodeConfig{
		Identity:              reconstructedID3,
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		SequenceStore:         seqStore,
		Models:                models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prov3.Start(ctx); err != nil {
		t.Fatal(err)
	}
	seq3 := prov3.SelfAnnouncement().Seq
	if seq3 <= seq2 {
		t.Fatalf("expected FileSequenceStore restart to advance seq beyond %d, got %d", seq2, seq3)
	}
	if err := prov3.Bootstrap(ctx, []PeerInfo{client.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	prov3.PublishAnnouncement(ctx)
	if _, err := client.RouteInference(ctx, InferenceRequest{ModelID: "restart-model", PromptDigest: "boot3"}); err != nil {
		t.Fatalf("boot3 RouteInference failed after FileSequenceStore restart: %v", err)
	}
	prov3.Shutdown()

	// Boot 4: cold restart with a brand-new *Identity struct and NO SequenceStore -> mesh self-signed catch-up!
	reconstructedID4 := &Identity{
		PeerID:     provID.PeerID,
		PublicKey:  provID.PublicKey,
		PrivateKey: provID.PrivateKey,
		KeyHash:    provID.KeyHash,
	}
	prov4, err := NewNode(NodeConfig{
		Identity:              reconstructedID4,
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models:                models,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := prov4.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer prov4.Shutdown()
	if err := prov4.Bootstrap(ctx, []PeerInfo{client.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	prov4.PublishAnnouncement(ctx)
	if gotSeq4 := prov4.SelfAnnouncement().Seq; gotSeq4 <= seq3 {
		t.Fatalf("expected cold restart mesh catch-up to advance seq beyond %d, got %d", seq3, gotSeq4)
	}
	if _, err := client.RouteInference(ctx, InferenceRequest{ModelID: "restart-model", PromptDigest: "boot4"}); err != nil {
		t.Fatalf("boot4 RouteInference failed after cold mesh catch-up: %v", err)
	}
}

// Finding 7 regression: circuit-relayed inference responses returned in
// RouteResult.Response MUST remain unmodified after verification so callers can
// independently verify res.Response with VerifyInferenceResponse.
func TestCircuitRelayedInferenceResponseSignatureRemainsValid(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	relay, err := NewNode(NodeConfig{
		Role:                  RoleRelay,
		NATMode:               NATRelayServer,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer relay.Shutdown()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATRelayed,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{{
			ModelID:                  "relayed-sig-model",
			MaxContextTokens:         8192,
			EstTokPerSec:             100,
			BaseLatencyMs:            5,
			PriceMicroUSDPer1MTokens: 100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	seeds := []PeerInfo{relay.PeerInfo()}
	if err := provider.Bootstrap(ctx, seeds); err != nil {
		t.Fatal(err)
	}
	if err := client.Bootstrap(ctx, seeds); err != nil {
		t.Fatal(err)
	}

	req := InferenceRequest{
		RequestID:       "relayed-sig-check-1",
		ModelID:         "relayed-sig-model",
		PromptTokens:    32,
		MaxOutputTokens: 16,
		PromptDigest:    "verify-sig-intact",
	}
	res, err := client.RouteInference(ctx, req)
	if err != nil {
		t.Fatalf("RouteInference via relay failed: %v", err)
	}
	if !res.UsedRelay || res.RelayPeerID != relay.ID.PeerID {
		t.Fatalf("expected relayed route via %s, got used=%v relay=%s", relay.ID.PeerID, res.UsedRelay, res.RelayPeerID)
	}
	if err := VerifyInferenceResponse(res.Response, provider.ID.PeerID); err != nil {
		t.Fatalf("returned RouteResult.Response failed VerifyInferenceResponse: %v", err)
	}
	if err := VerifyInferenceResponseForRequest(res.Response, req, provider.ID.PeerID); err != nil {
		t.Fatalf("returned RouteResult.Response failed VerifyInferenceResponseForRequest: %v", err)
	}
}

// Round-2 Finding 1 regression: a circuit relay that preserves RequestID and
// ModelID while modifying PromptDigest or MaxOutputTokens before forwarding to
// an honest provider MUST be rejected by VerifyInferenceResponseForRequest and
// RouteInference.
func TestCircuitRelayRequestTamperingRejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Unit-level binding check on PromptDigest and MaxOutputTokens.
	provID, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	origReq := InferenceRequest{
		RequestID:       "req-tamper-1",
		ModelID:         "model-tamper",
		PromptTokens:    32,
		MaxOutputTokens: 8,
		PromptDigest:    "honest-prompt-digest",
	}
	tamperedReq := origReq
	tamperedReq.PromptDigest = "evil-prompt-digest"
	tamperedReq.MaxOutputTokens = 1

	respForTampered, err := SignInferenceResponse(provID, InferenceResponse{
		RequestID:       tamperedReq.RequestID,
		ModelID:         tamperedReq.ModelID,
		RequestDigest:   InferenceRequestDigest(tamperedReq),
		TokensGenerated: 1,
		TTFTMs:          0.1,
		TotalLatencyMs:  0.2,
		CostMicroUSD:    1,
		OutputDigest:    "abcd1234",
		Timestamp:       time.Now().Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyInferenceResponseForRequest(respForTampered, origReq, provID.PeerID); !errors.Is(err, ErrResponseMismatch) {
		t.Fatalf("expected ErrResponseMismatch when relay tampers PromptDigest/MaxOutputTokens, got %v", err)
	}

	// End-to-end loopback reproduction: a TLS-authenticated relay intercepts
	// RPCInferRequest, mutates PromptDigest and reduces MaxOutputTokens 8 -> 1,
	// forwards to the honest provider, and returns the provider's signed reply.
	tamperRelayID, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tamperCert, err := GeneratePeerTLSCert(tamperRelayID)
	if err != nil {
		t.Fatal(err)
	}
	tamperLn, err := tls.Listen("tcp4", "127.0.0.1:0", ServerTLSConfig(tamperCert))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tamperLn.Close() }()
	tamperAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", tamperLn.Addr().(*net.TCPAddr).Port, tamperRelayID.PeerID)

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATRelayed,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{
			{ModelID: "model-tamper", MaxContextTokens: 8192, EstTokPerSec: 100, BaseLatencyMs: 5, PriceMicroUSDPer1MTokens: 100000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	var sessMu sync.Mutex
	sessions := make(map[PeerID]*relaySession)

	go func() {
		for {
			conn, err := tamperLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				tlsConn := c.(*tls.Conn)
				if err := tlsConn.Handshake(); err != nil {
					_ = c.Close()
					return
				}
				env, _, err := readFrame(c)
				if err != nil {
					_ = c.Close()
					return
				}
				switch env.Type {
				case RPCRelayRegister:
					circuitAddr := FormatCircuitMultiaddr(tamperRelayID.PeerID, env.FromPeer)
					payload, _ := json.Marshal(RelayRegisterResponse{Accepted: true, CircuitAddr: circuitAddr})
					_, _ = writeFrame(c, RPCEnvelope{
						RequestID: env.RequestID,
						Type:      env.Type,
						FromPeer:  tamperRelayID.PeerID,
						ToPeer:    env.FromPeer,
						Payload:   payload,
					})
					sessMu.Lock()
					sessions[env.FromPeer] = &relaySession{peerID: env.FromPeer, conn: c}
					sessMu.Unlock()
				case RPCRelayForward:
					defer func() { _ = c.Close() }()
					var rf RelayForwardRequest
					if err := json.Unmarshal(env.Payload, &rf); err != nil {
						return
					}
					// Tamper with PromptDigest and MaxOutputTokens while preserving RequestID and ModelID!
					if rf.Inner.Type == RPCInferRequest {
						var ireq InferenceRequest
						if err := json.Unmarshal(rf.Inner.Payload, &ireq); err == nil {
							ireq.PromptDigest = "relay-tampered-prompt"
							ireq.MaxOutputTokens = 1
							rf.Inner.Payload, _ = json.Marshal(ireq)
						}
					}
					sessMu.Lock()
					sess := sessions[rf.TargetPeer]
					sessMu.Unlock()
					if sess == nil {
						return
					}
					fwdResp, _, _, err := sess.forward(ctx, rf.Inner)
					if err != nil {
						return
					}
					out, _ := json.Marshal(fwdResp)
					_, _ = writeFrame(c, RPCEnvelope{
						RequestID: env.RequestID,
						Type:      env.Type,
						FromPeer:  tamperRelayID.PeerID,
						ToPeer:    env.FromPeer,
						Payload:   out,
					})
				default:
					_ = c.Close()
				}
			}(conn)
		}
	}()

	tamperPeer := PeerInfo{
		PeerID:           tamperRelayID.PeerID,
		PublicKey:        tamperRelayID.PublicKeyHex(),
		Addrs:            []string{tamperAddr},
		NATMode:          NATRelayServer,
		Role:             RoleRelay,
		LivenessVerified: true,
	}
	if _, err := provider.RegisterWithRelay(ctx, tamperPeer); err != nil {
		t.Fatalf("RegisterWithRelay(tamperRelay): %v", err)
	}
	ann, err := provider.RefreshSelfAnnouncement()
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()
	client.Table.UpsertPeer(tamperPeer)
	if err := client.IngestAnnouncement(ann, tamperRelayID.PeerID, true); err != nil {
		t.Fatal(err)
	}

	// RouteInference must reject the honest provider's response to the relay-tampered request!
	if _, err := client.RouteInference(ctx, InferenceRequest{
		RequestID:       "req-e2e-tamper",
		ModelID:         "model-tamper",
		PromptTokens:    32,
		MaxOutputTokens: 8,
		PromptDigest:    "honest-client-prompt",
	}); err == nil {
		t.Fatalf("expected RouteInference to reject relay-tampered PromptDigest/MaxOutputTokens")
	}
}

// Round-2 Finding 2 regression: an arbitrary TLS-authenticated neighbor sending
// GOSSIP_SYNC.Peers with a live relay's PeerID/PublicKey and a bogus TCP address
// MUST NOT overwrite the validated relay endpoint or break inference routing.
func TestGossipSyncCannotPoisonValidatedRelayEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relay, err := NewNode(NodeConfig{
		Role:                  RoleRelay,
		NATMode:               NATRelayServer,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer relay.Shutdown()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATRelayed,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{
			{ModelID: "gossip-guard-model", MaxContextTokens: 8192, EstTokPerSec: 100, BaseLatencyMs: 5, PriceMicroUSDPer1MTokens: 100000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()

	attacker, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := attacker.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer attacker.Shutdown()

	seeds := []PeerInfo{relay.PeerInfo()}
	if err := provider.Bootstrap(ctx, seeds); err != nil {
		t.Fatal(err)
	}
	if err := client.Bootstrap(ctx, seeds); err != nil {
		t.Fatal(err)
	}

	// Verify baseline relayed inference works.
	if _, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:      "gossip-guard-model",
		PromptDigest: "before-poison",
	}); err != nil {
		t.Fatalf("baseline RouteInference failed: %v", err)
	}

	// Attacker sends GOSSIP_SYNC.Peers containing relay's genuine PeerID/PublicKey
	// paired with a bogus dead TCP address.
	poisonedRelay := PeerInfo{
		PeerID:           relay.ID.PeerID,
		PublicKey:        relay.ID.PublicKeyHex(),
		Addrs:            []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/1/p2p/%s", relay.ID.PeerID)},
		NATMode:          NATRelayServer,
		Role:             RoleRelay,
		LivenessVerified: true,
	}
	gossipPayload, _ := json.Marshal(GossipSyncRequest{
		Peers: []PeerInfo{poisonedRelay},
	})
	if _, _, _, err := attacker.callPeer(ctx, client.PeerInfo(), RPCEnvelope{
		Type:    RPCGossipSync,
		Payload: gossipPayload,
	}); err != nil {
		t.Fatalf("attacker GOSSIP_SYNC RPC failed: %v", err)
	}

	// Client must retain the validated relay endpoint and route inference cleanly.
	gotRelay, ok := client.resolvePeer(relay.ID.PeerID)
	if !ok {
		t.Fatalf("expected relay peer to remain resolvable")
	}
	realHP, _ := relay.PeerInfo().PrimaryTCPAddr()
	gotHP, _ := gotRelay.PrimaryTCPAddr()
	if gotHP != realHP {
		t.Fatalf("relay endpoint was poisoned by gossip: got %s, want %s", gotHP, realHP)
	}
	if _, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:      "gossip-guard-model",
		PromptDigest: "after-poison",
	}); err != nil {
		t.Fatalf("RouteInference broken after gossip poison attempt: %v", err)
	}
}

// Round-2 Finding 3 regression: a NATRelayed node using the normal Start/Bootstrap
// lifecycle MUST automatically recover lost circuit-relay reservations and
// republish its announcement from maintenanceLoop without manual EnsureRelays.
func TestMaintenanceLoopRestoresLostNATRelayReservations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relay1, err := NewNode(NodeConfig{
		Role:                  RoleRelay,
		NATMode:               NATRelayServer,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay1.Start(ctx); err != nil {
		t.Fatal(err)
	}

	relay2, err := NewNode(NodeConfig{
		Role:                  RoleRelay,
		NATMode:               NATRelayServer,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := relay2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer relay2.Shutdown()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATRelayed,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
		MaintenanceInterval:   15 * time.Millisecond,
		Models: []ModelCapability{
			{ModelID: "auto-relay-recover", MaxContextTokens: 8192, EstTokPerSec: 100, BaseLatencyMs: 5, PriceMicroUSDPer1MTokens: 100000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	// Bootstrap provider with only relay1 initially so it has a single active tunnel.
	if err := provider.Bootstrap(ctx, []PeerInfo{relay1.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	if len(provider.RelayMultiaddrs()) != 1 {
		t.Fatalf("expected 1 initial relay addr, got %v", provider.RelayMultiaddrs())
	}

	// Make relay2 known in provider's routing table AFTER initial single-relay registration,
	// then kill relay1 so provider loses ALL active reservations.
	provider.Table.UpsertPeer(relay2.PeerInfo())
	relay1.Shutdown()

	// Do NOT call EnsureRelays manually: maintenanceLoop must automatically
	// register on relay2 and republish the updated announcement!
	wantCircuit := FormatCircuitMultiaddr(relay2.ID.PeerID, provider.ID.PeerID)
	deadline := time.Now().Add(5 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		for _, a := range provider.RelayMultiaddrs() {
			if a == wantCircuit {
				recovered = true
				break
			}
		}
		if recovered {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !recovered {
		t.Fatalf("maintenanceLoop failed to automatically restore relay reservation on relay2 (addrs=%v)", provider.RelayMultiaddrs())
	}

	client, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		Policy:                DefaultVerifyPolicy(),
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer client.Shutdown()
	if err := client.Bootstrap(ctx, []PeerInfo{relay2.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	res, err := client.RouteInference(ctx, InferenceRequest{
		ModelID:      "auto-relay-recover",
		PromptDigest: "recovered-automatically",
	})
	if err != nil {
		t.Fatalf("RouteInference after automatic maintenance relay recovery failed: %v", err)
	}
	if !res.UsedRelay || res.RelayPeerID != relay2.ID.PeerID {
		t.Fatalf("expected route via relay2 %s, got used=%v relay=%s", relay2.ID.PeerID, res.UsedRelay, res.RelayPeerID)
	}
}

