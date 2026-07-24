package books

// import_bank.go — the OFX/QFX/CSV statement-import connector. This is how the books start
// with a REAL Bank of America history TODAY: no credentials, no link flow — a human exports
// a statement and uploads the file. Import is push-based (rows arrive via POST
// /v1/books/bank/import, parsed by Parse), so its Fetch contributes nothing to a pull sync.
//
// Implementing Parse makes importConn an Importer; the /v1/books/bank/import route
// type-asserts to that and maps + posts each row through the SAME idempotent engine
// (mapAndPost) every connector shares. This file adds NO new posting rule — it only turns
// bytes into the canonical BankTxn.
//
// FORMATS, one parser each, detected by content:
//   - OFX 1.x (SGML) and OFX 2.x (XML): both carry <STMTTRN> aggregates whose leaf values
//     run from ">" to the next "<" — identical extraction, so ONE ofx parser serves both
//     (the SGML/XML distinction never has to be decided).
//   - Bank of America CSV export: a "Date,Description,Amount,Running Bal." table after a
//     summary preamble.
//
// MONEY. Amounts arrive as decimal strings and go through parseCents — exact int64 cents,
// never float64. The signed cents resolve into a non-negative AmountCents MAGNITUDE plus a
// Direction (negative → Outflow, positive → Inflow), which is the shape validateTxn and
// mapAndPost require.
//
// IDEMPOTENCY. OFX rows carry a bank-assigned FITID, used verbatim as ExternalID. A BoA CSV
// row has no such id, so ExternalID is a deterministic content hash (date, amount,
// description, running balance) — re-uploading the same export yields the same ids, so the
// (connector, external_id) key makes a re-import a no-op, never a double-post.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type importConn struct{}

func newImport() *importConn { return &importConn{} }

func (*importConn) Name() string { return "import" }

// Fetch is a no-op for a push connector: import rows come from an upload, not a pull.
func (*importConn) Fetch(_ context.Context, _org, cursor string) ([]BankTxn, string, error) {
	return nil, cursor, nil
}

// Parse turns an uploaded statement body into normalized BankTxns. It detects the format
// from the content — a PDF eStatement, OFX (either 1.x SGML or 2.x XML), or a CSV export —
// and dispatches to the one parser for it. PDF is checked first because its "%PDF" magic is
// binary and unambiguous (it can never be mistaken for the text OFX/CSV forms). It is the
// Importer capability the import route depends on.
func (*importConn) Parse(data []byte) ([]BankTxn, error) {
	if looksPDF(data) {
		return parsePDF(data) // binary magic — check before trimBOM, which is a text-format concern
	}
	data = trimBOM(data)
	if looksOFX(data) {
		return parseOFX(data)
	}
	return parseBoACSV(data)
}

// looksOFX reports whether the body is an OFX/QFX document: a "<OFX" root (XML or SGML), the
// "OFXHEADER" preamble of a 1.x file, or — defensively — a bare "<STMTTRN" statement block.
func looksOFX(data []byte) bool {
	head := strings.ToUpper(strings.TrimSpace(string(data)))
	if len(head) > 4096 {
		head = head[:4096]
	}
	return strings.Contains(head, "<OFX") ||
		strings.Contains(head, "OFXHEADER") ||
		strings.Contains(head, "<STMTTRN")
}

// parseOFX extracts every <STMTTRN> aggregate from an OFX 1.x (SGML) or 2.x (XML) body. A
// single scanner works for both dialects because in each an element's value runs from its
// ">" to the next "<" — SGML simply omits the leaf close tag XML includes.
func parseOFX(data []byte) ([]BankTxn, error) {
	s := string(data)
	currency := firstNonEmpty(strings.ToLower(strings.TrimSpace(ofxLeaf(s, "CURDEF"))), "usd")

	var txns []BankTxn
	rest := s
	for {
		i := indexFold(rest, "<STMTTRN>")
		if i < 0 {
			break
		}
		rest = rest[i+len("<STMTTRN>"):]

		var block string
		if end := indexFold(rest, "</STMTTRN>"); end >= 0 {
			block, rest = rest[:end], rest[end+len("</STMTTRN>"):]
		} else if next := indexFold(rest, "<STMTTRN>"); next >= 0 {
			// A malformed 1.x file may omit even the aggregate close; bound the block at the
			// next record so a missing tag never swallows the rest of the statement.
			block, rest = rest[:next], rest[next:]
		} else {
			block, rest = rest, ""
		}

		bt, err := ofxTxn(block, currency)
		if err != nil {
			return nil, err
		}
		if bt == nil { // zero-amount noise (e.g. a $0.00 memo line) — nothing to book
			continue
		}
		txns = append(txns, *bt)
	}
	if len(txns) == 0 {
		return nil, fmt.Errorf("books import: no <STMTTRN> records in OFX")
	}
	return txns, nil
}

// ofxTxn normalizes one <STMTTRN> block. FITID is the idempotency key (a bank-assigned,
// statement-stable id); TRNAMT's sign resolves Direction and its magnitude AmountCents;
// DTPOSTED becomes the ledger-comparable RFC3339 PostedAt; NAME/MEMO carry the description.
func ofxTxn(block, currency string) (*BankTxn, error) {
	fitid := strings.TrimSpace(ofxLeaf(block, "FITID"))
	if fitid == "" {
		return nil, fmt.Errorf("books import: <STMTTRN> missing FITID")
	}
	signed, err := parseCents(ofxLeaf(block, "TRNAMT"))
	if err != nil {
		return nil, fmt.Errorf("books import: FITID %s: %w", fitid, err)
	}
	if signed == 0 {
		return nil, nil
	}
	postedAt, err := ofxDate(ofxLeaf(block, "DTPOSTED"))
	if err != nil {
		return nil, fmt.Errorf("books import: FITID %s: %w", fitid, err)
	}

	name := strings.TrimSpace(ofxLeaf(block, "NAME"))
	memo := strings.TrimSpace(ofxLeaf(block, "MEMO"))
	dir, cents := directed(signed)

	return &BankTxn{
		Connector:   "import",
		ExternalID:  fitid,
		PostedAt:    postedAt,
		AmountCents: cents,
		Currency:    currency,
		Description: firstNonEmpty(strings.TrimSpace(name+" "+memo), name, memo),
		Merchant:    name,
		Direction:   dir,
	}, nil
}

// ofxLeaf returns the value of the first <tag> in s: the text from the tag's ">" up to the
// next "<". This is exactly the SGML rule (value ends at the next tag) and coincides with
// the XML rule (value ends at the close tag), so one function reads both dialects. Matching
// is case-insensitive; an absent tag yields "".
func ofxLeaf(s, tag string) string {
	open := "<" + tag + ">"
	i := indexFold(s, open)
	if i < 0 {
		return ""
	}
	v := s[i+len(open):]
	if j := strings.IndexByte(v, '<'); j >= 0 {
		v = v[:j]
	}
	return strings.TrimSpace(v)
}

// ofxDate parses an OFX datetime — "YYYYMMDD", "YYYYMMDDHHMMSS", or either with a
// ".xxx[-5:EST]" fractional/zone suffix — into RFC3339 UTC. Only the calendar fields are
// kept (the reconciler matches on a day window), so a bare date becomes midnight UTC.
func ofxDate(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".["); i >= 0 {
		s = s[:i]
	}
	if len(s) > 14 {
		s = s[:14]
	}
	if len(s) < 8 {
		return "", fmt.Errorf("books import: bad OFX date %q", s)
	}
	for len(s) < 14 { // pad missing time-of-day to midnight
		s += "0"
	}
	t, err := time.Parse("20060102150405", s)
	if err != nil {
		return "", fmt.Errorf("books import: bad OFX date %q: %w", s, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// parseBoACSV parses a Bank of America CSV export. The download opens with a summary
// preamble, then a "Date,Description,Amount,Running Bal." header and the rows. The header is
// located by name (not position) so the preamble is skipped without guessing line counts;
// a row's ExternalID is a deterministic content hash, giving idempotency a bank-native FITID
// would otherwise provide.
func parseBoACSV(data []byte) ([]BankTxn, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1 // the preamble rows have a different width than the table
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("books import: malformed CSV: %w", err)
	}

	hi, cols := findCSVHeader(records)
	if hi < 0 {
		return nil, fmt.Errorf("books import: no Date/Amount header row in CSV")
	}

	var txns []BankTxn
	for _, row := range records[hi+1:] {
		dateRaw := csvAt(row, cols.date)
		amtRaw := csvAt(row, cols.amount)
		if dateRaw == "" || amtRaw == "" {
			continue // blank spacer or a trailing summary line — not a transaction
		}
		signed, err := parseCents(amtRaw)
		if err != nil {
			return nil, fmt.Errorf("books import: CSV row %q: %w", dateRaw, err)
		}
		if signed == 0 {
			continue
		}
		postedAt, err := csvDate(dateRaw)
		if err != nil {
			return nil, err
		}
		desc := csvAt(row, cols.desc)
		bal := csvAt(row, cols.balance)
		dir, cents := directed(signed)

		txns = append(txns, BankTxn{
			Connector:   "import",
			ExternalID:  csvExternalID(dateRaw, amtRaw, desc, bal),
			PostedAt:    postedAt,
			AmountCents: cents,
			Currency:    "usd",
			Description: desc,
			Merchant:    desc,
			Direction:   dir,
		})
	}
	if len(txns) == 0 {
		return nil, fmt.Errorf("books import: no transaction rows under CSV header")
	}
	return txns, nil
}

// csvCols is the located column layout of a BoA CSV table (indices into a row).
type csvCols struct{ date, desc, amount, balance int }

// findCSVHeader scans for the transaction table's header row — one bearing a "Date" column
// and an "Amount" column — and returns its index and the resolved column layout. Returns -1
// when no such header exists (the file is not a recognizable statement table).
func findCSVHeader(records [][]string) (int, csvCols) {
	for idx, row := range records {
		cols := csvCols{date: -1, desc: -1, amount: -1, balance: -1}
		for i, cell := range row {
			switch c := strings.ToLower(strings.TrimSpace(cell)); {
			case c == "date":
				cols.date = i
			case c == "description":
				cols.desc = i
			case c == "amount":
				cols.amount = i
			case strings.HasPrefix(c, "running bal"):
				cols.balance = i
			}
		}
		if cols.date >= 0 && cols.amount >= 0 {
			return idx, cols
		}
	}
	return -1, csvCols{}
}

// csvAt safely reads a (possibly absent) column, trimmed.
func csvAt(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// csvDate parses BoA's "MM/DD/YYYY" cell into RFC3339 UTC midnight.
func csvDate(s string) (string, error) {
	t, err := time.Parse("01/02/2006", strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("books import: bad CSV date %q: %w", s, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// csvExternalID is the deterministic idempotency key for a CSV row that carries no bank
// FITID: a content hash over the fields that make the row unique (running balance
// distinguishes otherwise-identical same-day amounts). Re-uploading the same export
// reproduces the same id, so the (connector, external_id) guard makes it a no-op.
func csvExternalID(date, amount, desc, balance string) string {
	sum := sha256.Sum256([]byte(date + "|" + amount + "|" + desc + "|" + balance))
	return "boa-csv:" + hex.EncodeToString(sum[:])[:24]
}

// directed splits a SIGNED cents amount into the (Direction, non-negative magnitude) pair
// the engine stores: money leaving the account (negative) is an Outflow, money arriving
// (positive) an Inflow. Callers skip a zero amount before reaching here.
func directed(signed int64) (Direction, int64) {
	if signed < 0 {
		return Outflow, -signed
	}
	return Inflow, signed
}

// indexFold is a case-insensitive strings.Index — OFX tags are uppercase by spec, but real
// exports vary in case, so tag matching never assumes it.
func indexFold(s, sub string) int {
	if len(sub) == 0 {
		return 0
	}
	last := len(s) - len(sub)
	for i := 0; i <= last; i++ {
		if strings.EqualFold(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}

// trimBOM strips a leading UTF-8 byte-order mark, which bank exports frequently prepend and
// which would otherwise corrupt the first tag or CSV cell.
func trimBOM(data []byte) []byte {
	return bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
}
