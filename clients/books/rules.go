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

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Rule is one categorization rule: a merchant substring pattern, the category (COA account)
// it books to, and a priority (higher wins when several patterns match).
type Rule struct {
	Pattern  string `json:"pattern"`
	Category string `json:"category"` // COA account number
	Priority int    `json:"priority"`
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

// rulesListHandler answers GET /v1/books/rules: the org's rules, priority-descending.
func rulesListHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view rules")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	rules, err := st.listRules(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rules read failed")
	}
	return booksJSON(c, map[string]any{"rules": rules})
}

// ruleUpsertHandler answers POST /v1/books/rules: create or update a rule by its pattern.
// The category is normalized to a real COA expense account.
func ruleUpsertHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to manage rules")
	}
	var in Rule
	if err := c.Bind(&in); err != nil {
		return err
	}
	in.Pattern = strings.TrimSpace(in.Pattern)
	if in.Pattern == "" {
		return zip.ErrBadRequest("pattern is required")
	}
	in.Category = categoryAccount(in.Category)
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	if err := st.upsertRule(c.Context(), in); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rule write failed")
	}
	return booksJSON(c, in)
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
