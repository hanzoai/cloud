package books

// rules.go — auto-categorization RULES: a match pattern → a category (a COA expense
// account), highest priority first. A rule is the human's standing instruction "anything
// whose merchant contains X books to category Y", so once set, a known vendor's future bills
// self-classify without asking again. Rules are checked before a vendor's default category
// (vendors.go), so an explicit rule overrides.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// Rule is one categorization rule: a merchant substring pattern, the category (COA account)
// it books to, and a priority (higher wins when several patterns match).
type Rule struct {
	// Pattern is the merchant substring the rule matches on, case-insensitively. It is
	// also the key an upsert writes by.
	Pattern string `json:"pattern"`
	// Category is the COA expense account a matching bill books to. An upsert normalizes
	// a slug ("cloud") to its account number.
	Category string `json:"category"` // COA account number
	// Priority breaks ties: when several patterns match, the highest wins.
	Priority int `json:"priority"`
}

// matchRule finds the highest-priority rule whose pattern is a substring of the raw merchant
// (case-insensitive), returning its category account. found=false when no rule matches.
func (s *store) matchRule(ctx context.Context, raw string) (account string, found bool, err error) {
	rules, err := s.listRules(ctx)
	if err != nil {
		return "", false, err
	}
	low := strings.ToLower(strings.TrimSpace(raw))
	for _, r := range rules { // listRules returns priority-descending
		p := strings.ToLower(strings.TrimSpace(r.Pattern))
		if p != "" && strings.Contains(low, p) {
			return r.Category, true, nil
		}
	}
	return "", false, nil
}

// ── handlers ──

// rulesOut is the org's categorization rule book.
type rulesOut struct {
	// Rules is every rule the org has set, highest priority first — the order they
	// are matched in.
	Rules []Rule `json:"rules"`
}

// ListRules returns the org's auto-categorization rules, highest priority first. A rule
// is a standing instruction — "anything whose merchant contains X books to category Y" —
// and it overrides a vendor's default category, so this is the list that decides how a
// future bill classifies itself.
//
// Example: {"sandbox": "false"}
func (o booksOps) listRules(ctx context.Context, in *ledgerIn) (*rulesOut, error) {
	st, err := o.ledger(ctx, in.Sandbox, "view rules")
	if err != nil {
		return nil, err
	}
	rules, err := st.listRules(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rules read failed")
	}
	return &rulesOut{Rules: rules}, nil
}

// UpsertRule creates or updates one auto-categorization rule, keyed by its pattern —
// writing a pattern that already exists REPLACES that row's category and priority. The
// category is normalized to a real COA expense account, and anything unrecognized becomes
// 5900 Uncategorized rather than a guessed real account. It answers the row exactly as
// stored, so the caller sees the normalization. A rule overrides a vendor's default
// category, so this is the standing instruction that decides how a future bill classifies.
//
// Example: {"pattern": "aws", "category": "cloud", "priority": 5}
func (o booksOps) upsertRule(ctx context.Context, in *Rule) (*Rule, error) {
	// Tenant first, the order this route has always refused in: an anonymous caller is
	// 401 whatever it sends.
	org, err := tenant(ctx, "manage rules")
	if err != nil {
		return nil, err
	}
	row := *in
	row.Pattern = strings.TrimSpace(row.Pattern)
	if row.Pattern == "" {
		return nil, zip.ErrBadRequest("pattern is required")
	}
	row.Category = categoryAccount(row.Category)
	st, err := o.s.State.storeFor(org, sandboxOf(query(ctx, "sandbox")))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	if err := st.upsertRule(ctx, row); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rule write failed")
	}
	return &row, nil
}

// ── store methods ──

// listRules returns the org's rules, priority-descending (ties broken by pattern) — the
// order matchRule walks so the highest-priority match wins.
func (s *store) listRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT pattern, category, priority FROM books_rule ORDER BY priority DESC, pattern ASC`)
	if err != nil {
		return nil, fmt.Errorf("books listRules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		if err := rows.Scan(&r.Pattern, &r.Category, &r.Priority); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// upsertRule creates or updates a rule keyed by pattern.
func (s *store) upsertRule(ctx context.Context, r Rule) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO books_rule (pattern, category, priority)
		 VALUES (?,?,?)
		 ON CONFLICT(pattern) DO UPDATE SET
		   category = excluded.category,
		   priority = excluded.priority`,
		r.Pattern, r.Category, r.Priority)
	if err != nil {
		return fmt.Errorf("books upsertRule: %w", err)
	}
	return nil
}
