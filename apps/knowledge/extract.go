package knowledge

import (
	azip "archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/internal/pdftext"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// extract.go reads the text AND the structure out of a stored file: the words,
// and where each heading starts and how deep it sits. Structure is read from
// what the format already says — HTML headings and landmarks, Markdown headings,
// a PDF's outline or its larger type, DOCX heading styles, a code file's
// declarations, a sheet's tabs — and never guessed where the format says
// nothing; a long run of text with no structure is cut into sections when the
// table of contents is built (toc.go). A picture is described by a vision model
// and a recording is transcribed by zen-scribe, on the org's own meter.

const (
	// segText is how much of a file's text one step of its ingest reads, parses
	// and writes (ingest.go): a file of any length is indexed a segment at a
	// time, each written before the next is read.
	segText = 4 << 20
	// maxFile bounds what an ingest holds in memory to read a file that must be
	// read whole (a PDF, a DOCX, a sheet, an HTML page, an archive), and the
	// text such a file is read for. A text file is read a segment at a time
	// and has no such bound.
	maxFile = 256 << 20
	// maxImage and maxAudio bound what is sent to the vision and speech models.
	maxImage = 10 << 20
	maxAudio = 25 << 20
	// maxPart bounds one inflated part of an Office file or one archive entry.
	maxPart = 64 << 20
	// pdfPages and pdfRows bound a PDF's walk.
	pdfPages, pdfRows = 4000, 1_000_000
	// sheetRows is how many rows of a table one section holds.
	sheetRows = 100
)

// errSkip is a file whose kind or size this ingest cannot read. It is stored and
// listed, and no retry changes the answer.
var errSkip = errors.New("not indexed")

// errGone is an object deleted before its ingest read it.
var errGone = errors.New("the object is gone from the store")

// parsed is a file read into text and structure.
type parsed struct {
	// title is the document's own title when it states one (an HTML <title>).
	title string
	text  string
	marks []mark
	refs  []ref
}

// mark is a heading: where its line starts in the text, how deep it sits, what
// it says, and the anchor a link reaches it by.
type mark struct {
	at     int
	level  int
	title  string
	anchor string
}

// ref is a link found in the text: where it is, and what it points at.
type ref struct {
	at     int
	target string
}

// fileKind is how a file is read.
type fileKind int

const (
	kindNone fileKind = iota
	kindText
	kindMarkdown
	kindHTML
	kindCode
	kindCSV
	kindXLSX
	kindPDF
	kindDOCX
	kindImage
	kindAudio
	kindZip
)

const (
	docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	xlsxType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
)

var codeExt = map[string]string{
	".go": "go", ".py": "py", ".pyi": "py", ".js": "js", ".mjs": "js", ".cjs": "js", ".jsx": "js",
	".ts": "js", ".tsx": "js", ".rs": "rs", ".java": "java", ".kt": "java", ".kts": "java",
	".cs": "java", ".swift": "java", ".scala": "java", ".php": "java", ".rb": "rb",
	".c": "c", ".h": "c", ".cc": "c", ".cpp": "c", ".hpp": "c", ".cxx": "c", ".sh": "sh",
	".bash": "sh", ".zsh": "sh", ".sol": "java", ".proto": "c", ".sql": "",
}

// byName is how a file would be read judging by its name and type alone —
// what an ingest asks before it reads, to read a text file only as far as it
// will be indexed.
func byName(name, typ string) fileKind {
	ext := strings.ToLower(path.Ext(name))
	typ = strings.ToLower(strings.TrimSpace(typ))
	switch {
	case ext == ".pdf" || typ == "application/pdf":
		return kindPDF
	case ext == ".docx" || typ == docxType:
		return kindDOCX
	case ext == ".xlsx" || typ == xlsxType:
		return kindXLSX
	case ext == ".zip" || typ == "application/zip" || typ == "application/x-zip-compressed":
		return kindZip
	case ext == ".svg" || typ == "image/svg+xml":
		return kindText
	case strings.HasPrefix(typ, "image/") || oneOf(ext, ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".avif", ".heic"):
		return kindImage
	case strings.HasPrefix(typ, "audio/") || typ == "video/mp4" || typ == "video/webm" ||
		oneOf(ext, ".mp3", ".wav", ".m4a", ".ogg", ".oga", ".opus", ".flac", ".aac", ".webm", ".mpga", ".mp4"):
		return kindAudio
	case oneOf(ext, ".html", ".htm", ".xhtml") || typ == "text/html" || typ == "application/xhtml+xml":
		return kindHTML
	case oneOf(ext, ".md", ".mdx", ".markdown") || typ == "text/markdown":
		return kindMarkdown
	case oneOf(ext, ".csv", ".tsv") || typ == "text/csv" || typ == "text/tab-separated-values":
		return kindCSV
	}
	if _, ok := codeExt[ext]; ok {
		return kindCode
	}
	if strings.HasPrefix(typ, "text/") || oneOf(typ, "application/json", "application/xml", "application/x-yaml", "application/yaml") ||
		oneOf(ext, ".txt", ".log", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".xml", ".ini", ".cfg", ".conf", ".env", ".tex", ".rst", ".org") {
		return kindText
	}
	return kindNone
}

// textual is a kind whose bytes are its text, so only the beginning that will
// be indexed need be read.
func textual(k fileKind) bool {
	return k == kindText || k == kindMarkdown || k == kindHTML || k == kindCode || k == kindCSV
}

// readAs decides how to read a file from its bytes first and its name second: a
// PDF's magic outranks a name that says otherwise, and bytes nobody named are
// text when they read as text.
func readAs(name, typ string, data []byte) fileKind {
	k := byName(name, typ)
	switch {
	case pdftext.Looks(data):
		return kindPDF
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		if k == kindDOCX || k == kindXLSX || k == kindZip {
			return k
		}
		return kindNone
	case k == kindImage || k == kindAudio:
		return k
	case k == kindPDF || k == kindDOCX || k == kindXLSX || k == kindZip:
		if texty(data) {
			return kindText
		}
		return kindNone
	case k == kindNone && texty(data):
		head := strings.ToLower(string(data[:min(len(data), 512)]))
		if strings.Contains(head, "<!doctype html") || strings.Contains(head, "<html") {
			return kindHTML
		}
		return kindText
	}
	return k
}

func oneOf(s string, set ...string) bool { return slices.Contains(set, s) }

// texty reports bytes that are text: valid UTF-8 with no NUL in what is read.
func texty(data []byte) bool {
	head := data[:min(len(data), 64<<10)]
	for len(head) > 0 && len(head) < len(data) && !utf8.RuneStart(data[len(head)]) {
		head = head[:len(head)-1]
	}
	return len(data) > 0 && bytes.IndexByte(head, 0) < 0 && utf8.Valid(head)
}

// streams reports a kind read a segment at a time: text whose lines parse on
// their own, so any stretch of the file can be read without the rest.
func streams(k fileKind) bool {
	return k == kindText || k == kindMarkdown || k == kindCode || k == kindCSV
}

// extract reads a file read whole into text and structure. errSkip wraps a
// file no retry can read; any other error is worth a retry.
func (x *indexer) extract(ctx context.Context, org, project, name, typ string, data []byte) (parsed, error) {
	p, err := x.parse(ctx, org, project, name, typ, readAs(name, typ, data), data)
	if err != nil {
		return parsed{}, err
	}
	if strings.TrimSpace(p.text) == "" {
		return parsed{}, fmt.Errorf("%w: no text found in the file", errSkip)
	}
	return p, nil
}

// parse reads data as kind: a whole file, or one segment of a file that streams.
func (x *indexer) parse(ctx context.Context, org, project, name, typ string, kind fileKind, data []byte) (parsed, error) {
	var (
		p   parsed
		err error
	)
	switch kind {
	case kindText:
		text := plain(string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
		if hasATX(text) {
			p = markdown(text)
		} else {
			p = parsed{text: text}
		}
	case kindMarkdown:
		p = markdown(plain(string(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))))
	case kindHTML:
		p = htmlDoc(data)
	case kindCode:
		p = code(strings.ToLower(path.Ext(name)), plain(string(data)))
	case kindCSV:
		p, err = table(name, data)
	case kindXLSX:
		p, err = sheets(data)
	case kindPDF:
		p, err = pdfDoc(data)
	case kindDOCX:
		p, err = docxDoc(data)
	case kindImage:
		var caption string
		if caption, err = x.describe(ctx, org, project, typ, data); err == nil {
			p = parsed{text: plain(caption)}
		}
	case kindAudio:
		var transcript string
		if transcript, err = x.transcribe(ctx, org, project, name, data); err == nil {
			p = parsed{text: plain(transcript)}
		}
	default:
		return parsed{}, fmt.Errorf("%w: %s files are stored but not indexed", errSkip, extOf(name))
	}
	return p, err
}

func extOf(name string) string {
	if ext := strings.TrimPrefix(strings.ToLower(path.Ext(name)), "."); ext != "" {
		return ext
	}
	return "these"
}

// ---- the writer every structured reader shares ----

// writer accumulates clean text — valid UTF-8, no NUL or carriage return, at
// most one blank line in a row — with the marks and links found as it goes, so
// every offset it records indexes the text it returns.
type writer struct {
	b     strings.Builder
	marks []mark
	refs  []ref
}

func clean(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\x00", "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// text writes inline text.
func (w *writer) text(s string) { w.b.WriteString(clean(s)) }

func (w *writer) endsWith(s string) bool { return strings.HasSuffix(w.b.String(), s) }

// brk ends the current line, if one is open.
func (w *writer) brk() {
	if w.b.Len() > 0 && !w.endsWith("\n") {
		w.b.WriteByte('\n')
	}
}

// para ends the current paragraph: a blank line, never two.
func (w *writer) para() {
	w.brk()
	if w.b.Len() > 0 && !w.endsWith("\n\n") {
		w.b.WriteByte('\n')
	}
}

// line writes s as a line of its own.
func (w *writer) line(s string) {
	s = strings.TrimSpace(clean(s))
	if s == "" {
		return
	}
	w.brk()
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

// heading writes a heading as a paragraph of its own and marks it.
func (w *writer) heading(level int, title, anchor string) {
	title = strings.Join(strings.Fields(clean(title)), " ")
	if title == "" {
		return
	}
	w.para()
	w.marks = append(w.marks, mark{at: w.b.Len(), level: min(max(level, 1), 9), title: clip(title, 200), anchor: anchor})
	w.b.WriteString(title)
	w.b.WriteString("\n\n")
}

// link records a link at the current position.
func (w *writer) link(target string) {
	if t := strings.TrimSpace(target); t != "" && !strings.HasPrefix(t, "javascript:") && !strings.HasPrefix(t, "mailto:") {
		w.refs = append(w.refs, ref{at: w.b.Len(), target: t})
	}
}

// done answers the text with its trailing blank trimmed — never its leading
// one, which would move every offset already recorded.
func (w *writer) done(title string) parsed {
	return parsed{title: strings.TrimSpace(title), text: strings.TrimRight(w.b.String(), "\n\t "), marks: w.marks, refs: w.refs}
}

// ---- markdown and plain text ----

var (
	atxRe    = regexp.MustCompile(`^(#{1,6})[ \t]+(.+?)[ \t]*#*[ \t]*$`)
	mdLinkRe = regexp.MustCompile(`\[[^\]\n]*\]\(([^)\s]+)`)
)

func hasATX(text string) bool {
	for _, line := range strings.SplitN(text, "\n", 2000) {
		if atxRe.MatchString(line) {
			return true
		}
	}
	return false
}

// markdown marks the ATX and setext headings outside fenced code, and the links.
func markdown(text string) parsed {
	var (
		p      = parsed{text: text}
		fence  string
		at     int
		prev   string
		prevAt int
	)
	for _, line := range strings.SplitAfter(text, "\n") {
		trim := strings.TrimRight(line, "\n")
		t := strings.TrimSpace(trim)
		inFence := fence != ""
		switch {
		case fence != "":
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
		case strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~"):
			fence = t[:3]
			inFence = true
		default:
			if m := atxRe.FindStringSubmatch(trim); m != nil {
				p.marks = append(p.marks, mark{at: at, level: len(m[1]), title: clip(m[2], 200)})
			} else if prev != "" && len(t) >= 3 && (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == "" && !strings.HasPrefix(prev, "-")) &&
				(len(p.marks) == 0 || p.marks[len(p.marks)-1].at != prevAt) {
				level := 1
				if t[0] == '-' {
					level = 2
				}
				p.marks = append(p.marks, mark{at: prevAt, level: level, title: clip(prev, 200)})
			}
			for _, m := range mdLinkRe.FindAllStringSubmatchIndex(trim, -1) {
				p.refs = append(p.refs, ref{at: at + m[0], target: trim[m[2]:m[3]]})
			}
		}
		prev, prevAt = t, at
		if inFence {
			prev = ""
		}
		at += len(line)
	}
	return p
}

// plain makes text indexable: valid UTF-8, no NUL or carriage return, no
// trailing blanks, at most one blank line in a row, trimmed.
func plain(s string) string {
	s = clean(s)
	var b strings.Builder
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

// ---- HTML ----

// landmarks are the regions a page names that are not its content; each is a
// section of its own so its text never reads as part of the article beside it.
var landmarks = map[atom.Atom]string{atom.Nav: "Navigation", atom.Aside: "Aside", atom.Footer: "Footer", atom.Header: "Header"}

// skipped are elements whose contents are not the page's text.
func skipped(a atom.Atom) bool {
	switch a {
	case atom.Script, atom.Style, atom.Noscript, atom.Template, atom.Svg, atom.Iframe, atom.Head:
		return true
	}
	return false
}

// htmlDoc reads a page: its <title>, its text with block structure, its h1–h6
// and landmarks as headings (a heading's id is its anchor), and its links.
func htmlDoc(data []byte) parsed {
	var (
		w       writer
		title   string
		skip    int
		pre     int
		head    = -1
		hbuf    strings.Builder
		hanchor string
		inTitle bool
	)
	z := html.NewTokenizer(bytes.NewReader(data))
	attr := func(t html.Token, k string) string {
		for _, a := range t.Attr {
			if a.Key == k {
				return a.Val
			}
		}
		return ""
	}
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		t := z.Token()
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			if t.DataAtom == atom.Title {
				inTitle = tt == html.StartTagToken
				continue
			}
			if skipped(t.DataAtom) {
				if tt == html.StartTagToken {
					skip++
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if lvl := headingLevel(t.DataAtom); lvl > 0 {
				head, hanchor = lvl, attr(t, "id")
				hbuf.Reset()
				continue
			}
			if name, ok := landmarks[t.DataAtom]; ok && head < 0 {
				if label := attr(t, "aria-label"); label != "" {
					name = label
				}
				w.heading(1, name, attr(t, "id"))
				continue
			}
			switch t.DataAtom {
			case atom.A:
				w.link(attr(t, "href"))
			case atom.Br:
				w.brk()
			case atom.Pre:
				pre++
				w.para()
			case atom.Td, atom.Th:
				w.text(" | ")
			case atom.Img:
				if alt := attr(t, "alt"); alt != "" && head < 0 {
					w.text(" " + alt + " ")
				}
			default:
				if blockish(t.DataAtom) {
					w.para()
				}
			}
		case html.EndTagToken:
			if t.DataAtom == atom.Title {
				inTitle = false
				continue
			}
			if skipped(t.DataAtom) {
				if skip > 0 {
					skip--
				}
				continue
			}
			if skip > 0 {
				continue
			}
			if lvl := headingLevel(t.DataAtom); lvl > 0 && head > 0 {
				w.heading(head, hbuf.String(), hanchor)
				head = -1
				continue
			}
			if t.DataAtom == atom.Pre && pre > 0 {
				pre--
			}
			if blockish(t.DataAtom) || t.DataAtom == atom.Tr {
				w.para()
			}
		case html.TextToken:
			switch {
			case inTitle:
				title += t.Data
			case skip > 0:
			case head > 0:
				hbuf.WriteString(t.Data)
				hbuf.WriteByte(' ')
			case pre > 0:
				w.text(t.Data)
			default:
				if s := collapse(t.Data); s != "" && (s != " " || !w.endsWith(" ") && !w.endsWith("\n") && w.b.Len() > 0) {
					w.text(s)
				}
			}
		}
	}
	return w.done(strings.Join(strings.Fields(title), " "))
}

func headingLevel(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	case atom.H6:
		return 6
	}
	return 0
}

func blockish(a atom.Atom) bool {
	switch a {
	case atom.P, atom.Div, atom.Li, atom.Ul, atom.Ol, atom.Section, atom.Article, atom.Main,
		atom.Blockquote, atom.Table, atom.Dl, atom.Dt, atom.Dd, atom.Figure, atom.Figcaption,
		atom.Form, atom.Fieldset, atom.Details, atom.Summary, atom.Address, atom.Hr, atom.Body:
		return true
	}
	return false
}

// collapse folds an HTML text run's whitespace to single spaces, keeping one at
// either edge so adjacent runs do not run together.
func collapse(s string) string {
	if strings.TrimSpace(s) == "" {
		if s == "" {
			return ""
		}
		return " "
	}
	out := strings.Join(strings.Fields(s), " ")
	if unicode.IsSpace(rune(s[0])) {
		out = " " + out
	}
	if unicode.IsSpace(rune(s[len(s)-1])) {
		out += " "
	}
	return out
}

// ---- code ----

type symbol struct {
	re    *regexp.Regexp
	level int
}

var symbolRes = map[string][]symbol{
	"go": {{regexp.MustCompile(`^(func|type)\s+\S`), 1}},
	"py": {{regexp.MustCompile(`^(class|def|async\s+def)\s+\w`), 1}, {regexp.MustCompile(`^\s{2,8}(def|async\s+def)\s+\w`), 2}},
	"js": {
		{regexp.MustCompile(`^(export\s+)?(default\s+)?(declare\s+)?(abstract\s+)?(async\s+)?(function\*?|class|interface|type|enum|namespace)\s+\w`), 1},
		{regexp.MustCompile(`^(export\s+)?(const|let|var)\s+\w+\s*(:[^=]+)?=\s*(async\s*)?(\(|function|<)`), 1},
	},
	"rs":   {{regexp.MustCompile(`^(pub(\([\w:]+\))?\s+)?(async\s+)?(fn|struct|enum|trait|impl|mod|union|macro_rules!)\s*\S`), 1}, {regexp.MustCompile(`^\s{4}(pub(\([\w:]+\))?\s+)?(async\s+)?fn\s+\w`), 2}},
	"java": {{regexp.MustCompile(`^((public|private|protected|internal|open|abstract|final|sealed|data|static|export)\s+)*(class|interface|enum|object|record|struct|trait|protocol|extension|fun|func|function|contract|library|message|service)\s+\w`), 1}},
	"rb":   {{regexp.MustCompile(`^(class|module)\s+\w`), 1}, {regexp.MustCompile(`^\s*def\s+\w`), 2}},
	"c":    {{regexp.MustCompile(`^(struct|class|enum|union|namespace|message|service)\s+\w`), 1}, {regexp.MustCompile(`^[A-Za-z_][\w \t\*:<>,&]*\b\w+\s*\([^;]*$`), 1}},
	"sh":   {{regexp.MustCompile(`^(function\s+)?[\w-]+\s*\(\)\s*\{?`), 1}},
}

// code marks a source file's declarations: a top-level type or function is a
// section, a method a subsection.
func code(ext, text string) parsed {
	p := parsed{text: text}
	res := symbolRes[codeExt[ext]]
	at := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trim := strings.TrimRight(line, "\n")
		for _, r := range res {
			if r.re.MatchString(trim) {
				p.marks = append(p.marks, mark{at: at, level: r.level, title: clip(strings.TrimSpace(strings.TrimRight(trim, "{ ")), 120)})
				break
			}
		}
		at += len(line)
	}
	return p
}

// ---- tables ----

// table reads a CSV or TSV: its header, then its rows a hundred to a section,
// each row written as "column: value" so a passage carries what its numbers are.
func table(name string, data []byte) (parsed, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	r := csv.NewReader(bytes.NewReader(data))
	sample := data[:min(len(data), 4096)]
	if strings.EqualFold(path.Ext(name), ".tsv") || bytes.Count(sample, []byte("\t")) > bytes.Count(sample, []byte(",")) {
		r.Comma = '\t'
	}
	r.LazyQuotes, r.FieldsPerRecord = true, -1
	var rows [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if len(rows) == 0 {
				return parsed{}, fmt.Errorf("%w: %v", errSkip, err)
			}
			break
		}
		rows = append(rows, rec)
	}
	var w writer
	tabulate(&w, 1, rows)
	return w.done(""), nil
}

// tabulate writes rows under level: the header as a line, then sections of
// sheetRows rows.
func tabulate(w *writer, level int, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	header := rows[0]
	w.line("Columns: " + strings.Join(header, " | "))
	for lo := 1; lo < len(rows); lo += sheetRows {
		if w.b.Len() > maxFile {
			return
		}
		hi := min(len(rows), lo+sheetRows)
		w.heading(level, fmt.Sprintf("Rows %d–%d", lo, hi-1), "")
		for _, row := range rows[lo:hi] {
			cells := make([]string, 0, len(row))
			for i, v := range row {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				if i < len(header) && strings.TrimSpace(header[i]) != "" {
					cells = append(cells, strings.TrimSpace(header[i])+": "+v)
				} else {
					cells = append(cells, v)
				}
			}
			w.line(strings.Join(cells, " · "))
		}
	}
}

// sheets reads a workbook: each sheet a section, its rows as a table's.
func sheets(data []byte) (parsed, error) {
	zr, err := azip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return parsed{}, fmt.Errorf("%w: open xlsx: %v", errSkip, err)
	}
	part := func(name string) []byte {
		for _, f := range zr.File {
			if f.Name == name {
				rc, err := f.Open()
				if err != nil {
					return nil
				}
				defer func() { _ = rc.Close() }()
				b, _ := io.ReadAll(io.LimitReader(rc, maxPart))
				return b
			}
		}
		return nil
	}
	var book struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(part("xl/workbook.xml"), &book); err != nil {
		return parsed{}, fmt.Errorf("%w: read xlsx workbook: %v", errSkip, err)
	}
	var rels struct {
		Rel []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	_ = xml.Unmarshal(part("xl/_rels/workbook.xml.rels"), &rels)
	target := map[string]string{}
	for _, r := range rels.Rel {
		t := strings.TrimPrefix(r.Target, "/")
		if !strings.HasPrefix(t, "xl/") {
			t = "xl/" + t
		}
		target[r.ID] = t
	}
	var shared struct {
		SI []struct {
			T string `xml:"t"`
			R []struct {
				T string `xml:"t"`
			} `xml:"r"`
		} `xml:"si"`
	}
	_ = xml.Unmarshal(part("xl/sharedStrings.xml"), &shared)
	strs := make([]string, len(shared.SI))
	for i, si := range shared.SI {
		s := si.T
		for _, r := range si.R {
			s += r.T
		}
		strs[i] = s
	}
	var w writer
	for i, sh := range book.Sheets {
		name := target[sh.RID]
		if name == "" {
			name = fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		}
		var ws struct {
			Rows []struct {
				C []struct {
					R  string `xml:"r,attr"`
					T  string `xml:"t,attr"`
					V  string `xml:"v"`
					IS struct {
						T string `xml:"t"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if err := xml.Unmarshal(part(name), &ws); err != nil {
			continue
		}
		rows := make([][]string, 0, len(ws.Rows))
		for _, row := range ws.Rows {
			var cells []string
			for _, c := range row.C {
				for col := column(c.R); len(cells) < col; {
					cells = append(cells, "")
				}
				v := c.V
				switch c.T {
				case "s":
					if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(strs) {
						v = strs[n]
					}
				case "inlineStr":
					v = c.IS.T
				}
				cells = append(cells, v)
			}
			rows = append(rows, cells)
		}
		w.heading(1, "Sheet: "+sh.Name, "")
		tabulate(&w, 2, rows)
	}
	return w.done(""), nil
}

// column is the zero-based column of a cell reference like "C7".
func column(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return max(n-1, 0)
}

// ---- PDF ----

// pdfDoc reads a PDF's rows in layout order and marks its headings: the
// outline's titles where the document has one, else the rows set in type
// larger than its body, ranked by size.
func pdfDoc(data []byte) (parsed, error) {
	rows, err := pdftext.Rows(data, pdfPages, pdfRows)
	if errors.Is(err, pdftext.ErrNoText) {
		return parsed{}, fmt.Errorf("%w: the PDF has no text layer", errSkip)
	}
	if err != nil {
		return parsed{}, fmt.Errorf("%w: %v", errSkip, err)
	}
	depth := map[string]int{}
	for _, e := range pdftext.Outline(data) {
		k := fold(e.Title)
		if _, ok := depth[k]; !ok && len(k) >= 3 {
			depth[k] = min(e.Depth, 6)
		}
	}
	levels := sizeLevels(rows)
	var (
		w    writer
		page int
		used = map[string]bool{}
	)
	for _, r := range rows {
		if r.Page != page {
			if page != 0 {
				w.para()
			}
			page = r.Page
		}
		k := fold(r.Text)
		if d, ok := depth[k]; ok && !used[k] {
			used[k] = true
			w.heading(d, r.Text, "")
			continue
		}
		if len(depth) == 0 {
			if lvl, ok := levels[sizeKey(r.Size)]; ok && headingLike(r.Text) {
				w.heading(lvl, r.Text, "")
				continue
			}
		}
		w.line(r.Text)
	}
	return w.done(""), nil
}

func fold(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

func sizeKey(size float64) int { return int(size*2 + 0.5) }

// sizeLevels ranks the sizes larger than the body's — the size most of the text
// is set in — as heading levels, largest first, at most four.
func sizeLevels(rows []pdftext.Row) map[int]int {
	weight := map[int]int{}
	for _, r := range rows {
		weight[sizeKey(r.Size)] += len(r.Text)
	}
	body, most := 0, -1
	for k, n := range weight {
		if n > most || n == most && k < body {
			body, most = k, n
		}
	}
	var bigger []int
	for k := range weight {
		if k > 0 && float64(k) >= float64(body)*1.15 {
			bigger = append(bigger, k)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(bigger)))
	out := map[int]int{}
	for i, k := range bigger {
		if i == 4 {
			break
		}
		out[k] = i + 1
	}
	return out
}

// headingLike is a row short enough to be a heading and not a sentence.
func headingLike(s string) bool {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < 2 || n > 120 {
		return false
	}
	if strings.HasSuffix(s, ".") && !strings.HasSuffix(s, "..") || strings.HasSuffix(s, ",") || strings.HasSuffix(s, ";") {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters*2 >= utf8.RuneCountInString(s)
}

// ---- DOCX ----

var headingStyle = regexp.MustCompile(`(?i)^(heading|titre|überschrift|encabezado)\s*([1-9])$|^(title)$`)

// docxDoc reads a DOCX's document part: each paragraph a line, a paragraph in a
// heading style (or with an outline level) a heading at that level.
func docxDoc(data []byte) (parsed, error) {
	zr, err := azip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return parsed{}, fmt.Errorf("%w: open docx: %v", errSkip, err)
	}
	for _, f := range zr.File {
		if f.Name != "word/document.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return parsed{}, fmt.Errorf("%w: open docx body: %v", errSkip, err)
		}
		defer func() { _ = rc.Close() }()
		p, err := wordRuns(io.LimitReader(rc, maxPart))
		if err != nil {
			return parsed{}, fmt.Errorf("%w: %v", errSkip, err)
		}
		return p, nil
	}
	return parsed{}, fmt.Errorf("%w: the DOCX has no document part", errSkip)
}

// wordRuns walks WordprocessingML: w:t is text, w:tab a tab, w:br and w:cr a line
// break, w:pStyle and w:outlineLvl say a paragraph is a heading, and the end of
// w:p ends a paragraph.
func wordRuns(r io.Reader) (parsed, error) {
	var (
		w     writer
		para  strings.Builder
		level int
		inT   bool
	)
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return parsed{}, fmt.Errorf("read docx body: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				para.Reset()
				level = 0
			case "t":
				inT = true
			case "tab":
				para.WriteByte('\t')
			case "br", "cr":
				para.WriteByte('\n')
			case "pStyle":
				for _, a := range t.Attr {
					if a.Name.Local != "val" {
						continue
					}
					if m := headingStyle.FindStringSubmatch(a.Value); m != nil {
						level = 1
						if m[2] != "" {
							level, _ = strconv.Atoi(m[2])
						}
					}
				}
			case "outlineLvl":
				for _, a := range t.Attr {
					if n, err := strconv.Atoi(a.Value); a.Name.Local == "val" && err == nil && n < 9 && level == 0 {
						level = n + 1
					}
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "p":
				if level > 0 {
					w.heading(level, para.String(), "")
				} else {
					w.line(para.String())
				}
			}
		case xml.CharData:
			if inT {
				para.Write(t)
			}
		}
	}
	return w.done(""), nil
}

// ---- pictures and recordings ----

// visionModel and scribeModel are the served SKUs a picture and a recording are
// read through.
func visionModel() string { return env.Or("CLOUD_VISION_MODEL", "zen-vl") }
func scribeModel() string { return env.Or("CLOUD_SCRIBE_MODEL", "zen-scribe") }

const describePrompt = "Transcribe every piece of text visible in this image exactly as written. " +
	"Then describe what the image shows in detail: its subject, layout, any charts, tables, " +
	"numbers, labels and colors. Answer in plain text with no preamble."

// describe reads a picture through a vision model: the text in it, then what it
// shows, so a search for either finds it.
func (x *indexer) describe(ctx context.Context, org, project, typ string, data []byte) (string, error) {
	if len(data) > maxImage {
		return "", fmt.Errorf("%w: pictures over %d MB are stored but not described", errSkip, maxImage>>20)
	}
	if x.chat == nil {
		return "", fmt.Errorf("%w: no vision model in this deployment", errSkip)
	}
	if !strings.HasPrefix(typ, "image/") {
		typ = "image/png"
	}
	uri := "data:" + typ + ";base64," + base64.StdEncoding.EncodeToString(data)
	res, err := x.chat.ChatCompletion(ctx, &infer.ChatRequest{
		Model:     visionModel(),
		Org:       org,
		Payer:     cloud.PayerOf(ctx).Wallet,
		Project:   project,
		MaxTokens: 1500,
		Prompt:    describePrompt,
		Messages:  []infer.ChatMessage{{Role: infer.RoleUser, Content: describePrompt, Images: []string{uri}}},
	})
	if err != nil {
		return "", fmt.Errorf("describe picture: %w", err)
	}
	return "Image description\n\n" + res.Content, nil
}

// transcribe reads a recording through zen-scribe.
func (x *indexer) transcribe(ctx context.Context, org, project, name string, data []byte) (string, error) {
	if len(data) > maxAudio {
		return "", fmt.Errorf("%w: recordings over %d MB are stored but not transcribed", errSkip, maxAudio>>20)
	}
	t, ok := x.chat.(infer.Transcriber)
	if !ok {
		return "", fmt.Errorf("%w: no speech model in this deployment", errSkip)
	}
	text, err := t.Transcribe(ctx, &infer.TranscribeRequest{
		Model: scribeModel(), Audio: data, Name: name,
		Org: org, Payer: cloud.PayerOf(ctx).Wallet, Project: project,
	})
	if err != nil {
		return "", fmt.Errorf("transcribe recording: %w", err)
	}
	return text, nil
}
