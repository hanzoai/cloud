package books

// vendors.go — VENDOR resolution: turn a raw merchant string (as printed on a receipt) into
// a canonical vendor and its default expense category. A vendor carries alias spellings and
// a default category (a COA account), so a scan whose merchant matches the canonical name or
// any alias auto-fills its category at confidence "auto".
//
// classifyMerchant is the ONE resolver the scanner calls. It layers vendor + rule matching:
// a rule (rules.go) can override or supply a category; a known vendor supplies its default.
// An unknown merchant resolves to confidence "low", which drives a clarifying question so a
// human confirms — and, once they persist a vendor or rule, the SAME bill self-classifies
// next time.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// VendorRow is one row of the vendor book: a canonical name, its alias spellings, and the
// COA expense account new bills from it default to.
//
// It is …Row, like GLRow and BankTxnRow, because the OpenAPI schema namespace is FLAT
// across the whole fleet and admin already publishes a `Vendor` that is a different
// thing — a vendor COST LINE (vendor, service, amountCents, source). One name, two
// shapes is refused at the weave, and rightly: every generated SDK would bind whichever
// it read last. This side moved because this side's schema had never been published; a
// rename here costs no caller anything, and admin's would move a live SDK model.
type VendorRow struct {
	Canonical       string   `json:"canonical"`
	Aliases         []string `json:"aliases,omitempty"`
	DefaultCategory string   `json:"defaultCategory,omitempty"` // COA account number
}

// classifyMerchant resolves a raw merchant string to (vendor, category account, confidence).
// Resolution, most-authoritative first:
//
//  1. a matching RULE (rules.go), highest priority — its category wins, confidence "auto"
//  2. a matching VENDOR (canonical or alias) with a default category — confidence "auto"
//  3. no match — vendor is the raw string, no category, confidence "low"
//
// A rule that matches but a vendor that also matches still uses the rule's category (the
// explicit rule is the human's override), keeping the vendor's canonical name for display.
func (s *store) classifyMerchant(ctx context.Context, raw string) (vendor, account, confidence string, err error) {
	raw = strings.TrimSpace(raw)
	vendor = raw

	v, found, err := s.matchVendor(ctx, raw)
	if err != nil {
		return "", "", "", err
	}
	if found {
		vendor = v.Canonical
	}

	ruleAcct, ruleHit, err := s.matchRule(ctx, raw)
	if err != nil {
		return "", "", "", err
	}
	switch {
	case ruleHit:
		return vendor, categoryAccount(ruleAcct), "auto", nil
	case found && strings.TrimSpace(v.DefaultCategory) != "":
		return vendor, categoryAccount(v.DefaultCategory), "auto", nil
	default:
		return vendor, "", "low", nil
	}
}

// matchVendor finds the vendor whose canonical name or any alias matches the raw merchant,
// case-insensitively, exact-then-substring. Exact wins over substring so a specific vendor is
// not shadowed by a looser alias of another.
func (s *store) matchVendor(ctx context.Context, raw string) (VendorRow, bool, error) {
	vendors, err := s.listVendors(ctx)
	if err != nil {
		return VendorRow{}, false, err
	}
	low := strings.ToLower(raw)
	// exact pass
	for _, v := range vendors {
		if strings.ToLower(v.Canonical) == low {
			return v, true, nil
		}
		for _, a := range v.Aliases {
			if strings.ToLower(strings.TrimSpace(a)) == low {
				return v, true, nil
			}
		}
	}
	// substring pass (either direction: the receipt string contains the name, or vice versa)
	for _, v := range vendors {
		if matchToken(low, v.Canonical) {
			return v, true, nil
		}
		for _, a := range v.Aliases {
			if matchToken(low, a) {
				return v, true, nil
			}
		}
	}
	return VendorRow{}, false, nil
}

// matchToken reports whether needle (a vendor name/alias) is a non-empty substring of the
// lowercased haystack, or the haystack is a substring of it — the tolerant match a printed
// merchant line ("SQ *ACME COFFEE #42") needs against a canonical "Acme Coffee".
func matchToken(lowHaystack, needle string) bool {
	n := strings.ToLower(strings.TrimSpace(needle))
	if n == "" {
		return false
	}
	return strings.Contains(lowHaystack, n) || strings.Contains(n, lowHaystack)
}

// ── handlers ──

// vendorsOut is the org's vendor book.
type vendorsOut struct {
	// Vendors is every vendor the org has recorded, canonical name ascending.
	Vendors []VendorRow `json:"vendors"`
}

// ListVendors returns the org's vendor book: each canonical vendor, the alias spellings a
// receipt may print it under, and the expense account new bills from it default to. A
// vendor here is what makes a scanned bill self-classify instead of asking again.
//
// Example: {"sandbox": "false"}
func (o booksOps) listVendors(ctx context.Context, in *ledgerIn) (*vendorsOut, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view vendors")
	if err != nil {
		return nil, err
	}
	vendors, err := st.listVendors(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "vendors read failed")
	}
	return &vendorsOut{Vendors: vendors}, nil
}

// vendorUpsertHandler answers POST /v1/books/vendors: create or update a vendor by its
// canonical name. The default category is normalized to a real COA expense account.
func vendorUpsertHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage vendors")
	}
	var in VendorRow
	if err := c.Bind(&in); err != nil {
		return err
	}
	in.Canonical = strings.TrimSpace(in.Canonical)
	if in.Canonical == "" {
		return zip.ErrBadRequest("canonical name is required")
	}
	if strings.TrimSpace(in.DefaultCategory) != "" {
		in.DefaultCategory = categoryAccount(in.DefaultCategory)
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	if err := st.upsertVendor(c.Context(), in); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "vendor write failed")
	}
	return booksJSON(c, in)
}

// ── store methods ──

// listVendors returns the org's vendor book (canonical-ascending).
func (s *store) listVendors(ctx context.Context) ([]VendorRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT canonical, aliases, default_category FROM books_vendor ORDER BY canonical`)
	if err != nil {
		return nil, fmt.Errorf("books listVendors: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []VendorRow{}
	for rows.Next() {
		var v VendorRow
		var aliases string
		if err := rows.Scan(&v.Canonical, &aliases, &v.DefaultCategory); err != nil {
			return nil, err
		}
		if strings.TrimSpace(aliases) != "" {
			_ = json.Unmarshal([]byte(aliases), &v.Aliases)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// upsertVendor creates or updates a vendor keyed by canonical name.
func (s *store) upsertVendor(ctx context.Context, v VendorRow) error {
	aliases, err := json.Marshal(v.Aliases)
	if err != nil {
		return fmt.Errorf("books upsertVendor marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO books_vendor (canonical, aliases, default_category)
		 VALUES (?,?,?)
		 ON CONFLICT(canonical) DO UPDATE SET
		   aliases = excluded.aliases,
		   default_category = excluded.default_category`,
		v.Canonical, string(aliases), v.DefaultCategory)
	if err != nil {
		return fmt.Errorf("books upsertVendor: %w", err)
	}
	return nil
}
