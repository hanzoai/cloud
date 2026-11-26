// SPDX-License-Identifier: Apache-2.0 OR MIT

package x402

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	protocol "github.com/hanzoai/commerce/payment/x402"
	"github.com/luxfi/crypto"
)

// ReceiptStatus records the lifecycle state of an x402 payment receipt.
type ReceiptStatus string

const (
	StatusPending        ReceiptStatus = "PENDING"
	StatusSettled        ReceiptStatus = "SETTLED"
	StatusVoided         ReceiptStatus = "VOIDED"
	StatusFailed         ReceiptStatus = "FAILED"
	StatusReversed       ReceiptStatus = "REVERSED"
	StatusRefundRequired ReceiptStatus = "REFUND_REQUIRED"
)

const genesisReceiptHash = "0x0000000000000000000000000000000000000000000000000000000000000000"

// Receipt is a cryptographically hash-chained, provider-signed settlement
// audit record that users and providers can export and verify independently.
type Receipt struct {
	Seq                 uint64        `json:"seq"`
	ReceiptID           string        `json:"receiptId"`
	QuoteID             string        `json:"quoteId,omitempty"`
	IdempotencyKey      string        `json:"idempotencyKey,omitempty"`
	Resource            string        `json:"resource"`
	Model               string        `json:"model,omitempty"`
	FacilitatorID       string        `json:"facilitatorId"`
	FacilitatorOperator string        `json:"facilitatorOperator,omitempty"`
	Payer               string        `json:"payer"`
	PayTo               string        `json:"payTo"`
	Network             string        `json:"network"`
	Asset               string        `json:"asset"`
	AmountAtomic        string        `json:"amountAtomic"`
	ChargedAtomic       string        `json:"chargedAtomic"`
	Nonce               string        `json:"nonce"`
	Transaction         string        `json:"transaction,omitempty"`
	Status              ReceiptStatus `json:"status"`
	ReversalReason      string        `json:"reversalReason,omitempty"`
	ReversalTx          string        `json:"reversalTx,omitempty"`
	ErrorReason         string        `json:"errorReason,omitempty"`
	CreatedAt           int64         `json:"createdAt"`
	UpdatedAt           int64         `json:"updatedAt"`
	ProviderAddress     string        `json:"providerAddress,omitempty"`
	PrevHash            string        `json:"prevHash"`
	RecordHash          string        `json:"recordHash"`
	Signature           string        `json:"signature,omitempty"`
}

// ReceiptFilter narrows ReceiptLedger.List queries.
type ReceiptFilter struct {
	FacilitatorID string
	Status        ReceiptStatus
	Payer         string
}

// ReversalEvent reports a facilitator settlement revert, chain reorg, or
// dispute reversal for deterministic receipt reconciliation.
type ReversalEvent struct {
	ReceiptID            string `json:"receiptId,omitempty"`
	FacilitatorID        string `json:"facilitatorId"`
	Transaction          string `json:"transaction,omitempty"`
	ReversalTx           string `json:"reversalTx,omitempty"`
	Reason               string `json:"reason"`
	RequireRefund        bool   `json:"requireRefund,omitempty"`
	Timestamp            int64  `json:"timestamp,omitempty"`
	FacilitatorAddress   string `json:"facilitatorAddress,omitempty"`
	FacilitatorSignature string `json:"facilitatorSignature,omitempty"`
}

var reversalDomainHash = crypto.Keccak256([]byte("HanzoX402ReversalEvent:v1"))

// Digest computes the deterministic domain-separated Keccak-256 digest over
// every canonical field of ev excluding FacilitatorSignature.
func (ev ReversalEvent) Digest() ([32]byte, error) {
	var out [32]byte
	if strings.TrimSpace(ev.FacilitatorID) == "" {
		return out, errors.New("x402: reversal event requires facilitatorId")
	}
	if strings.TrimSpace(ev.ReceiptID) == "" && strings.TrimSpace(ev.Transaction) == "" {
		return out, errors.New("x402: reversal event requires receiptId or transaction")
	}
	if strings.TrimSpace(ev.ReversalTx) == "" {
		return out, errors.New("x402: reversal event requires reversalTx")
	}
	if strings.TrimSpace(ev.Reason) == "" {
		return out, errors.New("x402: reversal event requires reason")
	}
	cp := ev
	cp.FacilitatorSignature = ""
	cp.FacilitatorAddress = strings.ToLower(strings.TrimSpace(cp.FacilitatorAddress))
	raw, err := json.Marshal(cp)
	if err != nil {
		return out, fmt.Errorf("x402: marshal reversal event: %w", err)
	}
	structHash := crypto.Keccak256(raw)
	copy(out[:], crypto.Keccak256([]byte{0x19, 0x01}, reversalDomainHash, structHash))
	return out, nil
}

// SignReversalEvent signs ev with a facilitator's secp256k1 private key.
func SignReversalEvent(key *ecdsa.PrivateKey, ev *ReversalEvent) error {
	if key == nil || ev == nil {
		return errors.New("x402: reversal signing requires key and event")
	}
	ev.FacilitatorAddress = strings.ToLower(crypto.PubkeyToAddress(key.PublicKey).Hex())
	digest, err := ev.Digest()
	if err != nil {
		return err
	}
	sig, err := crypto.Sign(digest[:], key)
	if err != nil {
		return fmt.Errorf("x402: sign reversal event: %w", err)
	}
	canonical, err := protocol.Canonical(protocol.Hex(sig))
	if err != nil {
		return fmt.Errorf("x402: canonical reversal signature: %w", err)
	}
	ev.FacilitatorSignature = canonical
	return nil
}

// VerifyReversalEventSignature verifies the secp256k1 facilitator signature on ev
// and, when expectedFacilitatorAddr is non-empty, checks that the recovered signer
// matches expectedFacilitatorAddr.
func VerifyReversalEventSignature(ev ReversalEvent, expectedFacilitatorAddr string) error {
	if strings.TrimSpace(ev.FacilitatorSignature) == "" {
		return errors.New("x402: reversal event missing facilitatorSignature")
	}
	digest, err := ev.Digest()
	if err != nil {
		return err
	}
	recovered, err := protocol.Recover(digest, ev.FacilitatorSignature)
	if err != nil {
		return fmt.Errorf("x402: invalid reversal facilitatorSignature: %w", err)
	}
	if strings.TrimSpace(ev.FacilitatorAddress) != "" {
		declared, err := protocol.Address(ev.FacilitatorAddress)
		if err != nil {
			return fmt.Errorf("x402: invalid reversal facilitatorAddress: %w", err)
		}
		if recovered != declared {
			return fmt.Errorf("x402: reversal signer %s does not match facilitatorAddress %s",
				protocol.Hex(recovered[:]), ev.FacilitatorAddress)
		}
	}
	if strings.TrimSpace(expectedFacilitatorAddr) != "" {
		want, err := protocol.Address(expectedFacilitatorAddr)
		if err != nil {
			return fmt.Errorf("x402: invalid expected facilitator address: %w", err)
		}
		if recovered != want {
			return fmt.Errorf("x402: reversal signer %s does not match trusted facilitator %s",
				protocol.Hex(recovered[:]), expectedFacilitatorAddr)
		}
	}
	return nil
}

// ExportVerification summarizes independent offline verification of a JSONL
// receipt export.
type ExportVerification struct {
	TotalEntries     int            `json:"totalEntries"`
	UniqueReceipts   int            `json:"uniqueReceipts"`
	SignedEntries    int            `json:"signedEntries"`
	HeadHash         string         `json:"headHash"`
	StatusCounts     map[string]int `json:"statusCounts"`
	ChainIntact      bool           `json:"chainIntact"`
	SignaturesIntact bool           `json:"signaturesIntact"`
}

// ComputeRecordHash calculates the canonical SHA-256 hash of a Receipt entry
// excluding RecordHash and Signature themselves.
func (r Receipt) ComputeRecordHash() ([32]byte, error) {
	cp := r
	cp.RecordHash = ""
	cp.Signature = ""
	raw, err := json.Marshal(cp)
	if err != nil {
		return [32]byte{}, fmt.Errorf("x402: marshal receipt for hash: %w", err)
	}
	return sha256.Sum256(raw), nil
}

// MemoryReceiptLedger is a concurrency-safe, hash-chained, optionally
// secp256k1-signed and JSONL-persisted ReceiptLedger.
type MemoryReceiptLedger struct {
	mu               sync.RWMutex
	seq              uint64
	headHash         string
	journal          []Receipt
	byID             map[string]Receipt
	byFacTx          map[string]string // lower(facilitatorId|tx) -> receiptID
	key              *ecdsa.PrivateKey
	journalPath      string
	now              func() time.Time
	reversalVerifier func(ctx context.Context, ev ReversalEvent, target Receipt) error
}

// SetReversalVerifier installs an independent reversal-evidence validator that
// ReconcileReversal must pass before transitioning any receipt.
func (l *MemoryReceiptLedger) SetReversalVerifier(fn func(ctx context.Context, ev ReversalEvent, target Receipt) error) {
	l.mu.Lock()
	l.reversalVerifier = fn
	l.mu.Unlock()
}

// NewMemoryReceiptLedger creates an in-memory ReceiptLedger. key may be nil
// for unsigned local tests or a secp256k1 private key for provider-signed receipts.
func NewMemoryReceiptLedger(key *ecdsa.PrivateKey) *MemoryReceiptLedger {
	return &MemoryReceiptLedger{
		headHash: genesisReceiptHash,
		byID:     make(map[string]Receipt),
		byFacTx:  make(map[string]string),
		key:      key,
	}
}

// NewFileReceiptLedger opens or creates a persistent JSONL-backed ReceiptLedger.
func NewFileReceiptLedger(path string, key *ecdsa.PrivateKey) (*MemoryReceiptLedger, error) {
	l := NewMemoryReceiptLedger(key)
	l.journalPath = path
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("x402: receipt ledger dir: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, fmt.Errorf("x402: open receipt ledger: %w", err)
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 64*1024)
	for {
		rawLine, readErr := rd.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, fmt.Errorf("x402: scan receipt ledger: %w", readErr)
		}
		line := strings.TrimSpace(string(rawLine))
		if line != "" {
			var r Receipt
			if err := json.Unmarshal([]byte(line), &r); err != nil {
				return nil, fmt.Errorf("x402: corrupt receipt ledger: %w", err)
			}
			l.indexEntryLocked(r)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return l, nil
}

func (l *MemoryReceiptLedger) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Record appends a new receipt to the hash-chained ledger and signs it if a
// provider key is configured.
func (l *MemoryReceiptLedger) Record(_ context.Context, rec Receipt) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.appendLocked(rec)
}

func (l *MemoryReceiptLedger) appendLocked(rec Receipt) (Receipt, error) {
	now := l.clock().UTC().Unix()
	if rec.ReceiptID == "" {
		var idBytes [16]byte
		if _, err := rand.Read(idBytes[:]); err != nil {
			return Receipt{}, fmt.Errorf("x402: receipt id: %w", err)
		}
		rec.ReceiptID = "rcpt_" + hex.EncodeToString(idBytes[:])
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	if rec.ChargedAtomic == "" {
		if rec.Status == StatusSettled {
			rec.ChargedAtomic = rec.AmountAtomic
		} else {
			rec.ChargedAtomic = "0"
		}
	}
	if l.key != nil {
		rec.ProviderAddress = strings.ToLower(crypto.PubkeyToAddress(l.key.PublicKey).Hex())
	}
	rec.Seq = l.seq + 1
	rec.PrevHash = l.headHash
	hash, err := rec.ComputeRecordHash()
	if err != nil {
		return Receipt{}, err
	}
	rec.RecordHash = protocol.Hex(hash[:])
	if l.key != nil {
		sig, err := crypto.Sign(hash[:], l.key)
		if err != nil {
			return Receipt{}, fmt.Errorf("x402: sign receipt: %w", err)
		}
		canonical, err := protocol.Canonical(protocol.Hex(sig))
		if err != nil {
			return Receipt{}, fmt.Errorf("x402: canonical receipt signature: %w", err)
		}
		rec.Signature = canonical
	}

	if l.journalPath != "" {
		line, err := json.Marshal(rec)
		if err != nil {
			return Receipt{}, fmt.Errorf("x402: marshal receipt: %w", err)
		}
		f, err := os.OpenFile(l.journalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return Receipt{}, fmt.Errorf("x402: append receipt journal: %w", err)
		}
		_, werr := f.Write(append(line, '\n'))
		cerr := f.Close()
		if werr != nil {
			return Receipt{}, fmt.Errorf("x402: write receipt journal: %w", werr)
		}
		if cerr != nil {
			return Receipt{}, fmt.Errorf("x402: close receipt journal: %w", cerr)
		}
	}

	l.indexEntryLocked(rec)
	return rec, nil
}

func (l *MemoryReceiptLedger) indexEntryLocked(r Receipt) {
	l.journal = append(l.journal, r)
	if r.Seq > l.seq {
		l.seq = r.Seq
	}
	l.headHash = r.RecordHash
	l.byID[r.ReceiptID] = r
	if r.FacilitatorID != "" && r.Transaction != "" {
		l.byFacTx[facTxKey(r.FacilitatorID, r.Transaction)] = r.ReceiptID
	}
}

func facTxKey(facilitatorID, tx string) string {
	return strings.ToLower(strings.TrimSpace(facilitatorID)) + "|" + strings.ToLower(strings.TrimSpace(tx))
}

// Get fetches the latest state of a receipt by ID.
func (l *MemoryReceiptLedger) Get(_ context.Context, receiptID string) (Receipt, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	r, ok := l.byID[receiptID]
	return r, ok
}

// FindByTransaction fetches the latest state of a receipt by facilitator ID and
// settlement transaction hash.
func (l *MemoryReceiptLedger) FindByTransaction(_ context.Context, facilitatorID, txHash string) (Receipt, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	id, ok := l.byFacTx[facTxKey(facilitatorID, txHash)]
	if !ok {
		return Receipt{}, false
	}
	r, ok := l.byID[id]
	return r, ok
}

// List returns the latest state of all matching receipts ordered deterministically
// by monotonic sequence number ascending.
func (l *MemoryReceiptLedger) List(_ context.Context, filter ReceiptFilter) []Receipt {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Receipt, 0, len(l.byID))
	for _, r := range l.byID {
		if filter.FacilitatorID != "" && !strings.EqualFold(r.FacilitatorID, filter.FacilitatorID) {
			continue
		}
		if filter.Status != "" && r.Status != filter.Status {
			continue
		}
		if filter.Payer != "" && !strings.EqualFold(r.Payer, filter.Payer) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Seq < out[j].Seq
	})
	return out
}

// ReconcileReversal deterministically locates the exact receipt by ReceiptID or
// (FacilitatorID, Transaction), validates facilitator attribution and reversal
// evidence, and appends a new hash-chained REVERSED or REFUND_REQUIRED record.
func (l *MemoryReceiptLedger) ReconcileReversal(ctx context.Context, ev ReversalEvent) (Receipt, error) {
	if strings.TrimSpace(ev.FacilitatorID) == "" {
		return Receipt{}, errors.New("x402: reversal event requires facilitatorId")
	}
	if strings.TrimSpace(ev.ReceiptID) == "" && strings.TrimSpace(ev.Transaction) == "" {
		return Receipt{}, errors.New("x402: reversal event requires receiptId or transaction")
	}
	if strings.TrimSpace(ev.ReversalTx) == "" {
		return Receipt{}, errors.New("x402: reversal event requires reversalTx")
	}
	if strings.TrimSpace(ev.Reason) == "" {
		return Receipt{}, errors.New("x402: reversal event requires reason")
	}
	if strings.TrimSpace(ev.FacilitatorSignature) != "" || strings.TrimSpace(ev.FacilitatorAddress) != "" {
		if err := VerifyReversalEventSignature(ev, ev.FacilitatorAddress); err != nil {
			return Receipt{}, err
		}
	}

	l.mu.RLock()
	target, found := l.lookupReversalTargetLocked(ev)
	verifier := l.reversalVerifier
	l.mu.RUnlock()
	if !found {
		return Receipt{}, fmt.Errorf("x402: reversal target receipt not found (facilitator=%q receiptId=%q tx=%q)",
			ev.FacilitatorID, ev.ReceiptID, ev.Transaction)
	}
	if err := validateReversalTarget(ev, target); err != nil {
		return Receipt{}, err
	}
	if verifier != nil {
		if err := verifier(ctx, ev, target); err != nil {
			return Receipt{}, fmt.Errorf("x402: reversal evidence verification failed: %w", err)
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	target, found = l.lookupReversalTargetLocked(ev)
	if !found {
		return Receipt{}, fmt.Errorf("x402: reversal target receipt not found (facilitator=%q receiptId=%q tx=%q)",
			ev.FacilitatorID, ev.ReceiptID, ev.Transaction)
	}
	if err := validateReversalTarget(ev, target); err != nil {
		return Receipt{}, err
	}

	next := target
	if ev.RequireRefund {
		next.Status = StatusRefundRequired
	} else {
		next.Status = StatusReversed
	}
	next.ChargedAtomic = "0"
	next.ReversalReason = ev.Reason
	next.ReversalTx = ev.ReversalTx
	if ev.Timestamp > 0 {
		next.UpdatedAt = ev.Timestamp
	}
	return l.appendLocked(next)
}

func (l *MemoryReceiptLedger) lookupReversalTargetLocked(ev ReversalEvent) (Receipt, bool) {
	if ev.ReceiptID != "" {
		target, found := l.byID[ev.ReceiptID]
		return target, found
	}
	if id, ok := l.byFacTx[facTxKey(ev.FacilitatorID, ev.Transaction)]; ok {
		target, found := l.byID[id]
		return target, found
	}
	return Receipt{}, false
}

func validateReversalTarget(ev ReversalEvent, target Receipt) error {
	if !strings.EqualFold(target.FacilitatorID, ev.FacilitatorID) {
		return fmt.Errorf("x402: reversal facilitator mismatch: receipt settled by %q, reversal from %q",
			target.FacilitatorID, ev.FacilitatorID)
	}
	if ev.Transaction != "" && target.Transaction != "" && !strings.EqualFold(target.Transaction, ev.Transaction) {
		return fmt.Errorf("x402: reversal transaction mismatch: receipt tx %q, reversal tx %q",
			target.Transaction, ev.Transaction)
	}
	if target.Status != StatusSettled && target.Status != StatusReversed && target.Status != StatusRefundRequired {
		return fmt.Errorf("x402: cannot reverse receipt in status %q", target.Status)
	}
	return nil
}

// ExportJSONL writes the complete immutable hash-chained audit trail as JSONL.
func (l *MemoryReceiptLedger) ExportJSONL(w io.Writer) error {
	l.mu.RLock()
	entries := append([]Receipt(nil), l.journal...)
	l.mu.RUnlock()

	for _, r := range entries {
		b, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("x402: export receipt: %w", err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("x402: write exported receipt: %w", err)
		}
	}
	return nil
}

// VerifyExportJSONL independently verifies every line of an exported JSONL
// receipt ledger: sequence monotonicity, PrevHash continuity, SHA-256 RecordHash
// integrity, and (when expectedProviderAddr is non-empty) secp256k1 signatures.
func VerifyExportJSONL(r io.Reader, expectedProviderAddr string) (ExportVerification, error) {
	res := ExportVerification{
		HeadHash:         genesisReceiptHash,
		StatusCounts:     make(map[string]int),
		ChainIntact:      true,
		SignaturesIntact: true,
	}
	var wantAddr [20]byte
	checkProvider := strings.TrimSpace(expectedProviderAddr) != ""
	if checkProvider {
		var err error
		if wantAddr, err = protocol.Address(expectedProviderAddr); err != nil {
			return res, fmt.Errorf("x402: invalid expectedProviderAddr: %w", err)
		}
	}

	latest := make(map[string]Receipt)
	prevHash := genesisReceiptHash
	var prevSeq uint64

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lineNo++
		var rec Receipt
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			res.ChainIntact = false
			return res, fmt.Errorf("x402: line %d invalid json: %w", lineNo, err)
		}
		if rec.Seq != prevSeq+1 {
			res.ChainIntact = false
			return res, fmt.Errorf("x402: line %d sequence gap: got %d, want %d", lineNo, rec.Seq, prevSeq+1)
		}
		if !strings.EqualFold(rec.PrevHash, prevHash) {
			res.ChainIntact = false
			return res, fmt.Errorf("x402: line %d broken hash chain: prevHash %s != %s", lineNo, rec.PrevHash, prevHash)
		}
		computed, err := rec.ComputeRecordHash()
		if err != nil {
			res.ChainIntact = false
			return res, fmt.Errorf("x402: line %d hash error: %w", lineNo, err)
		}
		computedHex := protocol.Hex(computed[:])
		if !strings.EqualFold(rec.RecordHash, computedHex) {
			res.ChainIntact = false
			return res, fmt.Errorf("x402: line %d tampered recordHash: got %s, computed %s", lineNo, rec.RecordHash, computedHex)
		}
		if checkProvider || rec.Signature != "" {
			if rec.Signature == "" {
				res.SignaturesIntact = false
				return res, fmt.Errorf("x402: line %d missing provider signature", lineNo)
			}
			recovered, err := protocol.Recover(computed, rec.Signature)
			if err != nil {
				res.SignaturesIntact = false
				return res, fmt.Errorf("x402: line %d signature invalid: %w", lineNo, err)
			}
			if checkProvider && recovered != wantAddr {
				res.SignaturesIntact = false
				return res, fmt.Errorf("x402: line %d signer %s != expected %s", lineNo, protocol.Hex(recovered[:]), expectedProviderAddr)
			}
			if rec.ProviderAddress != "" {
				declared, err := protocol.Address(rec.ProviderAddress)
				if err != nil || recovered != declared {
					res.SignaturesIntact = false
					return res, fmt.Errorf("x402: line %d signer %s != declared %s", lineNo, protocol.Hex(recovered[:]), rec.ProviderAddress)
				}
			}
			res.SignedEntries++
		}
		prevHash = rec.RecordHash
		prevSeq = rec.Seq
		res.TotalEntries++
		latest[rec.ReceiptID] = rec
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("x402: scan export: %w", err)
	}
	res.HeadHash = prevHash
	res.UniqueReceipts = len(latest)
	for _, rec := range latest {
		res.StatusCounts[string(rec.Status)]++
	}
	return res, nil
}
