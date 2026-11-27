// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func TestIdentityPeerIDAndPeerRecord(t *testing.T) {
	idA, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateIdentity A: %v", err)
	}
	idB, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateIdentity B: %v", err)
	}
	if idA.PeerID == idB.PeerID {
		t.Fatal("expected distinct PeerIDs for distinct keys")
	}
	if err := VerifyPeerIDBinding(idA.PeerID, idA.PublicKey); err != nil {
		t.Fatalf("valid binding failed: %v", err)
	}
	if err := VerifyPeerIDBinding(idA.PeerID, idB.PublicKey); !errors.Is(err, ErrPeerIDMismatch) {
		t.Fatalf("expected ErrPeerIDMismatch, got %v", err)
	}

	// Self-signed TLS 1.3 cert verification.
	certA, err := GeneratePeerTLSCert(idA)
	if err != nil {
		t.Fatalf("GeneratePeerTLSCert: %v", err)
	}
	gotID, _, err := VerifyPeerCert(certA.Certificate, idA.PeerID)
	if err != nil || gotID != idA.PeerID {
		t.Fatalf("VerifyPeerCert valid failed: id=%s err=%v", gotID, err)
	}
	if _, _, err := VerifyPeerCert(certA.Certificate, idB.PeerID); !errors.Is(err, ErrPeerIDMismatch) {
		t.Fatalf("expected ErrPeerIDMismatch for wrong expected peer, got %v", err)
	}

	// Signed PeerRecord validation & fail-closed checks.
	now := int64(1_800_000_000)
	spr, err := SignPeerRecord(idA, PeerRecord{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 40001, idA.PeerID)},
		NATMode:   NATDirect,
		Seq:       1,
		Nonce:     "nonce-peerrec-1",
		IssuedAt:  now,
		ExpiresAt: now + 120,
	})
	if err != nil {
		t.Fatalf("SignPeerRecord: %v", err)
	}
	if spr.VerificationClass != VerificationClassCryptoPeerSignature {
		t.Fatalf("unexpected verification class: %s", spr.VerificationClass)
	}
	if err := VerifyPeerRecord(spr, now+10, DefaultMaxClockSkewSec); err != nil {
		t.Fatalf("VerifyPeerRecord valid failed: %v", err)
	}
	// Expired record must fail closed.
	if err := VerifyPeerRecord(spr, now+121, DefaultMaxClockSkewSec); !errors.Is(err, ErrAnnouncementExpired) {
		t.Fatalf("expected ErrAnnouncementExpired, got %v", err)
	}
	// Future clock-skew record must fail closed.
	if err := VerifyPeerRecord(spr, now-60, DefaultMaxClockSkewSec); !errors.Is(err, ErrClockSkewFuture) {
		t.Fatalf("expected ErrClockSkewFuture, got %v", err)
	}
	// Tampered address must fail signature check.
	tampered := spr
	tampered.Record.Addrs = []string{FormatMultiaddr("tcp", "127.0.0.1", 49999, idA.PeerID)}
	if err := VerifyPeerRecord(tampered, now+10, DefaultMaxClockSkewSec); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature on tampered PeerRecord, got %v", err)
	}
}

func TestCapabilityAnnouncementFailClosedAdversarialVectors(t *testing.T) {
	id, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := GenerateIdentity(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_800_000_000)
	store := NewDHTStore(DefaultVerifyPolicy())

	validAnn, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
		Addrs:   []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, id.PeerID)},
		NATMode: NATDirect,
		Role:    RoleProvider,
		Models: []ModelCapability{
			{ModelID: "llama-3.3-70b", MaxContextTokens: 131072, EstTokPerSec: 95, BaseLatencyMs: 12, PriceMicroUSDPer1MTokens: 320000},
		},
		Hardware: HardwareMetadata{
			GPUModel:         "H100-80GB",
			GPUCount:         2,
			VRAMGB:           160,
			Region:           "us-east",
			HardwareAttested: false,
		},
		Seq:       2,
		Nonce:     "valid-nonce-2",
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatalf("SignCapabilityAnnouncement: %v", err)
	}
	if validAnn.VerificationClass != VerificationClassCryptoPeerSignature {
		t.Fatalf("unexpected verification class %s", validAnn.VerificationClass)
	}
	if validAnn.Hardware.HardwareAttested {
		t.Fatal("hardware attestation must not be claimed by default")
	}
	if err := store.PutAnnouncement(validAnn, "", true, now); err != nil {
		t.Fatalf("PutAnnouncement valid failed: %v", err)
	}

	// 1. Forged signature.
	forged := CloneAnnouncement(validAnn)
	forged.Seq = 3
	forged.Nonce = "forged-sig-3"
	forged.Signature = hex.EncodeToString(make([]byte, 64))
	if err := store.PutAnnouncement(forged, other.PeerID, false, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("forged signature: expected ErrInvalidSignature, got %v", err)
	}

	// 2. Tampered capability fields (model list & price modified after signing).
	tampered := CloneAnnouncement(validAnn)
	tampered.Models[0].ModelID = "tampered-model-999b"
	tampered.Models[0].PriceMicroUSDPer1MTokens = 1
	if err := store.PutAnnouncement(tampered, other.PeerID, false, now); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered capability: expected ErrInvalidSignature, got %v", err)
	}

	// 3. Expired / stale TTL announcement.
	expired, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, id.PeerID)},
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Seq:       4,
		Nonce:     "expired-nonce-4",
		IssuedAt:  now - 600,
		ExpiresAt: now - 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnouncement(expired, other.PeerID, false, now); !errors.Is(err, ErrAnnouncementExpired) {
		t.Fatalf("expired ttl: expected ErrAnnouncementExpired, got %v", err)
	}

	// 4. Sequence rollback replay (Seq=1 after Seq=2 already accepted).
	rollback, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, id.PeerID)},
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Seq:       1,
		Nonce:     "rollback-nonce-1",
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnouncement(rollback, other.PeerID, false, now); !errors.Is(err, ErrSequenceRollback) {
		t.Fatalf("seq rollback: expected ErrSequenceRollback, got %v", err)
	}

	// 5. Replay nonce (reusing "valid-nonce-2" even with higher Seq=5).
	replayNonce, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, id.PeerID)},
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Seq:       5,
		Nonce:     "valid-nonce-2",
		IssuedAt:  now,
		ExpiresAt: now + 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnouncement(replayNonce, other.PeerID, false, now); !errors.Is(err, ErrReplayNonce) {
		t.Fatalf("replay nonce: expected ErrReplayNonce, got %v", err)
	}

	// 6. Mismatched PeerID-to-pubkey binding.
	spoofed := CloneAnnouncement(validAnn)
	spoofed.PeerID = other.PeerID
	spoofed.Addrs = []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, other.PeerID)}
	if err := store.PutAnnouncement(spoofed, other.PeerID, false, now); !errors.Is(err, ErrPeerIDMismatch) {
		t.Fatalf("peer id mismatch: expected ErrPeerIDMismatch, got %v", err)
	}

	// 7. Clock-skew future announcement.
	future, err := SignCapabilityAnnouncement(id, CapabilityAnnouncement{
		Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 41001, id.PeerID)},
		NATMode:   NATDirect,
		Role:      RoleProvider,
		Seq:       6,
		Nonce:     "future-nonce-6",
		IssuedAt:  now + DefaultMaxClockSkewSec + 60,
		ExpiresAt: now + 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutAnnouncement(future, other.PeerID, false, now); !errors.Is(err, ErrClockSkewFuture) {
		t.Fatalf("clock skew future: expected ErrClockSkewFuture, got %v", err)
	}

	// 8. Sybil burst flood from a single unprobed sender exceeding window cap.
	sybilSender := other.PeerID
	sybilRejected := 0
	for i := 0; i < DefaultMaxNewPeersPerSenderWindow+10; i++ {
		sybID, err := GenerateIdentity(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sybAnn, err := SignCapabilityAnnouncement(sybID, CapabilityAnnouncement{
			Addrs:     []string{FormatMultiaddr("tcp", "127.0.0.1", 45000+i, sybID.PeerID)},
			NATMode:   NATDirect,
			Role:      RoleProvider,
			Seq:       1,
			Nonce:     fmt.Sprintf("sybil-%d", i),
			IssuedAt:  now,
			ExpiresAt: now + 300,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.PutAnnouncement(sybAnn, sybilSender, false, now); errors.Is(err, ErrSybilFloodDetected) {
			sybilRejected++
		}
	}
	if sybilRejected != 10 {
		t.Fatalf("expected 10 Sybil flood rejections, got %d", sybilRejected)
	}

	stats := store.Stats()
	if stats.Rejected < 17 {
		t.Fatalf("expected at least 17 total rejections recorded, got %d", stats.Rejected)
	}
}
