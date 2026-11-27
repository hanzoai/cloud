package pdftext

import (
	"bytes"
	"compress/lzw"
	"errors"
	"io"
	"math/rand/v2"
	"strings"
	"testing"

	tifflzw "golang.org/x/image/tiff/lzw"
)

// prose is long enough to carry LZW past every code width and through a table
// clear, and repetitive enough to build long table entries.
func prose(n int) []byte {
	words := strings.Fields("the notice period is ninety days and the parties agree that signed in boston " +
		"under the laws of massachusetts each party shall bear its own costs indemnification capped")
	r := rand.New(rand.NewPCG(1, 2))
	var b bytes.Buffer
	for b.Len() < n {
		b.WriteString(words[r.IntN(len(words))])
		b.WriteByte(" \n,."[r.IntN(4)])
	}
	return b.Bytes()[:n]
}

// The specification's own example (§7.4.4.2, Example 2): "-----A---B" encodes
// to 80 0B 60 50 22 0C 0C 85 01.
func TestLZWDecodesTheSpecificationsExample(t *testing.T) {
	got, err := unlzw([]byte{0x80, 0x0B, 0x60, 0x50, 0x22, 0x0C, 0x0C, 0x85, 0x01}, 1, 1<<20)
	if err != nil || string(got) != "-----A---B" {
		t.Fatalf("unlzw = %q, %v; want %q", got, err, "-----A---B")
	}
}

// compress/lzw is PDF's LZW with EarlyChange 0 and x/image/tiff/lzw is TIFF's,
// which is EarlyChange 1, so between them they check both widths against
// implementations nobody here wrote — and check the test's encoder too.
func TestLZWAgreesWithIndependentImplementations(t *testing.T) {
	for _, n := range []int{1, 300, 5000, 60000} {
		data := prose(n)

		var w bytes.Buffer
		zw := lzw.NewWriter(&w, lzw.MSB, 8)
		_, _ = zw.Write(data)
		_ = zw.Close()
		if got, err := unlzw(w.Bytes(), 0, 1<<24); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: unlzw(compress/lzw, early 0) = %d bytes, %v", n, len(got), err)
		}

		mine := lzwEncode(data, 0)
		ref, err := io.ReadAll(lzw.NewReader(bytes.NewReader(mine), lzw.MSB, 8))
		if err != nil || !bytes.Equal(ref, data) {
			t.Fatalf("%d bytes: compress/lzw cannot read the test's early-0 encoding: %v", n, err)
		}

		mine = lzwEncode(data, 1)
		ref, err = io.ReadAll(tifflzw.NewReader(bytes.NewReader(mine), tifflzw.MSB, 8))
		if err != nil || !bytes.Equal(ref, data) {
			t.Fatalf("%d bytes: tiff/lzw cannot read the test's early-1 encoding: %v", n, err)
		}
		if got, err := unlzw(mine, 1, 1<<24); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%d bytes: unlzw(early 1) = %d bytes, %v", n, len(got), err)
		}
	}
}

func TestASCII85(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"<~87cURD]i,\"Ebo80~>", "Hello World!"},
		{"  <~87cURD]i,\n\"Ebo80~>trailing", "Hello World!"},
		{"87cUR D]i,\"Eb o80~>", "Hello World!"}, // no opener, spaces inside
		{"z~>", "\x00\x00\x00\x00"},
		{"<~z87cURD]i,\"Ebo80", "\x00\x00\x00\x00Hello World!"}, // unterminated: read to the end
	} {
		got, err := un85([]byte(c.in), 1<<20)
		if err != nil || string(got) != c.want {
			t.Errorf("un85(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestASCIIHex(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"48656C6C6F>", "Hello"},
		{"48 65 6c\n6C 6F>ignored", "Hello"},
		{"4865 6>", "He`"}, // an odd last digit is followed by an implied 0
		{"486", "H`"},      // no closing >: read to the end
	} {
		got, err := unhex([]byte(c.in), 1<<20)
		if err != nil || string(got) != c.want {
			t.Errorf("unhex(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := unhex([]byte("48zz>"), 1<<20); err == nil {
		t.Error("unhex accepted a byte that is not hex")
	}
}

func TestRunLength(t *testing.T) {
	data := []byte("aaaaaaaaaaaaaaaabcdefggggggggg" + strings.Repeat("x", 300) + "end")
	got, err := unrun(rle(data), 1<<20)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("unrun = %q, %v", got, err)
	}
	// Hand-made: two literals, a run of four, the end, and bytes past it ignored.
	got, err = unrun([]byte{1, 'h', 'i', 253, 'z', 128, 'X'}, 1<<20)
	if err != nil || string(got) != "hizzzz" {
		t.Fatalf("unrun = %q, %v; want %q", got, err, "hizzzz")
	}
}

// Every PNG row filter, at one, two and three bytes per pixel, and a last row
// cut short.
func TestPNGPredictor(t *testing.T) {
	data := prose(1001)
	for _, c := range []struct{ columns, colors, bpc int }{{8, 1, 8}, {5, 2, 8}, {7, 3, 8}, {4, 1, 16}} {
		enc := pngEncode(data, c.columns, c.colors, c.bpc)
		row := (c.colors*c.bpc*c.columns + 7) / 8
		got, err := unpng(enc, row, max(1, (c.colors*c.bpc+7)/8), 1<<20)
		if err != nil || !bytes.HasPrefix(got, data) {
			t.Fatalf("%+v: unpng lost the data: %v", c, err)
		}
	}
}

func TestTIFFPredictor(t *testing.T) {
	data := prose(997)
	for _, c := range []struct{ columns, colors, bpc int }{{16, 1, 8}, {5, 3, 8}, {8, 1, 16}, {12, 1, 4}, {9, 2, 2}, {31, 1, 1}} {
		row := (c.colors*c.bpc*c.columns + 7) / 8
		got := untiff(tiffEncode(data, c.columns, c.colors, c.bpc), row, c.colors, c.bpc)
		if !bytes.HasPrefix(got, data) {
			t.Fatalf("%+v: untiff lost the data", c)
		}
	}
}

// Each decoder stops at its limit rather than inflating a bomb.
func TestDecodersRefusePastTheirLimit(t *testing.T) {
	big := bytes.Repeat([]byte("a"), 1<<16)
	for name, run := range map[string]func() error{
		"flate": func() error { _, err := inflate(zip(big), 1000); return err },
		"lzw":   func() error { _, err := unlzw(lzwEncode(big, 1), 1, 1000); return err },
		"rl":    func() error { _, err := unrun(rle(big), 1000); return err },
		"a85":   func() error { _, err := un85(bytes.Repeat([]byte("z"), 1000), 1000); return err },
		"hex":   func() error { _, err := unhex(bytes.Repeat([]byte("41"), 2000), 1000); return err },
		"chain": func() error {
			_, err := decode([]step{{name: "ASCII85Decode"}, {name: "FlateDecode"}}, a85(zip(big)), 1000)
			return err
		},
	} {
		if err := run(); !errors.Is(err, errTooBig) {
			t.Errorf("%s: err = %v, want errTooBig", name, err)
		}
	}
}

// A picture codec is reported, not decoded, and an unknown filter is an error;
// a damaged stream yields what decoded before the damage.
func TestDecodeReportsWhatItCannotRead(t *testing.T) {
	if _, err := decode([]step{{name: "DCTDecode"}}, []byte{1}, 100); !errors.Is(err, errPicture) {
		t.Errorf("DCTDecode: err = %v, want errPicture", err)
	}
	if _, err := decode([]step{{name: "NoSuchDecode"}}, []byte{1}, 100); err == nil {
		t.Error("an unknown filter decoded")
	}
	z := zip(prose(4000))
	got, err := decode([]step{{name: "FlateDecode"}}, z[:len(z)/2], 1<<20)
	if err == nil || len(got) == 0 {
		t.Errorf("a truncated Flate stream: %d bytes, err %v; want a partial read and an error", len(got), err)
	}
}

// A document that needs nothing rewritten is handed back as it came — the same
// bytes, not a copy — so every PDF that read before reads exactly as it did.
func TestAReadableDocumentIsUntouched(t *testing.T) {
	for _, doc := range [][]byte{
		onePage(said),
		page("4 0 R", stream("/Filter /FlateDecode", zip(text(said)))),
		xrefStream(stream("/Filter /FlateDecode", zip(text(said)))),
	} {
		got, err := plain(doc)
		if err != nil || len(got) != len(doc) || &got[0] != &doc[0] {
			t.Fatalf("plain rewrote a readable document (%d → %d bytes, err %v)", len(doc), len(got), err)
		}
	}
}

// An encrypted document is the reader's to decrypt, so plain leaves it alone.
func TestAnEncryptedDocumentIsLeftAlone(t *testing.T) {
	doc := page("4 0 R", stream("/Filter /ASCII85Decode", a85(text(said))))
	doc = bytes.Replace(doc, []byte("/Root 1 0 R"), []byte("/Root 1 0 R /Encrypt << /Filter /Standard >>"), 1)
	if got, err := plain(doc); err != nil || &got[0] != &doc[0] {
		t.Fatalf("plain touched an encrypted document: %v", err)
	}
}
