package sqlcheck

import "testing"

func TestProblems(t *testing.T) {
	for _, c := range []struct {
		sql  string
		pure bool
		want int
	}{
		{`SELECT a FROM t WHERE a = $1 AND b = $2`, true, 0},
		{`SELECT a FROM t WHERE b = $2 AND a = $1`, true, 1},
		{`SELECT a FROM t WHERE a = $1 OR b = $1 OR c = $2`, true, 0},
		{`SELECT a FROM t WHERE b = $2`, true, 1},
		{` AND b = $2`, true, 0}, // a fragment is held to order alone
		{`SELECT a FROM t WHERE a = $1 AND b = $3`, true, 1},
		{`SELECT a FROM t WHERE a = $1 AND b = $3`, false, 0}, // a number may be worked out at run time
		{`SELECT a FROM t WHERE a = ?`, true, 1},
		{`SELECT a FROM t WHERE a = ?1`, true, 1},
		{`INSERT INTO t (a, b) VALUES (?, ?)`, true, 1},
		{`SELECT a FROM t WHERE a IN (?,?)`, true, 1},
		{`INSERT OR IGNORE INTO t (a) VALUES ($1)`, true, 1},
		{`INSERT OR FAIL INTO t (a) VALUES ($1)`, true, 1},
		{`INSERT OR/**/IGNORE INTO t (a) VALUES ($1)`, true, 1},
		{`UPDATE OR REPLACE t SET a = $1`, true, 1},
		{`REPLACE INTO t (a) VALUES ($1)`, true, 1},
		{`INSERT INTO t (a) VALUES ($1) ON CONFLICT DO NOTHING`, true, 0},
		// not these
		{`^(?:true)$`, true, 0},
		{`(?i)acme|(?P<x>y)`, true, 0},
		{`which orgs are meant?`, true, 0},
		{`?`, true, 0}, // a character: a URL is split on it
		{`INSERT OR IGNORE`, true, 0},
		{`failed: INSERT OR IGNORE is SQLite's`, true, 0},
		{`SELECT a FROM t WHERE x = $1 OR ignore = $2 OR fail = $3`, true, 0},
		{`SELECT a FROM t WHERE a = $1 -- a note about ? and $2 and REPLACE INTO t (a)`, true, 0},
		{`SELECT a FROM t WHERE a = $1 /* ? */`, true, 0},
		{`SELECT a FROM t WHERE x = '-- ' AND y = ?`, true, 1}, // a quoted -- is text, so the ? after it is read
		{`SELECT a FROM t WHERE x = '/* ' AND y = ? AND z = ' */'`, true, 1},
		// a dollar amount in prose is not a placeholder
		{`revenue must be $500.00 (usage $200 + MRR $300), got 5`, true, 0},
		{`IRC §6041(a): $600 (Notice 2025-62); royalties $10 (IRC §6050N(a))`, true, 0},
		{`after a $50 top-up and a $20 refund`, true, 0},
		{`SELECT a FROM t WHERE a = $1 AND b IN ($2,$3) LIMIT $4 OFFSET $5`, true, 0},
		{`SELECT CASE WHEN a = $1 THEN $2 ELSE $3 END FROM t`, true, 0},
		{`SELECT a FROM t WHERE b = $2 AND a = $1`, false, 1},
		{`?,`, true, 1},
		{`,?`, true, 1},
		{`(?)`, true, 1},
		{`?, ?`, true, 1},
	} {
		if got := Problems(c.sql, c.pure); len(got) != c.want {
			t.Errorf("%q: %d problems %v, want %d", c.sql, len(got), got, c.want)
		}
	}
}

func TestStrip(t *testing.T) {
	for in, want := range map[string]string{
		`a /* b */ c`:             `a   c`,
		"a -- b\nc":               "a  \nc",
		`'-- not'`:                `'-- not'`,
		`'it''s -- text' -- note`: `'it''s -- text'  `,
		`"a--b"`:                  `"a--b"`,
		`a /* unterminated`:       `a  `,
	} {
		if got := Strip(in); got != want {
			t.Errorf("Strip(%q) = %q, want %q", in, got, want)
		}
	}
}
