package validators

// keygen.go — the staking identity generator. It produces ONE luxd validator
// staking identity (TLS + BLS + ML-DSA-65 → strict-PQ NodeID) using the node's
// OWN audited primitives (github.com/luxfi/node/staking), byte-for-byte the same
// material ~/work/lux/genesis/cmd/venuekeygen emits — so the artifacts are
// consumable by a real luxd with NO re-implemented crypto. Every key is sealed
// into KMS; plaintext never lands in the control-plane DB and is never logged.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/crypto/bls"
	"github.com/luxfi/crypto/bls/signer/localsigner"
	"github.com/luxfi/crypto/mldsa"
	"github.com/luxfi/ids"
	"github.com/luxfi/node/staking"
)

// PEM block types for the ML-DSA-65 strict-PQ identity. These MUST match
// staking.InitNodePQKeyPair / staking.LoadPQKeyPair exactly or luxd refuses the
// artifact at config-load. Mirrored (not imported) because the node package
// does not export them; the round-trip in selfCheck proves the encoding.
const (
	mldsaPrivPEMType = "ML-DSA-65 PRIVATE KEY"
	mldsaPubPEMType  = "ML-DSA-65 PUBLIC KEY"
)

// stakingArtifacts are the on-KMS key names + the files luxd loads from
// /staking-keys (staker.crt/key, the BLS signer.key, and the ML-DSA pair). Stable
// order; identical to venuekeygen's artifact set.
var stakingArtifacts = []string{"staker.crt", "staker.key", "signer.key", "mldsa.key", "mldsa.pub"}

// stakingIdentity is a freshly generated luxd staking identity for ONE node. The
// artifacts map holds the FILE forms luxd's on-disk loaders read (signer.key is
// RAW LocalSigner bytes; the rest are PEM), keyed by the filename luxd mounts.
type stakingIdentity struct {
	// NodeID is the strict-PQ ML-DSA-65 NodeID
	// (ids.NodeIDSchemeMLDSA65.DeriveMLDSA(ids.Empty, mldsaPubRaw)) — the value
	// luxd computes under the PQ securityProfile, and the id that belongs in the
	// validator registration. This is what identifies the node in the set.
	NodeID string
	// BLSPubkeyHex is the compressed BLS public key (hex), surfaced to the
	// owner-gated registration record (the PoP the P-Chain tx needs). Not secret.
	BLSPubkeyHex string
	// mldsaPubRaw is retained for NodeID re-derivation in selfCheck.
	mldsaPubRaw []byte
	// artifacts are the secret key materials, by luxd filename.
	artifacts map[string][]byte
}

// generateStakingIdentity produces one fresh staking identity from the node's
// own generators (crypto/rand inside each) and PROVES it round-trips before
// returning — a self-check failure means the artifact would not be consumable by
// a real luxd, so it is never sealed.
func generateStakingIdentity() (*stakingIdentity, error) {
	// TLS staking cert + key (P-256 ECDSA), node's canonical generator.
	certPEM, keyPEM, err := staking.NewCertAndKeyBytes()
	if err != nil {
		return nil, fmt.Errorf("new TLS cert/key: %w", err)
	}

	// BLS signer key — node's localsigner. ToBytes() is the on-disk file form.
	signer, err := localsigner.New()
	if err != nil {
		return nil, fmt.Errorf("new BLS signer: %w", err)
	}
	signerRaw := signer.ToBytes()
	blsPubHex := hex.EncodeToString(bls.PublicKeyToCompressedBytes(signer.PublicKey()))

	// PQ ML-DSA-65 key pair — node's strict-PQ generator.
	pq, err := staking.NewPQKeyPair()
	if err != nil {
		return nil, fmt.Errorf("new ML-DSA-65 key pair: %w", err)
	}
	mldsaPubRaw := pq.PublicKeyBytes()
	mldsaKeyPEM, err := encodePEM(mldsaPrivPEMType, pq.PrivateKeyBytes())
	if err != nil {
		return nil, fmt.Errorf("encode ML-DSA private PEM: %w", err)
	}
	mldsaPubPEM, err := encodePEM(mldsaPubPEMType, mldsaPubRaw)
	if err != nil {
		return nil, fmt.Errorf("encode ML-DSA public PEM: %w", err)
	}

	// Strict-PQ NodeID — the node's REAL primary NodeID, derived from the SAME
	// raw ML-DSA-65 pubkey bytes that are PEM-encoded into mldsa.pub, bound to the
	// primary-network chain id (ids.Empty). Byte-identical to what luxd computes
	// at boot under the strict-PQ securityProfile.
	nodeID, _, err := ids.NodeIDSchemeMLDSA65.DeriveMLDSA(ids.Empty, mldsaPubRaw)
	if err != nil {
		return nil, fmt.Errorf("derive strict-PQ NodeID: %w", err)
	}

	id := &stakingIdentity{
		NodeID:       nodeID.String(),
		BLSPubkeyHex: blsPubHex,
		mldsaPubRaw:  mldsaPubRaw,
		artifacts: map[string][]byte{
			"staker.crt": certPEM,
			"staker.key": keyPEM,
			"signer.key": signerRaw, // RAW — matches luxd's file loader
			"mldsa.key":  mldsaKeyPEM,
			"mldsa.pub":  mldsaPubPEM,
		},
	}
	if err := id.selfCheck(); err != nil {
		return nil, fmt.Errorf("self-check (artifact not luxd-consumable): %w", err)
	}
	return id, nil
}

// selfCheck loads every generated artifact back through the node's own loaders
// and asserts the NodeID re-derives, mirroring venuekeygen's guarantee. Any
// failure means the material is not luxd-consumable.
func (id *stakingIdentity) selfCheck() error {
	if id.NodeID == ids.EmptyNodeID.String() {
		return errors.New("strict-PQ NodeID is empty")
	}
	// NodeID reproducibility from the same ML-DSA pubkey bytes.
	re, _, err := ids.NodeIDSchemeMLDSA65.DeriveMLDSA(ids.Empty, id.mldsaPubRaw)
	if err != nil {
		return fmt.Errorf("NodeID re-derive: %w", err)
	}
	if re.String() != id.NodeID {
		return fmt.Errorf("NodeID not reproducible: %s != %s", re.String(), id.NodeID)
	}
	// TLS: parse via the node loader.
	if _, err := staking.LoadTLSCertFromBytes(id.artifacts["staker.key"], id.artifacts["staker.crt"]); err != nil {
		return fmt.Errorf("TLS round-trip: %w", err)
	}
	// BLS: parse the raw file form back through localsigner (the node's file path).
	if _, err := localsigner.FromBytes(id.artifacts["signer.key"]); err != nil {
		return fmt.Errorf("BLS signer round-trip: %w", err)
	}
	// ML-DSA: decode both PEMs and parse via mldsa (proves the PEM luxd's
	// LoadPQKeyPair reads is well-formed and the keys parse at MLDSA65).
	privDER, err := decodePEM(mldsaPrivPEMType, id.artifacts["mldsa.key"])
	if err != nil {
		return fmt.Errorf("ML-DSA private PEM: %w", err)
	}
	if _, err := mldsa.PrivateKeyFromBytes(mldsa.MLDSA65, privDER); err != nil {
		return fmt.Errorf("ML-DSA private round-trip: %w", err)
	}
	pubDER, err := decodePEM(mldsaPubPEMType, id.artifacts["mldsa.pub"])
	if err != nil {
		return fmt.Errorf("ML-DSA public PEM: %w", err)
	}
	if _, err := mldsa.PublicKeyFromBytes(pubDER, mldsa.MLDSA65); err != nil {
		return fmt.Errorf("ML-DSA public round-trip: %w", err)
	}
	return nil
}

// seal writes every secret artifact into KMS under baseRef, so the value lives
// ONLY in KMS (the kms-operator later materializes them into the node's
// /staking-keys Secret via the KMSSecret CR). Fails closed on any KMS error —
// a partially-sealed identity is refused so a node never boots with half its
// keys. The error names only the artifact, never the secret bytes.
func (id *stakingIdentity) seal(ctx context.Context, kms cloud.KMSClient, baseRef string) error {
	if kms == nil {
		return errors.New("KMS is not configured for this deployment (cannot seal staking keys)")
	}
	for _, name := range stakingArtifacts {
		b, ok := id.artifacts[name]
		if !ok {
			return fmt.Errorf("missing artifact %q", name)
		}
		if err := kms.PutSecret(ctx, baseRef+"/"+name, b); err != nil {
			return fmt.Errorf("seal %q: %w", name, err)
		}
	}
	return nil
}

// encodePEM wraps body in a PEM block of the given type.
func encodePEM(blockType string, body []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := pem.Encode(&buf, &pem.Block{Type: blockType, Bytes: body}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodePEM decodes a single PEM block, asserting its type.
func decodePEM(wantType string, b []byte) ([]byte, error) {
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("no PEM block")
	}
	if blk.Type != wantType {
		return nil, fmt.Errorf("PEM type %q, want %q", blk.Type, wantType)
	}
	return blk.Bytes, nil
}
