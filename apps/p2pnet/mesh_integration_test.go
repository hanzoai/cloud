// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func TestTwentyNodeMeshDiscoveryFailoverAndAdversarialRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// Build a 20-node decentralized mesh over real loopback sockets:
	//   0..2  : 3 bootstrap nodes (NATRelayServer)
	//   3..5  : 3 intermediate relay/router nodes (NATRelayServer)
	//   6..15 : 10 direct model-serving provider nodes
	//   16..17: 2 NAT-relayed model-serving provider nodes (NO direct TCP listener)
	//   18..19: 2 client/edge router nodes
	const totalNodes = 20
	nodes := make([]*Node, totalNodes)
	defer func() {
		for _, n := range nodes {
			if n != nil {
				n.Shutdown()
			}
		}
	}()

	models := []string{"llama-3.3-70b", "qwen-2.5-72b", "deepseek-r1-32b", "nat-exclusive-model-8b"}

	for i := 0; i < totalNodes; i++ {
		cfg := NodeConfig{
			AnnouncementTTLSec:    300,
			Policy:                DefaultVerifyPolicy(),
			HanzoRegistryDisabled: true,
		}
		switch {
		case i < 3:
			cfg.Role = RoleBootstrap
			cfg.NATMode = NATRelayServer
		case i < 6:
			cfg.Role = RoleRelay
			cfg.NATMode = NATRelayServer
		case i < 16:
			cfg.Role = RoleProvider
			cfg.NATMode = NATDirect
			m := models[(i-6)%3]
			cfg.Models = []ModelCapability{{
				ModelID:                  m,
				MaxContextTokens:         65536,
				EstTokPerSec:             100,
				BaseLatencyMs:            float64(8 + (i % 5)),
				PriceMicroUSDPer1MTokens: int64(280000 + (i%4)*15000),
			}}
			cfg.Hardware = HardwareMetadata{GPUModel: "H100-80GB", GPUCount: 1, VRAMGB: 80, Region: "us-west"}
		case i < 18:
			cfg.Role = RoleProvider
			cfg.NATMode = NATRelayed
			cfg.Models = []ModelCapability{{
				ModelID:                  "nat-exclusive-model-8b",
				MaxContextTokens:         32768,
				EstTokPerSec:             85,
				BaseLatencyMs:            14,
				PriceMicroUSDPer1MTokens: 240000,
			}}
			cfg.Hardware = HardwareMetadata{GPUModel: "RTX-4090", GPUCount: 1, VRAMGB: 24, Region: "eu-central"}
		default:
			cfg.Role = RoleClient
			cfg.NATMode = NATDirect
		}
		n, err := NewNode(cfg)
		if err != nil {
			t.Fatalf("NewNode(%d): %v", i, err)
		}
		if err := n.Start(ctx); err != nil {
			t.Fatalf("Start(%d): %v", i, err)
		}
		nodes[i] = n
	}

	bootstraps := []PeerInfo{nodes[0].PeerInfo(), nodes[1].PeerInfo(), nodes[2].PeerInfo()}
	// Bootstrap seed nodes first, then all remaining 17 nodes concurrently (zero central registry).
	errCh := make(chan error, totalNodes)
	var bwg sync.WaitGroup
	for i := 0; i < 3; i++ {
		bwg.Add(1)
		go func(idx int) {
			defer bwg.Done()
			if err := nodes[idx].Bootstrap(ctx, bootstraps); err != nil {
				errCh <- fmt.Errorf("Bootstrap(%d): %w", idx, err)
			}
		}(i)
	}
	bwg.Wait()
	for i := 3; i < totalNodes; i++ {
		bwg.Add(1)
		go func(idx int) {
			defer bwg.Done()
			if err := nodes[idx].Bootstrap(ctx, bootstraps); err != nil {
				errCh <- fmt.Errorf("Bootstrap(%d): %w", idx, err)
			}
		}(i)
	}
	bwg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for i := 0; i < totalNodes; i++ {
			bwg.Add(1)
			go func(idx int) {
				defer bwg.Done()
				nodes[idx].GossipRound(ctx, 5)
			}(i)
		}
		bwg.Wait()
	}
	// Ensure NAT peers have reservations on relay servers (nodes 3..5) in addition to bootstraps.
	for i := 16; i < 18; i++ {
		if err := nodes[i].EnsureRelays(ctx, 3); err != nil {
			t.Fatalf("EnsureRelays(%d): %v", i, err)
		}
	}
	for i := 0; i < totalNodes; i++ {
		bwg.Add(1)
		go func(idx int) {
			defer bwg.Done()
			nodes[idx].GossipRound(ctx, 5)
		}(i)
	}
	bwg.Wait()

	// Verify mutual-TLS PING and signed UDP/QUIC-ready probe over real loopback sockets.
	if rtt, err := nodes[18].Ping(ctx, nodes[6].PeerInfo()); err != nil || rtt <= 0 {
		t.Fatalf("TLS Ping failed: rtt=%v err=%v", rtt, err)
	}
	if rtt, err := nodes[18].ProbeUDP(ctx, nodes[6].PeerInfo()); err != nil || rtt <= 0 {
		t.Fatalf("UDP Probe failed: rtt=%v err=%v", rtt, err)
	}

	// Verify convergence: client node 18 sees >= 18 peers and discovers all 4 models.
	if sz := nodes[18].Table.Size(); sz < 18 {
		t.Fatalf("expected client routing table >= 18 peers, got %d", sz)
	}
	for _, m := range models {
		provs := nodes[18].FindProviders(ctx, m)
		if len(provs) == 0 {
			t.Fatalf("model %q had 0 discovered providers", m)
		}
	}

	// Pre-kill end-to-end routing across all models (including NAT-relayed exclusive model!).
	for _, m := range models {
		res, err := nodes[18].RouteInference(ctx, InferenceRequest{
			ModelID:         m,
			PromptTokens:    64,
			MaxOutputTokens: 32,
			PromptDigest:    "pre-kill-test",
		})
		if err != nil {
			t.Fatalf("pre-kill RouteInference(%s): %v", m, err)
		}
		if res.Response.TokensGenerated != 32 {
			t.Fatalf("expected 32 tokens, got %d", res.Response.TokensGenerated)
		}
		if m == "nat-exclusive-model-8b" && !res.UsedRelay {
			t.Fatal("expected nat-exclusive-model-8b to route via circuit relay")
		}
	}

	// Inject stale and forged resource advertisements into client node 18 -> 100% rejected.
	now := time.Now().Unix()
	attackerID, _ := GenerateIdentity(rand.Reader)
	forgedAd := nodes[6].SelfAnnouncement()
	forgedAd.Seq += 10
	forgedAd.Nonce = "forged-mesh-nonce"
	forgedAd.Signature = hex.EncodeToString(make([]byte, 64))
	if err := nodes[18].SubmitAnnouncement(ctx, forgedAd, attackerID.PeerID); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected forged ad rejected with ErrInvalidSignature, got %v", err)
	}
	staleAd, _ := SignCapabilityAnnouncement(attackerID, CapabilityAnnouncement{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 49991, attackerID.PeerID)},
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Seq:       1,
		Nonce:     "stale-mesh-nonce",
		IssuedAt:  now - 1000,
		ExpiresAt: now - 50,
	})
	if err := nodes[18].SubmitAnnouncement(ctx, staleAd, attackerID.PeerID); !errors.Is(err, ErrAnnouncementExpired) {
		t.Fatalf("expected stale ad rejected with ErrAnnouncementExpired, got %v", err)
	}

	// KILL ALL 3 BOOTSTRAP NODES (0, 1, 2) AND 2 INTERMEDIATE RELAY/ROUTING NODES (3, 4)!
	for _, killIdx := range []int{0, 1, 2, 3, 4} {
		nodes[killIdx].Shutdown()
	}

	// NAT-relayed providers automatically re-establish relay on surviving relay node 5 and republish.
	for i := 16; i < 18; i++ {
		if err := nodes[i].EnsureRelays(ctx, 1); err != nil {
			t.Fatalf("post-kill EnsureRelays(%d): %v", i, err)
		}
	}
	for _, idx := range []int{5, 6, 18, 19} {
		nodes[idx].GossipRound(ctx, 6)
	}

	// Post-kill end-to-end routing from BOTH client nodes across ALL 4 models MUST succeed 100%!
	for _, clientIdx := range []int{18, 19} {
		for _, m := range models {
			res, err := nodes[clientIdx].RouteInference(ctx, InferenceRequest{
				ModelID:         m,
				PromptTokens:    64,
				MaxOutputTokens: 32,
				PromptDigest:    fmt.Sprintf("post-kill-%d-%s", clientIdx, m),
			})
			if err != nil {
				t.Fatalf("post-kill client %d RouteInference(%s): %v", clientIdx, m, err)
			}
			if res.Response.TokensGenerated != 32 {
				t.Fatalf("post-kill expected 32 tokens, got %d", res.Response.TokensGenerated)
			}
			if m == "nat-exclusive-model-8b" && res.RelayPeerID != nodes[5].ID.PeerID {
				t.Fatalf("expected surviving relay %s, got %s", nodes[5].ID.PeerID, res.RelayPeerID)
			}
		}
	}

	// Confirm ZERO Hanzo control-plane calls across all 20 nodes.
	for i, n := range nodes {
		if n.HanzoCalls() != 0 {
			t.Fatalf("node %d made %d HanzoCalls; expected 0", i, n.HanzoCalls())
		}
	}
}

func TestTotalPartitionEclipseFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{{
			ModelID:                  "llama-3.3-70b",
			MaxContextTokens:         65536,
			EstTokPerSec:             90,
			BaseLatencyMs:            10,
			PriceMicroUSDPer1MTokens: 300000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	isolated, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := isolated.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer isolated.Shutdown()

	if err := isolated.Bootstrap(ctx, []PeerInfo{provider.PeerInfo()}); err != nil {
		t.Fatal(err)
	}
	// Kill 100% of neighboring peers -> total eclipse / partition.
	provider.Shutdown()

	_, err = isolated.RouteInference(ctx, InferenceRequest{
		ModelID:         "llama-3.3-70b",
		PromptTokens:    32,
		MaxOutputTokens: 16,
		PromptDigest:    "partition-test",
	})
	if !errors.Is(err, ErrNoRouteFound) {
		t.Fatalf("expected ErrNoRouteFound under total partition, got %v", err)
	}
}

func TestSovereigntyBundleForkAndDualSurfaceHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	provider, err := NewNode(NodeConfig{
		Role:                  RoleProvider,
		NATMode:               NATDirect,
		HanzoRegistryDisabled: true,
		Models: []ModelCapability{{
			ModelID:                  "qwen-2.5-72b",
			MaxContextTokens:         65536,
			EstTokPerSec:             110,
			BaseLatencyMs:            9,
			PriceMicroUSDPer1MTokens: 260000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer provider.Shutdown()

	edge, err := NewNode(NodeConfig{
		Role:                  RoleClient,
		NATMode:               NATDirect,
		HanzoRegistryDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := edge.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer edge.Shutdown()

	if err := edge.Bootstrap(ctx, []PeerInfo{provider.PeerInfo()}); err != nil {
		t.Fatal(err)
	}

	// Export signed MeshExportBundle and verify offline.
	now := time.Now().Unix()
	bundle, err := edge.ExportMeshBundle(now)
	if err != nil {
		t.Fatalf("ExportMeshBundle: %v", err)
	}
	if err := VerifyMeshBundle(bundle, now, DefaultVerifyPolicy()); err != nil {
		t.Fatalf("VerifyMeshBundle valid failed: %v", err)
	}
	tampered := bundle
	tampered.ExportedAt++
	if err := VerifyMeshBundle(tampered, now, DefaultVerifyPolicy()); !errors.Is(err, ErrBundleTampered) {
		t.Fatalf("expected ErrBundleTampered, got %v", err)
	}

	// Cold-start fork into an isolated sovereign node and route with 0 Hanzo calls.
	forked, err := ForkFromBundle(ctx, NodeConfig{Role: RoleClient, NATMode: NATDirect}, bundle, now)
	if err != nil {
		t.Fatalf("ForkFromBundle: %v", err)
	}
	defer forked.Shutdown()

	res, err := forked.RouteInference(ctx, InferenceRequest{
		ModelID:         "qwen-2.5-72b",
		PromptTokens:    32,
		MaxOutputTokens: 24,
		PromptDigest:    "post-fork-route",
	})
	if err != nil {
		t.Fatalf("post-fork RouteInference: %v", err)
	}
	if res.Response.ServedByPeerID != provider.ID.PeerID {
		t.Fatalf("expected served by %s, got %s", provider.ID.PeerID, res.Response.ServedByPeerID)
	}
	if forked.HanzoCalls() != 0 {
		t.Fatalf("expected 0 HanzoCalls on forked node, got %d", forked.HanzoCalls())
	}

	// Dual-surface HTTP tests: both github.com/zap-proto/zip AND standard net/http.
	srv, err := NewServer(forked)
	if err != nil {
		t.Fatal(err)
	}
	zipApp := zip.New(zip.Config{})
	if err := Mount(zipApp, luxlog.New("test"), srv); err != nil {
		t.Fatalf("Mount zip: %v", err)
	}
	stdHandler := srv.Handler()

	doBoth := func(method, path string, body []byte) (int, []byte, int, []byte) {
		req1 := httptest.NewRequest(method, path, bytes.NewReader(body))
		resp1, err := zipApp.Test(req1, zip.TestConfig{Timeout: 5 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("zip %s %s: %v", method, path, err)
		}
		defer resp1.Body.Close()
		b1, _ := io.ReadAll(resp1.Body)

		req2 := httptest.NewRequest(method, path, bytes.NewReader(body))
		rec2 := httptest.NewRecorder()
		stdHandler.ServeHTTP(rec2, req2)
		return resp1.StatusCode, b1, rec2.Code, rec2.Body.Bytes()
	}

	// 1. Health parity.
	c1, _, c2, _ := doBoth(http.MethodGet, "/v1/p2p/health", nil)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("health status mismatch: zip=%d std=%d", c1, c2)
	}

	// 2. Peers & providers parity.
	c1, _, c2, _ = doBoth(http.MethodGet, "/v1/p2p/peers", nil)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("peers status mismatch: zip=%d std=%d", c1, c2)
	}
	c1, _, c2, _ = doBoth(http.MethodGet, "/v1/p2p/providers?model=qwen-2.5-72b", nil)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("providers status mismatch: zip=%d std=%d", c1, c2)
	}

	// 3. Forged announcement rejected fail-closed with 403 on both surfaces.
	badAnn := provider.SelfAnnouncement()
	badAnn.Signature = hex.EncodeToString(make([]byte, 64))
	badBody, _ := json.Marshal(badAnn)
	c1, _, c2, _ = doBoth(http.MethodPost, "/v1/p2p/announcements", badBody)
	if c1 != http.StatusForbidden || c2 != http.StatusForbidden {
		t.Fatalf("expected 403 on forged announcement, got zip=%d std=%d", c1, c2)
	}

	// 4. Route inference on both surfaces.
	routeBody, _ := json.Marshal(InferenceRequest{
		ModelID:         "qwen-2.5-72b",
		PromptTokens:    32,
		MaxOutputTokens: 16,
		PromptDigest:    "http-route",
	})
	c1, _, c2, _ = doBoth(http.MethodPost, "/v1/p2p/route", routeBody)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("route status mismatch: zip=%d std=%d", c1, c2)
	}

	// 5. Export and verify-bundle on both surfaces.
	c1, expBody, c2, _ := doBoth(http.MethodGet, "/v1/p2p/export", nil)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("export status mismatch: zip=%d std=%d", c1, c2)
	}
	c1, _, c2, _ = doBoth(http.MethodPost, "/v1/p2p/verify-bundle", expBody)
	if c1 != http.StatusOK || c2 != http.StatusOK {
		t.Fatalf("verify-bundle status mismatch: zip=%d std=%d", c1, c2)
	}
}
