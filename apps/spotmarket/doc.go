// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package spotmarket implements an open, permissionless GPU spot market and
// reverse-auction capacity marketplace per HIP-1350.
//
// Design principles:
//   - Zero host / Hanzo lock-in: imports only the Go standard library,
//     github.com/zap-proto/zip, github.com/luxfi/log, and
//     github.com/luxfi/crypto/secp256k1. Operates identically with all
//     Hanzo-operated control planes disabled (HanzoCalls == 0).
//   - Cryptographically signed offers and bids: providers sign canonical
//     SHA-256 commitments with secp256k1 using EIP-2 low-S normalization;
//     forged, malleated (high-S), expired, and replayed messages fail closed.
//   - Neutral Enso multi-dimensional selection: ranks eligible candidates by
//     measured latency, bid price, observed reliability, and explicit buyer
//     policy (VRAM, model, region/jurisdiction, max price, max TTFT). Provider
//     operator labels are never consulted by the ranker.
//   - Akash-inspired reverse-auction lifecycle: Order -> SignedBid -> Lease ->
//     SettlementReceipt with partial fills, mid-lease failover, bonded
//     collateral slashing on SLA breach or bait-and-switch, and exact integer
//     microdollar ledger conservation (0 microdollar drift).
//   - Defensible verification boundaries: explicitly separates cryptographic
//     message signatures (VerificationCryptoSignature), economic collateral
//     slashing (VerificationEconomicStake), and hardware attestation quotes
//     (VerificationHardwareAttestation), never conflating one for another.
//   - Sovereignty and portability: exports a self-contained signed
//     MarketExportBundle that any independent operator can verify and import
//     to fork or migrate the market without upstream coordination.
package spotmarket
