package pdftext

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"

	"rsc.io/pdf"
)

// ErrNoText is a PDF with nothing drawn as text: a scan with no text layer,
// or an empty document.
var ErrNoText = errors.New("pdf has no extractable text")

// Looks reports whether data is a PDF: the "%PDF-" magic that opens every PDF
// file, allowing for a stray leading byte or two some exporters prepend.
func Looks(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("%PDF-"))
}

// Row is one printed row: its text, the largest font size drawn on it, and the
// page it is on. The size is what a heading is told from body text by.
type Row struct {
	Text string
	Size float64
	Page int
}

// Lines renders a PDF into layout-ordered text lines, one per printed row.
func Lines(data []byte, maxPages, maxLines int) ([]string, error) {
	rows, err := Rows(data, maxPages, maxLines)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Text
	}
	return out, nil
}

// Rows renders a PDF into its printed rows in layout order, each with its font
// size and page.
//
// Every standard text filter is read (plain.go): ASCII85, ASCIIHex, LZW,
// RunLength and Flate with its predictors, alone or chained, as are a page drawn
// from several content streams and one carrying inline images. Each glyph's text
// is its font's /ToUnicode entry first (face.go), so subset and composite fonts —
// pdfTeX, Word, a browser's print, CJK — read as the characters they draw.
//
// The work is bounded: a small, highly compressed PDF is a decompression bomb,
// so the streams plain decodes stop at plainLimit, and a PDF over maxPages pages,
// or whose text runs past maxLines rows, is refused rather than read to the end.
func Rows(data []byte, maxPages, maxLines int) (out []Row, err error) {
	// rsc.io/pdf panics on malformed structure rather than returning an error;
	// a stored file is anything its owner uploaded, so a panic is an answer.
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, fmt.Errorf("pdf is malformed: %v", r)
		}
	}()
	if data, err = plain(data); err != nil {
		return nil, fmt.Errorf("pdf streams: %w", err)
	}
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	if n := r.NumPage(); n > maxPages {
		return nil, fmt.Errorf("pdf has %d pages, past the %d-page limit", n, maxPages)
	}
	var rows []Row
	fs := faces{}
	for i := 1; i <= r.NumPage(); i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		for _, row := range group(drawn(p, fs)) {
			row.Page = i
			rows = append(rows, row)
		}
		if len(rows) > maxLines {
			return nil, fmt.Errorf("pdf runs past the %d-line limit", maxLines)
		}
	}
	if len(rows) == 0 {
		return nil, ErrNoText
	}
	return rows, nil
}

// Entry is one bookmark of a PDF's outline: its depth (1 for the top level)
// and its title.
type Entry struct {
	Depth int
	Title string
}

// Outline answers a PDF's bookmarks in reading order, or none when it has no
// outline. It never fails a read: an outline is a hint about structure.
func Outline(data []byte) (out []Entry) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	var walk func(o pdf.Outline, depth int)
	walk = func(o pdf.Outline, depth int) {
		for _, c := range o.Child {
			if len(out) >= 4096 {
				return
			}
			if t := strings.TrimSpace(c.Title); t != "" {
				out = append(out, Entry{Depth: depth, Title: t})
			}
			walk(c, depth+1)
		}
	}
	walk(r.Outline(), 1)
	return out
}

// group turns positioned text fragments into printed rows. Fragments are
// bucketed into rows by Y (a 3pt tolerance absorbs baseline jitter within a
// line), rows are ordered top to bottom, and within a row fragments are ordered
// by X and joined — a space goes in only where the gap to the previous fragment
// exceeds a fifth of the font size, so the glyphs of one word stay together while
// separate words and columns stay apart. A fifth, because the narrowest standard
// space is a quarter: Times-Roman's 250/1000 em, which a quarter-em threshold met
// exactly and so joined every word set in it.
func group(texts []pdf.Text) []Row {
	if len(texts) == 0 {
		return nil
	}
	frags := make([]pdf.Text, len(texts))
	copy(frags, texts)
	sort.SliceStable(frags, func(i, j int) bool { return frags[i].Y > frags[j].Y })

	const yTol = 3.0
	var rows []Row
	row := []pdf.Text{frags[0]}
	rowY := frags[0].Y
	flush := func(r []pdf.Text) {
		sort.SliceStable(r, func(i, j int) bool { return r[i].X < r[j].X })
		var (
			b       strings.Builder
			prevEnd float64
			size    float64
		)
		for k, f := range r {
			gap := f.FontSize * 0.2
			if gap < 1 {
				gap = 1
			}
			if k > 0 && f.X-prevEnd > gap {
				b.WriteByte(' ')
			}
			b.WriteString(f.S)
			prevEnd = f.X + f.W
			size = max(size, f.FontSize)
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			rows = append(rows, Row{Text: s, Size: size})
		}
	}
	for _, f := range frags[1:] {
		if rowY-f.Y > yTol {
			flush(row)
			row, rowY = []pdf.Text{f}, f.Y
			continue
		}
		row = append(row, f)
	}
	flush(row)
	return rows
}
