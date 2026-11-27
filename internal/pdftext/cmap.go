package pdftext

// cmap.go reads a CMap (PDF 32000-1 §9.7.5, §9.10.3): the codespace that splits a
// shown string into codes of one to four bytes, and what each code maps to — text
// for a font's /ToUnicode map, a CID for a composite font's encoding.

import (
	"sort"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// code is one character code: its bytes read big-endian, and how many there were.
// <0041> and <41> are different codes.
type code struct {
	v uint32
	n int
}

func codeOf(b string) code {
	var v uint32
	for i := 0; i < len(b); i++ {
		v = v<<8 | uint32(b[i])
	}
	return code{v, len(b)}
}

// span is one codespace range. A code lies in it when each of its bytes lies
// between the bytes of lo and hi at that place (§9.7.6.2): <8140> <9FFC> holds
// <8150> but not <A040>, though both sort between the two.
type span struct{ lo, hi string }

func (s span) holds(b string) bool {
	for i := 0; i < len(b); i++ {
		if b[i] < s.lo[i] || b[i] > s.hi[i] {
			return false
		}
	}
	return true
}

// textRange is a bfrange whose destination is one string: the first code maps to
// dst, and each code after it to dst with its last UTF-16 unit counted up.
type textRange struct {
	lo, hi code
	dst    []uint16
}

// cidRange is a cidrange: lo maps to cid, and each code after it to the next CID.
type cidRange struct {
	lo, hi code
	cid    int
}

type cmap struct {
	space  [5][]span // by code length, 1 to 4
	text   map[code]string
	ranges []textRange // by lo
	cid    map[code]int
	cids   []cidRange // by lo
	use    string     // the CMap usecmap names
}

// parseCMap reads a CMap program. It never fails: what it cannot read it skips,
// so a damaged map yields what it could read, and the caller decides whether an
// empty one is worth keeping.
func parseCMap(data []byte) *cmap {
	m := &cmap{text: map[code]string{}, cid: map[code]int{}}
	l := lexer{b: data}
	var ops []value
	for {
		l.white()
		if l.pos >= len(data) {
			break
		}
		v, err := l.value()
		if err != nil {
			// A procedure brace or a stray delimiter: not part of any mapping.
			l.pos++
			ops = ops[:0]
			continue
		}
		if v.kind != kKeyword {
			ops = append(ops, v)
			continue
		}
		switch v.s {
		case "endcodespacerange":
			for i := 0; i+1 < len(ops); i += 2 {
				lo, hi := ops[i], ops[i+1]
				if n := len(lo.s); lo.kind == kString && hi.kind == kString && n >= 1 && n <= 4 && len(hi.s) == n {
					m.space[n] = append(m.space[n], span{lo.s, hi.s})
				}
			}
		case "endbfchar":
			for i := 0; i+1 < len(ops); i += 2 {
				src, dst := ops[i], ops[i+1]
				if isCode(src) && dst.kind == kString {
					m.text[codeOf(src.s)] = utf16Text(units(dst.s))
				}
			}
		case "endbfrange":
			for i := 0; i+2 < len(ops); i += 3 {
				lo, hi, dst := ops[i], ops[i+1], ops[i+2]
				if !isCode(lo) || !isCode(hi) || len(lo.s) != len(hi.s) {
					continue
				}
				a, b := codeOf(lo.s), codeOf(hi.s)
				if b.v < a.v {
					continue
				}
				switch dst.kind {
				case kString:
					if u := units(dst.s); len(u) > 0 {
						m.ranges = append(m.ranges, textRange{a, b, u})
					}
				case kArray:
					// One destination per code, in order; the array is the bound.
					for k, d := range dst.a {
						if uint64(a.v)+uint64(k) > uint64(b.v) {
							break
						}
						if d.kind == kString {
							m.text[code{a.v + uint32(k), a.n}] = utf16Text(units(d.s))
						}
					}
				}
			}
		case "endcidchar":
			for i := 0; i+1 < len(ops); i += 2 {
				src, cid := ops[i], ops[i+1]
				if isCode(src) && cid.kind == kInt && cid.i >= 0 {
					m.cid[codeOf(src.s)] = int(cid.i)
				}
			}
		case "endcidrange":
			for i := 0; i+2 < len(ops); i += 3 {
				lo, hi, cid := ops[i], ops[i+1], ops[i+2]
				if isCode(lo) && isCode(hi) && len(lo.s) == len(hi.s) && cid.kind == kInt && cid.i >= 0 {
					if a, b := codeOf(lo.s), codeOf(hi.s); a.v <= b.v {
						m.cids = append(m.cids, cidRange{a, b, int(cid.i)})
					}
				}
			}
		case "usecmap":
			if n := len(ops); n > 0 && ops[n-1].kind == kName {
				m.use = ops[n-1].s
			}
		}
		ops = ops[:0]
	}
	sort.SliceStable(m.ranges, func(i, j int) bool { return m.ranges[i].lo.v < m.ranges[j].lo.v })
	sort.SliceStable(m.cids, func(i, j int) bool { return m.cids[i].lo.v < m.cids[j].lo.v })
	return m
}

func isCode(v value) bool { return v.kind == kString && len(v.s) >= 1 && len(v.s) <= 4 }

// units reads a destination string as UTF-16BE code units. A lone byte is a unit
// of its own: some writers map a code to <20> where they meant <0020>.
func units(s string) []uint16 {
	if len(s) == 1 {
		return []uint16{uint16(s[0])}
	}
	u := make([]uint16, 0, len(s)/2)
	for i := 0; i+1 < len(s); i += 2 {
		u = append(u, uint16(s[i])<<8|uint16(s[i+1]))
	}
	return u
}

// utf16Text is UTF-16 as text, a surrogate pair as the one character it names. A
// NUL and U+FFFD are dropped: a map that sends a code to <0000> or <FFFD> says it
// does not know what the glyph is, and so does a lone surrogate.
func utf16Text(u []uint16) string {
	return strings.Map(func(r rune) rune {
		if r == 0 || r == utf8.RuneError {
			return -1
		}
		return r
	}, string(utf16.Decode(u)))
}

// empty reports a map with no codespace.
func (m *cmap) empty() bool {
	for _, s := range m.space {
		if len(s) > 0 {
			return false
		}
	}
	return true
}

// next splits the first code off b (§9.7.6.2): the shortest prefix lying in a
// codespace range of its own length. A prefix in none is read at the length of the
// shortest range its first byte falls in, and one byte when no range holds even
// that, so a stray byte costs one glyph rather than the rest of the string.
func (m *cmap) next(b string) (code, int) {
	for n := 1; n <= 4 && n <= len(b); n++ {
		for _, s := range m.space[n] {
			if s.holds(b[:n]) {
				return codeOf(b[:n]), n
			}
		}
	}
	n := 1
	for k := 1; k <= 4; k++ {
		if found(m.space[k], b[0]) {
			n = k
			break
		}
	}
	n = min(n, len(b))
	return codeOf(b[:n]), n
}

func found(spans []span, first byte) bool {
	for _, s := range spans {
		if first >= s.lo[0] && first <= s.hi[0] {
			return true
		}
	}
	return false
}

// rangeReach bounds how far back a lookup walks among ranges that start at or
// before the code, which only overlapping ranges make it do.
const rangeReach = 16

// lookup answers the text a ToUnicode map gives a code.
func (m *cmap) lookup(c code) (string, bool) {
	if s, ok := m.text[c]; ok {
		return s, true
	}
	i := sort.Search(len(m.ranges), func(i int) bool { return m.ranges[i].lo.v > c.v })
	for j := i - 1; j >= 0 && j >= i-rangeReach; j-- {
		r := m.ranges[j]
		if r.lo.n != c.n || c.v > r.hi.v {
			continue
		}
		u := append([]uint16(nil), r.dst...)
		u[len(u)-1] += uint16(c.v - r.lo.v)
		return utf16Text(u), true
	}
	return "", false
}

// cidOf answers the CID an encoding map gives a code.
func (m *cmap) cidOf(c code) (int, bool) {
	if cid, ok := m.cid[c]; ok {
		return cid, true
	}
	i := sort.Search(len(m.cids), func(i int) bool { return m.cids[i].lo.v > c.v })
	for j := i - 1; j >= 0 && j >= i-rangeReach; j-- {
		r := m.cids[j]
		if r.lo.n == c.n && c.v <= r.hi.v {
			return r.cid + int(c.v-r.lo.v), true
		}
	}
	return 0, false
}

// identity is Identity-H and Identity-V (§9.7.5.2): every two-byte code, its own CID.
func identity() *cmap {
	m := &cmap{}
	m.space[2] = []span{{"\x00\x00", "\xff\xff"}}
	return m
}

// unicodeCMap is a predefined CMap whose codes are Unicode text in the encoding its
// name ends in (Uni…-UCS2-H, Uni…-UTF16-H, Uni…-UTF8-H): the codespace that
// encoding's sequences make, and how to read one as text. nil for any other name.
func unicodeCMap(name string) (*cmap, func(string) string) {
	m := &cmap{}
	switch {
	case strings.Contains(name, "UCS2"):
		m.space[2] = []span{{"\x00\x00", "\xff\xff"}}
		return m, func(b string) string { return utf16Text(units(b)) }
	case strings.Contains(name, "UTF16"):
		m.space[2] = []span{{"\x00\x00", "\xd7\xff"}, {"\xe0\x00", "\xff\xff"}}
		m.space[4] = []span{{"\xd8\x00\xdc\x00", "\xdb\xff\xdf\xff"}}
		return m, func(b string) string { return utf16Text(units(b)) }
	case strings.Contains(name, "UTF8"):
		m.space[1] = []span{{"\x00", "\x7f"}}
		m.space[2] = []span{{"\xc2\x80", "\xdf\xbf"}}
		m.space[3] = []span{{"\xe0\x80\x80", "\xef\xbf\xbf"}}
		m.space[4] = []span{{"\xf0\x80\x80\x80", "\xf4\xbf\xbf\xbf"}}
		return m, func(b string) string { return strings.ToValidUTF8(b, "") }
	}
	return nil, nil
}
