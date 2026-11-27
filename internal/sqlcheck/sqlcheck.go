package sqlcheck

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// statement is the shape of a complete statement, which is when the numbers in it
	// must run 1 to n. A fragment is held only to the order its numbers appear in.
	statement = regexp.MustCompile(`(?is)\bselect\b.+\bfrom\b|\binsert\s+into\b|\bupdate\s+\w+\s+set\b|\bdelete\s+from\b|\bcreate\s+(unique\s+)?(table|index|trigger)\b|\balter\s+table\b`)
	// A $n placeholder follows an operator, a comma, an opening parenthesis or a keyword
	// that takes a value. A dollar amount in prose ("pays $500 then $200", "a $10
	// refund") follows a word that is none of those, and is not one.
	numbered = regexp.MustCompile(`(?i)(?:[=<>(,+\-*/|:]|\b(?:select|in|like|limit|offset|values|between|and|or|set|where|then|else|when|on|is|not|from|any|all|cast|returning)\b)\s*\$(\d+)`)

	// A ? where a value goes: after an operator, a comma, an opening parenthesis or a
	// keyword that takes one, and before whatever ends a value. `(?:` and `(?i)` of a
	// regular expression have a letter or colon after the ? and are not this.
	question = regexp.MustCompile(`(?i)(^|[\s(,=<>]|\b(?:in|like|limit|offset|values|between|and|or|set|where))\s*\?\d*($|[\s,);])`)
	// A literal that is nothing but placeholders, commas and parentheses, with a comma
	// or a parenthesis or a second mark in it: the "?," that strings.Repeat is given. A
	// lone "?" is a character, not a placeholder (a URL is split on it).
	marks = regexp.MustCompile(`^[\s,()]*\?[\s,?()]*$`)

	// SQLite-only statements, by the shape of the statement and not by the words: prose
	// that says INSERT OR IGNORE, and a column that happens to be called ignore, are
	// not these.
	conflict = `(?:IGNORE|REPLACE|ROLLBACK|ABORT|FAIL)`
	sqlite   = regexp.MustCompile(`(?i)\bREPLACE\s+INTO\s+["\w]+\s*(?:\(|VALUES\b|SELECT\b|DEFAULT\b)|\bINSERT\s+OR\s+` + conflict + `\s+INTO\b|\bUPDATE\s+OR\s+` + conflict + `\s+["\w]+\s+SET\b`)

	spaces = regexp.MustCompile(`\s+`)
)

// Problems says what is wrong with one SQL text, or nothing. pure says the text is
// whole: made of literals alone, or executed as it stands, so its numbers must run 1
// to n without a gap; a piece of a statement is held only to their order.
func Problems(text string, pure bool) []string {
	text = strings.TrimSpace(spaces.ReplaceAllString(" "+Strip(text)+" ", " "))
	var out []string
	if text != "?" && (question.MatchString(text) || (strings.ContainsAny(text, ",()") || strings.Count(text, "?") > 1) && marks.MatchString(text)) {
		out = append(out, "a ? or ?N placeholder, which PostgreSQL does not read; write $n")
	}
	if sqlite.MatchString(text) {
		out = append(out, "INSERT OR / UPDATE OR / REPLACE INTO, which PostgreSQL does not read; write ON CONFLICT")
	}
	var seen []int
	for _, m := range numbered.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		known := false
		for _, k := range seen {
			known = known || k == n
		}
		if !known {
			seen = append(seen, n)
		}
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			out = append(out, "$"+strconv.Itoa(seen[i])+" first appears after $"+strconv.Itoa(seen[i-1])+": SQLite binds $n by order of appearance, so the arguments would be swapped")
			break
		}
	}
	if pure && len(seen) > 0 && statement.MatchString(text) {
		highest := 0
		for _, n := range seen {
			highest = max(highest, n)
		}
		if len(seen) != highest {
			out = append(out, "its placeholders are not $1 to $"+strconv.Itoa(highest)+" without a gap: a statement numbers from $1")
		}
	}
	return out
}

// Strip removes SQL comments, /* */ and -- to the end of the line, and only where
// they are comments: the same characters inside a quoted string or identifier are
// text, and a ? after "-- " in a string literal is still a ? in the statement.
func Strip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c { // a doubled quote is a quote in the string
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(s))
			b.WriteString(s[i:end])
			i = end
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				i = len(s)
			} else {
				i += end + 4
			}
			b.WriteByte(' ')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
