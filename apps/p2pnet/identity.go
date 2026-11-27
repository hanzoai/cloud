// SPDX-License-Identifier: Apache-2.0 OR MIT

package p2pnet

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion identifies the wire and envelope schema version.
const ProtocolVersion = "p2pnet/1.0"

// DefaultMaxClockSkewSec is the default maximum forward clock skew accepted
// for signed peer records and capability announcements.
const DefaultMaxClockSkewSec int64 = 30

// Domain separators prevent cross-protocol signature replay.
const (
	domainPeerRecord           = "hanzo-p2pnet-peer-record-v1\x00"
	domainCapabilityAnnounce   = "hanzo-p2pnet-capability-v1\x00"
	domainInferenceResponse    = "hanzo-p2pnet-inference-resp-v1\x00"
	domainMeshExportBundle     = "hanzo-p2pnet-mesh-export-v1\x00"
	domainUDPProbe             = "hanzo-p2pnet-udp-probe-v1\x00"
	libp2pEd25519PubKeyPrefix0 = 0x08
	libp2pEd25519PubKeyPrefix1 = 0x01
	libp2pEd25519PubKeyPrefix2 = 0x12
	libp2pEd25519PubKeyPrefix3 = 0x20
	multihashSHA256Code        = 0x12
	multihashSHA256Len         = 0x20
)

// PeerID is a self-certifying libp2p-style base58btc SHA2-256 multihash of a
// peer's Ed25519 public key envelope.
type PeerID string

// NATReachability describes how a peer can be dialed by the mesh.
type NATReachability string

const (
	NATDirect      NATReachability = "direct"
	NATRelayed     NATReachability = "nat_relayed"
	NATRelayServer NATReachability = "relay_server"
)

// NodeRole describes the operational role of a peer in the mesh.
type NodeRole string

const (
	RoleBootstrap NodeRole = "bootstrap"
	RoleRouter    NodeRole = "router"
	RoleRelay     NodeRole = "relay"
	RoleProvider  NodeRole = "provider"
	RoleClient    NodeRole = "client"
)

// VerificationClass explicitly distinguishes cryptographic identity proofs,
// live network reachability probes, and optional external hardware TEE quotes.
type VerificationClass string

const (
	VerificationClassCryptoPeerSignature  VerificationClass = "crypto_peer_signature"
	VerificationClassNetworkLivenessProbe VerificationClass = "network_liveness_probe"
	VerificationClassHardwareAttestation  VerificationClass = "hardware_attestation"
)

// Sentinel verification errors returned fail-closed across identity,
// announcement, DHT, and routing verification paths.
var (
	ErrInvalidProtocolVersion = errors.New("p2pnet: invalid protocol version")
	ErrInvalidPublicKey       = errors.New("p2pnet: invalid ed25519 public key")
	ErrPeerIDMismatch         = errors.New("p2pnet: peer id does not match public key")
	ErrInvalidSignature       = errors.New("p2pnet: invalid ed25519 signature")
	ErrAnnouncementExpired    = errors.New("p2pnet: announcement expired or invalid ttl")
	ErrClockSkewFuture        = errors.New("p2pnet: announcement issued too far in future")
	ErrSequenceRollback       = errors.New("p2pnet: non-monotonic sequence rollback")
	ErrReplayNonce            = errors.New("p2pnet: duplicate replay nonce")
	ErrMissingNonce           = errors.New("p2pnet: missing replay nonce")
	ErrInvalidSequence        = errors.New("p2pnet: sequence must be >= 1")
	ErrMissingEndpoints       = errors.New("p2pnet: announcement has no dialable or relay addresses")
	ErrSybilFloodDetected     = errors.New("p2pnet: sybil flood or liveness admission check failed")
	ErrNoRouteFound           = errors.New("p2pnet: no reachable verified provider route found")
	ErrRelayUnavailable       = errors.New("p2pnet: circuit relay unavailable")
	ErrBundleTampered         = errors.New("p2pnet: export bundle digest or signature invalid")
	ErrResponseMismatch       = errors.New("p2pnet: inference response does not match request")
)

// Identity holds a node's Ed25519 keypair, self-certifying PeerID, and
// Kademlia XOR key hash. It also tracks the highest announcement sequence
// issued by this identity in-process so reusing an Identity across restarts
// preserves monotonic sequencing.
type Identity struct {
	PeerID     PeerID
	PublicKey  ed25519.PublicKey
	PrivateKey ed25519.PrivateKey
	KeyHash    [32]byte
	lastSeq    atomic.Uint64
}

// LastSeq returns the highest announcement sequence recorded on this Identity.
func (id *Identity) LastSeq() uint64 {
	if id == nil {
		return 0
	}
	return id.lastSeq.Load()
}

// ObserveSeq raises this Identity's sequence floor to at least seq.
func (id *Identity) ObserveSeq(seq uint64) {
	if id == nil || seq == 0 {
		return
	}
	for {
		cur := id.lastSeq.Load()
		if seq <= cur {
			return
		}
		if id.lastSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// AdvanceSeq atomically advances this Identity's sequence to max(lastSeq, floor)+1
// and returns the newly allocated monotonic sequence number.
func (id *Identity) AdvanceSeq(floor uint64) uint64 {
	if id == nil {
		return floor + 1
	}
	for {
		cur := id.lastSeq.Load()
		base := cur
		if floor > base {
			base = floor
		}
		next := base + 1
		if id.lastSeq.CompareAndSwap(cur, next) {
			return next
		}
	}
}

// SequenceStore durably persists the highest issued announcement sequence per
// PeerID across process restarts.
type SequenceStore interface {
	LoadSequence(peerID PeerID) (uint64, error)
	SaveSequence(peerID PeerID, seq uint64) error
}

// FileSequenceStore persists per-PeerID announcement sequences in a JSON file
// using atomic temp-file writes so restarts never roll back sequences.
type FileSequenceStore struct {
	mu   sync.Mutex
	path string
}

// NewFileSequenceStore creates a durable JSON-backed SequenceStore at path.
func NewFileSequenceStore(path string) *FileSequenceStore {
	return &FileSequenceStore{path: path}
}

// LoadSequence returns the persisted sequence for peerID, or 0 if not present.
func (fs *FileSequenceStore) LoadSequence(peerID PeerID) (uint64, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	m, err := fs.readLocked()
	if err != nil {
		return 0, err
	}
	return m[peerID], nil
}

// SaveSequence atomically persists seq for peerID if it exceeds the stored value.
func (fs *FileSequenceStore) SaveSequence(peerID PeerID, seq uint64) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	m, err := fs.readLocked()
	if err != nil {
		return err
	}
	if seq <= m[peerID] {
		return nil
	}
	m[peerID] = seq
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	dir := filepath.Dir(fs.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := fs.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, fs.path)
}

func (fs *FileSequenceStore) readLocked() (map[PeerID]uint64, error) {
	raw, err := os.ReadFile(fs.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return make(map[PeerID]uint64), nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return make(map[PeerID]uint64), nil
	}
	m := make(map[PeerID]uint64)
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// GenerateIdentity creates a new Ed25519 identity. When rng is nil,
// crypto/rand.Reader is used.
func GenerateIdentity(rng io.Reader) (*Identity, error) {
	if rng == nil {
		rng = rand.Reader
	}
	pub, priv, err := ed25519.GenerateKey(rng)
	if err != nil {
		return nil, fmt.Errorf("p2pnet: generate ed25519 key: %w", err)
	}
	pid, err := PeerIDFromPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return &Identity{
		PeerID:     pid,
		PublicKey:  pub,
		PrivateKey: priv,
		KeyHash:    PeerKeyHash(pid),
	}, nil
}

// PublicKeyHex returns the lowercase hex representation of the peer's Ed25519
// public key.
func (id *Identity) PublicKeyHex() string {
	return hex.EncodeToString(id.PublicKey)
}

// DecodePublicKeyHex decodes a hex-encoded 32-byte Ed25519 public key.
func DecodePublicKeyHex(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, ErrInvalidPublicKey
	}
	return ed25519.PublicKey(b), nil
}

// PeerIDFromPublicKey derives a self-certifying libp2p-style base58btc
// SHA2-256 multihash PeerID from an Ed25519 public key.
func PeerIDFromPublicKey(pub ed25519.PublicKey) (PeerID, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", ErrInvalidPublicKey
	}
	// libp2p protobuf PublicKey encoding for Ed25519:
	// field 1 (KeyType = Ed25519 = 1): 0x08 0x01
	// field 2 (Data = 32 bytes):       0x12 0x20 <32-byte pubkey>
	env := make([]byte, 4+ed25519.PublicKeySize)
	env[0] = libp2pEd25519PubKeyPrefix0
	env[1] = libp2pEd25519PubKeyPrefix1
	env[2] = libp2pEd25519PubKeyPrefix2
	env[3] = libp2pEd25519PubKeyPrefix3
	copy(env[4:], pub)

	digest := sha256.Sum256(env)
	mh := make([]byte, 2+len(digest))
	mh[0] = multihashSHA256Code
	mh[1] = multihashSHA256Len
	copy(mh[2:], digest[:])
	return PeerID(base58Encode(mh)), nil
}

// VerifyPeerIDBinding checks that id is the exact self-certifying multihash of pub.
func VerifyPeerIDBinding(id PeerID, pub ed25519.PublicKey) error {
	derived, err := PeerIDFromPublicKey(pub)
	if err != nil {
		return err
	}
	if derived != id {
		return fmt.Errorf("%w: expected %s got %s", ErrPeerIDMismatch, derived, id)
	}
	return nil
}

// PeerKeyHash returns the 256-bit Kademlia key hash for a PeerID.
func PeerKeyHash(id PeerID) [32]byte {
	return sha256.Sum256([]byte(id))
}

// ModelKeyHash returns the 256-bit Kademlia DHT provider key for a model ID.
func ModelKeyHash(modelID string) [32]byte {
	return sha256.Sum256([]byte("model:" + strings.TrimSpace(modelID)))
}

// XORDistance returns the Kademlia XOR distance between two 256-bit keys.
func XORDistance(a, b [32]byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// CompareDistance compares two 256-bit XOR distances lexicographically (big-endian).
// Returns -1 if a < b, 0 if a == b, +1 if a > b.
func CompareDistance(a, b [32]byte) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// CommonPrefixLen returns the number of leading matching bits between a and b (0..256).
func CommonPrefixLen(a, b [32]byte) int {
	for i := range a {
		x := a[i] ^ b[i]
		if x != 0 {
			return i*8 + bits.LeadingZeros8(x)
		}
	}
	return 256
}

// PeerRecord is a libp2p-style self-signed peer addressing envelope with
// monotonic sequence numbers, TTL expiry, and anti-replay nonces.
type PeerRecord struct {
	ProtocolVersion string          `json:"protocol_version"`
	PeerID          PeerID          `json:"peer_id"`
	PublicKey       string          `json:"public_key"`
	Addrs           []string        `json:"addrs"`
	RelayPeers      []PeerID        `json:"relay_peers,omitempty"`
	NATMode         NATReachability `json:"nat_mode"`
	Seq             uint64          `json:"seq"`
	Nonce           string          `json:"nonce"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expires_at"`
}

// SignedPeerRecord binds a PeerRecord to its Ed25519 signature and explicit
// VerificationClass.
type SignedPeerRecord struct {
	Record            PeerRecord        `json:"record"`
	Signature         string            `json:"signature"`
	VerificationClass VerificationClass `json:"verification_class"`
}

// CanonicalPeerRecordBytes encodes a PeerRecord with domain separation for signing.
func CanonicalPeerRecordBytes(rec PeerRecord) ([]byte, error) {
	raw, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(domainPeerRecord)+len(raw))
	out = append(out, domainPeerRecord...)
	out = append(out, raw...)
	return out, nil
}

// SignPeerRecord signs rec with id's Ed25519 private key.
func SignPeerRecord(id *Identity, rec PeerRecord) (SignedPeerRecord, error) {
	if id == nil {
		return SignedPeerRecord{}, errors.New("p2pnet: nil identity")
	}
	if rec.ProtocolVersion == "" {
		rec.ProtocolVersion = ProtocolVersion
	}
	rec.PeerID = id.PeerID
	rec.PublicKey = id.PublicKeyHex()
	if rec.Seq == 0 {
		return SignedPeerRecord{}, ErrInvalidSequence
	}
	if strings.TrimSpace(rec.Nonce) == "" {
		return SignedPeerRecord{}, ErrMissingNonce
	}
	payload, err := CanonicalPeerRecordBytes(rec)
	if err != nil {
		return SignedPeerRecord{}, err
	}
	sig := ed25519.Sign(id.PrivateKey, payload)
	return SignedPeerRecord{
		Record:            rec,
		Signature:         hex.EncodeToString(sig),
		VerificationClass: VerificationClassCryptoPeerSignature,
	}, nil
}

// VerifyPeerRecord validates a SignedPeerRecord fail-closed.
func VerifyPeerRecord(spr SignedPeerRecord, nowUnix int64, maxClockSkewSec int64) error {
	rec := spr.Record
	if rec.ProtocolVersion != ProtocolVersion {
		return ErrInvalidProtocolVersion
	}
	if rec.Seq == 0 {
		return ErrInvalidSequence
	}
	if strings.TrimSpace(rec.Nonce) == "" {
		return ErrMissingNonce
	}
	if rec.ExpiresAt <= rec.IssuedAt || nowUnix > rec.ExpiresAt {
		return ErrAnnouncementExpired
	}
	if maxClockSkewSec <= 0 {
		maxClockSkewSec = DefaultMaxClockSkewSec
	}
	if rec.IssuedAt > nowUnix+maxClockSkewSec {
		return ErrClockSkewFuture
	}
	if len(rec.Addrs) == 0 && len(rec.RelayPeers) == 0 {
		return ErrMissingEndpoints
	}
	pub, err := DecodePublicKeyHex(rec.PublicKey)
	if err != nil {
		return err
	}
	if err := VerifyPeerIDBinding(rec.PeerID, pub); err != nil {
		return err
	}
	sig, err := hex.DecodeString(strings.TrimSpace(spr.Signature))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrInvalidSignature
	}
	payload, err := CanonicalPeerRecordBytes(rec)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, sig) {
		return ErrInvalidSignature
	}
	return nil
}

// GeneratePeerTLSCert mints a self-signed Ed25519 X.509 certificate binding
// the peer's public key and PeerID for mutual-TLS 1.3 peer authentication.
func GeneratePeerTLSCert(id *Identity) (tls.Certificate, error) {
	if id == nil {
		return tls.Certificate{}, errors.New("p2pnet: nil identity")
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 120)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   string(id.PeerID),
			Organization: []string{"p2pnet-self-certifying"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{string(id.PeerID), "localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, id.PublicKey, id.PrivateKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("p2pnet: create peer tls cert: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  id.PrivateKey,
		Leaf:        leaf,
	}, nil
}

// VerifyPeerCert extracts and validates the remote peer's Ed25519 self-signed
// X.509 certificate, returning its authenticated PeerID. If expectedPeer is
// non-empty, the derived PeerID must match it exactly.
func VerifyPeerCert(rawCerts [][]byte, expectedPeer PeerID) (PeerID, ed25519.PublicKey, error) {
	if len(rawCerts) == 0 {
		return "", nil, errors.New("p2pnet: peer presented no tls certificate")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return "", nil, fmt.Errorf("p2pnet: parse peer cert: %w", err)
	}
	if err := cert.CheckSignatureFrom(cert); err != nil {
		return "", nil, fmt.Errorf("%w: self-signed cert signature check failed: %v", ErrInvalidSignature, err)
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return "", nil, ErrInvalidPublicKey
	}
	derived, err := PeerIDFromPublicKey(pub)
	if err != nil {
		return "", nil, err
	}
	if cert.Subject.CommonName != string(derived) {
		return "", nil, fmt.Errorf("%w: cert CN %q != derived %q", ErrPeerIDMismatch, cert.Subject.CommonName, derived)
	}
	if expectedPeer != "" && derived != expectedPeer {
		return "", nil, fmt.Errorf("%w: expected %s got %s", ErrPeerIDMismatch, expectedPeer, derived)
	}
	return derived, pub, nil
}

// ServerTLSConfig returns a mutual-TLS 1.3 server configuration authenticating
// every connecting peer via its Ed25519 certificate.
func ServerTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{ProtocolVersion},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			_, _, err := VerifyPeerCert(rawCerts, "")
			return err
		},
	}
}

// ClientTLSConfig returns a mutual-TLS 1.3 client configuration that verifies
// the remote server's Ed25519 self-certifying PeerID.
func ClientTLSConfig(cert tls.Certificate, expectedPeer PeerID) *tls.Config {
	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // Verified via VerifyPeerCertificate self-certifying Ed25519 check.
		NextProtos:         []string{ProtocolVersion},
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			_, _, err := VerifyPeerCert(rawCerts, expectedPeer)
			return err
		},
	}
}

// FormatMultiaddr formats a loopback TCP or QUIC-v1 multiaddr string.
func FormatMultiaddr(transport string, host string, port int, peerID PeerID) string {
	switch transport {
	case "quic-v1", "udp":
		return fmt.Sprintf("/ip4/%s/udp/%d/quic-v1/p2p/%s", host, port, peerID)
	default:
		return fmt.Sprintf("/ip4/%s/tcp/%d/p2p/%s", host, port, peerID)
	}
}

// FormatCircuitMultiaddr formats a libp2p circuit-relay v2 multiaddr string.
func FormatCircuitMultiaddr(relayID PeerID, targetID PeerID) string {
	return fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", relayID, targetID)
}

// ParseTCPMultiaddr extracts host:port and PeerID from /ip4/<ip>/tcp/<port>/p2p/<id>.
func ParseTCPMultiaddr(addr string) (hostPort string, peerID PeerID, ok bool) {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	// Expected: ["", "ip4", host, "tcp", port, "p2p", peerID]
	if len(parts) == 7 && parts[1] == "ip4" && parts[3] == "tcp" && parts[5] == "p2p" {
		if parts[2] == "" || parts[4] == "" || parts[6] == "" {
			return "", "", false
		}
		return net.JoinHostPort(parts[2], parts[4]), PeerID(parts[6]), true
	}
	return "", "", false
}

// ParseUDPMultiaddr extracts host:port and PeerID from /ip4/<ip>/udp/<port>/quic-v1/p2p/<id>.
func ParseUDPMultiaddr(addr string) (hostPort string, peerID PeerID, ok bool) {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	// Expected: ["", "ip4", host, "udp", port, "quic-v1", "p2p", peerID]
	if len(parts) == 8 && parts[1] == "ip4" && parts[3] == "udp" && parts[5] == "quic-v1" && parts[6] == "p2p" {
		if parts[2] == "" || parts[4] == "" || parts[7] == "" {
			return "", "", false
		}
		return net.JoinHostPort(parts[2], parts[4]), PeerID(parts[7]), true
	}
	return "", "", false
}

// ParseCircuitMultiaddr extracts relay PeerID and target PeerID from
// /p2p/<relayID>/p2p-circuit/p2p/<targetID>.
func ParseCircuitMultiaddr(addr string) (relayID PeerID, targetID PeerID, ok bool) {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	// Expected: ["", "p2p", relayID, "p2p-circuit", "p2p", targetID]
	if len(parts) == 6 && parts[1] == "p2p" && parts[3] == "p2p-circuit" && parts[4] == "p2p" {
		if parts[2] == "" || parts[5] == "" {
			return "", "", false
		}
		return PeerID(parts[2]), PeerID(parts[5]), true
	}
	return "", "", false
}

// GenerateNonce creates a 16-byte hex cryptographic nonce.
func GenerateNonce(rng io.Reader) (string, error) {
	if rng == nil {
		rng = rand.Reader
	}
	var b [16]byte
	if _, err := io.ReadFull(rng, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(input []byte) string {
	if len(input) == 0 {
		return ""
	}
	x := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	zero := big.NewInt(0)
	mod := new(big.Int)
	var out []byte
	for x.Cmp(zero) > 0 {
		x.QuoRem(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for _, b := range input {
		if b != 0 {
			break
		}
		out = append(out, base58Alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}
