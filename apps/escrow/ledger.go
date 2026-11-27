// SPDX-License-Identifier: Apache-2.0 OR MIT

package escrow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ExportLedgerJSONL writes every hash-chained settlement event as newline-delimited JSON
// suitable for sovereign offline auditing, archival, or independent operator verification.
func (e *EscrowEngine) ExportLedgerJSONL(w io.Writer) error {
	e.mu.RLock()
	entries := make([]LedgerEntry, len(e.ledger))
	copy(entries, e.ledger)
	e.mu.RUnlock()

	enc := json.NewEncoder(w)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return fmt.Errorf("escrow: encode ledger entry %d: %w", entry.Seq, err)
		}
	}
	return nil
}

// VerifyLedgerJSONLOffline verifies a hash-chained JSONL settlement ledger completely
// offline with zero network, RPC, or Hanzo dependencies.
func VerifyLedgerJSONLOffline(r io.Reader) (*LedgerAuditReport, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	report := &LedgerAuditReport{
		HeadHash:   genesisLedgerHash,
		ChainValid: true,
	}

	var expectedSeq uint64 = 1
	prevHash := genesisLedgerHash

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry LedgerEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			report.ChainValid = false
			report.TamperDetected = true
			report.VerificationError = fmt.Sprintf("malformed JSON at seq %d: %v", expectedSeq, err)
			return report, fmt.Errorf("%w: %s", ErrLedgerTampered, report.VerificationError)
		}
		if entry.Seq != expectedSeq {
			report.ChainValid = false
			report.TamperDetected = true
			report.VerificationError = fmt.Sprintf("sequence gap: expected %d, got %d", expectedSeq, entry.Seq)
			return report, fmt.Errorf("%w: %s", ErrLedgerTampered, report.VerificationError)
		}
		if !strings.EqualFold(entry.PrevHash, prevHash) {
			report.ChainValid = false
			report.TamperDetected = true
			report.VerificationError = fmt.Sprintf("prevHash mismatch at seq %d: expected %s, got %s", entry.Seq, prevHash, entry.PrevHash)
			return report, fmt.Errorf("%w: %s", ErrLedgerTampered, report.VerificationError)
		}
		recomputed := computeLedgerEntryHash(entry)
		if !strings.EqualFold(entry.EntryHash, recomputed) {
			report.ChainValid = false
			report.TamperDetected = true
			report.VerificationError = fmt.Sprintf("entryHash tampered at seq %d: expected %s, got %s", entry.Seq, recomputed, entry.EntryHash)
			return report, fmt.Errorf("%w: %s", ErrLedgerTampered, report.VerificationError)
		}

		switch entry.EventType {
		case "AccountMinted":
			report.TotalMinted += entry.Amount
		case "EscrowSettled":
			report.SettledCount++
		case "EscrowSlashed":
			report.SlashedCount++
		case "EscrowRefunded":
			report.RefundedCount++
		}

		prevHash = entry.EntryHash
		report.HeadHash = entry.EntryHash
		report.EntriesVerified++
		expectedSeq++
	}
	if err := scanner.Err(); err != nil {
		report.ChainValid = false
		report.VerificationError = err.Error()
		return report, err
	}
	return report, nil
}

type snapshotLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type snapshotHeader struct {
	Version             string       `json:"version"`
	Domain              EIP712Domain `json:"domain"`
	Policy              Policy       `json:"policy"`
	InitialMintedSupply uint64       `json:"initialMintedSupply"`
	HeadHash            string       `json:"headHash"`
}

type snapshotBalance struct {
	Address string `json:"address"`
	Balance uint64 `json:"balance"`
}

type snapshotFooter struct {
	SnapshotDigest string `json:"snapshotDigest"`
}

// ExportSnapshotJSONL exports the complete sovereign state of the EscrowEngine (header,
// deterministic balances, all escrows with signed quotes/claims/challenges/payouts,
// hash-chained ledger entries, and canonical digest footer) as JSONL.
func (e *EscrowEngine) ExportSnapshotJSONL(w io.Writer) error {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var body bytes.Buffer
	writeLine := func(typ string, v any) error {
		payload, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(snapshotLine{Type: typ, Payload: payload})
		if err != nil {
			return err
		}
		body.Write(raw)
		body.WriteByte('\n')
		return nil
	}

	hdr := snapshotHeader{
		Version:             "hip-1350-escrow/v1",
		Domain:              e.domain,
		Policy:              e.policy,
		InitialMintedSupply: e.initialMintedSupply,
		HeadHash:            e.headHash,
	}
	if err := writeLine("header", hdr); err != nil {
		return err
	}
	for _, addr := range sortedBalanceKeys(e.balances) {
		if err := writeLine("balance", snapshotBalance{Address: addr, Balance: e.balances[addr]}); err != nil {
			return err
		}
	}
	for _, id := range e.escrowOrder {
		if rec, ok := e.escrows[id]; ok {
			if err := writeLine("escrow", cloneEscrowRecord(rec)); err != nil {
				return err
			}
		}
	}
	for _, entry := range e.ledger {
		if err := writeLine("ledger", entry); err != nil {
			return err
		}
	}

	digest := Keccak256Hex([]byte("escrow:snapshot:v1:"), body.Bytes())
	if _, err := w.Write(body.Bytes()); err != nil {
		return err
	}
	footerPayload, err := json.Marshal(snapshotFooter{SnapshotDigest: digest})
	if err != nil {
		return err
	}
	footerLine, err := json.Marshal(snapshotLine{Type: "footer", Payload: footerPayload})
	if err != nil {
		return err
	}
	footerLine = append(footerLine, '\n')
	_, err = w.Write(footerLine)
	return err
}

// ImportSnapshotJSONL verifies every cryptographic signature, hash-chain link, snapshot
// digest, and global conservation invariant completely offline, then hydrates a fresh,
// independent EscrowEngine ready to continue operations without Hanzo infrastructure.
func ImportSnapshotJSONL(r io.Reader, clock Clock) (*EscrowEngine, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	var bodyBuf bytes.Buffer
	var hdr *snapshotHeader
	var footer *snapshotFooter
	balances := make(map[string]uint64)
	var escrows []*EscrowRecord
	var ledger []LedgerEntry

	for scanner.Scan() {
		rawLine := scanner.Bytes()
		trimmed := bytes.TrimSpace(rawLine)
		if len(trimmed) == 0 {
			continue
		}
		var sl snapshotLine
		if err := json.Unmarshal(trimmed, &sl); err != nil {
			return nil, fmt.Errorf("%w: invalid snapshot JSON: %v", ErrLedgerTampered, err)
		}
		if sl.Type == "footer" {
			var f snapshotFooter
			if err := json.Unmarshal(sl.Payload, &f); err != nil {
				return nil, fmt.Errorf("%w: invalid snapshot footer: %v", ErrLedgerTampered, err)
			}
			footer = &f
			continue
		}
		bodyBuf.Write(trimmed)
		bodyBuf.WriteByte('\n')

		switch sl.Type {
		case "header":
			var h snapshotHeader
			if err := json.Unmarshal(sl.Payload, &h); err != nil {
				return nil, fmt.Errorf("%w: invalid header: %v", ErrLedgerTampered, err)
			}
			hdr = &h
		case "balance":
			var b snapshotBalance
			if err := json.Unmarshal(sl.Payload, &b); err != nil {
				return nil, fmt.Errorf("%w: invalid balance: %v", ErrLedgerTampered, err)
			}
			norm, err := NormalizeAddress(b.Address)
			if err != nil {
				return nil, err
			}
			balances[norm] = b.Balance
		case "escrow":
			var rec EscrowRecord
			if err := json.Unmarshal(sl.Payload, &rec); err != nil {
				return nil, fmt.Errorf("%w: invalid escrow: %v", ErrLedgerTampered, err)
			}
			escrows = append(escrows, &rec)
		case "ledger":
			var le LedgerEntry
			if err := json.Unmarshal(sl.Payload, &le); err != nil {
				return nil, fmt.Errorf("%w: invalid ledger entry: %v", ErrLedgerTampered, err)
			}
			ledger = append(ledger, le)
		default:
			return nil, fmt.Errorf("%w: unknown snapshot record type %q", ErrLedgerTampered, sl.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if hdr == nil || footer == nil {
		return nil, fmt.Errorf("%w: missing snapshot header or footer", ErrLedgerTampered)
	}

	recomputedDigest := Keccak256Hex([]byte("escrow:snapshot:v1:"), bodyBuf.Bytes())
	if !strings.EqualFold(recomputedDigest, footer.SnapshotDigest) {
		return nil, fmt.Errorf("%w: snapshot digest mismatch", ErrLedgerTampered)
	}

	// Verify ledger hash chain offline.
	var ledgerBuf bytes.Buffer
	enc := json.NewEncoder(&ledgerBuf)
	for _, le := range ledger {
		if err := enc.Encode(le); err != nil {
			return nil, err
		}
	}
	audit, err := VerifyLedgerJSONLOffline(&ledgerBuf)
	if err != nil || !audit.ChainValid {
		return nil, fmt.Errorf("%w: ledger verification failed: %v", ErrLedgerTampered, err)
	}
	if !strings.EqualFold(audit.HeadHash, hdr.HeadHash) {
		return nil, fmt.Errorf("%w: snapshot headHash mismatch", ErrLedgerTampered)
	}

	// Re-verify all cryptographic EIP-712 signatures on every imported escrow offline.
	for _, rec := range escrows {
		if err := VerifyJobQuote(hdr.Domain, rec.Quote); err != nil {
			return nil, fmt.Errorf("%w: escrow %s quote signature invalid: %v", ErrLedgerTampered, rec.JobID, err)
		}
		if rec.Acceptance != nil {
			if err := VerifyQuoteAcceptance(hdr.Domain, *rec.Acceptance); err != nil {
				return nil, fmt.Errorf("%w: escrow %s acceptance signature invalid: %v", ErrLedgerTampered, rec.JobID, err)
			}
		}
		if rec.Claim != nil {
			if err := VerifyCompletionClaim(hdr.Domain, *rec.Claim); err != nil {
				return nil, fmt.Errorf("%w: escrow %s claim signature invalid: %v", ErrLedgerTampered, rec.JobID, err)
			}
		}
		if rec.Challenge != nil {
			if _, err := ClassifyViolation(rec.Challenge.Evidence.ViolationType); err != nil {
				return nil, fmt.Errorf("%w: escrow %s contains forbidden violation type: %v", ErrLedgerTampered, rec.JobID, err)
			}
			if err := VerifyChallengeSubmission(hdr.Domain, *rec.Challenge); err != nil {
				return nil, fmt.Errorf("%w: escrow %s challenge signature invalid: %v", ErrLedgerTampered, rec.JobID, err)
			}
		}
	}

	pol := hdr.Policy
	eng := NewEscrowEngine(hdr.Domain, clock, &pol)
	eng.initialMintedSupply = hdr.InitialMintedSupply
	eng.balances = balances
	eng.headHash = hdr.HeadHash
	eng.ledger = ledger
	for _, rec := range escrows {
		eng.escrows[rec.JobID] = cloneEscrowRecord(rec)
		eng.escrowOrder = append(eng.escrowOrder, rec.JobID)
	}

	cons := eng.VerifyConservation()
	if !cons.Conserved {
		return nil, fmt.Errorf("%w: drift=%d doublePayouts=%d", ErrConservationViolation, cons.Drift, cons.DoublePayoutCount)
	}
	return eng, nil
}
