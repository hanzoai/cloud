package pdftext

import (
	"bytes"
	"testing"
)

// bare is a one-page document drawing content in a font written as font is.
func bare(font string, content string) []byte {
	return classic(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		content,
		font,
	)
}

// A standard font with no /Widths — how FPDF and TCPDF write every core font —
// came back as one word a line: rsc.io/pdf places glyphs by /Widths alone and
// draws no space, so "The notice period is ninety days" read
// "Thenoticeperiodisninetydays". Times-Roman is in the list because its space is
// exactly the quarter em the old threshold asked a gap to exceed.
func TestAStandardFontWithoutWidthsKeepsItsWords(t *testing.T) {
	for _, base := range []string{"Helvetica", "Helvetica-Bold", "Times-Roman", "Times-Italic", "Courier"} {
		t.Run(base, func(t *testing.T) {
			doc := bare("<< /Type /Font /Subtype /Type1 /BaseFont /"+base+" /Encoding /WinAnsiEncoding >>", stream("", text(said)))
			reads(t, doc, said)
		})
	}
	t.Run("behind ASCII85 and Flate", func(t *testing.T) {
		doc := bare("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
			stream("/Filter [/ASCII85Decode /FlateDecode]", a85(zip(text(said)))))
		reads(t, doc, said)
	})
}

// A font that states its own widths is the document's word, and a document that
// needs nothing rewritten comes back byte for byte.
func TestAFontWithItsOwnWidthsIsLeftAsItCame(t *testing.T) {
	doc := page("4 0 R", stream("", text(said)))
	out, err := plain(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, doc) {
		t.Fatalf("plain rewrote a document whose font carries its widths:\n%s", out[len(doc):])
	}
}

// Only the fourteen standard fonts have widths to give; any other font named
// without them is left as it came.
func TestOnlyAStandardFontIsGivenWidths(t *testing.T) {
	doc := bare("<< /Type /Font /Subtype /Type1 /BaseFont /Arial >>", stream("", text(said)))
	out, err := plain(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, doc) {
		t.Fatalf("plain rewrote a font that is not one of the standard fourteen:\n%s", out[len(doc):])
	}
}
