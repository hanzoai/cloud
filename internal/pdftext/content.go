package pdftext

// content.go reads the text a page draws: the text operators of §9.4 run over the
// page's content stream, each shown string split into glyphs by its font (face.go)
// and placed by the text and graphics matrices. It is rsc.io/pdf's Page.Content
// with the font half replaced — that one reads a byte per glyph, so a two-byte
// composite font came back as its raw codes and a subset font's ToUnicode map was
// never read.

import (
	"strings"
	"unicode"

	"rsc.io/pdf"
)

type matrix [3][3]float64

var ident = matrix{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}

func (x matrix) mul(y matrix) matrix {
	var z matrix
	for i := range 3 {
		for j := range 3 {
			for k := range 3 {
				z[i][j] += x[i][k] * y[k][j]
			}
		}
	}
	return z
}

func translate(tx, ty float64) matrix { return matrix{{1, 0, 0}, {0, 1, 0}, {tx, ty, 1}} }

// state is the part of the graphics state text placement reads (§9.3).
type state struct {
	Tc, Tw, Th, Tl, Tfs, Trise float64
	font                       *face
	Tm, Tlm, CTM               matrix
}

// drawn answers the page's text, one fragment per glyph with its position, size
// and width, in the order drawn. A glyph whose text is blank is not a fragment;
// it moves the next one along, and the gap it leaves is where group sees a word
// break.
func drawn(p pdf.Page, fs faces) []pdf.Text {
	g := state{Th: 1, CTM: ident, Tm: ident, Tlm: ident, font: &face{scale: 0.001}}
	var stack []state
	var out []pdf.Text
	fonts := p.Resources().Key("Font")
	show := func(s string) {
		f := g.font
		f.glyphs(s, func(gl glyph) {
			if !blank(gl.text) {
				trm := matrix{{g.Tfs * g.Th, 0, 0}, {0, g.Tfs, 0}, {0, g.Trise, 1}}.mul(g.Tm).mul(g.CTM)
				out = append(out, pdf.Text{
					Font: f.name, FontSize: trm[0][0], X: trm[2][0], Y: trm[2][1],
					W: gl.w * trm[0][0], S: unligate.Replace(gl.text),
				})
			}
			tx := gl.w*g.Tfs + g.Tc
			if gl.space {
				tx += g.Tw
			}
			g.Tm = translate(tx*g.Th, 0).mul(g.Tm)
		})
	}
	nextLine := func() {
		g.Tlm = translate(0, -g.Tl).mul(g.Tlm)
		g.Tm = g.Tlm
	}
	num := func(v pdf.Value) float64 { return v.Float64() }
	pdf.Interpret(p.V.Key("Contents"), func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		want := func(n int) bool { return len(args) == n }
		switch op {
		case "q":
			if len(stack) < 1<<10 {
				stack = append(stack, g)
			}
		case "Q":
			if n := len(stack); n > 0 {
				g, stack = stack[n-1], stack[:n-1]
			}
		case "cm":
			if want(6) {
				var m matrix
				for i := range 6 {
					m[i/2][i%2] = num(args[i])
				}
				m[2][2] = 1
				g.CTM = m.mul(g.CTM)
			}
		case "BT":
			g.Tm, g.Tlm = ident, ident
		case "Tm":
			if want(6) {
				var m matrix
				for i := range 6 {
					m[i/2][i%2] = num(args[i])
				}
				m[2][2] = 1
				g.Tm, g.Tlm = m, m
			}
		case "TD":
			if want(2) {
				g.Tl = -num(args[1])
				g.Tlm = translate(num(args[0]), num(args[1])).mul(g.Tlm)
				g.Tm = g.Tlm
			}
		case "Td":
			if want(2) {
				g.Tlm = translate(num(args[0]), num(args[1])).mul(g.Tlm)
				g.Tm = g.Tlm
			}
		case "T*":
			nextLine()
		case "TL":
			if want(1) {
				g.Tl = num(args[0])
			}
		case "Tc":
			if want(1) {
				g.Tc = num(args[0])
			}
		case "Tw":
			if want(1) {
				g.Tw = num(args[0])
			}
		case "Tz":
			if want(1) {
				g.Th = num(args[0]) / 100
			}
		case "Ts":
			if want(1) {
				g.Trise = num(args[0])
			}
		case "Tf":
			if want(2) {
				g.font = fs.of(fonts.Key(args[0].Name()))
				g.Tfs = num(args[1])
			}
		case "Tj":
			if want(1) {
				show(args[0].RawString())
			}
		case "'":
			if want(1) {
				nextLine()
				show(args[0].RawString())
			}
		case "\"":
			if want(3) {
				g.Tw, g.Tc = num(args[0]), num(args[1])
				nextLine()
				show(args[2].RawString())
			}
		case "TJ":
			if want(1) {
				a := args[0]
				for i := 0; i < a.Len(); i++ {
					if x := a.Index(i); x.Kind() == pdf.String {
						show(x.RawString())
					} else {
						g.Tm = translate(-num(x)/1000*g.Tfs*g.Th, 0).mul(g.Tm)
					}
				}
			}
		}
	})
	return out
}

// unligate spells out the Latin ligatures (U+FB00–U+FB06) a ToUnicode map may
// name a ligature glyph by, so "oﬃce" is found by "office".
var unligate = strings.NewReplacer("\ufb00", "ff", "\ufb01", "fi", "\ufb02", "fl",
	"\ufb03", "ffi", "\ufb04", "ffl", "\ufb05", "st", "\ufb06", "st")

// blank is text that draws nothing a reader would search for.
func blank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
