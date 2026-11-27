package pdftext

// face.go reads a font dictionary into what text extraction needs of it: how a
// shown string splits into codes, each code's text, and each code's width.
//
// Text comes from the font's /ToUnicode map first (§9.10.2), because that is the
// one statement of what a glyph means that survives subsetting: a subset font
// numbers its glyphs as it pleases, and a composite font's codes are glyph or CID
// numbers that mean nothing as characters. A simple font falls back to its
// /Encoding; a composite font whose encoding is a Unicode CMap reads the code
// itself; a code nothing explains is dropped rather than emitted as its bytes.

import (
	"io"
	"strings"
	"unicode"

	"rsc.io/pdf"
)

// cmapLimit bounds the bytes read from one embedded CMap. Adobe's largest
// predefined maps run to a few hundred kilobytes.
const cmapLimit = 4 << 20

// widthLimit bounds the entries read from a composite font's /W array.
const widthLimit = 1 << 16

// face is one font as the page draws with it.
type face struct {
	name string // BaseFont, less a subset's ABCDEF+ tag
	uni  *cmap  // the /ToUnicode map; nil when there is none or it cannot be read

	// A composite (Type0) font: enc splits strings into codes and names their
	// CIDs; ident says a code is its own CID; codeText reads a code as text when
	// the encoding is a Unicode CMap.
	composite bool
	enc       *cmap
	ident     bool
	codeText  func(string) string
	dw        float64
	w         map[int]float64
	wr        []widthRange

	// A simple font: one byte per code, widths from /FirstChar and /Widths.
	base    pdf.TextEncoding // nil: no encoding rsc.io/pdf reads safely
	simple  [256]string
	decoded [256]bool
	first   int
	widths  []float64
	missing float64

	scale float64 // glyph space to text space: 1/1000, or a Type 3 font's /FontMatrix
}

// widthRange is a /W entry of the c_first c_last w form.
type widthRange struct {
	first, last int
	w           float64
}

// glyph is one code shown: its text ("" when nothing explains it), its advance in
// text space units, and whether it is the single-byte code 32 word spacing applies
// to (§9.3.3).
type glyph struct {
	text  string
	w     float64
	space bool
}

// faces caches each font the document draws with, keyed by its dictionary as
// rsc.io/pdf prints it — which names every stream it refers to by object number,
// so two keys agree only when the fonts are the same.
type faces map[string]*face

func (fs faces) of(v pdf.Value) *face {
	key := v.String()
	if f, ok := fs[key]; ok {
		return f
	}
	f := load(v)
	fs[key] = f
	return f
}

// load reads a font dictionary. A null value — a Tf naming a font the page does
// not have — is a simple font with no widths and the document encoding, as
// rsc.io/pdf read it.
func load(v pdf.Value) *face {
	name := v.Key("BaseFont").Name()
	if i := strings.Index(name, "+"); i >= 0 {
		name = name[i+1:]
	}
	f := &face{name: name, scale: 0.001}
	if tu := v.Key("ToUnicode"); tu.Kind() == pdf.Stream {
		if b, ok := streamBytes(tu); ok {
			if m := parseCMap(b); len(m.text) > 0 || len(m.ranges) > 0 {
				f.uni = m
			}
		}
	}
	if v.Key("Subtype").Name() == "Type0" {
		f.loadComposite(v)
		return f
	}
	f.loadSimple(v)
	return f
}

func (f *face) loadComposite(v pdf.Value) {
	f.composite = true
	enc := v.Key("Encoding")
	switch enc.Kind() {
	case pdf.Name:
		switch n := enc.Name(); n {
		case "Identity-H", "Identity-V":
			f.enc, f.ident = identity(), true
		default:
			f.enc, f.codeText = unicodeCMap(n)
		}
	case pdf.Stream:
		if b, ok := streamBytes(enc); ok {
			m := parseCMap(b)
			if m.use == "Identity-H" || m.use == "Identity-V" {
				f.ident = true
				if m.empty() {
					m.space = identity().space
				}
			}
			if !m.empty() {
				f.enc = m
			}
		}
	}
	if f.enc == nil {
		// A predefined CMap whose tables are not carried here: the ToUnicode map's
		// codespace is the encoding's in every writer that writes one, and two
		// bytes is the common case when there is none.
		f.enc = identity()
		if f.uni != nil && !f.uni.empty() {
			f.enc = &cmap{space: f.uni.space}
		}
	}
	d := v.Key("DescendantFonts").Index(0)
	f.dw = 1000
	if dw := d.Key("DW"); dw.Kind() == pdf.Integer || dw.Kind() == pdf.Real {
		f.dw = dw.Float64()
	}
	f.w = map[int]float64{}
	w := d.Key("W")
	for i, n := 0, 0; i+1 < w.Len() && n < widthLimit; {
		first := int(w.Index(i).Int64())
		next := w.Index(i + 1)
		if next.Kind() == pdf.Array {
			for j := 0; j < next.Len() && n < widthLimit; j++ {
				f.w[first+j] = next.Index(j).Float64()
				n++
			}
			i += 2
			continue
		}
		if i+2 >= w.Len() {
			break
		}
		f.wr = append(f.wr, widthRange{first, int(next.Int64()), w.Index(i + 2).Float64()})
		n++
		i += 3
	}
}

func (f *face) loadSimple(v pdf.Value) {
	enc := v.Key("Encoding")
	// rsc.io/pdf's own encoder for the encodings it reads without complaint. It
	// prints for an encoding name it does not know, and would run its own CMap
	// reader over a /ToUnicode written as a bare dictionary; neither is called.
	switch enc.Kind() {
	case pdf.Dict:
		f.base = pdf.Font{V: v}.Encoder()
	case pdf.Name:
		if n := enc.Name(); n == "WinAnsiEncoding" || n == "MacRomanEncoding" {
			f.base = pdf.Font{V: v}.Encoder()
		}
	case pdf.Null:
		if v.Key("ToUnicode").Kind() != pdf.Dict {
			f.base = pdf.Font{V: v}.Encoder()
		}
	}
	f.first = int(v.Key("FirstChar").Int64())
	ws := v.Key("Widths")
	for i := 0; i < ws.Len() && i < 256; i++ {
		f.widths = append(f.widths, ws.Index(i).Float64())
	}
	f.missing = v.Key("FontDescriptor").Key("MissingWidth").Float64()
	if v.Key("Subtype").Name() == "Type3" {
		if m := v.Key("FontMatrix"); m.Len() == 6 && m.Index(0).Float64() != 0 {
			f.scale = m.Index(0).Float64()
		}
	}
}

// streamBytes is a stream's data, decoded, or false when rsc.io/pdf cannot decode
// it (it panics on a filter it does not know) or it runs past cmapLimit.
func streamBytes(v pdf.Value) (b []byte, ok bool) {
	defer func() {
		if recover() != nil {
			b, ok = nil, false
		}
	}()
	b, err := io.ReadAll(io.LimitReader(v.Reader(), cmapLimit+1))
	if err != nil || len(b) > cmapLimit {
		return nil, false
	}
	return b, true
}

// glyphs splits a shown string into its glyphs.
func (f *face) glyphs(s string, each func(glyph)) {
	if !f.composite {
		for i := 0; i < len(s); i++ {
			each(glyph{f.byteText(s[i]), f.byteWidth(s[i]) * f.scale, s[i] == ' '})
		}
		return
	}
	for len(s) > 0 {
		c, n := f.enc.next(s)
		raw := s[:n]
		s = s[n:]
		text, ok := "", false
		if f.uni != nil {
			text, ok = f.uni.lookup(c)
		}
		if !ok && f.codeText != nil {
			text = f.codeText(raw)
		}
		cid, known := f.enc.cidOf(c)
		if !known && f.ident {
			cid, known = int(c.v), true
		}
		w := f.dw
		if known {
			w = f.cidWidth(cid)
		}
		each(glyph{text, w * f.scale, n == 1 && raw[0] == ' '})
	}
}

// byteText is a simple font's text for a code: its ToUnicode entry, else what its
// encoding says, else the byte itself where it is printable Latin-1. An encoding
// answers a code it has no character for with U+FFFD, or with the raw byte when
// it names a glyph rsc.io/pdf does not know — a control character under 0x20 and
// in 0x80–0x9f — and neither is text, so both are dropped.
func (f *face) byteText(b byte) string {
	if f.uni != nil {
		if t, ok := f.uni.lookup(code{uint32(b), 1}); ok {
			return t
		}
	}
	if !f.decoded[b] {
		f.decoded[b] = true
		switch {
		case f.base != nil:
			if t := f.base.Decode(string([]byte{b})); !unnamed(t) {
				f.simple[b] = t
			}
		case b >= 0x20 && b < 0x7f || b >= 0xa0:
			f.simple[b] = string(rune(b))
		}
	}
	return f.simple[b]
}

// unnamed reports text that names no character: control characters and U+FFFD.
func unnamed(s string) bool {
	for _, r := range s {
		if !unicode.IsControl(r) && r != unicode.ReplacementChar {
			return false
		}
	}
	return true
}

func (f *face) byteWidth(b byte) float64 {
	if i := int(b) - f.first; i >= 0 && i < len(f.widths) {
		return f.widths[i]
	}
	return f.missing
}

func (f *face) cidWidth(cid int) float64 {
	if w, ok := f.w[cid]; ok {
		return w
	}
	for _, r := range f.wr {
		if cid >= r.first && cid <= r.last {
			return r.w
		}
	}
	return f.dw
}
