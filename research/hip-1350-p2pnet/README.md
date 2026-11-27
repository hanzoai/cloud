# HIP-1350 Research Dossier — Decentralized Peer Discovery & Routing (`apps/p2pnet`)

**Upstream Issue**: `hanzoai/cloud#13` — *Decentralized peer discovery and routing (libp2p)*  
**Protocol Version**: `p2pnet/1.0` | **License**: `Apache-2.0 OR MIT` | **Host Dependencies**: `0` (`TestAppsImportNothingOfTheirHost` passes; `go.mod`/`go.sum` untouched)

## 1. Protocol Architecture & Clean-Room Design

`apps/p2pnet` replaces mandatory centralized peer registration with an independently operable, cryptographically authenticated P2P discovery and model-routing mesh modeled on `rust-libp2p` (MIT) principles using pure Go stdlib networking + `github.com/zap-proto/zip`:

- **Self-Certifying Ed25519 Peer Identities ([`identity.go`](../../apps/p2pnet/identity.go))**: Base58BTC SHA2-256 multihash `PeerID`s derived from libp2p protobuf Ed25519 key envelopes, signed `PeerRecord` envelopes with monotonic sequence numbers, TTL expiry, and anti-replay nonces, plus mutual-TLS 1.3 Ed25519 X.509 peer authentication.
- **Authenticated Capability Announcements ([`announce.go`](../../apps/p2pnet/announce.go))**: Domain-separated Ed25519-signed `CapabilityAnnouncement` payloads advertising multiaddr endpoints, circuit-relay paths, hosted AI models, GPU/VRAM metadata, pricing, sequence numbers, nonces, and TTLs. Fail-closed validation rejects forged signatures, tampered fields, expired TTLs, sequence rollbacks, replay nonces, PeerID-to-pubkey mismatches, future clock skew, and unprobed Sybil floods.
- **XOR-Metric Kademlia DHT, Gossip & Circuit-Relay NAT Traversal ([`dht.go`](../../apps/p2pnet/dht.go), [`transport.go`](../../apps/p2pnet/transport.go), [`router.go`](../../apps/p2pnet/router.go))**: 256 k-bucket XOR routing table with iterative `FindNode`, `FindProviders(model)`, `PutAnnouncement`, anti-entropy `GossipRound`, real loopback mutual-TLS 1.3 TCP streams, signed UDP/QUIC-ready datagram probes, and zero-registry circuit-relay v2 forwarding (`NATRelayed` providers automatically migrate across surviving relays when intermediate relays fail).
- **Sovereignty & Dual-Surface HTTP Control API ([`sovereignty.go`](../../apps/p2pnet/sovereignty.go), [`http.go`](../../apps/p2pnet/http.go))**: Ed25519-signed `MeshExportBundle` snapshots support offline verification and zero-Hanzo cold-start node forking (`HanzoCalls = 0`), exposed identically over `github.com/zap-proto/zip` (`Mount`) and standard-library `net/http` (`Handler`) under `/v1/p2p/*`.

## 2. Threat Model & Explicit Verification Boundaries

Per HIP-1350 proof-hygiene rules, cryptographic peer signatures, live network probes, and hardware TEE attestations are strictly separated:

| Mechanism | `VerificationClass` | What It Proves | What It Does NOT Prove |
|---|---|---|---|
| Domain-separated Ed25519 signature + self-certifying multihash `PeerID` + monotonic `Seq` + `Nonce` + TTL/skew checks | `crypto_peer_signature` | Signer key possession, PeerID binding, payload integrity, anti-replay, freshness | Live network reachability, physical GPU ownership, or honest inference weights |
| Mutual-TLS 1.3 Ed25519 `PING`/`PONG` + signed UDP/QUIC-ready datagram probe | `network_liveness_probe` | Active socket reachability of the claimed `PeerID` and defense against unreachable Sybil floods | Hardware enclave integrity or inference quality |
| Optional external TEE quote (`HardwareAttestationHash`) | `hardware_attestation` | Enclave measurement when externally verified | **Not claimed in this simulation (`0/20` hardware-attested)** |

## 3. Experimental Method & Reproducibility

- **Real Loopback Sockets**: All 20 nodes execute over real Linux loopback mutual-TLS 1.3 TCP sockets and UDP probe sockets (`127.0.0.1:0` ephemeral ports): 3 bootstrap nodes (`relay_server`), 3 intermediate relay/routing nodes (`relay_server`), 10 direct GPU provider nodes, 2 `nat_relayed` GPU provider nodes (zero direct TCP listener — served exclusively via circuit-relay v2), and 2 edge client nodes.
- **Deterministic Seed**: `1350` (`80` inference requests per primary/baseline regime across 4 models: `llama-3.3-70b`, `qwen-2.5-72b`, `deepseek-r1-32b`, `mistral-large-24b`).
- **Mid-Run Failure Injection**: In `hip1350_decentralized_p2p_20n`, **ALL 3 bootstrap nodes AND 2 intermediate relay/routing nodes** (5 nodes total) are killed mid-run; NAT-relayed peers migrate to the surviving relay and clients continue discovering and routing requests with zero central registry.
- **Reproduce Exact Numbers**:

```bash
go run ./cmd/p2pnet-bench -seed 1350 -nodes 20 -requests 80 -write-dossier research/hip-1350-p2pnet
go test -race -v -count=1 ./apps/p2pnet/... ./cmd/p2pnet-bench/...
CGO_ENABLED=0 go test -v ./apps -run TestAppsImportNothingOfTheirHost
```

## 4. Measured Results Across All 5 HIP-1350 Dimensions

1. **Cost**: Primary decentralized 20-node P2P mesh achieves **$0.2660 / 1M useful tokens** (explicitly including 222 uUSD Ed25519 verification overhead across 2212 verify ops and 694 uUSD DHT/gossip/relay egress overhead across 34691212 bytes) vs **$0.3031 / 1M useful tokens** on the centralized registry baseline (which incurs 8668 uUSD central registry overhead across 100 API calls) — a **12.24% realized cost reduction**.
2. **Performance**: Full 20-node Kademlia + gossip convergence completed in **1850.36 ms** (mean **19.00** discovered peers per node; signed UDP/QUIC-ready probe mean RTT **0.93 ms**). Overall routing latency p50/p95/p99 = **2.51 / 6.69 / 10.16 ms** (pre-kill p50/p95/p99 = **2.46 / 6.69 / 9.91 ms**, post-kill p50/p95/p99 = **2.56 / 6.57 / 10.16 ms**), measured provider-local first-token p50/p95/p99 = **0.01 / 0.01 / 0.02 ms**, estimated client TTFT (unary-buffered estimate: provider-local first-token + 45% routing latency; not streamed client-observed TTFT) p50/p95/p99 = **1.14 / 3.02 / 4.58 ms**, throughput = **340.79 req/s** (**872429.13 tok/s**), with **100.00%** pre-kill completion and **100.00%** post-kill completion (including **25** NAT circuit-relayed inference completions and **1** automatic routing failovers).
3. **Trust**: **100/100 (100.00%)** injected adversarial resource advertisements rejected fail-closed across all 8 attack vectors (`forged_signature`, `tampered_capability_fields`, `expired_ttl`, `sequence_rollback`, `replay_nonce`, `peer_id_mismatch`, `clock_skew_future`, `sybil_flood`), with **0** hijacked requests. Hardware TEE attestation is explicitly reported as **0/20 attested (not claimed)**.
4. **Decentralization**: With `HanzoRegistryDisabled = true` and **ALL 3 bootstrap nodes + 2 intermediate relay nodes killed**, **100.00%** of discovery and end-to-end inference routing succeeded with **`HanzoCalls = 0`** across **12** active serving providers (**HHI = 0.1088**).
5. **Sovereignty**: Signed `MeshExportBundle` (**37129 bytes** covering 19 routing peers, 18 bootstrap seeds, and 21 verified announcements) exported in **0.48 ms**, verified offline in **2.78 ms** (tamper rejection = `true`), and cold-start forked into a brand-new standalone node in **7.97 ms** (total migration **11.22 ms**), with post-fork live routing verified (`PostForkRouteOK = true`, `PostForkHanzoCalls = 0`).

## 5. Honest Negative & Boundary Regimes

Three honest negative/boundary regimes are executed on the exact same codebase to verify that security invariants and availability limits are real:

1. **Unverified Advertisements Negative Control (`neg_unverified_advertisements_control`)**: Setting `DisableVerification = true` collapses adversarial detection to **0/100 (0.00%)** and allows forged, expired ultra-low-price capability ads from a rogue node to poison routing and hijack **80/80 (100.00%)** of inference requests (`0` useful tokens delivered vs `0.00%` hijack with verification enabled).
2. **Centralized Registry Kill Negative Control (`neg_central_registry_kill_negative`)**: Shutting down the single centralized registry server collapses uncached discovery and routing completion to **0/40 (0.00%)**, contrasting directly with **100.00%** post-kill completion in the decentralized Kademlia DHT after killing all 3 bootstrap nodes and 2 intermediate relays.
3. **Total Partition / Eclipse Boundary (`neg_total_partition_eclipse_boundary`)**: Isolating a node behind 100% killed neighbor peers while injecting 100 forged eclipse announcements yields **100/100 (100.00%)** adversarial rejection and **0/40 (0.00%)** liveness completion — cleanly returning `ErrNoRouteFound` fail-closed with **0** forged routes accepted, honestly documenting CAP network-partition availability boundaries.

## 6. Limitations & Pre-Production Security Review

- **Unary Buffered Inference & Estimated Client TTFT**: The current `INFER_REQUEST` RPC buffers the complete inference response until generation finishes and does not stream token chunks to the client. Accordingly, `provider_local_ttft` reports measured provider-local first-token hash timing, while `estimated_client_ttft` (`provider_local_ttft + 0.45 * complete_routing_latency`) is explicitly an analytical estimate rather than a measured client-observed streaming first-token event.
- **Transport & QUIC-v1 Framing**: To preserve zero new `go.mod` dependencies and zero host imports, the current transport runs mutual-TLS 1.3 over loopback TCP streams alongside a real signed UDP/QUIC-v1 probe socket advertising `/udp/<port>/quic-v1/p2p/<peerID>` multiaddrs. Full QUIC-v1 multiplexed streams (`quic-go` or `rust-libp2p` QUIC bridge) can swap directly beneath the internal RPC framing layer without changing identities, announcements, or DHT routing.
- **NAT Hole-Punching (DCUtR)**: Symmetric NAT traversal is fully implemented via zero-registry circuit-relay v2 with automatic relay failover; direct UDP hole-punching upgrade (DCUtR) is an incremental transport optimization for WAN deployments.
- **Sybil Resistance at Internet Scale**: On an open WAN, an adversary controlling many distinct live IP/key pairs can still pass `network_liveness_probe`. Production deployments should layer S/Kademlia static crypto puzzles, per-/24 IP-diversity bucket quotas, and bonded provider stake (`hanzoai/cloud#14`/`#17`), and undergo independent external security review prior to routing production value.
- **Full Tables & Machine-Readable Telemetry**: See [`score_table.md`](./score_table.md) and [`raw_results.json`](./raw_results.json).
