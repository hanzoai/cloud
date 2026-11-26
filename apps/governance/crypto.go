// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"
)

const signatureDomain = "hanzo.governance.hip1352.v1"

// writeField writes a big-endian uint32 length prefix followed by the raw bytes
// of s, preventing any field-boundary concatenation ambiguity in signing inputs.
func writeField(buf *bytes.Buffer, s string) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
	buf.Write(lenBuf[:])
	buf.WriteString(s)
}

func writeInt64(buf *bytes.Buffer, v int64) {
	writeField(buf, strconv.FormatInt(v, 10))
}

func writeUint64(buf *bytes.Buffer, v uint64) {
	writeField(buf, strconv.FormatUint(v, 10))
}

// DecodePublicKey parses a hex-encoded 32-byte Ed25519 public key.
func DecodePublicKey(pubHex string) (ed25519.PublicKey, error) {
	raw, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid public key hex: %v", ErrInvalidSignature, err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: expected %d-byte ed25519 public key, got %d", ErrInvalidSignature, ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func verifyHexSig(pubHex string, msg []byte, sigHex string) error {
	pub, err := DecodePublicKey(pubHex)
	if err != nil {
		return err
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature encoding", ErrInvalidSignature)
	}
	if !ed25519.Verify(pub, msg, sig) {
		return ErrInvalidSignature
	}
	return nil
}

func signHex(priv ed25519.PrivateKey, msg []byte) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: invalid ed25519 private key length %d", ErrInvalidSignature, len(priv))
	}
	return hex.EncodeToString(ed25519.Sign(priv, msg)), nil
}

// HashPayload returns the deterministic SHA-256 hex digest of a ProposalPayload.
func HashPayload(p ProposalPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal proposal payload: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// HashSnapshotStateRoot returns a deterministic SHA-256 root over snapshot power
// and delegation maps.
func HashSnapshotStateRoot(snapshotID uint64, ts time.Time, totalSupply uint64, effective map[string]uint64, delegations map[string]string) string {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, "snapshot_root")
	writeUint64(&buf, snapshotID)
	writeInt64(&buf, ts.UTC().UnixNano())
	writeUint64(&buf, totalSupply)

	powerKeys := make([]string, 0, len(effective))
	for k := range effective {
		powerKeys = append(powerKeys, k)
	}
	sort.Strings(powerKeys)
	writeUint64(&buf, uint64(len(powerKeys)))
	for _, k := range powerKeys {
		writeField(&buf, k)
		writeUint64(&buf, effective[k])
	}

	delKeys := make([]string, 0, len(delegations))
	for k := range delegations {
		delKeys = append(delKeys, k)
	}
	sort.Strings(delKeys)
	writeUint64(&buf, uint64(len(delKeys)))
	for _, k := range delKeys {
		writeField(&buf, k)
		writeField(&buf, delegations[k])
	}

	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func proposalSigningBytes(protocolID string, req ProposalRequest) ([]byte, error) {
	payloadHash, err := HashPayload(req.Payload)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "proposal")
	writeField(&buf, req.ProposalID)
	writeField(&buf, req.ProposerID)
	writeField(&buf, req.Org)
	writeField(&buf, string(req.Kind))
	writeField(&buf, req.Title)
	writeField(&buf, req.Description)
	writeField(&buf, payloadHash)
	writeInt64(&buf, req.CreatedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes(), nil
}

// SignProposal signs a ProposalRequest with the proposer's Ed25519 private key.
func SignProposal(protocolID string, req ProposalRequest, priv ed25519.PrivateKey) (string, error) {
	msg, err := proposalSigningBytes(protocolID, req)
	if err != nil {
		return "", err
	}
	return signHex(priv, msg)
}

// VerifyProposalSignature verifies a ProposalRequest's Ed25519 signature.
func VerifyProposalSignature(protocolID string, req ProposalRequest, pubHex string) error {
	msg, err := proposalSigningBytes(protocolID, req)
	if err != nil {
		return err
	}
	return verifyHexSig(pubHex, msg, req.Signature)
}

func voteSigningBytes(protocolID string, req VoteRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "vote")
	writeField(&buf, req.ProposalID)
	writeField(&buf, req.VoterID)
	writeField(&buf, req.Org)
	writeField(&buf, string(req.Support))
	writeField(&buf, req.Reason)
	writeInt64(&buf, req.CastAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignVote signs a VoteRequest with the voter's Ed25519 private key.
func SignVote(protocolID string, req VoteRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, voteSigningBytes(protocolID, req))
}

// VerifyVoteSignature verifies a VoteRequest's Ed25519 signature.
func VerifyVoteSignature(protocolID string, req VoteRequest, pubHex string) error {
	return verifyHexSig(pubHex, voteSigningBytes(protocolID, req), req.Signature)
}

func queueSigningBytes(protocolID string, req QueueRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "queue")
	writeField(&buf, req.ProposalID)
	writeField(&buf, req.CallerID)
	writeField(&buf, req.Org)
	writeInt64(&buf, req.QueuedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignQueue signs a QueueRequest with the caller's Ed25519 private key.
func SignQueue(protocolID string, req QueueRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, queueSigningBytes(protocolID, req))
}

// VerifyQueueSignature verifies a QueueRequest's Ed25519 signature.
func VerifyQueueSignature(protocolID string, req QueueRequest, pubHex string) error {
	return verifyHexSig(pubHex, queueSigningBytes(protocolID, req), req.Signature)
}

func executeSigningBytes(protocolID string, req ExecuteRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "execute")
	writeField(&buf, req.ProposalID)
	writeField(&buf, req.ExecutorID)
	writeField(&buf, req.Org)
	writeInt64(&buf, req.ExecutedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignExecute signs an ExecuteRequest with the executor's Ed25519 private key.
func SignExecute(protocolID string, req ExecuteRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, executeSigningBytes(protocolID, req))
}

// VerifyExecuteSignature verifies an ExecuteRequest's Ed25519 signature.
func VerifyExecuteSignature(protocolID string, req ExecuteRequest, pubHex string) error {
	return verifyHexSig(pubHex, executeSigningBytes(protocolID, req), req.Signature)
}

func cancelSigningBytes(protocolID string, req CancelRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "cancel")
	writeField(&buf, req.ProposalID)
	writeField(&buf, req.CallerID)
	writeField(&buf, req.Org)
	writeField(&buf, req.Reason)
	writeInt64(&buf, req.CanceledAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignCancel signs a CancelRequest with the caller's Ed25519 private key.
func SignCancel(protocolID string, req CancelRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, cancelSigningBytes(protocolID, req))
}

// VerifyCancelSignature verifies a CancelRequest's Ed25519 signature.
func VerifyCancelSignature(protocolID string, req CancelRequest, pubHex string) error {
	return verifyHexSig(pubHex, cancelSigningBytes(protocolID, req), req.Signature)
}

func delegationSigningBytes(protocolID string, req DelegationRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "delegation")
	writeField(&buf, req.DelegatorID)
	writeField(&buf, req.DelegateeID)
	writeField(&buf, req.Org)
	writeInt64(&buf, req.IssuedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignDelegation signs a DelegationRequest with the delegator's Ed25519 private key.
func SignDelegation(protocolID string, req DelegationRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, delegationSigningBytes(protocolID, req))
}

// VerifyDelegationSignature verifies a DelegationRequest's Ed25519 signature.
func VerifyDelegationSignature(protocolID string, req DelegationRequest, pubHex string) error {
	return verifyHexSig(pubHex, delegationSigningBytes(protocolID, req), req.Signature)
}

func transferPowerSigningBytes(protocolID string, req TransferPowerRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "transfer_power")
	writeField(&buf, req.FromMemberID)
	writeField(&buf, req.ToMemberID)
	writeUint64(&buf, req.Amount)
	writeField(&buf, req.Org)
	writeInt64(&buf, req.IssuedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignTransferPower signs a voting-power transfer request with the sender's private key.
func SignTransferPower(protocolID string, req TransferPowerRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, transferPowerSigningBytes(protocolID, req))
}

// VerifyTransferPowerSignature verifies a TransferPowerRequest's Ed25519 signature.
func VerifyTransferPowerSignature(protocolID string, req TransferPowerRequest, pubHex string) error {
	return verifyHexSig(pubHex, transferPowerSigningBytes(protocolID, req), req.Signature)
}

func emergencySigningBytes(protocolID string, req EmergencyActionRequest) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, protocolID)
	writeField(&buf, "emergency")
	writeField(&buf, req.ActionID)
	writeField(&buf, req.GuardianID)
	writeField(&buf, req.Org)
	writeField(&buf, string(req.Kind))
	writeField(&buf, req.TargetSubsystem)
	writeInt64(&buf, req.Duration.Nanoseconds())
	writeField(&buf, req.Reason)
	writeInt64(&buf, req.IssuedAt.UTC().UnixNano())
	writeField(&buf, req.Nonce)
	return buf.Bytes()
}

// SignEmergencyAction signs an EmergencyActionRequest with the guardian's Ed25519 private key.
func SignEmergencyAction(protocolID string, req EmergencyActionRequest, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, emergencySigningBytes(protocolID, req))
}

// VerifyEmergencySignature verifies an EmergencyActionRequest's Ed25519 signature.
func VerifyEmergencySignature(protocolID string, req EmergencyActionRequest, pubHex string) error {
	return verifyHexSig(pubHex, emergencySigningBytes(protocolID, req), req.Signature)
}

// ComputeStateDigest returns the deterministic SHA-256 digest of SovereignState.
func ComputeStateDigest(state SovereignState) (string, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("marshal sovereign state: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// ComputeBundleHash binds the bundle metadata, StateDigest, and LedgerHeadHash.
func ComputeBundleHash(b *SovereignBundle) string {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, "bundle_hash")
	writeField(&buf, b.BundleVersion)
	writeField(&buf, b.ProtocolID)
	writeField(&buf, b.NetworkVersion)
	writeField(&buf, b.ParentProtocolID)
	writeField(&buf, b.ParentBundleHash)
	writeInt64(&buf, b.ExportedAt.UTC().UnixNano())
	writeField(&buf, b.StateDigest)
	writeField(&buf, b.LedgerHeadHash)
	writeUint64(&buf, uint64(len(b.Ledger)))
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func bundleSigningBytes(b *SovereignBundle) []byte {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, b.ProtocolID)
	writeField(&buf, "sovereign_bundle")
	writeField(&buf, b.BundleVersion)
	writeField(&buf, b.BundleHash)
	writeField(&buf, b.ExporterID)
	writeField(&buf, b.ExporterPubKeyHex)
	writeInt64(&buf, b.ExportedAt.UTC().UnixNano())
	return buf.Bytes()
}

// SignSovereignBundle signs a prepared SovereignBundle with the exporter's private key.
func SignSovereignBundle(b *SovereignBundle, priv ed25519.PrivateKey) (string, error) {
	return signHex(priv, bundleSigningBytes(b))
}

// VerifySovereignBundleSignature checks the exporter's Ed25519 signature over b.
func VerifySovereignBundleSignature(b *SovereignBundle) error {
	return verifyHexSig(b.ExporterPubKeyHex, bundleSigningBytes(b), b.ExporterSignature)
}
