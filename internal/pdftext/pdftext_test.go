package pdftext

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// onePage builds a one-page PDF drawing each line in Helvetica, with glyph
// widths and a correct cross-reference table, so the reader is exercised on a
// real document rather than on lines handed to it. The widths matter: the
// reader drops a space glyph and finds the word break in the gap it leaves.
func onePage(lines ...string) []byte {
	var stream strings.Builder
	stream.WriteString("BT /F1 12 Tf 72 720 Td 14 TL\n")
	for _, l := range lines {
		fmt.Fprintf(&stream, "(%s) Tj T*\n", l)
	}
	stream.WriteString("ET")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", stream.Len(), stream.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /FirstChar 32 /LastChar 126 /Widths [" +
			strings.Repeat("500 ", 95) + "] >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestLinesReadsWhatThePageDraws(t *testing.T) {
	doc := onePage("The notice period is ninety days", "Signed in Boston")
	if !Looks(doc) {
		t.Fatal("a PDF does not look like one")
	}
	lines, err := Lines(doc, 10, 100)
	if err != nil {
		t.Fatalf("Lines: %v", err)
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"The notice period is ninety days", "Signed in Boston"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Lines = %q, want it to hold %q", got, want)
		}
	}
	if strings.Index(got, "notice") > strings.Index(got, "Boston") {
		t.Fatalf("Lines = %q: the top of the page must come first", got)
	}
}

func TestLinesRefusesWhatItCannotBound(t *testing.T) {
	doc := onePage("a", "b", "c")
	if _, err := Lines(doc, 10, 1); err == nil {
		t.Fatal("a page past the line limit was read")
	}
	if _, err := Lines([]byte("%PDF-1.4 not really"), 10, 10); err == nil {
		t.Fatal("bytes that are not a PDF were read")
	}
	empty := onePage()
	if _, err := Lines(empty, 10, 10); !errors.Is(err, ErrNoText) {
		t.Fatalf("a page with no text: err = %v, want ErrNoText", err)
	}
	if Looks([]byte("hello")) {
		t.Fatal("text looks like a PDF")
	}
}
