package text

import (
	"regexp"
	"strings"
	"unicode"
)

// OneLine keeps a value on one line: a subject is a header, and a line break in it
// would start another. Every control, space, line or paragraph separator and format
// character (a bidirectional override among them) is a break here, and a run of them
// is one space, so no run of blanks can wrap a stranger's text onto a line of its own.
func OneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, unseen), " ")
}

// Email reports whether s is shaped like one email address: one @, something before
// it, and a dotted domain after it, with no space and none of the characters that
// join or quote a second address (a comma, a semicolon, an escape), and none of the
// characters a header or a log line can be bent by: a control (NUL among them), a
// space or separator of any script, or a format character such as a zero-width space
// or a bidirectional override.
func Email(s string) bool {
	local, domain, ok := strings.Cut(s, "@")
	return ok && local != "" && !strings.ContainsAny(s, ",;%\"<>()[]\\") && strings.IndexFunc(s, unseen) < 0 &&
		!strings.Contains(domain, "@") && strings.Contains(strings.Trim(domain, "."), ".")
}

// unseen is a character no one sees or that bends what is around it: a control, a
// space, a line or paragraph separator, or a format character.
func unseen(r rune) bool {
	return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp, unicode.Cf)
}

// tag is a BCP 47 language tag as a browser reports one: en, pt-BR, zh-Hant-TW.
var tag = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{1,8}){0,4}$`)

// Tag reports whether s is shaped like a BCP 47 language tag.
func Tag(s string) bool { return tag.MatchString(s) }
