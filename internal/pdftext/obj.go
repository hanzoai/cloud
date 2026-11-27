package pdftext

// obj.go reads PDF objects (§7.3) well enough to find a document's streams and
// rewrite the few objects plain changes. Every value remembers the span of the
// file it was read from, so a dictionary is written back with its entries copied
// byte for byte rather than re-rendered.

import (
	"errors"
	"strconv"
)

type kind uint8

const (
	kNull kind = iota
	kBool
	kInt
	kReal
	kName
	kString
	kArray
	kDict
	kRef
	kKeyword
)

// value is one PDF object. lo and hi are its span in the buffer it was read from.
type value struct {
	kind   kind
	i, gen int64  // an integer, or a reference's object and generation numbers
	s      string // a name, a keyword, or a string's bytes
	a      []value
	d      *dict
	lo, hi int
}

// dict is a dictionary in the order its entries were written. key holds each
// key's span, so a name spelled with #xx escapes is written back as it was.
type dict struct {
	names []string
	key   [][2]int
	vals  []value
}

func (d *dict) get(k string) (value, bool) {
	if d == nil {
		return value{}, false
	}
	for i, n := range d.names {
		if n == k {
			return d.vals[i], true
		}
	}
	return value{}, false
}

func (d *dict) int(k string) (int64, bool) {
	v, ok := d.get(k)
	return v.i, ok && v.kind == kInt
}

func (d *dict) name(k string) (string, bool) {
	v, ok := d.get(k)
	return v.s, ok && v.kind == kName
}

func isWhite(c byte) bool {
	return c == ' ' || c == '\n' || c == '\r' || c == '\t' || c == '\f' || c == 0
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

var errSyntax = errors.New("pdf syntax")

// maxDepth bounds nesting, so a hostile file of ten thousand '[' cannot exhaust
// the stack.
const maxDepth = 64

// lexer reads values out of b from pos.
type lexer struct {
	b     []byte
	pos   int
	depth int
}

// white skips white space and comments.
func (l *lexer) white() {
	for l.pos < len(l.b) {
		switch c := l.b[l.pos]; {
		case isWhite(c):
			l.pos++
		case c == '%':
			for l.pos < len(l.b) && l.b[l.pos] != '\n' && l.b[l.pos] != '\r' {
				l.pos++
			}
		default:
			return
		}
	}
}

// token is the regular characters from pos: a number, a keyword, or a name's body.
func (l *lexer) token() string {
	start := l.pos
	for l.pos < len(l.b) && !isWhite(l.b[l.pos]) && !isDelim(l.b[l.pos]) {
		l.pos++
	}
	return string(l.b[start:l.pos])
}

func (l *lexer) value() (value, error) {
	l.white()
	if l.pos >= len(l.b) {
		return value{}, errSyntax
	}
	start := l.pos
	v, err := l.read()
	v.lo, v.hi = start, l.pos
	return v, err
}

func (l *lexer) read() (value, error) {
	b := l.b
	switch c := b[l.pos]; {
	case c == '/':
		l.pos++
		return value{kind: kName, s: unescape(l.token())}, nil
	case c == '(':
		start := l.pos
		end, ok := stringEnd(b, l.pos)
		l.pos = end
		if !ok {
			return value{}, errSyntax
		}
		return value{kind: kString, s: literal(b[start+1 : end-1])}, nil
	case c == '<' && l.pos+1 < len(b) && b[l.pos+1] == '<':
		return l.dict()
	case c == '<':
		start := l.pos + 1
		for l.pos < len(b) && b[l.pos] != '>' {
			l.pos++
		}
		if l.pos >= len(b) {
			return value{}, errSyntax
		}
		l.pos++
		return value{kind: kString, s: hexText(b[start : l.pos-1])}, nil
	case c == '[':
		return l.array()
	case isDigit(c) || c == '+' || c == '-' || c == '.':
		return l.number()
	case isDelim(c):
		return value{}, errSyntax
	}
	switch t := l.token(); t {
	case "true", "false":
		return value{kind: kBool}, nil
	case "null":
		return value{kind: kNull}, nil
	default:
		return value{kind: kKeyword, s: t}, nil
	}
}

func (l *lexer) dict() (value, error) {
	if l.depth++; l.depth > maxDepth {
		return value{}, errSyntax
	}
	defer func() { l.depth-- }()
	l.pos += 2
	d := &dict{}
	for {
		l.white()
		if l.pos+1 < len(l.b) && l.b[l.pos] == '>' && l.b[l.pos+1] == '>' {
			l.pos += 2
			return value{kind: kDict, d: d}, nil
		}
		k, err := l.value()
		if err != nil || k.kind != kName {
			return value{}, errSyntax
		}
		v, err := l.value()
		if err != nil {
			return value{}, err
		}
		d.names = append(d.names, k.s)
		d.key = append(d.key, [2]int{k.lo, k.hi})
		d.vals = append(d.vals, v)
	}
}

func (l *lexer) array() (value, error) {
	if l.depth++; l.depth > maxDepth {
		return value{}, errSyntax
	}
	defer func() { l.depth-- }()
	l.pos++
	var a []value
	for {
		l.white()
		if l.pos < len(l.b) && l.b[l.pos] == ']' {
			l.pos++
			return value{kind: kArray, a: a}, nil
		}
		v, err := l.value()
		if err != nil {
			return value{}, err
		}
		a = append(a, v)
	}
}

// number reads an integer or a real, and an integer followed by another and R is
// a reference.
func (l *lexer) number() (value, error) {
	t := l.token()
	n, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(t, 64)
		if ferr != nil {
			return value{}, errSyntax
		}
		return value{kind: kReal, i: int64(f)}, nil
	}
	save := l.pos
	l.white()
	if l.pos < len(l.b) && isDigit(l.b[l.pos]) {
		if gen, err := strconv.ParseInt(l.token(), 10, 64); err == nil {
			l.white()
			if l.pos < len(l.b) && l.b[l.pos] == 'R' && (l.pos+1 == len(l.b) || isWhite(l.b[l.pos+1]) || isDelim(l.b[l.pos+1])) {
				l.pos++
				return value{kind: kRef, i: n, gen: gen}, nil
			}
		}
	}
	l.pos = save
	return value{kind: kInt, i: n}, nil
}

// stringEnd answers the index just past the literal string opening at i: balanced
// parentheses nest, and a backslash escapes the byte after it.
func stringEnd(b []byte, i int) (int, bool) {
	depth := 0
	for ; i < len(b); i++ {
		switch b[i] {
		case '\\':
			i++
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i + 1, true
			}
		}
	}
	return len(b), false
}

// literal decodes a literal string's body (§7.3.4.2): its backslash escapes,
// octal included, a backslash before an end of line joining two lines, and an
// end of line written as CR or CR LF read as LF.
func literal(b []byte) string {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == '\r' {
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c != '\\' || i+1 == len(b) {
			out = append(out, c)
			continue
		}
		i++
		switch c = b[i]; c {
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case '\r':
			if i+1 < len(b) && b[i+1] == '\n' {
				i++
			}
		case '\n':
		default:
			if c < '0' || c > '7' {
				out = append(out, c)
				continue
			}
			n := 0
			for k := 0; k < 3 && i < len(b) && b[i] >= '0' && b[i] <= '7'; k++ {
				n = n*8 + int(b[i]-'0')
				i++
			}
			i--
			out = append(out, byte(n))
		}
	}
	return string(out)
}

// hexText decodes a hexadecimal string's body (§7.3.4.3): white space is
// skipped, and an odd final digit is read as followed by 0.
func hexText(b []byte) string {
	out := make([]byte, 0, len(b)/2)
	hi, half := byte(0), false
	for _, c := range b {
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			continue
		}
		if half {
			out = append(out, hi<<4|v)
		} else {
			hi = v
		}
		half = !half
	}
	if half {
		out = append(out, hi<<4)
	}
	return string(out)
}

// unescape decodes a name's #xx escapes (§7.3.5).
func unescape(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '#' {
			continue
		}
		out := []byte(s[:i])
		for ; i < len(s); i++ {
			if s[i] == '#' && i+2 < len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					out = append(out, byte(v))
					i += 2
					continue
				}
			}
			out = append(out, s[i])
		}
		return string(out)
	}
	return s
}
