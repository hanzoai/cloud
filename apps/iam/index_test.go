// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"strings"
	"testing"
)

// The queries below are the ones the ORM writes (hanzoai/orm db/sqlite.go): a kind,
// the live rows, and json_extract on the field. Each has to be answered by a SEARCH
// on its index, never a SCAN of the kind, and a directory page has to come back in
// name order without a temporary sort.
func TestTheStoreIndexesAnswerIAMsLookups(t *testing.T) {
	_, conn, err := openStore(dataDirWithStore(t))
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	if _, err := ensureIndexes(ctx, conn); err != nil {
		t.Fatalf("build the indexes: %v", err)
	}
	// Idempotent: a second open of a store that has them changes nothing.
	if _, err := ensureIndexes(ctx, conn); err != nil {
		t.Fatalf("rebuild the indexes: %v", err)
	}

	const live = "SELECT id, data FROM _entities WHERE kind = ? AND deleted = 0"
	cases := []struct {
		name, query, index string
		args               []any
	}{
		{"one org's members, by name", live + " AND json_extract(data, '$.owner') = ? ORDER BY json_extract(data, '$.name') ASC LIMIT 50",
			"idx_entities_owner_name", []any{"user", "hanzo"}},
		{"one org's member count", "SELECT COUNT(*) FROM _entities WHERE kind = ? AND deleted = 0 AND json_extract(data, '$.owner') = ?",
			"idx_entities_owner_name", []any{"user", "hanzo"}},
		{"a person by natural key", live + " AND json_extract(data, '$.owner') = ? AND json_extract(data, '$.name') = ?",
			"idx_entities_owner_name", []any{"user", "hanzo", "z"}},
		{"the directory, by name", live + " ORDER BY json_extract(data, '$.name') ASC LIMIT 50 OFFSET 100",
			"idx_entities_name", []any{"user"}},
		{"sign-in by address", live + " AND json_extract(data, '$.email') = ?",
			"idx_entities_email", []any{"user", "z@hanzo.test"}},
		{"a bearer credential", live + " AND json_extract(data, '$.accessTokenHash') = ? LIMIT 1",
			"idx_entities_access_token", []any{"token", "h"}},
		{"a refresh credential", live + " AND json_extract(data, '$.refreshTokenHash') = ? LIMIT 1",
			"idx_entities_refresh_token", []any{"token", "h"}},
		{"what a user holds", live + " AND json_extract(data, '$.user') = ?",
			"idx_entities_user", []any{"session", "hanzo/z"}},
	}
	for _, c := range cases {
		rows, err := conn.QueryContext(ctx, "EXPLAIN QUERY PLAN "+c.query, c.args...)
		if err != nil {
			t.Fatalf("%s: explain: %v", c.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatalf("%s: read plan: %v", c.name, err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		got := strings.Join(plan, " | ")
		if !strings.Contains(got, "USING INDEX "+c.index) && !strings.Contains(got, "USING COVERING INDEX "+c.index) {
			t.Errorf("%s: plan %q does not use %s", c.name, got, c.index)
		}
		if strings.Contains(got, "TEMP B-TREE") {
			t.Errorf("%s: plan %q sorts in a temporary b-tree", c.name, got)
		}
	}
}
