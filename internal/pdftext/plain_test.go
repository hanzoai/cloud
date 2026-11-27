package pdftext

// plain_test.go reads real documents whose text sits behind each standard
// filter, alone and chained, and behind the page shapes rsc.io/pdf cannot read on
// its own: a /Contents array, an inline image, a cross-reference stream with the
// page tree in an object stream, and an incremental update. Every one of these
// failed the whole file before plain.go.

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

const said = "The notice period is ninety days"

// text is a content stream drawing each line in Helvetica, one under another.
func text(lines ...string) []byte {
	var b strings.Builder
	b.WriteString("BT /F1 12 Tf 72 720 Td 14 TL\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "(%s) Tj T*\n", l)
	}
	b.WriteString("ET")
	return []byte(b.String())
}

// stream is a stream object: entries (the filter and its parameters) and data.
func stream(entries string, data []byte) string {
	return fmt.Sprintf("<< /Length %d %s >>\nstream\n%s\nendstream", len(data), entries, data)
}

const helvetica = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [" +
	"500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 " +
	"500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 " +
	"500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 " +
	"500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500 500] >>"

// classic writes objects 1..n with a cross-reference table.
func classic(objs ...string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	at := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, at)
	return b.Bytes()
}

// page is a one-page document whose /Contents is contents (a reference or an
// array of them), with object 4 the first content stream and 5 the font; more
// objects follow from 6.
func page(contents string, first string, more ...string) []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents " + contents +
			" /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 7 0 R >> >> >>",
		first,
		helvetica,
	}
	return classic(append(objs, more...)...)
}

// reads asserts a document's text holds every line.
func reads(t *testing.T, doc []byte, lines ...string) {
	t.Helper()
	got, err := Lines(doc, 10, 100)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	all := strings.Join(got, "\n")
	for _, l := range lines {
		if !strings.Contains(all, l) {
			t.Fatalf("Lines = %q, want it to hold %q", all, l)
		}
	}
}

func a85(b []byte) []byte {
	out := make([]byte, ascii85.MaxEncodedLen(len(b)))
	n := ascii85.Encode(out, b)
	// Line breaks inside the data and the optional <~ opener are both legal.
	var w bytes.Buffer
	w.WriteString("<~")
	for i := 0; i < n; i += 40 {
		w.Write(out[i:min(n, i+40)])
		w.WriteByte('\n')
	}
	w.WriteString("~>")
	return w.Bytes()
}

func ahx(b []byte) []byte {
	h := strings.ToUpper(hex.EncodeToString(b))
	var w strings.Builder
	for i := 0; i < len(h); i += 32 {
		w.WriteString(h[i:min(len(h), i+32)])
		w.WriteString(" \n")
	}
	w.WriteString(">")
	return []byte(w.String())
}

func zip(b []byte) []byte {
	var w bytes.Buffer
	z := zlib.NewWriter(&w)
	_, _ = z.Write(b)
	_ = z.Close()
	return w.Bytes()
}

// rle is RunLengthDecode's encoder: a repeat for a run of three or more, a
// literal for the rest, and the end-of-data byte.
func rle(b []byte) []byte {
	var out []byte
	for i := 0; i < len(b); {
		j := i
		for j < len(b) && j-i < 128 && b[j] == b[i] {
			j++
		}
		if j-i >= 3 {
			out = append(out, byte(257-(j-i)), b[i])
			i = j
			continue
		}
		k := i
		for k < len(b) && k-i < 128 && !(k+2 < len(b) && b[k] == b[k+1] && b[k] == b[k+2]) {
			k++
		}
		out = append(out, byte(k-i-1))
		out = append(out, b[i:k]...)
		i = k
	}
	return append(out, 128)
}

func TestEachFilterReads(t *testing.T) {
	body := text(said, "Signed in Boston")
	for _, c := range []struct {
		name, entries string
		data          []byte
	}{
		{"ASCII85Decode", "/Filter /ASCII85Decode", a85(body)},
		{"ASCIIHexDecode", "/Filter /ASCIIHexDecode", ahx(body)},
		{"LZWDecode", "/Filter /LZWDecode", lzwEncode(body, 1)},
		{"LZWDecode EarlyChange 0", "/Filter /LZWDecode /DecodeParms << /EarlyChange 0 >>", lzwEncode(body, 0)},
		{"RunLengthDecode", "/Filter /RunLengthDecode", rle(body)},
		{"FlateDecode PNG predictor", "/Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 8 >>", zip(pngEncode(body, 8, 1, 8))},
		{"FlateDecode TIFF predictor", "/Filter /FlateDecode /DecodeParms << /Predictor 2 /Columns 16 >>", zip(tiffEncode(body, 16, 1, 8))},
		{"LZWDecode PNG predictor", "/Filter /LZWDecode /DecodeParms << /Predictor 15 /Columns 5 /Colors 2 >>", lzwEncode(pngEncode(body, 5, 2, 8), 1)},
	} {
		t.Run(c.name, func(t *testing.T) {
			reads(t, page("4 0 R", stream(c.entries, c.data)), said, "Signed in Boston")
		})
	}
}

func TestChainedFiltersReadInOrder(t *testing.T) {
	body := text(said)
	for _, c := range []struct {
		name, entries string
		data          []byte
	}{
		{"ASCII85 over Flate", "/Filter [/ASCII85Decode /FlateDecode]", a85(zip(body))},
		{"ASCIIHex over LZW", "/Filter [/ASCIIHexDecode /LZWDecode] /DecodeParms [null << /EarlyChange 1 >>]", ahx(lzwEncode(body, 1))},
		{"ASCII85 over RunLength over Flate", "/Filter [/ASCII85Decode /RunLengthDecode /FlateDecode]", a85(rle(zip(body)))},
		{"abbreviated names", "/Filter [/A85 /Fl]", a85(zip(body))},
		{"a chain's predictor sits on its own filter", "/Filter [/ASCIIHexDecode /FlateDecode] /DecodeParms [null << /Predictor 11 /Columns 6 >>]", ahx(zip(pngEncode(body, 6, 1, 8)))},
	} {
		t.Run(c.name, func(t *testing.T) {
			reads(t, page("4 0 R", stream(c.entries, c.data)), said)
		})
	}
}

// A blank page carries no /Contents at all, and a document with one is read
// for the pages that do draw; one that is only blank pages has no text.
func TestABlankPageDoesNotFailTheDocument(t *testing.T) {
	doc := classic(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> >>",
		stream("", text(said)),
		helvetica,
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
	)
	reads(t, doc, said)
	only := classic(
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	)
	if _, err := Lines(only, 10, 100); !errors.Is(err, ErrNoText) {
		t.Fatalf("a document of blank pages: err = %v, want ErrNoText", err)
	}
}

func TestAPageDrawnFromSeveralStreamsReads(t *testing.T) {
	doc := page("[4 0 R 6 0 R]",
		stream("", text(said)),
		stream("/Filter /ASCII85Decode", a85([]byte("BT /F1 12 Tf 72 600 Td (Signed in Boston) Tj ET"))),
		stream("", nil),
	)
	reads(t, doc, said, "Signed in Boston")
}

// An inline image's data is not text and may hold any byte; the hex one here
// carries the '>' that ends its data, which the reader's lexer refuses.
func TestInlineImagesAreLiftedOut(t *testing.T) {
	for _, c := range []struct{ name, image string }{
		{"ASCIIHex", "BI /W 4 /H 1 /CS /G /BPC 8 /F /AHx ID 00ff00ff> EI"},
		{"ASCII85", "BI /W 4 /H 1 /CS /G /BPC 8 /F [/A85 /Fl] ID " + string(a85(zip([]byte{0, 255, 0, 255}))) + " EI"},
		{"binary, by length", "BI /W 4 /H 1 /CS /G /BPC 8 /L 6 ID \x00) EI(\xff EI"},
		{"binary", "BI /W 2 /H 1 /CS /G /BPC 8 ID \x00\xff) EI"},
	} {
		t.Run(c.name, func(t *testing.T) {
			content := "BT /F1 12 Tf 72 720 Td (" + said + ") Tj ET q " + c.image +
				" Q BT /F1 12 Tf 72 700 Td (Signed in Boston) Tj ET"
			reads(t, page("4 0 R", stream("", []byte(content)), stream("", nil), stream("", nil)), said, "Signed in Boston")
		})
	}
}

// A picture's codec is never decoded: an image stream is left as it was, and a
// stream that claims a picture codec where the page's text belongs is read as
// empty, so neither costs the rest of the document.
func TestPicturesAndUnknownFiltersDoNotFailTheFile(t *testing.T) {
	jpeg := []byte("\xff\xd8\xff\xe0 not really a jpeg \xff\xd9")
	image := stream("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter [/ASCII85Decode /DCTDecode]", a85(jpeg))
	doc := page("[4 0 R 6 0 R 8 0 R]",
		stream("/Filter /ASCII85Decode", a85(text(said))),
		stream("/Filter /DCTDecode", jpeg),
		image,
		stream("/Filter /NoSuchDecode", []byte("garbage")),
	)
	reads(t, doc, said)
}

// xrefStream writes the page tree into an object stream stored under objstm
// (filter entries and encoder) and indexes it with a cross-reference stream
// predicted the way producers write one (PNG Up), which is the shape most PDFs
// written since 2003 take. contents is object 4, top level.
func xrefStream(contents string, objstm ...any) []byte {
	inner := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
	}
	nums := []int{1, 2, 3, 5}
	inner = append(inner, helvetica)
	var head, body bytes.Buffer
	for i, o := range inner {
		fmt.Fprintf(&head, "%d %d ", nums[i], body.Len())
		body.WriteString(o)
		body.WriteByte('\n')
	}
	filter, encode := "/Filter /FlateDecode", zip
	if len(objstm) == 2 {
		filter, encode = objstm[0].(string), objstm[1].(func([]byte) []byte)
	}
	inside := append(head.Bytes(), body.Bytes()...)
	var b bytes.Buffer
	b.WriteString("%PDF-1.5\n")
	at4 := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n%s\nendobj\n", contents)
	at6 := b.Len()
	fmt.Fprintf(&b, "6 0 obj\n%s\nendobj\n", stream(fmt.Sprintf("/Type /ObjStm /N 4 /First %d %s", head.Len(), filter), encode(inside)))
	at7 := b.Len()
	var rows bytes.Buffer
	row := func(kind byte, a, c int) {
		rows.Write([]byte{kind, byte(a >> 24), byte(a >> 16), byte(a >> 8), byte(a), byte(c >> 8), byte(c)})
	}
	row(0, 0, 65535)
	row(2, 6, 0)
	row(2, 6, 1)
	row(2, 6, 2)
	row(1, at4, 0)
	row(2, 6, 3)
	row(1, at6, 0)
	row(1, at7, 0)
	xref := zip(pngUp(rows.Bytes(), 7))
	fmt.Fprintf(&b, "7 0 obj\n%s\nendobj\n", stream("/Type /XRef /Size 8 /W [1 4 2] /Root 1 0 R /Filter /FlateDecode /DecodeParms << /Predictor 12 /Columns 7 >>", xref))
	fmt.Fprintf(&b, "startxref\n%d\n%%%%EOF\n", at7)
	return b.Bytes()
}

func TestACrossReferenceStreamDocumentReads(t *testing.T) {
	reads(t, xrefStream(stream("", text(said))), said) // the control: nothing to decode
	reads(t, xrefStream(stream("/Filter /ASCII85Decode", a85(text(said)))), said)
	// The page tree itself behind a filter the reader cannot decode.
	reads(t, xrefStream(stream("", text(said)), "/Filter [/ASCII85Decode /FlateDecode]",
		func(b []byte) []byte { return a85(zip(b)) }), said)
}

// update appends an incremental update replacing object 4 with body.
func update(doc []byte, body string) []byte {
	tail := doc[bytes.LastIndex(doc, []byte("startxref"))+len("startxref"):]
	at, _ := strconv.Atoi(strings.Fields(string(tail))[0])
	var b bytes.Buffer
	b.Write(doc)
	o := b.Len()
	fmt.Fprintf(&b, "4 0 obj\n%s\nendobj\n", body)
	x := b.Len()
	fmt.Fprintf(&b, "xref\n4 1\n%010d 00000 n \ntrailer\n<< /Size 8 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", o, at, x)
	return b.Bytes()
}

// The newest definition of an object is the one read, so a document edited by
// appending (every signer and annotator does) reads its latest page.
func TestAnIncrementalUpdateIsReadAtItsLatest(t *testing.T) {
	doc := page("4 0 R", stream("", text("An earlier draft")), stream("", nil), stream("", nil))
	doc = update(doc, stream("/Filter /ASCII85Decode", a85(text(said))))
	got, err := Lines(doc, 10, 100)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	if all := strings.Join(got, "\n"); !strings.Contains(all, said) || strings.Contains(all, "earlier draft") {
		t.Fatalf("Lines = %q, want the updated page alone", all)
	}
}
