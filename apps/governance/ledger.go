// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GenesisPrevHash is the canonical previous hash for ledger entry sequence 1.
const GenesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// AuditLedger maintains an append-only, SHA-256 hash-chained sequence of
// governance events with deterministic JSONL serialization.
type AuditLedger struct {
	entries  []LedgerEntry
	filePath string
}

// NewAuditLedger creates an empty in-memory AuditLedger, optionally mirrored
// to an append-only JSONL file at filePath.
func NewAuditLedger(filePath string) *AuditLedger {
	return &AuditLedger{
		entries:  make([]LedgerEntry, 0, 64),
		filePath: filePath,
	}
}

// ComputeLedgerEntryHash returns the canonical SHA-256 hash for a ledger entry.
func ComputeLedgerEntryHash(e LedgerEntry) string {
	var buf bytes.Buffer
	writeField(&buf, signatureDomain)
	writeField(&buf, "ledger_entry")
	writeUint64(&buf, e.Seq)
	writeInt64(&buf, e.Timestamp.UTC().UnixNano())
	writeField(&buf, e.ProtocolID)
	writeField(&buf, e.EventType)
	writeField(&buf, e.ActorID)
	writeField(&buf, e.SubjectID)
	writeField(&buf, e.PayloadHash)
	writeField(&buf, e.PrevHash)
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// Append records a new hash-chained event onto the ledger.
func (l *AuditLedger) Append(ts time.Time, protocolID, eventType, actorID, subjectID string, payload any) (LedgerEntry, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("marshal ledger payload: %w", err)
	}
	payloadSum := sha256.Sum256(rawPayload)
	payloadHash := hex.EncodeToString(payloadSum[:])

	prevHash := GenesisPrevHash
	seq := uint64(1)
	if n := len(l.entries); n > 0 {
		prevHash = l.entries[n-1].EntryHash
		seq = l.entries[n-1].Seq + 1
	}

	entry := LedgerEntry{
		Seq:         seq,
		Timestamp:   ts.UTC(),
		ProtocolID:  protocolID,
		EventType:   eventType,
		ActorID:     actorID,
		SubjectID:   subjectID,
		PayloadHash: payloadHash,
		Payload:     json.RawMessage(rawPayload),
		PrevHash:    prevHash,
	}
	entry.EntryHash = ComputeLedgerEntryHash(entry)

	if l.filePath != "" {
		if err := appendJSONLLine(l.filePath, entry); err != nil {
			return LedgerEntry{}, err
		}
	}
	l.entries = append(l.entries, entry)
	return cloneLedgerEntry(entry), nil
}

// HeadHash returns the hash of the most recent entry, or GenesisPrevHash if empty.
func (l *AuditLedger) HeadHash() string {
	if len(l.entries) == 0 {
		return GenesisPrevHash
	}
	return l.entries[len(l.entries)-1].EntryHash
}

// Len returns the number of entries in the ledger.
func (l *AuditLedger) Len() int {
	return len(l.entries)
}

// Entries returns a deep copy of all ledger entries.
func (l *AuditLedger) Entries() []LedgerEntry {
	out := make([]LedgerEntry, len(l.entries))
	for i, e := range l.entries {
		out[i] = cloneLedgerEntry(e)
	}
	return out
}

// Replace loads pre-verified entries into the ledger (used during fork/restore).
func (l *AuditLedger) Replace(entries []LedgerEntry) error {
	if err := VerifyLedger(entries); err != nil {
		return err
	}
	copied := make([]LedgerEntry, len(entries))
	for i, e := range entries {
		copied[i] = cloneLedgerEntry(e)
	}
	l.entries = copied
	return nil
}

// MarshalJSONL encodes all entries as deterministic newline-delimited JSON.
func (l *AuditLedger) MarshalJSONL() ([]byte, error) {
	return MarshalLedgerJSONL(l.entries)
}

// MarshalLedgerJSONL encodes a slice of LedgerEntry into JSONL bytes.
func MarshalLedgerJSONL(entries []LedgerEntry) ([]byte, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			return nil, fmt.Errorf("marshal ledger entry %d: %w", e.Seq, err)
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// UnmarshalLedgerJSONL decodes JSONL bytes into LedgerEntry records and
// verifies the full hash chain before returning.
func UnmarshalLedgerJSONL(data []byte) ([]LedgerEntry, error) {
	var entries []LedgerEntry
	scanner := bufio.NewScanner(bytes.NewReader(data))
	// Support large payloads up to 16 MiB per entry.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var e LedgerEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("%w: decode line %d: %v", ErrInvalidLedger, lineNo, err)
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: scan jsonl: %v", ErrInvalidLedger, err)
	}
	if err := VerifyLedger(entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// VerifyLedger verifies sequence continuity, payload digests, prev-hash links,
// and entry hashes across the entire audit chain.
func VerifyLedger(entries []LedgerEntry) error {
	expectedPrev := GenesisPrevHash
	for i, e := range entries {
		expectedSeq := uint64(i + 1)
		if e.Seq != expectedSeq {
			return fmt.Errorf("%w: entry %d has seq %d, want %d", ErrInvalidLedger, i, e.Seq, expectedSeq)
		}
		if e.PrevHash != expectedPrev {
			return fmt.Errorf("%w: entry %d prev_hash mismatch", ErrInvalidLedger, e.Seq)
		}
		payloadSum := sha256.Sum256(e.Payload)
		wantPayloadHash := hex.EncodeToString(payloadSum[:])
		if e.PayloadHash != wantPayloadHash {
			return fmt.Errorf("%w: entry %d payload_hash mismatch", ErrInvalidLedger, e.Seq)
		}
		wantEntryHash := ComputeLedgerEntryHash(e)
		if e.EntryHash != wantEntryHash {
			return fmt.Errorf("%w: entry %d entry_hash mismatch", ErrInvalidLedger, e.Seq)
		}
		expectedPrev = e.EntryHash
	}
	return nil
}

func cloneLedgerEntry(e LedgerEntry) LedgerEntry {
	out := e
	if e.Payload != nil {
		out.Payload = append(json.RawMessage(nil), e.Payload...)
	}
	return out
}

func appendJSONLLine(filePath string, e LedgerEntry) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o750); err != nil {
		return fmt.Errorf("create ledger dir: %w", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal ledger line: %w", err)
	}
	line = append(line, '\n')
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open ledger file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write ledger file: %w", err)
	}
	return nil
}
