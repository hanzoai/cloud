package pdftext

// face_test.go reads real documents whose glyph codes mean nothing as characters
// until the font's /ToUnicode map is read: a pdfTeX subset TrueType font (simple,
// one byte a code, ligatures and Latin Extended in bfchar), a browser's print
// (Type0, Identity-H, two-byte glyph ids) and a CJK print of the same kind. Each
// was garbled — U+FFFD, PDFDocEncoding look-alikes, or raw two-byte codes — before
// face.go.
//
// The fixtures are made from source with the tools that write such files:
//
//	testdata/pdftex-truetype.pdf  pdftex, DejaVuSerif.ttf via ttf2tfm -p ec.enc,
//	                              \pdfgentounicode=1 \input glyphtounicode
//	testdata/chrome-type0.pdf     chromium --headless --print-to-pdf, DejaVu Sans
//	testdata/chrome-cjk.pdf       chromium --headless --print-to-pdf, Droid Sans Fallback

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"rsc.io/pdf"
)

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Lines(b, 10, 100)
	if err != nil {
		t.Fatalf("Lines(%s): %v", path, err)
	}
	return got
}

func TestSubsetFontsReadAsTheirCharacters(t *testing.T) {
	for _, c := range []struct {
		file string
		want []string
	}{
		{"testdata/pdftex-truetype.pdf", []string{
			"Quarterly revenue grew fifteen percent.",
			"The firm’s Łódź office: straße, œuvre.",
		}},
		{"testdata/chrome-type0.pdf", []string{
			"Invoice 2041 — total due: $1,250.00",
			"Office efficiency improved “significantly” in Q3.",
		}},
		{"testdata/chrome-cjk.pdf", []string{
			"季度收入增长了百分之十五。",
			"東京の会議室で打ち合わせ。",
		}},
	} {
		t.Run(c.file, func(t *testing.T) {
			same(t, lines(t, c.file), c.want)
		})
	}
}

// type0 is a one-page document drawing body in a Type0 font: Identity-H unless
// enc names another encoding (a name, or a reference to object 7), toUnicode the
// map's program (none when empty), w the descendant's /W.
func type0(body, enc, toUnicode, w string, extra ...string) []byte {
	content := fmt.Sprintf("BT /F1 12 Tf 72 720 Td %s ET", body)
	uni := ""
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		stream("", []byte(content)),
	}
	if toUnicode != "" {
		uni = " /ToUnicode 6 0 R"
	}
	objs = append(objs,
		fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /ABCDEF+Sample /Encoding %s /DescendantFonts [8 0 R]%s >>", enc, uni),
		stream("", []byte(toUnicode)),
		stream("", nil),
		fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /ABCDEF+Sample /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /DW 1000 /W %s >>", w),
	)
	for i, e := range extra {
		objs[5+i] = e // objects 6 and 7, in turn
	}
	return classic(objs...)
}

const hiThere = cmapHead + `1 begincodespacerange
<0000> <FFFF>
endcodespacerange
1 beginbfchar
<0003> <0020>
endbfchar
1 beginbfrange
<0010> <0019> <0061>
endbfrange
` + cmapTail

func TestAType0FontsSpaceGlyphKeepsWordsApart(t *testing.T) {
	// Codes <0010>..<0019> are a..j and <0003> the space: two-byte glyph ids,
	// each meaning nothing until the map is read.
	doc := type0("<0017001800130003001100100013> Tj", "/Identity-H", hiThere, "[3 [278] 16 25 600]")
	same(t, mustLines(t, doc), []string{"hid bad"})
}

func TestAWidthsRangeAndArrayBothPlaceGlyphs(t *testing.T) {
	doc := type0("<0010> Tj", "/Identity-H", hiThere, "[3 [278 300] 16 25 600 40 [0 0 0 777]]")
	r, err := pdf.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	f := load(r.Page(1).Resources().Key("Font").Key("F1"))
	for cid, want := range map[int]float64{3: 278, 4: 300, 16: 600, 25: 600, 43: 777, 26: 1000, 5: 1000} {
		if got := f.cidWidth(cid); got != want {
			t.Errorf("width of CID %d = %v, want %v", cid, got, want)
		}
	}
	if f.name != "Sample" {
		t.Errorf("name = %q: the subset tag stays on", f.name)
	}
}

func TestAType0FontWithoutAMapDrawsNothingRatherThanGarbage(t *testing.T) {
	doc := type0("<00170018> Tj", "/Identity-H", "", "[16 25 600]")
	if _, err := Lines(doc, 10, 100); !errors.Is(err, ErrNoText) {
		t.Fatalf("Lines = %v, want ErrNoText", err)
	}
}

func TestAUnicodeEncodingReadsItsCodes(t *testing.T) {
	doc := type0("<4E2D6587> Tj", "/UniGB-UCS2-H", "", "[]")
	same(t, mustLines(t, doc), []string{"中文"})
}

// An embedded encoding CMap stored behind ASCIIHex — a filter rsc.io/pdf cannot
// read — and a ToUnicode behind ASCII85: plain decodes both before the reader
// sees them, and the encoding's one-byte codespace splits the shown string.
func TestEmbeddedCMapsBehindAnyFilterAreRead(t *testing.T) {
	enc := cmapHead + `/Identity-H usecmap
1 begincodespacerange
<00> <FF>
endcodespacerange
1 begincidrange
<00> <FF> 0
endcidrange
` + cmapTail
	uni := cmapHead + `1 begincodespacerange
<00> <FF>
endcodespacerange
1 beginbfrange
<41> <5A> <0041>
endbfrange
1 beginbfchar
<20> <0020>
endbfchar
` + cmapTail
	doc := type0("(HELLO WORLD) Tj", "7 0 R", "x", "[0 [500] 32 [250] 65 90 600]",
		stream("/Filter /ASCII85Decode", a85([]byte(uni))),
		stream("/Filter /ASCIIHexDecode", ahx([]byte(enc))),
	)
	same(t, mustLines(t, doc), []string{"HELLO WORLD"})
}

// A simple font whose /ToUnicode maps the codes its encoding does not explain —
// a subset font numbering its glyphs from 1 — and whose encoding explains the rest.
func TestASimpleFontReadsToUnicodeFirstAndItsEncodingAfter(t *testing.T) {
	uni := cmapHead + `1 begincodespacerange
<00> <FF>
endcodespacerange
2 beginbfchar
<01> <0141>
<02> <00660069>
endbfchar
` + cmapTail
	font := "<< /Type /Font /Subtype /TrueType /BaseFont /XYZABC+Serif /FirstChar 1 /LastChar 126 /Encoding /WinAnsiEncoding /ToUnicode 6 0 R /Widths [" +
		strings.Repeat("600 ", 126) + "] >>"
	doc := classic(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		stream("", []byte(`BT /F1 12 Tf 72 720 Td (\001odz \002ne) Tj ET`)),
		font,
		stream("", []byte(uni)),
	)
	same(t, mustLines(t, doc), []string{"Łodz fine"})
}

func mustLines(t *testing.T, doc []byte) []string {
	t.Helper()
	got, err := Lines(doc, 10, 100)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	return got
}

// A glyph its encoding cannot name — a Type 3 bullet under a private glyph name,
// a code PDFDocEncoding leaves undefined — draws nothing rather than a control
// character or U+FFFD, and still moves the next glyph along.
func TestAGlyphNoEncodingNamesDrawsNothing(t *testing.T) {
	font := "<< /Type /Font /Subtype /Type1 /BaseFont /Bullets /FirstChar 32 /LastChar 255 /Encoding << /Differences [136 /a76] >> /Widths [" +
		strings.Repeat("600 ", 224) + "] >>"
	doc := classic(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		stream("", []byte(`BT /F1 12 Tf 72 720 Td (\210 Prefill) Tj ET`)),
		font,
	)
	same(t, mustLines(t, doc), []string{"Prefill"})
}
