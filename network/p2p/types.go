package p2p

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// CapabilityAnnouncement represents a signed advertisement of a node's available models/services.
type CapabilityAnnouncement struct {
	PeerID     peer.ID  `json:"peer_id"`
	Models     []string `json:"models"`
	Timestamp  int64    `json:"timestamp"`
	Expiration int64    `json:"expiration"`
	Signature  []byte   `json:"signature"`
}

// DataToSign returns the canonical byte representation of the announcement for signing/verification.
func (a *CapabilityAnnouncement) DataToSign() []byte {
	return []byte(fmt.Sprintf("%s:%v:%d:%d", a.PeerID.String(), a.Models, a.Timestamp, a.Expiration))
}

// Verify checks if the announcement is authentic and not expired.
func (a *CapabilityAnnouncement) Verify(pubKey ed25519.PublicKey) error {
	if time.Now().Unix() > a.Expiration {
		return errors.New("announcement expired")
	}
	if !ed25519.Verify(pubKey, a.DataToSign(), a.Signature) {
		return errors.New("invalid signature: forged advertisement")
	}
	return nil
}
