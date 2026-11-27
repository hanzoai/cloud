package pdftext

// cmap_test.go reads ToUnicode and encoding CMaps written the ways writers write
// them: pdfTeX's bfrange-then-bfchar, Skia's bfchar of two-byte glyph ids, array
// bfranges, ligatures, surrogate pairs, and the one- to four-byte codespaces a
// CJK encoding mixes.

import (
	"testing"
)

const cmapHead = `/CIDInit /ProcSet findresource begin
12 dict begin
begincmap
/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def
/CMapName /Adobe-Identity-UCS def
/CMapType 2 def
`

const cmapTail = `endcmap
CMapName currentdict /CMap defineresource pop
end
end
`

// split reads s into codes with m and answers each code's text.
func split(m *cmap, s string) []string {
	var out []string
	for len(s) > 0 {
		c, n := m.next(s)
		t, ok := m.lookup(c)
		if !ok {
			t = "?"
		}
		out = append(out, t)
		s = s[n:]
	}
	return out
}

func same(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestBfcharMapsEachCode(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `1 begincodespacerange
<0000> <FFFF>
endcodespacerange
3 beginbfchar
<0003> <0020>
<002C> <0049>
<13B4> <FB03>
endbfchar
` + cmapTail))
	same(t, split(m, "\x00\x2c\x00\x03\x13\xb4"), []string{"I", " ", "ﬃ"})
}

func TestBfrangeCountsUpItsLastUnit(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `1 begincodespacerange
<00> <FF>
endcodespacerange
2 beginbfrange
<21> <26> <0021>
<FD> <FF> <00FE>
endbfrange
` + cmapTail))
	// <FD>..<FF> from U+00FE runs across the byte boundary into U+0100: the
	// destination is a 16-bit unit, not a byte.
	same(t, split(m, "\x21\x26\xfd\xfe\xff"), []string{"!", "&", "þ", "ÿ", "Ā"})
}

func TestBfrangeWithAnArrayNamesEachCode(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `1 begincodespacerange
<0000> <FFFF>
endcodespacerange
1 beginbfrange
<0010> <0012> [<0041> <00660069> <D83DDE00>]
endbfrange
` + cmapTail))
	same(t, split(m, "\x00\x10\x00\x11\x00\x12"), []string{"A", "fi", "\U0001F600"})
	if _, ok := m.lookup(code{0x13, 2}); ok {
		t.Fatal("a code past the array's end is mapped")
	}
}

func TestASurrogatePairIsOneCharacter(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `1 begincodespacerange
<0000> <FFFF>
endcodespacerange
1 beginbfchar
<0001> <D840DC00>
endbfchar
1 beginbfrange
<0002> <0003> <D840DC01>
endbfrange
` + cmapTail))
	same(t, split(m, "\x00\x01\x00\x02\x00\x03"), []string{"\U00020000", "\U00020001", "\U00020002"})
}

func TestALigatureMapsToItsLetters(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `1 begincodespacerange
<00> <FF>
endcodespacerange
2 beginbfchar
<1C> <00660069>
<1E> <006600660069>
endbfchar
` + cmapTail))
	same(t, split(m, "\x1c\x1e"), []string{"fi", "ffi"})
}

func TestCodespacesOfOneToFourBytesSplitAMixedString(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `4 begincodespacerange
<00> <80>
<8140> <9FFC>
<A1A1A1> <A2FEFE>
<D800DC00> <DBFFDFFF>
endcodespacerange
4 beginbfchar
<41> <0041>
<8150> <3042>
<A1B0C0> <4E2D>
<D800DC05> <00E9>
endbfchar
` + cmapTail))
	same(t, split(m, "A\x81\x50\xa1\xb0\xc0\xd8\x00\xdc\x05A"), []string{"A", "あ", "中", "é", "A"})
}

// A codespace range is a box, byte by byte, not an interval: <9030> sorts between
// <8140> and <9FFC> but its second byte is outside 40–FC, so it is no two-byte code.
func TestACodespaceIsMatchedBytewise(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `2 begincodespacerange
<00> <80>
<8140> <9FFC>
endcodespacerange
` + cmapTail))
	if c, n := m.next("\x90\x50"); n != 2 || c != (code{0x9050, 2}) {
		t.Fatalf("<9050> split as %v/%d", c, n)
	}
	if _, n := m.next("\x90\x30"); n != 2 {
		// The partial-match rule (§9.7.6.3): a first byte in a two-byte range is
		// read as two bytes even when the second falls outside it.
		t.Fatalf("<9030> consumed %d bytes, want 2", n)
	}
	for _, s := range m.space[2] {
		if s.holds("\x90\x30") {
			t.Fatal("<9030> lies in the box <8140>..<9FFC>")
		}
	}
	if _, n := m.next("\xf0\x41"); n != 1 {
		t.Fatalf("a byte in no range consumed %d, want 1", n)
	}
}

func TestEncodingCMapsNameCIDs(t *testing.T) {
	m := parseCMap([]byte(cmapHead + `/Adobe-Japan1-UCS2 usecmap
1 begincodespacerange
<0000> <FFFF>
endcodespacerange
1 begincidrange
<0020> <007E> 1
endcidrange
1 begincidchar
<3042> 842
endcidchar
` + cmapTail))
	if cid, ok := m.cidOf(code{0x41, 2}); !ok || cid != 0x41-0x20+1 {
		t.Fatalf("cid of <0041> = %d, %v", cid, ok)
	}
	if cid, ok := m.cidOf(code{0x3042, 2}); !ok || cid != 842 {
		t.Fatalf("cid of <3042> = %d, %v", cid, ok)
	}
	if _, ok := m.cidOf(code{0x7f, 2}); ok {
		t.Fatal("a code past the range has a cid")
	}
	if m.use != "Adobe-Japan1-UCS2" {
		t.Fatalf("usecmap = %q", m.use)
	}
}

func TestADamagedCMapYieldsWhatItCouldRead(t *testing.T) {
	m := parseCMap([]byte(`{ junk } ) ] <00> <FF> 1 begincodespacerange <00> <FF> endcodespacerange
2 beginbfchar <41> <0058> <42> endbfchar <<`))
	if got, ok := m.lookup(code{0x41, 1}); !ok || got != "X" {
		t.Fatalf("<41> = %q, %v", got, ok)
	}
	for _, b := range [][]byte{nil, []byte("<"), []byte("[[[[[[[["), []byte("1 beginbfrange <00> <01> endbfrange")} {
		parseCMap(b)
	}
}

func TestUnicodeEncodingsReadTheirCodesAsText(t *testing.T) {
	m, text := unicodeCMap("UniGB-UTF16-H")
	c, n := m.next("\xd8\x40\xdc\x00rest")
	if n != 4 || text("\xd8\x40\xdc\x00") != "\U00020000" || c.n != 4 {
		t.Fatalf("UTF-16 surrogate code split as %v/%d", c, n)
	}
	m, text = unicodeCMap("UniJIS-UTF8-H")
	if _, n := m.next("中a"); n != 3 || text("中") != "中" {
		t.Fatalf("UTF-8 code consumed %d", n)
	}
	if m, _ := unicodeCMap("90ms-RKSJ-H"); m != nil {
		t.Fatal("a non-Unicode predefined CMap read as Unicode")
	}
}
