// SPDX-License-Identifier: Apache-2.0

package provenance

import (
	"crypto"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTLogOrigin identifies the default Hanzo transparency log checkpoint domain.
	DefaultTLogOrigin = "hanzo.ai/provenance/tlog/v1"

	// RFC 6962 domain separation prefixes prevent second-preimage attacks between
	// leaf hashes and interior node hashes.
	merkleLeafPrefix     byte = 0x00
	merkleInteriorPrefix byte = 0x01
)

var emptyTreeHash = sha256.Sum256(nil)

// HashLeaf computes the RFC 6962 domain-separated leaf hash SHA-256(0x00 || data).
func HashLeaf(data []byte) [32]byte {
	h := sha256.New()
	h.Write([]byte{merkleLeafPrefix})
	h.Write(data)
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// HashChildren computes the RFC 6962 domain-separated interior hash SHA-256(0x01 || left || right).
func HashChildren(left, right [32]byte) [32]byte {
	var buf [1 + 32 + 32]byte
	buf[0] = merkleInteriorPrefix
	copy(buf[1:33], left[:])
	copy(buf[33:65], right[:])
	return sha256.Sum256(buf[:])
}

// largestPowerOfTwoLessThan returns the largest power of 2 strictly less than n (for n > 1).
func largestPowerOfTwoLessThan(n uint64) uint64 {
	return uint64(1) << (bits.Len64(n-1) - 1)
}

func isPowerOfTwo(n uint64) bool {
	return n > 0 && (n&(n-1)) == 0
}

// ComputeMerkleRoot computes the RFC 6962 Merkle Tree Hash over pre-hashed leaves.
func ComputeMerkleRoot(leafHashes [][32]byte) [32]byte {
	n := uint64(len(leafHashes))
	if n == 0 {
		return emptyTreeHash
	}
	if n == 1 {
		return leafHashes[0]
	}
	k := largestPowerOfTwoLessThan(n)
	left := ComputeMerkleRoot(leafHashes[:k])
	right := ComputeMerkleRoot(leafHashes[k:])
	return HashChildren(left, right)
}

// InclusionProof proves a leaf at LogIndex is committed in the Merkle tree of size TreeSize.
type InclusionProof struct {
	LogIndex uint64   `json:"logIndex"`
	TreeSize uint64   `json:"treeSize"`
	LeafHash string   `json:"leafHash"`
	RootHash string   `json:"rootHash"`
	Hashes   []string `json:"hashes"`
}

// Clone returns a deep copy of InclusionProof.
func (p InclusionProof) Clone() InclusionProof {
	out := p
	if p.Hashes != nil {
		out.Hashes = make([]string, len(p.Hashes))
		copy(out.Hashes, p.Hashes)
	}
	return out
}

// ConsistencyProof proves the tree at FirstSize is an append-only prefix of the tree at SecondSize.
type ConsistencyProof struct {
	FirstSize  uint64   `json:"firstSize"`
	SecondSize uint64   `json:"secondSize"`
	FirstRoot  string   `json:"firstRoot"`
	SecondRoot string   `json:"secondRoot"`
	Hashes     []string `json:"hashes"`
}

// Clone returns a deep copy of ConsistencyProof.
func (p ConsistencyProof) Clone() ConsistencyProof {
	out := p
	if p.Hashes != nil {
		out.Hashes = make([]string, len(p.Hashes))
		copy(out.Hashes, p.Hashes)
	}
	return out
}

// SignedTreeHead (STH) is a signed transparency log checkpoint.
type SignedTreeHead struct {
	Origin    string    `json:"origin"`
	TreeSize  uint64    `json:"treeSize"`
	RootHash  string    `json:"rootHash"`
	Timestamp time.Time `json:"timestamp"`
	KeyID     string    `json:"keyId"`
	Algorithm string    `json:"algorithm"`
	Signature string    `json:"signature"`
}

// CanonicalCheckpointBytes returns the canonical signed message for a checkpoint STH.
func CanonicalCheckpointBytes(origin string, treeSize uint64, rootHash string, ts time.Time) []byte {
	var b strings.Builder
	b.Grow(len(origin) + len(rootHash) + 48)
	b.WriteString(origin)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatUint(treeSize, 10))
	b.WriteByte('\n')
	b.WriteString(rootHash)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(ts.UTC().UnixNano(), 10))
	b.WriteByte('\n')
	return []byte(b.String())
}

// VerifySignedTreeHead verifies the cryptographic signature and format of sth using pub.
func VerifySignedTreeHead(sth SignedTreeHead, expectedOrigin string, pub crypto.PublicKey) error {
	if pub == nil {
		return fmt.Errorf("%w: nil transparency log public key", ErrTLogInvalid)
	}
	if strings.TrimSpace(sth.Origin) == "" {
		return fmt.Errorf("%w: empty checkpoint origin", ErrTLogInvalid)
	}
	if expectedOrigin != "" && sth.Origin != expectedOrigin {
		return fmt.Errorf("%w: checkpoint origin %q does not match expected %q", ErrTLogInvalid, sth.Origin, expectedOrigin)
	}
	if err := ValidateSHA256Hex(sth.RootHash); err != nil {
		return fmt.Errorf("%w: invalid checkpoint root hash: %v", ErrTLogInvalid, err)
	}
	if sth.Timestamp.IsZero() {
		return fmt.Errorf("%w: zero checkpoint timestamp", ErrTLogInvalid)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(sth.Signature)
	if err != nil || len(sigBytes) == 0 {
		return fmt.Errorf("%w: invalid checkpoint signature encoding", ErrTLogInvalid)
	}
	msg := CanonicalCheckpointBytes(sth.Origin, sth.TreeSize, sth.RootHash, sth.Timestamp)
	if err := VerifySignature(pub, sth.Algorithm, msg, sigBytes); err != nil {
		return fmt.Errorf("%w: checkpoint signature invalid: %v", ErrTLogInvalid, err)
	}
	return nil
}

// VerifyInclusionProof verifies an RFC 6962 Merkle inclusion proof for leafData against expectedRoot.
func VerifyInclusionProof(leafData []byte, proof InclusionProof, expectedRoot [32]byte) error {
	if proof.TreeSize == 0 || proof.LogIndex >= proof.TreeSize {
		return fmt.Errorf("%w: invalid inclusion proof bounds index=%d treeSize=%d", ErrTLogInvalid, proof.LogIndex, proof.TreeSize)
	}
	leafHash := HashLeaf(leafData)
	leafHex := hex.EncodeToString(leafHash[:])
	if proof.LeafHash != "" && subtle.ConstantTimeCompare([]byte(proof.LeafHash), []byte(leafHex)) != 1 {
		return fmt.Errorf("%w: leaf hash mismatch", ErrTLogInvalid)
	}
	if proof.RootHash != "" {
		expHex := hex.EncodeToString(expectedRoot[:])
		if subtle.ConstantTimeCompare([]byte(proof.RootHash), []byte(expHex)) != 1 {
			return fmt.Errorf("%w: proof rootHash does not match checkpoint root", ErrTLogInvalid)
		}
	}

	fn := proof.LogIndex
	sn := proof.TreeSize - 1
	r := leafHash

	for i, hexHash := range proof.Hashes {
		if sn == 0 {
			return fmt.Errorf("%w: inclusion proof carries trailing hash at position %d", ErrTLogInvalid, i)
		}
		sibling, err := decodeHash32(hexHash)
		if err != nil {
			return fmt.Errorf("%w: inclusion proof hash[%d]: %v", ErrTLogInvalid, i, err)
		}
		if (fn&1) == 1 || fn == sn {
			r = HashChildren(sibling, r)
			for (fn&1) == 0 && fn != 0 {
				fn >>= 1
				sn >>= 1
			}
		} else {
			r = HashChildren(r, sibling)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 {
		return fmt.Errorf("%w: inclusion proof truncated (remaining tree bits)", ErrTLogInvalid)
	}
	if subtle.ConstantTimeCompare(r[:], expectedRoot[:]) != 1 {
		return fmt.Errorf("%w: inclusion proof root mismatch", ErrTLogInvalid)
	}
	return nil
}

// VerifyConsistencyProof verifies an RFC 6962 consistency proof between two tree checkpoints.
func VerifyConsistencyProof(proof ConsistencyProof, firstRoot, secondRoot [32]byte) error {
	if proof.FirstSize > proof.SecondSize {
		return fmt.Errorf("%w: consistency proof rollback firstSize=%d > secondSize=%d", ErrTLogInvalid, proof.FirstSize, proof.SecondSize)
	}
	if proof.FirstRoot != "" {
		if subtle.ConstantTimeCompare([]byte(proof.FirstRoot), []byte(hex.EncodeToString(firstRoot[:]))) != 1 {
			return fmt.Errorf("%w: consistency proof firstRoot mismatch", ErrTLogInvalid)
		}
	}
	if proof.SecondRoot != "" {
		if subtle.ConstantTimeCompare([]byte(proof.SecondRoot), []byte(hex.EncodeToString(secondRoot[:]))) != 1 {
			return fmt.Errorf("%w: consistency proof secondRoot mismatch", ErrTLogInvalid)
		}
	}
	if proof.FirstSize == 0 {
		if len(proof.Hashes) != 0 {
			return fmt.Errorf("%w: non-empty consistency proof for empty first tree", ErrTLogInvalid)
		}
		return nil
	}
	if proof.FirstSize == proof.SecondSize {
		if len(proof.Hashes) != 0 {
			return fmt.Errorf("%w: non-empty consistency proof for equal tree sizes", ErrTLogInvalid)
		}
		if subtle.ConstantTimeCompare(firstRoot[:], secondRoot[:]) != 1 {
			return fmt.Errorf("%w: split-view equivocation detected for treeSize=%d", ErrTLogInvalid, proof.FirstSize)
		}
		return nil
	}
	if len(proof.Hashes) == 0 {
		return fmt.Errorf("%w: empty consistency proof for growing tree (%d -> %d)", ErrTLogInvalid, proof.FirstSize, proof.SecondSize)
	}

	nodes := make([][32]byte, len(proof.Hashes))
	for i, h := range proof.Hashes {
		decoded, err := decodeHash32(h)
		if err != nil {
			return fmt.Errorf("%w: consistency proof hash[%d]: %v", ErrTLogInvalid, i, err)
		}
		nodes[i] = decoded
	}

	fn := proof.FirstSize - 1
	sn := proof.SecondSize - 1
	for (fn & 1) == 1 {
		fn >>= 1
		sn >>= 1
	}

	var fr, sr [32]byte
	idx := 0
	if isPowerOfTwo(proof.FirstSize) {
		fr = firstRoot
		sr = firstRoot
	} else {
		fr = nodes[0]
		sr = nodes[0]
		idx = 1
	}

	for ; idx < len(nodes); idx++ {
		if sn == 0 {
			return fmt.Errorf("%w: consistency proof carries trailing hash at position %d", ErrTLogInvalid, idx)
		}
		p := nodes[idx]
		if (fn&1) == 1 || fn == sn {
			fr = HashChildren(p, fr)
			sr = HashChildren(p, sr)
			for (fn&1) == 0 && fn != 0 {
				fn >>= 1
				sn >>= 1
			}
		} else {
			sr = HashChildren(sr, p)
		}
		fn >>= 1
		sn >>= 1
	}
	if sn != 0 {
		return fmt.Errorf("%w: consistency proof truncated", ErrTLogInvalid)
	}
	if subtle.ConstantTimeCompare(fr[:], firstRoot[:]) != 1 {
		return fmt.Errorf("%w: consistency proof failed to reconstruct firstRoot (equivocation)", ErrTLogInvalid)
	}
	if subtle.ConstantTimeCompare(sr[:], secondRoot[:]) != 1 {
		return fmt.Errorf("%w: consistency proof failed to reconstruct secondRoot", ErrTLogInvalid)
	}
	return nil
}

// LogEntry represents one appended transparency log entry with its inclusion proof and STH.
type LogEntry struct {
	LogIndex       uint64         `json:"logIndex"`
	IntegratedTime time.Time      `json:"integratedTime"`
	CanonicalBody  []byte         `json:"canonicalBody"`
	LeafHash       string         `json:"leafHash"`
	InclusionProof InclusionProof `json:"inclusionProof"`
	Checkpoint     SignedTreeHead `json:"checkpoint"`
}

// Clone returns a deep copy of LogEntry.
func (e LogEntry) Clone() LogEntry {
	out := e
	if e.CanonicalBody != nil {
		out.CanonicalBody = make([]byte, len(e.CanonicalBody))
		copy(out.CanonicalBody, e.CanonicalBody)
	}
	out.InclusionProof = e.InclusionProof.Clone()
	return out
}

// TransparencyLog is a concurrency-safe, append-only RFC 6962 Merkle transparency log.
type TransparencyLog struct {
	mu       sync.RWMutex
	origin   string
	signer   Signer
	entries  []LogEntry
	levels   [][][32]byte // levels[h][i] = complete subtree hash of size 2^h at offset i*2^h
	lastTime time.Time
	latest   SignedTreeHead
}

// NewTransparencyLog creates an empty append-only transparency log signed by signer.
func NewTransparencyLog(origin string, signer Signer) (*TransparencyLog, error) {
	if strings.TrimSpace(origin) == "" {
		origin = DefaultTLogOrigin
	}
	if signer == nil {
		return nil, errors.New("provenance: nil transparency log signer")
	}
	return &TransparencyLog{
		origin: origin,
		signer: signer,
		levels: make([][][32]byte, 1),
	}, nil
}

// Origin returns the transparency log's checkpoint origin string.
func (l *TransparencyLog) Origin() string {
	return l.origin
}

// PublicKey returns the transparency log's checkpoint verification public key.
func (l *TransparencyLog) PublicKey() crypto.PublicKey {
	return l.signer.PublicKey()
}

// Size returns the current number of entries in the transparency log.
func (l *TransparencyLog) Size() uint64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return uint64(len(l.entries))
}

// Append adds entryBytes to the append-only Merkle tree, enforces monotonic integrated time,
// signs a new checkpoint STH, and returns the resulting LogEntry with its inclusion proof.
func (l *TransparencyLog) Append(entryBytes []byte, now time.Time) (*LogEntry, error) {
	if len(entryBytes) == 0 {
		return nil, errors.New("provenance: cannot append empty transparency log entry")
	}
	explicitTime := !now.IsZero()

	l.mu.Lock()
	defer l.mu.Unlock()

	if explicitTime {
		now = now.UTC()
		if !l.lastTime.IsZero() && now.Before(l.lastTime) {
			return nil, fmt.Errorf("%w: refusing backdated entry timestamp %s < last %s", ErrTLogInvalid, now.Format(time.RFC3339Nano), l.lastTime.Format(time.RFC3339Nano))
		}
	} else {
		now = time.Now().UTC()
		if !l.lastTime.IsZero() && now.Before(l.lastTime) {
			now = l.lastTime
		}
	}

	bodyCopy := make([]byte, len(entryBytes))
	copy(bodyCopy, entryBytes)

	leafHash := HashLeaf(bodyCopy)
	idx := uint64(len(l.entries))
	l.appendLeafHashLocked(leafHash)
	treeSize := idx + 1

	root := l.subtreeHashLocked(0, treeSize)
	rootHex := hex.EncodeToString(root[:])

	sth, err := l.signCheckpointLocked(treeSize, rootHex, now)
	if err != nil {
		// Roll back the appended leaf if signing failed.
		l.rollbackLastLeafLocked()
		return nil, err
	}

	pathHashes := l.inclusionPathLocked(idx, 0, treeSize)
	proof := InclusionProof{
		LogIndex: idx,
		TreeSize: treeSize,
		LeafHash: hex.EncodeToString(leafHash[:]),
		RootHash: rootHex,
		Hashes:   encodeHashes(pathHashes),
	}

	entry := LogEntry{
		LogIndex:       idx,
		IntegratedTime: now,
		CanonicalBody:  bodyCopy,
		LeafHash:       hex.EncodeToString(leafHash[:]),
		InclusionProof: proof,
		Checkpoint:     sth,
	}
	l.entries = append(l.entries, entry)
	l.lastTime = now
	l.latest = sth

	out := entry.Clone()
	return &out, nil
}

// LatestCheckpoint returns the most recent SignedTreeHead (or signs an empty tree checkpoint if size == 0).
func (l *TransparencyLog) LatestCheckpoint() (SignedTreeHead, error) {
	l.mu.RLock()
	if len(l.entries) > 0 {
		sth := l.latest
		l.mu.RUnlock()
		return sth, nil
	}
	l.mu.RUnlock()

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) > 0 {
		return l.latest, nil
	}
	rootHex := hex.EncodeToString(emptyTreeHash[:])
	return l.signCheckpointLocked(0, rootHex, time.Now().UTC())
}

// Entry returns a deep copy of the log entry at index.
func (l *TransparencyLog) Entry(index uint64) (*LogEntry, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if index >= uint64(len(l.entries)) {
		return nil, fmt.Errorf("%w: entry index %d out of bounds (size %d)", ErrTLogInvalid, index, len(l.entries))
	}
	out := l.entries[index].Clone()
	return &out, nil
}

// InclusionProof computes an RFC 6962 inclusion proof for index at treeSize (which may be <= current size).
func (l *TransparencyLog) InclusionProof(index, treeSize uint64) (InclusionProof, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	current := uint64(len(l.entries))
	if treeSize == 0 || treeSize > current || index >= treeSize {
		return InclusionProof{}, fmt.Errorf("%w: invalid inclusion proof request index=%d treeSize=%d current=%d", ErrTLogInvalid, index, treeSize, current)
	}
	root := l.subtreeHashLocked(0, treeSize)
	leaf := l.levels[0][index]
	path := l.inclusionPathLocked(index, 0, treeSize)
	return InclusionProof{
		LogIndex: index,
		TreeSize: treeSize,
		LeafHash: hex.EncodeToString(leaf[:]),
		RootHash: hex.EncodeToString(root[:]),
		Hashes:   encodeHashes(path),
	}, nil
}

// ConsistencyProof computes an RFC 6962 consistency proof between firstSize and secondSize.
func (l *TransparencyLog) ConsistencyProof(firstSize, secondSize uint64) (ConsistencyProof, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	current := uint64(len(l.entries))
	if firstSize > secondSize || secondSize > current {
		return ConsistencyProof{}, fmt.Errorf("%w: invalid consistency proof range [%d, %d] (current=%d)", ErrTLogInvalid, firstSize, secondSize, current)
	}
	firstRoot := l.subtreeHashLocked(0, firstSize)
	secondRoot := l.subtreeHashLocked(0, secondSize)
	var hashes [][32]byte
	if firstSize > 0 && firstSize < secondSize {
		hashes = l.consistencySubproofLocked(firstSize, 0, secondSize, true)
	}
	return ConsistencyProof{
		FirstSize:  firstSize,
		SecondSize: secondSize,
		FirstRoot:  hex.EncodeToString(firstRoot[:]),
		SecondRoot: hex.EncodeToString(secondRoot[:]),
		Hashes:     encodeHashes(hashes),
	}, nil
}

func (l *TransparencyLog) appendLeafHashLocked(h [32]byte) {
	l.levels[0] = append(l.levels[0], h)
	level := 0
	for len(l.levels[level])%2 == 0 {
		n := len(l.levels[level])
		parent := HashChildren(l.levels[level][n-2], l.levels[level][n-1])
		level++
		if level >= len(l.levels) {
			l.levels = append(l.levels, nil)
		}
		l.levels[level] = append(l.levels[level], parent)
	}
}

func (l *TransparencyLog) rollbackLastLeafLocked() {
	if len(l.levels[0]) == 0 {
		return
	}
	oldLen := len(l.levels[0])
	l.levels[0] = l.levels[0][:oldLen-1]
	// Any higher level whose node completed on oldLen (i.e. oldLen divisible by 2^level) is trimmed.
	for level := 1; level < len(l.levels); level++ {
		if oldLen%(1<<level) == 0 && len(l.levels[level]) > 0 {
			l.levels[level] = l.levels[level][:len(l.levels[level])-1]
		}
	}
}

func (l *TransparencyLog) subtreeHashLocked(start, end uint64) [32]byte {
	n := end - start
	if n == 0 {
		return emptyTreeHash
	}
	if n == 1 {
		return l.levels[0][start]
	}
	if isPowerOfTwo(n) && (start&(n-1)) == 0 {
		h := bits.TrailingZeros64(n)
		return l.levels[h][start>>h]
	}
	k := largestPowerOfTwoLessThan(n)
	left := l.subtreeHashLocked(start, start+k)
	right := l.subtreeHashLocked(start+k, end)
	return HashChildren(left, right)
}

func (l *TransparencyLog) inclusionPathLocked(index, start, end uint64) [][32]byte {
	n := end - start
	if n <= 1 {
		return nil
	}
	k := largestPowerOfTwoLessThan(n)
	if index < k {
		path := l.inclusionPathLocked(index, start, start+k)
		return append(path, l.subtreeHashLocked(start+k, end))
	}
	path := l.inclusionPathLocked(index-k, start+k, end)
	return append(path, l.subtreeHashLocked(start, start+k))
}

func (l *TransparencyLog) consistencySubproofLocked(m, start, end uint64, complete bool) [][32]byte {
	n := end - start
	if m == n {
		if complete {
			return nil
		}
		return [][32]byte{l.subtreeHashLocked(start, end)}
	}
	k := largestPowerOfTwoLessThan(n)
	if m <= k {
		sub := l.consistencySubproofLocked(m, start, start+k, complete)
		return append(sub, l.subtreeHashLocked(start+k, end))
	}
	sub := l.consistencySubproofLocked(m-k, start+k, end, false)
	return append(sub, l.subtreeHashLocked(start, start+k))
}

func (l *TransparencyLog) signCheckpointLocked(treeSize uint64, rootHex string, ts time.Time) (SignedTreeHead, error) {
	msg := CanonicalCheckpointBytes(l.origin, treeSize, rootHex, ts)
	sig, err := l.signer.SignMessage(msg)
	if err != nil {
		return SignedTreeHead{}, fmt.Errorf("provenance: sign checkpoint: %w", err)
	}
	return SignedTreeHead{
		Origin:    l.origin,
		TreeSize:  treeSize,
		RootHash:  rootHex,
		Timestamp: ts.UTC(),
		KeyID:     l.signer.KeyID(),
		Algorithm: l.signer.Algorithm(),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

func encodeHashes(hs [][32]byte) []string {
	if len(hs) == 0 {
		return []string{}
	}
	out := make([]string, len(hs))
	for i, h := range hs {
		out[i] = hex.EncodeToString(h[:])
	}
	return out
}

func decodeHash32(s string) ([32]byte, error) {
	var out [32]byte
	if err := ValidateSHA256Hex(s); err != nil {
		return out, err
	}
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 32 {
		return out, errors.New("invalid 32-byte hex hash")
	}
	copy(out[:], b)
	return out, nil
}
