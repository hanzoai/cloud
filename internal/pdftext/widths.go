package pdftext

// widths.go gives a standard font the widths its document left out.
//
// A PDF may name one of the fourteen standard Type 1 fonts (§9.6.2.2) without a
// /Widths array — FPDF and TCPDF write every core font that way — and rsc.io/pdf
// places each glyph by /Widths alone and never emits a glyph for a space. A line
// set in such a font therefore came back as one word: every glyph at the line's
// start, with nothing between them for group to see. plain writes each such font
// back with the widths from its own metrics (the Adobe AFM figures pdfcpu carries),
// so a space leaves the gap it prints with and the words come apart.

import (
	"bytes"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/font"
)

// standard answers the standard font a font dictionary names when it states no
// widths of its own. A font that carries widths, or any other font, is left alone.
func standard(fd *dict) (string, bool) {
	if t, _ := fd.name("Type"); t != "Font" {
		return "", false
	}
	if s, ok := fd.name("Subtype"); ok && s != "Type1" {
		return "", false
	}
	if _, ok := fd.get("Widths"); ok {
		return "", false
	}
	name, ok := fd.name("BaseFont")
	if !ok || !font.IsCoreFont(name) {
		return "", false
	}
	return name, true
}

// widen is a standard font rewritten with its widths: the entries as written, less
// any character range they stated, and the font's own width for every code.
func widen(o *object, name string) edit {
	var b bytes.Buffer
	b.WriteString("<<")
	fd := o.dict()
	for i, n := range fd.names {
		if n == "FirstChar" || n == "LastChar" {
			continue
		}
		b.WriteByte(' ')
		b.Write(o.src[fd.key[i][0]:fd.key[i][1]])
		b.WriteByte(' ')
		b.Write(o.src[fd.vals[i].lo:fd.vals[i].hi])
	}
	b.WriteString(" /FirstChar 0 /LastChar 255 /Widths [")
	for c := range 256 {
		fmt.Fprintf(&b, " %d", font.CharWidth(name, rune(c)))
	}
	b.WriteString(" ] >>")
	return edit{num: o.num, gen: o.gen, text: b.Bytes()}
}
