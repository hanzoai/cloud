package pdftext

// plain.go hands rsc.io/pdf a document it can read. That reader decodes one
// filter — FlateDecode, with the PNG Up predictor at most — reads a page's
// /Contents only when it is a single stream, and lexes a content stream with no
// notion of an inline image, so a PDF whose text sits behind ASCII85, LZW or
// RunLength, a page drawn from an array of streams, or a page carrying an inline
// image with a '>' in its data fails as a whole file.
//
// The streams read are a page's content, the object streams holding the page
// tree, and the CMaps fonts carry (face.go reads them through the same reader; no
// XObject is read). So before the reader sees the file, plain decodes those, joins
// a page's array of content streams into one, lifts inline images out of content
// streams, and writes the widths of a standard font its document left without
// them, then appends the rewritten objects as an incremental update (PDF 32000-1
// §7.5.6): a new cross-reference section naming them, whose /Prev is the
// document's own. Nothing before the update is moved, so every offset the
// original cross-reference holds stays true, and a document that needs none of
// this is returned as it came. A content stream whose filter is a picture codec or
// unknown is stored empty, so the rest of the page and the document still read.

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

// plainLimit bounds the bytes plain decodes from one document, the same bound a
// knowledge file is read whole under.
const plainLimit = 256 << 20

// object is one indirect object as it was found: its value, read from src — the
// file, or the decoded object stream it sits in — and, for a stream, its data.
type object struct {
	num, gen int64
	src      []byte
	val      value
	stream   bool
	body     [2]int // the stream's data in src
	at       int    // where it sits in the file: a later definition replaces an earlier one
}

func (o *object) dict() *dict {
	if o == nil || o.val.kind != kDict {
		return nil
	}
	return o.val.d
}

// doc is what plain read: every object by number and the newest trailer.
type doc struct {
	data    []byte
	objs    map[int64]*object
	trailer *dict
	xref    int  // the document's own startxref
	stream  bool // its newest cross-reference section is a stream
	budget  int
}

// plain answers data with every stream rsc.io/pdf would read and cannot decode
// stored plainly; see the file comment. A document it cannot parse is returned
// unchanged — this never makes a file less readable than it was — and the only
// error is one that decodes past plainLimit.
func plain(data []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = data, nil
		}
	}()
	d := scan(data)
	if d == nil {
		return data, nil
	}
	edits, err := d.edits()
	if err != nil {
		return nil, err
	}
	if len(edits) == 0 {
		return data, nil
	}
	return d.update(edits), nil
}

// scan reads the document's objects and its newest trailer. nil is a document
// plain will not touch: one it cannot parse, or one that is encrypted (its
// streams decode only under the document's key, which rsc.io/pdf holds and plain
// does not).
func scan(data []byte) *doc {
	d := &doc{data: data, objs: map[int64]*object{}, budget: plainLimit}
	if !d.readTrailer() {
		return nil
	}
	if _, enc := d.trailer.get("Encrypt"); enc {
		return nil
	}
	for pos := 0; pos < len(data); {
		i := bytes.Index(data[pos:], []byte("obj"))
		if i < 0 {
			break
		}
		i += pos
		num, gen, start, ok := header(data, i)
		if !ok {
			pos = i + 3
			continue
		}
		l := lexer{b: data, pos: i + 3}
		v, err := l.value()
		if err != nil {
			pos = i + 3
			continue
		}
		o := &object{num: num, gen: gen, src: data, val: v, at: start}
		pos = l.pos
		if v.kind == kDict {
			l.white()
			if bytes.HasPrefix(data[l.pos:], []byte("stream")) {
				lo := eol(data, l.pos+len("stream"))
				hi := streamEnd(data, lo, v.d)
				if hi < 0 {
					pos = i + 3
					continue
				}
				o.stream, o.body = true, [2]int{lo, hi}
				pos = hi
			}
		}
		d.objs[num] = o
	}
	d.expand()
	return d
}

// header reads the "num gen" before the obj keyword at i, answering where the
// object begins. The keyword must be its own token: endobj is not obj.
func header(b []byte, i int) (num, gen int64, start int, ok bool) {
	if i+3 < len(b) && !isWhite(b[i+3]) && !isDelim(b[i+3]) {
		return 0, 0, 0, false
	}
	j := i
	read := func() (int64, bool) {
		end := j
		for j > 0 && isWhite(b[j-1]) {
			j--
		}
		if j == end {
			return 0, false
		}
		end = j
		for j > 0 && isDigit(b[j-1]) && end-j < 10 {
			j--
		}
		if j == end {
			return 0, false
		}
		n, err := strconv.ParseInt(string(b[j:end]), 10, 64)
		return n, err == nil
	}
	if gen, ok = read(); !ok {
		return
	}
	if num, ok = read(); !ok {
		return
	}
	if j > 0 && !isWhite(b[j-1]) && !isDelim(b[j-1]) {
		return 0, 0, 0, false
	}
	return num, gen, j, true
}

// eol skips the end of line that follows the stream keyword (§7.3.8.1).
func eol(b []byte, i int) int {
	switch {
	case i+1 < len(b) && b[i] == '\r' && b[i+1] == '\n':
		return i + 2
	case i < len(b) && (b[i] == '\n' || b[i] == '\r'):
		return i + 1
	}
	return i
}

// streamEnd answers where a stream's data ends: its direct /Length when that lands
// on endstream, else the endstream keyword itself, less the end of line before it.
func streamEnd(b []byte, lo int, d *dict) int {
	if n, ok := d.int("Length"); ok && n >= 0 && lo+int(n) <= len(b) {
		l := lexer{b: b, pos: lo + int(n)}
		l.white()
		if bytes.HasPrefix(b[l.pos:], []byte("endstream")) {
			return lo + int(n)
		}
	}
	i := bytes.Index(b[lo:], []byte("endstream"))
	if i < 0 {
		return -1
	}
	hi := lo + i
	if hi > lo && b[hi-1] == '\n' {
		hi--
	}
	if hi > lo && b[hi-1] == '\r' {
		hi--
	}
	return hi
}

// readTrailer finds the newest cross-reference section through the last
// startxref and reads the trailer it carries: the dictionary after a table, or
// the cross-reference stream's own.
func (d *doc) readTrailer() bool {
	b := d.data
	tail := max(0, len(b)-1024)
	i := bytes.LastIndex(b[tail:], []byte("startxref"))
	if i < 0 {
		return false
	}
	l := lexer{b: b, pos: tail + i + len("startxref")}
	v, err := l.value()
	if err != nil || v.kind != kInt || v.i < 0 || int(v.i) >= len(b) {
		return false
	}
	d.xref = int(v.i)
	l = lexer{b: b, pos: d.xref}
	l.white()
	if bytes.HasPrefix(b[l.pos:], []byte("xref")) {
		j := bytes.Index(b[l.pos:], []byte("trailer"))
		if j < 0 {
			return false
		}
		l.pos += j + len("trailer")
		t, err := l.value()
		if err != nil || t.kind != kDict {
			return false
		}
		d.trailer = t.d
		return true
	}
	for range 3 { // num gen obj
		if _, err := l.value(); err != nil {
			return false
		}
	}
	t, err := l.value()
	if err != nil || t.kind != kDict {
		return false
	}
	d.trailer, d.stream = t.d, true
	return true
}

// expand reads the objects held in object streams (§7.5.7). One defined in an
// object stream replaces an earlier definition and yields to a later one, by
// where the object stream sits in the file.
func (d *doc) expand() {
	var streams []*object
	for _, o := range d.objs {
		if t, _ := o.dict().name("Type"); o.stream && t == "ObjStm" {
			streams = append(streams, o)
		}
	}
	for _, os := range streams {
		data, err := d.decode(os)
		if data == nil || errors.Is(err, errTooBig) {
			continue
		}
		n, _ := os.dict().int("N")
		first, _ := os.dict().int("First")
		l := lexer{b: data}
		type entry struct{ num, off int64 }
		var entries []entry
		for range min(n, 1<<20) {
			a, err1 := l.value()
			b, err2 := l.value()
			if err1 != nil || err2 != nil || a.kind != kInt || b.kind != kInt {
				break
			}
			entries = append(entries, entry{a.i, b.i})
		}
		for _, e := range entries {
			at := int(first + e.off)
			if at < 0 || at >= len(data) {
				continue
			}
			ol := lexer{b: data, pos: at}
			v, err := ol.value()
			if err != nil {
				continue
			}
			if old := d.objs[e.num]; old != nil && old.at > os.at {
				continue
			}
			d.objs[e.num] = &object{num: e.num, src: data, val: v, at: os.at}
		}
	}
}

// resolve follows a reference to the value it names.
func (d *doc) resolve(v value) value {
	for range 8 {
		if v.kind != kRef {
			return v
		}
		o := d.objs[v.i]
		if o == nil {
			return value{}
		}
		v = o.val
	}
	return value{}
}

// chain is a stream's filters in the order they apply, each with its parameters.
func (d *doc) chain(sd *dict) []step {
	f, _ := sd.get("Filter")
	p, _ := sd.get("DecodeParms")
	f, p = d.resolve(f), d.resolve(p)
	parms := func(v value) *dict {
		if v = d.resolve(v); v.kind == kDict {
			return v.d
		}
		return nil
	}
	switch f.kind {
	case kName:
		return []step{{name: f.s, parms: parms(p)}}
	case kArray:
		out := make([]step, 0, len(f.a))
		for i, n := range f.a {
			n = d.resolve(n)
			if n.kind != kName {
				return []step{{name: "?"}}
			}
			s := step{name: n.s}
			if p.kind == kArray && i < len(p.a) {
				s.parms = parms(p.a[i])
			}
			out = append(out, s)
		}
		return out
	}
	return nil
}

// readable reports whether rsc.io/pdf decodes a chain itself: none, or a lone
// FlateDecode with no predictor. Its PNG predictor reads only the Up filter and
// fails a stream whose rows name any other, so a predicted stream is decoded here.
func readable(chain []step) bool {
	switch len(chain) {
	case 0:
		return true
	case 1:
		p, ok := chain[0].parms.int("Predictor")
		return chain[0].name == "FlateDecode" && (!ok || p <= 1)
	}
	return false
}

// decode runs one stream through its chain, against the document's budget.
func (d *doc) decode(o *object) ([]byte, error) {
	out, err := decode(d.chain(o.dict()), o.src[o.body[0]:o.body[1]], d.budget)
	if errors.Is(err, errTooBig) {
		return nil, err
	}
	d.budget -= len(out)
	if d.budget < 0 {
		return nil, errTooBig
	}
	return out, err
}

// edit is one object the update writes: a stream with its new data, or a page
// whose /Contents names a new stream.
type edit struct {
	num, gen int64
	text     []byte // the object, between "obj" and "endobj"
}

// edits decides what the update rewrites: each page's content made one readable
// stream, each object stream the reader cannot decode stored plainly, and each
// standard font written without widths given its own (widths.go). Those streams
// are the only ones rsc.io/pdf reads besides the cross-reference stream, which it
// reads before anything here could stand in for it.
func (d *doc) edits() ([]edit, error) {
	var out []edit
	// checked is the streams already judged as a page's whole content, so one
	// shared by many pages is decoded once. A page drawn from several streams
	// reads its parts into a new stream of its own and leaves them as they are.
	checked := map[int64]bool{}
	next := d.free()
	blank := int64(-1) // the one empty stream every page with nothing to draw names
	for _, num := range d.pages() {
		pg := d.objs[num]
		refs, many := d.contents(pg)
		if refs == nil {
			// A page with no /Contents is a blank page (§7.7.3.3), and the reader
			// fails the whole document on it; it is given an empty stream.
			if blank < 0 {
				blank = next
				next++
				out = append(out, edit{num: blank, text: plainStream(nil, nil)})
			}
			out = append(out, edit{num: pg.num, gen: pg.gen, text: d.repoint(pg, blank)})
			continue
		}
		if !many && checked[refs[0]] {
			continue
		}
		var parts [][]byte
		for _, r := range refs {
			o := d.objs[r]
			if o == nil || !o.stream {
				continue // a missing part draws nothing; the rest of the page still does
			}
			text, moved, err := d.content(o, many)
			if err != nil {
				return nil, err
			}
			parts = append(parts, text)
			if !many {
				checked[r] = true
				if moved {
					out = append(out, d.restream(o, text))
				}
			}
		}
		if many {
			out = append(out, edit{num: next, text: plainStream(nil, bytes.Join(parts, []byte("\n")))})
			out = append(out, edit{num: pg.num, gen: pg.gen, text: d.repoint(pg, next)})
			next++
		}
	}
	nums := make([]int64, 0, len(d.objs))
	for n := range d.objs {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	cmaps := d.cmaps()
	for _, n := range nums {
		o := d.objs[n]
		if name, ok := standard(o.dict()); ok && !o.stream {
			out = append(out, widen(o, name))
			continue
		}
		if cmaps[n] && o.stream && !readable(d.chain(o.dict())) {
			text, err := d.decode(o)
			if errors.Is(err, errTooBig) {
				return nil, err
			}
			if err == nil {
				out = append(out, d.restream(o, text))
			}
			continue
		}
		if t, _ := o.dict().name("Type"); !o.stream || t != "ObjStm" || readable(d.chain(o.dict())) {
			continue
		}
		text, err := d.decode(o)
		if errors.Is(err, errTooBig) {
			return nil, err
		}
		out = append(out, d.restream(o, text))
	}
	return out, nil
}

// cmaps is the streams fonts read as CMaps: every /ToUnicode, and a composite
// font's /Encoding when it names a stream. A font may sit directly in a resource
// dictionary, so every value is walked, not only each object's own.
func (d *doc) cmaps() map[int64]bool {
	out := map[int64]bool{}
	var walk func(v value, depth int)
	walk = func(v value, depth int) {
		if depth > maxDepth {
			return
		}
		switch v.kind {
		case kArray:
			for _, e := range v.a {
				walk(e, depth+1)
			}
		case kDict:
			if u, ok := v.d.get("ToUnicode"); ok && u.kind == kRef {
				out[u.i] = true
			}
			if s, _ := v.d.name("Subtype"); s == "Type0" {
				if e, ok := v.d.get("Encoding"); ok && e.kind == kRef {
					out[e.i] = true
				}
			}
			for _, e := range v.d.vals {
				walk(e, depth+1)
			}
		}
	}
	for _, o := range d.objs {
		walk(o.val, 0)
	}
	return out
}

// content is one content stream's text, decoded and with its inline images
// lifted out, and whether that differs from what rsc.io/pdf would read itself.
// Only text the update keeps is charged to the budget: a readable stream is
// decoded here to look for inline images, and one with none is left to the
// reader, so a long document is not refused for text it never rewrote.
func (d *doc) content(o *object, kept bool) ([]byte, bool, error) {
	chain := d.chain(o.dict())
	text, err := d.decode(o)
	if errors.Is(err, errTooBig) {
		return nil, false, err
	}
	text, lifted := lift(text)
	moved := lifted || err != nil || !readable(chain)
	if !moved && !kept {
		d.budget += len(text)
	}
	return text, moved, nil
}

// pages walks the page tree from the catalog and answers each page's number.
func (d *doc) pages() []int64 {
	root, _ := d.trailer.get("Root")
	cat := d.resolve(root)
	if cat.kind != kDict {
		return nil
	}
	top, _ := cat.d.get("Pages")
	var out []int64
	seen := map[int64]bool{}
	var walk func(v value, depth int)
	walk = func(v value, depth int) {
		if v.kind != kRef || seen[v.i] || depth > 64 || len(out) > 1<<20 {
			return
		}
		seen[v.i] = true
		n := d.resolve(v)
		if n.kind != kDict {
			return
		}
		if kids, ok := n.d.get("Kids"); ok {
			if k := d.resolve(kids); k.kind == kArray {
				for _, kid := range k.a {
					walk(kid, depth+1)
				}
				return
			}
		}
		if t, _ := n.d.name("Type"); t == "Pages" {
			return
		}
		out = append(out, v.i)
	}
	walk(top, 0)
	return out
}

// contents answers the streams a page draws, and whether they are several: a
// /Contents that names an array (directly, or through a reference) is drawn as
// their concatenation (§7.7.3.3). nil is a page that draws nothing: no /Contents,
// or one naming no stream.
func (d *doc) contents(pg *object) ([]int64, bool) {
	c, _ := pg.dict().get("Contents")
	if c.kind == kRef {
		if o := d.objs[c.i]; o != nil && o.stream {
			return []int64{c.i}, false
		}
	}
	a := d.resolve(c)
	if a.kind != kArray {
		return nil, false
	}
	refs := []int64{}
	for _, v := range a.a {
		if v.kind == kRef {
			refs = append(refs, v.i)
		}
	}
	return refs, true
}

// free is the first object number the update may take.
func (d *doc) free() int64 {
	next, _ := d.trailer.int("Size")
	for n := range d.objs {
		next = max(next, n+1)
	}
	return next
}

// plainStream writes a stream object's text: its dictionary — entries copied
// from src, less the ones that described the old data — and the new data.
func plainStream(entries []byte, data []byte) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "<<%s /Length %d >>\nstream\n", entries, len(data))
	b.Write(data)
	b.WriteString("\nendstream")
	return b.Bytes()
}

// stale are the stream entries that describe how the data was stored, not what
// it is, and so do not survive storing it plainly.
var stale = map[string]bool{
	"Length": true, "Filter": true, "DecodeParms": true, "DL": true,
}

// restream is a stream rewritten with new data and the rest of its dictionary
// as it was written.
func (d *doc) restream(o *object, data []byte) edit {
	var b bytes.Buffer
	sd := o.dict()
	for i, n := range sd.names {
		if stale[n] {
			continue
		}
		b.WriteByte(' ')
		b.Write(o.src[sd.key[i][0]:sd.key[i][1]])
		b.WriteByte(' ')
		b.Write(o.src[sd.vals[i].lo:sd.vals[i].hi])
	}
	return edit{num: o.num, gen: o.gen, text: plainStream(b.Bytes(), data)}
}

// repoint is a page rewritten to draw one stream, num.
func (d *doc) repoint(pg *object, num int64) []byte {
	var b bytes.Buffer
	b.WriteString("<<")
	pd := pg.dict()
	for i, n := range pd.names {
		if n == "Contents" {
			continue
		}
		b.WriteByte(' ')
		b.Write(pg.src[pd.key[i][0]:pd.key[i][1]])
		b.WriteByte(' ')
		b.Write(pg.src[pd.vals[i].lo:pd.vals[i].hi])
	}
	fmt.Fprintf(&b, " /Contents %d 0 R >>", num)
	return b.Bytes()
}

// update appends the edits as an incremental update: the objects, then a
// cross-reference section of the document's own kind naming them, whose /Prev is
// the section the document ended with.
func (d *doc) update(edits []edit) []byte {
	sort.Slice(edits, func(i, j int) bool { return edits[i].num < edits[j].num })
	var b bytes.Buffer
	b.Grow(len(d.data) + 4096)
	b.Write(d.data)
	if n := len(d.data); n > 0 && d.data[n-1] != '\n' && d.data[n-1] != '\r' {
		b.WriteByte('\n')
	}
	type entry struct {
		num, gen int64
		off      int
	}
	var table []entry
	for _, e := range edits {
		table = append(table, entry{e.num, e.gen, b.Len()})
		fmt.Fprintf(&b, "%d %d obj\n", e.num, e.gen)
		b.Write(e.text)
		b.WriteString("\nendobj\n")
	}
	size := d.free()
	for _, e := range edits {
		size = max(size, e.num+1)
	}
	keep := d.carried()
	if !d.stream {
		at := b.Len()
		b.WriteString("xref\n")
		for _, e := range table {
			fmt.Fprintf(&b, "%d 1\n%010d %05d n \n", e.num, e.off, e.gen)
		}
		fmt.Fprintf(&b, "trailer\n<< /Size %d /Prev %d%s >>\nstartxref\n%d\n%%%%EOF\n", size, d.xref, keep, at)
		return b.Bytes()
	}
	// A cross-reference stream names itself too (§7.5.8.1): type 1 entries of
	// eight-byte offsets and two-byte generations, in ascending order.
	self := size
	at := b.Len()
	table = append(table, entry{self, 0, at})
	var rows bytes.Buffer
	var index bytes.Buffer
	for _, e := range table {
		rows.WriteByte(1)
		for s := 56; s >= 0; s -= 8 {
			rows.WriteByte(byte(uint64(e.off) >> s))
		}
		rows.WriteByte(byte(e.gen >> 8))
		rows.WriteByte(byte(e.gen))
		fmt.Fprintf(&index, " %d 1", e.num)
	}
	fmt.Fprintf(&b, "%d 0 obj\n<< /Type /XRef /Size %d /W [1 8 2] /Index [%s ] /Prev %d%s /Length %d >>\nstream\n",
		self, self+1, index.Bytes(), d.xref, keep, rows.Len())
	b.Write(rows.Bytes())
	fmt.Fprintf(&b, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", at)
	return b.Bytes()
}

// carried is the trailer entries an update restates, written as they were: the
// catalog, the document information and the file identifier.
func (d *doc) carried() string {
	var b bytes.Buffer
	for i, n := range d.trailer.names {
		if n != "Root" && n != "Info" && n != "ID" {
			continue
		}
		b.WriteByte(' ')
		b.Write(d.data[d.trailer.key[i][0]:d.trailer.key[i][1]])
		b.WriteByte(' ')
		b.Write(d.data[d.trailer.vals[i].lo:d.trailer.vals[i].hi])
	}
	return b.String()
}

// lift removes the inline images from a content stream (§8.9.7): BI, the image's
// dictionary, ID, its data, EI. Their data is binary or encoded under filters the
// lexer does not know, and none of it is text. It answers whether it took any.
func lift(b []byte) ([]byte, bool) {
	var out []byte
	last := 0
	for i := 0; i < len(b); {
		switch c := b[i]; {
		case isWhite(c):
			i++
		case c == '%':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
		case c == '(':
			i, _ = stringEnd(b, i)
		case c == '<' && i+1 < len(b) && b[i+1] == '<', c == '>' && i+1 < len(b) && b[i+1] == '>':
			i += 2
		case c == '<':
			for i < len(b) && b[i] != '>' {
				i++
			}
			i++
		case isDelim(c):
			i++
		default:
			j := i
			for j < len(b) && !isWhite(b[j]) && !isDelim(b[j]) {
				j++
			}
			if string(b[i:j]) != "BI" {
				i = j
				continue
			}
			end := inlineEnd(b, j)
			out = append(out, b[last:i]...)
			out = append(out, ' ')
			last, i = end, end
		}
	}
	if out == nil {
		return b, false
	}
	return append(out, b[last:]...), true
}

// inlineEnd answers the index just past the EI that closes the inline image whose
// dictionary starts at i, or the end of the stream when there is none.
func inlineEnd(b []byte, i int) int {
	l := lexer{b: b, pos: i}
	var filter string
	length := -1
	var key string
	for {
		v, err := l.value()
		if err != nil {
			return len(b)
		}
		if v.kind == kKeyword && v.s == "ID" {
			break
		}
		if key == "" && v.kind == kName {
			key = v.s
			continue
		}
		switch key {
		case "F", "Filter":
			if v.kind == kName {
				filter = canonical(v.s)
			} else if v.kind == kArray && len(v.a) > 0 && v.a[0].kind == kName {
				filter = canonical(v.a[0].s)
			}
		case "L", "Length":
			if v.kind == kInt && v.i >= 0 {
				length = int(v.i)
			}
		}
		key = ""
	}
	at := l.pos
	if at < len(b) && isWhite(b[at]) {
		at++
	}
	switch {
	case length >= 0 && at+length <= len(b):
		at += length
	case filter == "ASCII85Decode":
		if k := bytes.Index(b[at:], []byte("~>")); k >= 0 {
			at += k + 2
		}
	case filter == "ASCIIHexDecode":
		if k := bytes.IndexByte(b[at:], '>'); k >= 0 {
			at += k + 1
		}
	}
	for k := at; k+1 < len(b); k++ {
		if b[k] == 'E' && b[k+1] == 'I' &&
			(k == at || isWhite(b[k-1])) &&
			(k+2 == len(b) || isWhite(b[k+2]) || isDelim(b[k+2])) {
			return k + 2
		}
	}
	return len(b)
}
