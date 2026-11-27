package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
)

// toc.go turns a file's marks into its table of contents — the primary unit of
// retrieval — and writes a one-line summary for each node, reading the
// entities each section names on the same pass.
//
// The tree's root is the document. A heading's section runs from its line to
// the next heading at its level or above, and holds its BODY — the text from
// its line to the next heading of any level — so the bodies tile the document
// and a passage cut inside one never crosses a section boundary. A body longer
// than synthMax is cut into synthetic subsections at paragraph breaks, which is
// how a long run of text with no structure gets one.

const (
	// synthMax is the longest body a section keeps; past it the body is cut
	// into synthetic subsections of about synthPart bytes.
	synthMax  = 16 << 10
	synthPart = 8 << 10
	// summaryMax is how many sections of one file are summarized by a model;
	// the rest are summarized by their first sentence.
	summaryMax = 600
	// summaryBatch is how many sections one model call summarizes, and
	// summaryCalls how many calls run at once. A call's time is its reply's
	// length, so small batches side by side finish sooner than large ones in
	// turn: 28 sections took 62s as two calls of 20.
	summaryBatch = 6
	summaryCalls = 8
)

// section is one node of a file's table of contents.
type section struct {
	id, parent, level int
	title, anchor     string
	// lo and hi are the section's span of the document, its descendants
	// included; its body starts at lo.
	lo, hi    int
	body      string
	summary   string
	synthetic bool
}

// grower is where a file's table of contents stands between two segments of
// its text (ingest.go reads a file a segment at a time): the next section's id,
// the headings still open from the document's root down, and where the next
// segment starts in the text. All of it is read back from the store
// (store.grower), so a segment's write is the whole of the state an ingest
// resumes from.
type grower struct {
	next int
	open []section // id, level, title; the root first
	at   int
	// parts is how many synthetic parts the deepest open section has: a
	// segment's leading text, before its first heading, continues that section
	// as more parts.
	parts int
}

// grow adds one segment of text to a table of contents. The first segment
// makes the document's root; a later segment's leading text continues the
// deepest open section as synthetic parts, and its headings nest under the
// headings still open. add is the new sections in document order; his is the
// end each earlier section's span now has, for the sections this segment
// closed or extended.
func (g *grower) grow(name string, p parsed) (add []section, his map[int]int) {
	marks := append([]mark(nil), p.marks...)
	sort.SliceStable(marks, func(i, j int) bool { return marks[i].at < marks[j].at })
	// Two marks at one offset are one heading; the shallower wins.
	marks = dedupe(marks)
	n := len(p.text)
	first := n
	if len(marks) > 0 {
		first = marks[0].at
	}
	his = map[int]int{}
	prev := g.open
	// heads are the segment's sections before long bodies are cut into parts;
	// a head's parent is an earlier section's id, or -2-k for heads[k].
	var heads []section
	stack := make([]int, 0, len(prev)+len(marks)) // >= 0: an open earlier section; < 0: -1-k for heads[k]
	for _, o := range prev {
		stack = append(stack, o.id)
	}
	earlier := func(ref int) section {
		for _, o := range prev {
			if o.id == ref {
				return o
			}
		}
		return section{}
	}
	levelOf := func(ref int) int {
		if ref >= 0 {
			return earlier(ref).level
		}
		return heads[-1-ref].level
	}
	closeAt := func(ref, at int) {
		if ref >= 0 {
			his[ref] = at
		} else {
			heads[-1-ref].hi = at
		}
	}
	if g.next == 0 {
		title := p.title
		if title == "" {
			title = name
		}
		// A document whose one top-level heading opens it is titled by that
		// heading: it is the document's name, not a section of it.
		if p.title == "" && len(marks) > 0 && marks[0].level == 1 && strings.TrimSpace(p.text[:marks[0].at]) == "" {
			ones := 0
			for _, m := range marks {
				if m.level == 1 {
					ones++
				}
			}
			if ones == 1 {
				title = marks[0].title
				marks = marks[1:]
				first = n
				if len(marks) > 0 {
					first = marks[0].at
				}
			}
		}
		heads = append(heads, section{parent: -1, level: 0, title: title, lo: 0, body: p.text[:first]})
		stack = append(stack, -1)
	}
	var lead []section
	if g.next > 0 && strings.TrimSpace(p.text[:first]) != "" {
		top := prev[len(prev)-1]
		at := g.at
		for k, piece := range cutAt(p.text[:first], synthPart) {
			lead = append(lead, section{
				parent: top.id, level: top.level + 1,
				title: fmt.Sprintf("%s — part %d", strings.TrimSuffix(clip(top.title, 80), "…"), g.parts+k+2) + firstWords(piece),
				lo:    at, hi: at + len(piece), body: piece, synthetic: true,
			})
			at += len(piece)
		}
	}
	for i, m := range marks {
		end := n
		if i+1 < len(marks) {
			end = marks[i+1].at
		}
		for len(stack) > 1 && levelOf(stack[len(stack)-1]) >= m.level {
			closeAt(stack[len(stack)-1], g.at+m.at)
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1]
		if parent < 0 {
			parent = -2 - (-1 - parent)
		}
		heads = append(heads, section{parent: parent, level: m.level, title: m.title, anchor: m.anchor, lo: g.at + m.at, body: p.text[m.at:end]})
		stack = append(stack, -len(heads))
	}
	// What is still open runs to the end of the segment, for now.
	for _, ref := range stack {
		closeAt(ref, g.at+n)
	}
	// Number the new sections in document order: the leading parts, then each
	// head followed by the parts its long body is cut into.
	id := g.next
	for _, s := range lead {
		s.id = id
		add = append(add, s)
		id++
	}
	final := make([]int, len(heads))
	for k, h := range heads {
		pieces := []string{h.body}
		if len(h.body) > synthMax {
			pieces = cutAt(h.body, synthPart)
		}
		h.id, h.body = id, pieces[0]
		final[k] = id
		add = append(add, h)
		id++
		at := h.lo + len(pieces[0])
		for j, piece := range pieces[1:] {
			add = append(add, section{
				id: id, parent: h.id, level: h.level + 1,
				title: fmt.Sprintf("%s — part %d", strings.TrimSuffix(clip(h.title, 80), "…"), j+2) + firstWords(piece),
				lo:    at, hi: at + len(piece), body: piece, synthetic: true,
			})
			at += len(piece)
			id++
		}
	}
	for i := range add {
		if p := add[i].parent; p <= -2 && !add[i].synthetic {
			add[i].parent = final[-2-p]
		}
	}
	// The grower moves past the segment: the open headings, renumbered.
	open := make([]section, 0, len(stack))
	for _, ref := range stack {
		if ref >= 0 {
			open = append(open, earlier(ref))
			continue
		}
		h := heads[-1-ref]
		open = append(open, section{id: final[-1-ref], level: h.level, title: h.title})
	}
	g.open = open
	if len(lead) > 0 && len(marks) == 0 {
		g.parts += len(lead)
	} else if len(marks) > 0 || g.next == 0 {
		g.parts = synthCount(add, g.open[len(g.open)-1].id)
	}
	g.next, g.at = id, g.at+n
	return add, his
}

// synthCount is how many synthetic parts of the section id are in secs.
func synthCount(secs []section, id int) int {
	n := 0
	for _, s := range secs {
		if s.synthetic && s.parent == id {
			n++
		}
	}
	return n
}

// tree builds the table of contents of a text read whole: one segment.
func tree(name string, p parsed) []section {
	var g grower
	add, _ := g.grow(name, p)
	return add
}

func dedupe(marks []mark) []mark {
	out := marks[:0]
	for _, m := range marks {
		if len(out) > 0 && out[len(out)-1].at == m.at {
			if m.level < out[len(out)-1].level {
				out[len(out)-1] = m
			}
			continue
		}
		out = append(out, m)
	}
	return out
}

// cutAt divides text into pieces of about n bytes at paragraph breaks, else
// line breaks, else spaces, else rune boundaries. The pieces tile the text.
func cutAt(text string, n int) []string {
	var out []string
	for len(text) > n+n/2 {
		i := cutOne(text, n)
		out = append(out, text[:i])
		text = text[i:]
	}
	return append(out, text)
}

// cutOne is where cutAt ends text's first piece of about n bytes; text is longer
// than n+n/2.
func cutOne(text string, n int) int {
	i := strings.LastIndex(text[:n+n/2], "\n\n")
	if i < n/2 {
		i = strings.LastIndex(text[:n+n/2], "\n")
	}
	if i < n/2 {
		i = strings.LastIndex(text[:n+n/2], " ")
	}
	if i < n/2 {
		i = n
		for i > 0 && !utf8.RuneStart(text[i]) {
			i--
		}
		return i
	}
	return i + 1 // the break stays with the piece it ends
}

// firstWords is ": " and the opening words of text, to tell synthetic parts
// apart in a table of contents.
func firstWords(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 {
		return ""
	}
	return ": " + clip(strings.Join(f[:min(len(f), 8)], " "), 60)
}

// trail is a section's place in its document: the titles from the document's own
// down to it, joined by " › ".
func trail(secs map[int]section, id int) string {
	var parts []string
	for s, ok := secs[id]; ok; s, ok = secs[s.parent] {
		parts = append(parts, s.title)
		if s.parent < 0 {
			break
		}
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, " › ")
}

// lead is the opening sentence of text, the summary a section gets when no model
// writes one.
func lead(title, body string) string {
	b := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(body), title))
	b = strings.Join(strings.Fields(b), " ")
	if i := strings.Index(b, ". "); i > 20 && i < 240 {
		return b[:i+1]
	}
	return clip(b, 200)
}

// ---- writing it ----

// putSegment writes one segment of a file's table of contents — its new
// sections, the ends of the earlier sections it closed or extended, and the
// hyperlinks in its text — and moves the file's resume point to done of total,
// in one transaction: a segment is written whole or not at all.
func (s *store) putSegment(ctx context.Context, f File, add []section, his map[int]int, refs []ref, done, total int64, chars int, clipped bool) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, sec := range add {
		syn := 0
		if sec.synthetic {
			syn = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sections (file,id,parent,level,title,anchor,lo,hi,body,summary,synthetic) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			f.ID, sec.id, sec.parent, sec.level, sec.title, sec.anchor, sec.lo, sec.hi, sec.body, sec.summary, syn); err != nil {
			return fmt.Errorf("kb: write section: %w", err)
		}
	}
	for id, hi := range his {
		if _, err := tx.ExecContext(ctx, `UPDATE sections SET hi=$1 WHERE file=$2 AND id=$3`, hi, f.ID, id); err != nil {
			return fmt.Errorf("kb: extend section: %w", err)
		}
	}
	ordered := append([]section(nil), add...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].lo < ordered[j].lo })
	for _, r := range refs {
		if len(ordered) == 0 {
			break
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO links (file,section,kind,target_file,target_section,label) VALUES ($1,$2,$3,'',0,$4) ON CONFLICT DO NOTHING`,
			f.ID, sectionAt(ordered, r.at), linkHref, clip(r.target, 500)); err != nil {
			return fmt.Errorf("kb: write link: %w", err)
		}
	}
	c := 0
	if clipped {
		c = 1
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE files SET done=$1, total=$2, chars=$3, clipped=$4, sections=(SELECT COUNT(*) FROM sections WHERE file=$5), updated=$6 WHERE id=$5 AND etag=$7`,
		done, total, chars, c, f.ID, time.Now().Unix(), f.etag)
	if err != nil {
		return fmt.Errorf("kb: record segment: %w", err)
	}
	if err := held(res); err != nil {
		return err // forgotten or replaced: the segment is not written
	}
	return tx.Commit()
}

// grower reads back where a file's table of contents stands: the next id, the
// open headings (the last heading written and its ancestors), the text written,
// and the deepest open heading's synthetic parts.
func (s *store) grower(ctx context.Context, f File) (grower, error) {
	g := grower{at: f.Chars}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sections WHERE file=$1`, f.ID).Scan(&g.next); err != nil {
		return g, fmt.Errorf("kb: read sections: %w", err)
	}
	if g.next == 0 {
		return g, nil
	}
	id := 0
	err := s.db.QueryRowContext(ctx, `SELECT id FROM sections WHERE file=$1 AND synthetic=0 ORDER BY id DESC LIMIT 1`, f.ID).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return g, fmt.Errorf("kb: read sections: %w", err)
	}
	for id >= 0 {
		var o section
		if err := s.db.QueryRowContext(ctx, `SELECT id,parent,level,title FROM sections WHERE file=$1 AND id=$2`, f.ID, id).
			Scan(&o.id, &o.parent, &o.level, &o.title); err != nil {
			return g, fmt.Errorf("kb: read section: %w", err)
		}
		g.open = append([]section{o}, g.open...)
		id = o.parent
	}
	top := g.open[len(g.open)-1].id
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sections WHERE file=$1 AND parent=$2 AND synthetic=1`, f.ID, top).Scan(&g.parts); err != nil {
		return g, fmt.Errorf("kb: read sections: %w", err)
	}
	return g, nil
}

// outline reads a file's table of contents without its text or summaries: what
// a stage needs to name a section's place (trail), for every section at once
// however long the file.
func (s *store) outline(ctx context.Context, id string) (map[int]section, []section, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,parent,level,title,anchor,lo,hi,synthetic FROM sections WHERE file=$1 ORDER BY id`, id)
	if err != nil {
		return nil, nil, fmt.Errorf("kb: read sections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byID := map[int]section{}
	var ordered []section
	for rows.Next() {
		var (
			sec section
			syn int
		)
		if err := rows.Scan(&sec.id, &sec.parent, &sec.level, &sec.title, &sec.anchor, &sec.lo, &sec.hi, &syn); err != nil {
			return nil, nil, fmt.Errorf("kb: read section: %w", err)
		}
		sec.synthetic = syn != 0
		byID[sec.id] = sec
		ordered = append(ordered, sec)
	}
	return byID, ordered, rows.Err()
}

// lineage reads a section and every section above it into toc — what trail
// needs to name its place — without reading the rest of the file's contents.
func (s *store) lineage(ctx context.Context, file string, id int, toc map[int]section) error {
	for id >= 0 {
		if _, ok := toc[id]; ok {
			return nil // read already, and its ancestors with it
		}
		var (
			sec section
			syn int
		)
		err := s.db.QueryRowContext(ctx,
			`SELECT id,parent,level,title,anchor,lo,hi,summary,synthetic FROM sections WHERE file=$1 AND id=$2`, file, id).
			Scan(&sec.id, &sec.parent, &sec.level, &sec.title, &sec.anchor, &sec.lo, &sec.hi, &sec.summary, &syn)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("kb: read section: %w", err)
		}
		sec.synthetic = syn != 0
		toc[id] = sec
		id = sec.parent
	}
	return nil
}

// pageBytes is about how much section text one page of a stage reads.
const pageBytes = 4 << 20

// page reads the sections after id from, with their text and summaries, in
// document order, up to about pageBytes of text: a stage walks a file a page at
// a time so it never holds more of it than that.
func (s *store) page(ctx context.Context, id string, from int) ([]section, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,parent,level,title,anchor,lo,hi,body,summary,synthetic FROM sections WHERE file=$1 AND id>$2 ORDER BY id LIMIT 2000`, id, from)
	if err != nil {
		return nil, fmt.Errorf("kb: read sections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var (
		out  []section
		size int
	)
	for rows.Next() && size < pageBytes {
		var (
			sec section
			syn int
		)
		if err := rows.Scan(&sec.id, &sec.parent, &sec.level, &sec.title, &sec.anchor, &sec.lo, &sec.hi, &sec.body, &sec.summary, &syn); err != nil {
			return nil, fmt.Errorf("kb: read section: %w", err)
		}
		sec.synthetic = syn != 0
		out = append(out, sec)
		size += len(sec.body)
	}
	return out, rows.Err()
}

// sectionsOf reads a file's table of contents in document order; withBody reads
// each section's text too.
func (s *store) sectionsOf(ctx context.Context, id string, withBody bool) ([]section, error) {
	body := `''`
	if withBody {
		body = `body`
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,parent,level,title,anchor,lo,hi,`+body+`,summary,synthetic FROM sections WHERE file=$1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("kb: read sections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []section
	for rows.Next() {
		var (
			sec section
			syn int
		)
		if err := rows.Scan(&sec.id, &sec.parent, &sec.level, &sec.title, &sec.anchor, &sec.lo, &sec.hi, &sec.body, &sec.summary, &syn); err != nil {
			return nil, fmt.Errorf("kb: read section: %w", err)
		}
		sec.synthetic = syn != 0
		out = append(out, sec)
	}
	return out, rows.Err()
}

// ---- summaries and entities ----

// filesModel is the served SKU a file's summaries, entities and the ToC pick
// are written by.
func filesModel() string { return env.Or("CLOUD_FILES_MODEL", "zen5-flash") }

// entityKinds are the kinds of entity the graph keeps.
var entityKinds = map[string]bool{"person": true, "org": true, "product": true, "term": true, "place": true}

// readout is what one model call answers about one section.
type readout struct {
	summary  string
	entities [][2]string
}

const summaryPrompt = `For each numbered section of the document below, write a one-sentence summary (at most 25 words) of what it covers, and list the named entities it mentions: people, organizations, products, places, and defined terms. Reply with ONLY a JSON object of this shape and nothing else:
{"s":[{"i":1,"summary":"...","e":[["Name","person|org|product|place|term"]]}]}

Document: %s
`

// readSections asks a model to summarize one page of a file's sections and name
// their entities — those of them the model reads (summarized), in batches with a
// bounded number of calls at once — and summarizes the rest by their first
// sentences. A batch the model does not answer is summarized by first sentences
// and names no entities: a summary is a help to retrieval, never a reason to
// fail a file. children are each section's subsections' titles.
func (x *indexer) readSections(ctx context.Context, org, project, doc string, secs []section, summarized map[int]bool, children map[int][]string) map[int]readout {
	out := map[int]readout{}
	var mu sync.Mutex
	pick := make([]section, 0, len(secs))
	for _, s := range secs {
		if summarized[s.id] {
			pick = append(pick, s)
		}
	}
	sem := make(chan struct{}, summaryCalls)
	var wg sync.WaitGroup
	for lo := 0; lo < len(pick) && x.chat != nil; lo += summaryBatch {
		batch := pick[lo:min(len(pick), lo+summaryBatch)]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			got, err := x.summarize(ctx, org, project, doc, batch, children)
			if err != nil {
				x.log.Warn("kb: summarize sections failed; first sentences stand in", "org", org, "err", err)
				return
			}
			mu.Lock()
			for id, r := range got {
				out[id] = r
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, s := range secs {
		if r, ok := out[s.id]; ok && r.summary != "" {
			continue
		}
		r := out[s.id]
		r.summary = lead(s.title, s.body)
		if r.summary == "" && len(children[s.id]) > 0 {
			r.summary = "Covers " + clip(strings.Join(children[s.id], "; "), 180)
		}
		out[s.id] = r
	}
	return out
}

// summarize is one model call over one batch.
func (x *indexer) summarize(ctx context.Context, org, project, doc string, batch []section, children map[int][]string) (map[int]readout, error) {
	var b strings.Builder
	fmt.Fprintf(&b, summaryPrompt, doc)
	for i, s := range batch {
		fmt.Fprintf(&b, "\n[%d] %s\n", i+1, s.title)
		if kids := children[s.id]; len(kids) > 0 {
			fmt.Fprintf(&b, "(subsections: %s)\n", clip(strings.Join(kids, "; "), 300))
		}
		fmt.Fprintf(&b, "%s\n", clip(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s.body), s.title)), 900))
	}
	res, err := x.chat.ChatCompletion(ctx, &infer.ChatRequest{
		Model: filesModel(), Prompt: b.String(), Org: org, Payer: cloud.PayerOf(ctx).Wallet, Project: project, MaxTokens: 6000,
	})
	if err != nil {
		return nil, err
	}
	var reply struct {
		S []struct {
			I       int        `json:"i"`
			Summary string     `json:"summary"`
			E       [][]string `json:"e"`
		} `json:"s"`
	}
	if err := json.Unmarshal([]byte(jsonOf(res.Content)), &reply); err != nil {
		return nil, fmt.Errorf("summaries: %w", err)
	}
	out := map[int]readout{}
	for _, r := range reply.S {
		if r.I < 1 || r.I > len(batch) {
			continue
		}
		var ents [][2]string
		for _, e := range r.E {
			if len(e) == 2 && entityKinds[strings.ToLower(strings.TrimSpace(e[1]))] {
				if name := strings.Join(strings.Fields(e[0]), " "); utf8.RuneCountInString(name) >= 2 && len(name) <= 80 {
					ents = append(ents, [2]string{name, strings.ToLower(strings.TrimSpace(e[1]))})
				}
			}
		}
		out[batch[r.I-1].id] = readout{summary: clip(strings.TrimSpace(r.Summary), 300), entities: ents}
	}
	return out, nil
}

// jsonOf is the span from the first '{' to the last '}' of a model's reply: the
// JSON it was asked for, without the prose or fences around it.
func jsonOf(s string) string {
	i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}')
	if i < 0 || j <= i {
		return "{}"
	}
	return s[i : j+1]
}

// entityID names an entity in its org by kind and folded name, so two files
// naming one entity meet on one node.
func entityID(name, kind string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + fold(name)))
	return "e" + hex.EncodeToString(sum[:])[:16]
}

// putReadouts writes one page's summaries onto a file's sections and its
// mentions of entities. What an earlier version said is gone already: extract
// forgets it (store.forget).
func (s *store) putReadouts(ctx context.Context, id string, reads map[int]readout) error {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return fmt.Errorf("kb: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for sec, r := range reads {
		// What a model wrote is kept as text a column can hold.
		if _, err := tx.ExecContext(ctx, `UPDATE sections SET summary=$1 WHERE file=$2 AND id=$3`, clean(r.summary), id, sec); err != nil {
			return fmt.Errorf("kb: write summary: %w", err)
		}
		for _, e := range r.entities {
			name, kind := clean(e[0]), clean(e[1])
			eid := entityID(name, kind)
			if _, err := tx.ExecContext(ctx, `INSERT INTO entities (id,name,kind) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, eid, name, kind); err != nil {
				return fmt.Errorf("kb: write entity: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO mentions (entity,file,section) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, eid, id, sec); err != nil {
				return fmt.Errorf("kb: write mention: %w", err)
			}
		}
	}
	return tx.Commit()
}
