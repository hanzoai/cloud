package books

// bank.go — the SHARED bank engine's vocabulary and the connector seam. This is the
// ONE type every bank connector (OFX/CSV import, Plaid, Teller) normalizes into, and the
// ONE interface the sync loop pulls through. It is deliberately connector-agnostic: a
// connector's ONLY job is to authenticate against its source (its access_token lives in
// KMS, never here) and hand back BankTxn values; everything downstream — mapping to a
// balanced GL voucher, reconciliation, the clarifying-question path — is this package's,
// so there is exactly one place that decides how bank money hits the books.
//
// READ-ONLY. A Connector INGESTS transactions + balances. There is no Send/Pay/Transfer
// method on this interface and there never will be — the engine cannot move money out of
// the real bank, only record what already moved.
//
// MONEY MATH. AmountCents is an exact int64 minor unit (USD cents), a MAGNITUDE paired
// with Direction. Bank feeds deliver amounts as decimal strings; a connector parses them
// with parseCents (below), never float64, so no rounding error can enter the ledger.
//
// This file is the frozen contract the parallel connector builds compile against:
// import_bank.go, plaid.go, and teller.go fill in newImport/newPlaid/newTeller WITHOUT
// editing this file.

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/hanzoai/cloud"
)

// Direction is a bank transaction's money direction relative to our account. It pairs
// with the (always non-negative) AmountCents magnitude: an Outflow is money leaving the
// account (an expense), an Inflow is money arriving (a deposit — reconciled or new), and
// a Transfer is a move between our OWN accounts with no profit-and-loss effect.
type Direction string

const (
	Inflow   Direction = "inflow"
	Outflow  Direction = "outflow"
	Transfer Direction = "transfer"
)

// BankTxn is the normalized bank transaction — the single shape every connector emits and
// the bank engine consumes. Connector + ExternalID is the idempotency key: the same
// statement row, re-imported or re-synced, always carries the same pair, so it maps and
// posts exactly once.
type BankTxn struct {
	Connector   string    `json:"connector"`
	ExternalID  string    `json:"externalId"`
	PostedAt    string    `json:"postedAt"` // RFC3339 posting time, ledger-comparable
	AmountCents int64     `json:"amountCents"`
	Currency    string    `json:"currency"`
	Description string    `json:"description"`
	Merchant    string    `json:"merchant"`
	Direction   Direction `json:"direction"`
}

// Connector is the pull seam to one bank source. Name is the stable connector id (the
// bank_txn.connector / cursor key). Fetch returns the batch of transactions AFTER cursor
// plus the nextCursor to persist; a connector resolves its own credentials from KMS
// inside Fetch and NEVER returns or logs them. A returned nextCursor equal to the input
// cursor means "no advance" (nothing new).
type Connector interface {
	Name() string
	Fetch(ctx context.Context, org, cursor string) (txns []BankTxn, nextCursor string, err error)
}

// connectors is the FIXED connector set the sync loop iterates. Import is push-based (it
// contributes nothing to a pull sync — its rows arrive via POST /v1/books/bank/import),
// so its Fetch returns nothing; Plaid and Teller pull over their cursors.
func connectors() []Connector { return []Connector{newImport(), newPlaid(), newTeller()} }

// Importer is the OPTIONAL capability a push-based connector (OFX/QFX/CSV import) adds on
// top of Connector: it parses an uploaded file body into BankTxns. The import route
// type-asserts to it, so a connector that has not yet implemented parsing simply is not an
// Importer and the route reports "not yet available" rather than mis-handling the upload.
type Importer interface {
	Parse(data []byte) ([]BankTxn, error)
}

// bankKMS is the connectors' accessor to the ONE credential store. Plaid/Teller fetch
// their per-item access_token through this at Fetch time and never persist it in books.db.
// nil when KMS is not wired (dev/single-node) — a credentialed connector then fails closed.
func bankKMS() cloud.KMSClient {
	if mounted == nil {
		return nil
	}
	return mounted.kms
}

// parseCents converts a bank feed's decimal amount string ("12.34", "-1,203.50", "$5")
// into exact int64 cents WITHOUT float64. It rejects sub-cent precision (more than two
// fractional digits) rather than rounding, because a bank amount that is not a whole
// number of cents is malformed input, not something to silently truncate. The returned
// value is SIGNED (a leading '-' yields a negative), so a caller derives Direction from
// the sign and stores the magnitude.
func parseCents(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("bank: empty amount")
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "$"))
	s = strings.ReplaceAll(s, ",", "")
	whole, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole, frac = s[:i], s[i+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > 2 {
		return 0, fmt.Errorf("bank: sub-cent precision in %q", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	dollars, err := parseDigits(whole)
	if err != nil {
		return 0, fmt.Errorf("bank: bad amount %q: %w", s, err)
	}
	cents, err := parseDigits(frac)
	if err != nil {
		return 0, fmt.Errorf("bank: bad amount %q: %w", s, err)
	}
	if dollars > (math.MaxInt64-cents)/100 {
		return 0, fmt.Errorf("bank: amount %q overflows int64 cents", s)
	}
	total := dollars*100 + cents
	if neg {
		total = -total
	}
	return total, nil
}

// parseDigits parses an all-digit string to int64, rejecting any non-digit rune. It exists
// so parseCents never reaches for strconv on a fragment that might carry a stray sign or
// separator — the whole/frac split has already stripped those, so anything non-digit here
// is malformed.
func parseDigits(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q", string(r))
		}
		d := int64(r - '0')
		if n > (math.MaxInt64-d)/10 {
			return 0, fmt.Errorf("digits overflow int64")
		}
		n = n*10 + d
	}
	return n, nil
}
