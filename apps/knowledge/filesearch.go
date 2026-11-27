package knowledge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	lexical "github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/search/rank"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// filesearch.go reads the three layers back, the way a reader works through a
// shelf: search finds the passages that match; the table of contents says where
// in a document to look; a section opens; the graph points at the next
// document. ToC-FIRST retrieval composes them — a model reads the candidate
// documents' tables of contents and picks the sections, hybrid search drills
// into the passages of those sections, and the graph expands to the sections
// they link to or share entities with — and every passage it answers cites its
// document › section › paragraph.

// fileBrief is the part of a file a passage cites.
type fileBrief struct {
	// ID is the file's id.
	ID string `json:"id"`
	// Name is the file's name.
	Name string `json:"name"`
	// Type is the file's media type.
	Type string `json:"type"`
	// Bucket and Key are where its bytes are: GET
	// /v1/s3/buckets/{bucket}/objects/{key} answers a signed download URL.
	Bucket string `json:"bucket"`
	// Key is the object's key in Bucket.
	Key string `json:"key"`
}

func briefOf(f File) fileBrief {
	return fileBrief{ID: f.ID, Name: f.Name, Type: f.Type, Bucket: f.Bucket, Key: f.Key}
}

// sectionBrief is a section as a passage cites it.
type sectionBrief struct {
	// ID is the section's number in its file's table of contents; 0 is the
	// document itself.
	ID int `json:"id"`
	// Title is the section's heading.
	Title string `json:"title"`
	// Path is the section's place in its document: the titles from the
	// document's own down to it, joined by " › ".
	Path string `json:"path"`
}

// citedPassage is one passage of one file, with where it is.
type citedPassage struct {
	// File is the file the passage is from.
	File fileBrief `json:"file"`
	// Section is the section it is in.
	Section sectionBrief `json:"section"`
	// Part is the passage's place in its section, from 1.
	Part int `json:"part"`
	// Text is the passage: a span of its section of about 2000 bytes, never
	// crossing into another section.
	Text string `json:"text"`
	// Cite is how an answer cites it: file › section path › ¶part.
	Cite string `json:"cite"`
	// Score is the fused rank score, comparable within one response only.
	Score float64 `json:"score"`
	// Via is how retrieval reached it: search (it matched), toc (it is in a
	// section the table of contents pointed at) or graph (a linked section).
	Via string `json:"via"`
	// Why says, for a passage reached through the graph, which edge led there.
	Why string `json:"why,omitempty"`
}

// ---- the hybrid search ----

// scope is what a search reads: some files, optionally only some of their
// sections.
type scope struct {
	files    map[string]File
	sections map[string]map[int]bool
}

func (sc scope) holds(file string, section int) bool {
	if _, ok := sc.files[file]; !ok {
		return false
	}
	if sc.sections == nil {
		return true
	}
	return sc.sections[file][section]
}

// sectionIDs is the sections of file a scope reads, in order.
func (sc scope) sectionIDs(file string) []int {
	out := make([]int, 0, len(sc.sections[file]))
	for id, ok := range sc.sections[file] {
		if ok {
			out = append(out, id)
		}
	}
	sort.Ints(out)
	return out
}

// phit is one scored passage.
type phit struct {
	file                       string
	seq, section, part, lo, hi int
	score                      float64
}

func (h phit) key() string { return passageKey(h.file, h.seq) }

// hybrid runs both legs over the passages in scope — every passage scored
// against the embedded query, and the full-text index — and fuses them by rank.
// degraded is true when a leg failed and the answer is what the other found.
func (x *indexer) hybrid(ctx context.Context, st *store, org, project, query string, sc scope, k int) ([]phit, bool, error) {
	if len(sc.files) == 0 {
		return nil, false, nil
	}
	var (
		lists    []rank.List[string]
		found    = map[string]phit{}
		degraded bool
	)
	if x.enabled() {
		hits, err := x.vectorLeg(ctx, st, org, project, query, sc, k*2)
		if err != nil {
			degraded = true
			x.log.Warn("kb file search: vector leg failed", "org", org, "err", err)
		} else {
			l := rank.List[string]{Source: legVector}
			for _, h := range hits {
				l.Keys, l.Scores = append(l.Keys, h.key()), append(l.Scores, h.score)
				found[h.key()] = h
			}
			lists = append(lists, l)
		}
	}
	// Inside sections, the words are scored here, over those sections'
	// passages, by the index's own rule: asking the org's whole index and
	// keeping the few rows that fall in them missed them in a large workspace.
	if sc.sections != nil {
		hits, err := wordsIn(ctx, st, query, sc, k*2)
		if err != nil {
			return nil, degraded, err
		}
		l := rank.List[string]{Source: legLexical}
		for _, h := range hits {
			l.Keys, l.Scores = append(l.Keys, h.key()), append(l.Scores, h.score)
			if _, ok := found[h.key()]; !ok {
				found[h.key()] = h
			}
		}
		lists = append(lists, l)
		return fuse(ctx, st, lists, found, k, degraded)
	}
	// The full-text index answers for the whole org; a search in a few files
	// reads deeper to find the rows that are theirs.
	rows, err := lexical.Query(ctx, org, filesIndex, query, []string{""}, k*8, 0)
	switch {
	case err == nil:
		l := rank.List[string]{Source: legLexical}
		for _, raw := range rows {
			var row struct {
				File    string `json:"file"`
				Section int    `json:"section"`
				Seq     int    `json:"seq"`
			}
			if json.Unmarshal(raw, &row) != nil || !sc.holds(row.File, row.Section) {
				continue
			}
			h := phit{file: row.File, seq: row.Seq, section: row.Section, lo: -1}
			l.Keys, l.Scores = append(l.Keys, h.key()), append(l.Scores, 0)
			if _, ok := found[h.key()]; !ok {
				found[h.key()] = h
			}
			if len(l.Keys) >= k*2 {
				break
			}
		}
		lists = append(lists, l)
	case noLexical(err):
	default:
		degraded = true
		x.log.Warn("kb file search: lexical leg failed", "org", org, "err", err)
	}
	return fuse(ctx, st, lists, found, k, degraded)
}

// fuse ranks the legs' lists together and gives every passage its span.
func fuse(ctx context.Context, st *store, lists []rank.List[string], found map[string]phit, k int, degraded bool) ([]phit, bool, error) {
	var out []phit
	for _, f := range rank.RRF(lists, k) {
		h := found[f.Key]
		if h.lo < 0 { // a lexical row carries no span: read it off the passage
			err := st.db.QueryRowContext(ctx, `SELECT section,part,lo,hi FROM file_passages WHERE file=$1 AND seq=$2`, h.file, h.seq).
				Scan(&h.section, &h.part, &h.lo, &h.hi)
			if errors.Is(err, sql.ErrNoRows) {
				continue // a row the index still holds for a passage re-cut since
			}
			if err != nil {
				return nil, degraded, fmt.Errorf("kb: read passage: %w", err)
			}
		}
		h.score = f.Score
		out = append(out, h)
	}
	return out, degraded, nil
}

// wordsIn scores the passages of a scope's sections by how many of the query's
// words each holds (the index's rule, index.Coverage), reading only those
// sections: the words leg of a search inside a few sections.
func wordsIn(ctx context.Context, st *store, query string, sc scope, k int) ([]phit, error) {
	var out []phit
	for file := range sc.files {
		ids := sc.sectionIDs(file)
		if len(ids) == 0 {
			continue
		}
		args := []any{file}
		for _, id := range ids {
			args = append(args, id)
		}
		in := `(` + dialect.Marks(2, len(ids)) + `)`
		bodies := map[int]section{}
		rows, err := st.db.QueryContext(ctx, `SELECT id,title,body FROM sections WHERE file=$1 AND id IN `+in, args...)
		if err != nil {
			return nil, fmt.Errorf("kb: read sections: %w", err)
		}
		for rows.Next() {
			var s section
			if err := rows.Scan(&s.id, &s.title, &s.body); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("kb: read section: %w", err)
			}
			bodies[s.id] = s
		}
		_ = rows.Close()
		rows, err = st.db.QueryContext(ctx, `SELECT seq,section,part,lo,hi FROM file_passages WHERE file=$1 AND section IN `+in, args...)
		if err != nil {
			return nil, fmt.Errorf("kb: read passages: %w", err)
		}
		for rows.Next() {
			h := phit{file: file}
			if err := rows.Scan(&h.seq, &h.section, &h.part, &h.lo, &h.hi); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("kb: read passage: %w", err)
			}
			s := bodies[h.section]
			if n := lexical.Coverage(query, s.title+"\n"+hit{lo: h.lo, hi: h.hi}.passage(s.body)); n > 0 {
				h.score = float64(n)
				out = append(out, h)
			}
		}
		_ = rows.Close()
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].seq < out[j].seq
	})
	return out[:min(len(out), k)], nil
}

// vectorLeg scores every embedded passage in scope against the query and keeps
// the k best.
// queryVectors holds a query's vector for the length of one retrieve, which
// searches once per section it reads and once per link it follows: the model
// costs seconds a call, and the query does not change between them.
type queryVectors struct {
	mu sync.Mutex
	m  map[string][]float32
}

type queryVectorsKey struct{}

// queryEmbedWait bounds how long a search waits for its question's vector.
const queryEmbedWait = 12 * time.Second

// withQueryVectors gives ctx a place to keep the query vectors it computes.
func withQueryVectors(ctx context.Context) context.Context {
	return context.WithValue(ctx, queryVectorsKey{}, &queryVectors{m: map[string][]float32{}})
}

// queryVector embeds a query, or answers the vector this ctx already holds for it.
func (x *indexer) queryVector(ctx context.Context, org, project, query string) ([]float32, error) {
	held, _ := ctx.Value(queryVectorsKey{}).(*queryVectors)
	if held != nil {
		held.mu.Lock()
		v, ok := held.m[query]
		held.mu.Unlock()
		if ok {
			return v, nil
		}
	}
	// A question waits a bounded while for its vector; past it, the search
	// answers by its words, said as degraded, rather than holding the turn.
	qctx, cancel := context.WithTimeout(ctx, queryEmbedWait)
	defer cancel()
	q, err := x.embedQuery(qctx, org, project, query)
	if err != nil {
		return nil, err
	}
	if held != nil {
		held.mu.Lock()
		held.m[query] = q
		held.mu.Unlock()
	}
	return q, nil
}

func (x *indexer) vectorLeg(ctx context.Context, st *store, org, project, query string, sc scope, k int) ([]phit, error) {
	// An index of another space refuses before the question's vector is bought.
	if err := kbIndex.Admit(ctx, st.db, x.space); err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	qv, err := x.queryVector(ctx, org, project, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	q := [][]float32{qv}
	// A question of another space than the org's index is refused, never
	// scored; the search says so as degraded and answers by words.
	qs, err := x.space.Of(qv)
	if err != nil {
		return nil, fmt.Errorf("kb: query: %w", err)
	}
	if err := kbIndex.Admit(ctx, st.db, qs); err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	all := map[string]phit{}
	// One read per file, and inside sections only those sections' passages:
	// the index on (file, section) answers them, where reading every vector of
	// a large file and dropping most of them cost seconds a drill.
	for id := range sc.files {
		query, args := `SELECT file,seq,section,part,lo,hi,vec FROM file_passages WHERE file=$1 AND model=$2 AND length(vec)=$3`,
			[]any{id, qs.Model, len(vec.Encode(q[0]))}
		if sc.sections != nil {
			ids := sc.sectionIDs(id)
			if len(ids) == 0 {
				continue
			}
			query += ` AND section IN (` + dialect.Marks(4, len(ids)) + `)`
			for _, s := range ids {
				args = append(args, s)
			}
		}
		if err := func() error {
			rows, err := st.db.QueryContext(ctx, query, args...)
			if err != nil {
				return fmt.Errorf("kb: read passages: %w", err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var (
					h   phit
					raw sql.RawBytes
				)
				if err := rows.Scan(&h.file, &h.seq, &h.section, &h.part, &h.lo, &h.hi, &raw); err != nil {
					return fmt.Errorf("kb: read passage: %w", err)
				}
				v, err := vec.Decode(raw)
				if err != nil {
					return fmt.Errorf("kb: passage %s: %w", h.key(), err)
				}
				if h.score, err = vec.Cos(q[0], v); err != nil {
					return fmt.Errorf("kb: passage %s: %w", h.key(), err)
				}
				all[h.key()] = h
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	top := vec.Top(func(yield func(string, float64) bool) {
		for key, h := range all {
			if !yield(key, h.score) {
				return
			}
		}
	}, k)
	out := make([]phit, len(top))
	for i, t := range top {
		out[i] = all[t.Key]
	}
	return out, nil
}

// cite renders scored passages with their text and where they are.
func cite(ctx context.Context, st *store, files map[string]File, hits []phit, via string) ([]citedPassage, error) {
	tocs := map[string]map[int]section{}
	bodies := map[string]string{}
	out := make([]citedPassage, 0, len(hits))
	for _, h := range hits {
		toc, ok := tocs[h.file]
		if !ok {
			toc = map[int]section{}
			tocs[h.file] = toc
		}
		if err := st.lineage(ctx, h.file, h.section, toc); err != nil {
			return nil, err
		}
		bk := h.file + "#" + strconv.Itoa(h.section)
		body, ok := bodies[bk]
		if !ok {
			if err := st.db.QueryRowContext(ctx, `SELECT body FROM sections WHERE file=$1 AND id=$2`, h.file, h.section).Scan(&body); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				return nil, fmt.Errorf("kb: read section: %w", err)
			}
			bodies[bk] = body
		}
		f := files[h.file]
		s := toc[h.section]
		out = append(out, citedPassage{
			File:    briefOf(f),
			Section: sectionBrief{ID: s.id, Title: s.title, Path: trail(toc, s.id)},
			Part:    h.part + 1,
			Text:    hit{lo: h.lo, hi: h.hi}.passage(body),
			Cite:    citation(f.Name, toc, s.id, h.part+1),
			Score:   h.score,
			Via:     via,
		})
	}
	return out, nil
}

// citation is "file › section path › ¶n", the document's own title left out
// because the file's name stands for it.
func citation(name string, toc map[int]section, id, part int) string {
	parts := []string{name}
	if id != 0 {
		p := strings.Split(trail(toc, id), " › ")
		if len(p) > 1 {
			parts = append(parts, p[1:]...)
		}
	}
	return strings.Join(append(parts, "¶"+strconv.Itoa(part)), " › ")
}

// inReach maps the files a request reaches — the named ones, or every file the
// org holds — narrowed to a project when asked.
func inReach(ctx context.Context, st *store, ids []string, project string) (map[string]File, error) {
	org, _ := principal.Acting(ctx)
	var fs []File
	if len(ids) > 0 {
		for _, id := range ids {
			f, err := st.file(ctx, strings.TrimSpace(id))
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return nil, err
			}
			fs = append(fs, f)
		}
	} else {
		var err error
		if fs, err = st.files(ctx, "", 1000); err != nil {
			return nil, err
		}
	}
	out := map[string]File{}
	for _, f := range fs {
		if (project == "" || f.Project == project) && readsFile(ctx, org, f) {
			out[f.ID] = f
		}
	}
	return out, nil
}

// ---- search ----

// fileSearchIn is POST /v1/knowledge/files/search. There is no org field: the
// org is the validated tenant.
type fileSearchIn struct {
	// Query is the question or phrase. Required.
	Query string `json:"query"`
	// Files restricts the search to these file ids. Empty searches every file
	// the org holds.
	Files []string `json:"files,omitempty"`
	// Project restricts the search to files indexed under one project scope.
	Project string `json:"project,omitempty"`
	// Limit bounds the passages returned. Default 8, maximum 50.
	Limit int `json:"limit,omitempty"`
}

// fileSearchOut is the passages that match, best first.
type fileSearchOut struct {
	// Passages are the matching passages, best first, at most Limit.
	Passages []citedPassage `json:"passages"`
	// Degraded is true when a leg failed — the query could not be embedded, or
	// the full-text index could not be read — and Passages are what the other
	// leg found.
	Degraded bool `json:"degraded,omitempty"`
}

func limitOf(n, def, most int) int {
	if n <= 0 || n > most {
		return def
	}
	return n
}

// SearchFiles answers the passages of the caller's org files that match a query,
// each citing its file › section › paragraph: the search behind Drive's box,
// and the first step an agent takes across a workspace. Name file ids to search
// only those files; name none to search them all. A semantic leg compares the
// query with every embedded passage and a full-text leg finds the passages
// holding its words; the two are fused, so a passage both found comes first.
//
// Example: {"query": "termination notice period", "limit": 5}
func (o ops) searchFiles(ctx context.Context, in *fileSearchIn) (*fileSearchOut, error) {
	org, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, zip.ErrBadRequest("query is required")
	}
	files, err := inReach(ctx, st, in.Files, strings.TrimSpace(in.Project))
	if err != nil {
		return nil, err
	}
	hits, degraded, err := idx.hybrid(ctx, st, org, in.Project, query, scope{files: files}, limitOf(in.Limit, 8, 50))
	if err != nil {
		return nil, err
	}
	ps, err := cite(ctx, st, files, hits, "search")
	if err != nil {
		return nil, err
	}
	return &fileSearchOut{Passages: ps, Degraded: degraded}, nil
}

// ---- table of contents ----

// tocEntry is one node of a file's table of contents.
type tocEntry struct {
	// ID is the section's number; 0 is the document itself.
	ID int `json:"id"`
	// Parent is the id of the section it sits in; -1 for the document.
	Parent int `json:"parent"`
	// Level is its depth: 0 for the document, 1 for a top-level heading.
	Level int `json:"level"`
	// Title is its heading.
	Title string `json:"title"`
	// Summary is a one-line account of what it covers.
	Summary string `json:"summary,omitempty"`
	// Size is how many bytes of the document it spans, subsections included.
	Size int `json:"size"`
	// Synthetic is true for a section the index cut out of a long run of text
	// the document gave no structure to.
	Synthetic bool `json:"synthetic,omitempty"`
}

// tocOut is a file's table of contents.
type tocOut struct {
	// File is the file it is of.
	File File `json:"file"`
	// Sections are its nodes in document order, the document first; Parent
	// makes them a tree.
	Sections []tocEntry `json:"sections"`
}

// FileToc answers one of the caller's org files' table of contents: every
// section in document order with its depth, its parent and a one-line summary.
// It is the map a reader — a person in Drive, or an agent deciding where to
// look — reads before opening a section with GET
// /v1/knowledge/files/{id}/sections/{section}.
func (o ops) fileToc(ctx context.Context, in *fileRef) (*tocOut, error) {
	_, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	f, err := st.file(ctx, strings.TrimSpace(in.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such file")
	}
	if err != nil {
		return nil, err
	}
	secs, err := st.sectionsOf(ctx, f.ID, false)
	if err != nil {
		return nil, err
	}
	out := &tocOut{File: f, Sections: make([]tocEntry, 0, len(secs))}
	for _, s := range secs {
		out.Sections = append(out.Sections, tocEntry{ID: s.id, Parent: s.parent, Level: s.level, Title: s.title,
			Summary: s.summary, Size: s.hi - s.lo, Synthetic: s.synthetic})
	}
	return out, nil
}

// ---- open a section ----

// sectionRef addresses one section of one file.
type sectionRef struct {
	// ID is the file's id.
	ID string `json:"id"`
	// Section is the section's number in the file's table of contents.
	Section string `json:"section"`
}

// sectionLink is a section the graph reaches from the one opened.
type sectionLink struct {
	// File is the file the linked section is in.
	File fileBrief `json:"file"`
	// Section is the linked section.
	Section sectionBrief `json:"section"`
	// Kind is href, xref, cites, or entity (the two sections name the same
	// entities).
	Kind string `json:"kind"`
	// Label is the link's text, or the entities the two share.
	Label string `json:"label"`
}

// sectionOut is one section, opened.
type sectionOut struct {
	// File is the file it is in.
	File fileBrief `json:"file"`
	// Section is where it is.
	Section sectionBrief `json:"section"`
	// Summary is its one-line account.
	Summary string `json:"summary,omitempty"`
	// Text is its own text — up to its first subsection — at most 64 KB.
	Text string `json:"text"`
	// Children are its subsections, to open next.
	Children []tocEntry `json:"children"`
	// Links are the sections the graph reaches from it, strongest first.
	Links []sectionLink `json:"links"`
	// Entities are the entities it names.
	Entities []fileEntity `json:"entities"`
}

// OpenSection answers one section of one of the caller's org files: its own
// text, its subsections, the sections it links to or shares entities with —
// in this file or another — and the entities it names. Open the document
// itself with section 0.
func (o ops) openSection(ctx context.Context, in *sectionRef) (*sectionOut, error) {
	_, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	f, err := st.file(ctx, strings.TrimSpace(in.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, zip.ErrNotFound("no such file")
	}
	if err != nil {
		return nil, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(in.Section))
	if err != nil || id < 0 {
		return nil, zip.ErrBadRequest("section is a section number from the table of contents")
	}
	secs, err := st.sectionsOf(ctx, f.ID, false)
	if err != nil {
		return nil, err
	}
	toc := make(map[int]section, len(secs))
	for _, s := range secs {
		toc[s.id] = s
	}
	s, ok := toc[id]
	if !ok {
		return nil, zip.ErrNotFound("no such section")
	}
	var body string
	if err := st.db.QueryRowContext(ctx, `SELECT body FROM sections WHERE file=$1 AND id=$2`, f.ID, id).Scan(&body); err != nil {
		return nil, fmt.Errorf("kb: read section: %w", err)
	}
	out := &sectionOut{
		File: briefOf(f), Section: sectionBrief{ID: id, Title: s.title, Path: trail(toc, id)},
		Summary: s.summary, Text: clip(body, 64<<10), Children: []tocEntry{}, Links: []sectionLink{}, Entities: []fileEntity{},
	}
	for _, c := range secs {
		if c.parent == id {
			out.Children = append(out.Children, tocEntry{ID: c.id, Parent: c.parent, Level: c.level, Title: c.title, Summary: c.summary, Size: c.hi - c.lo, Synthetic: c.synthetic})
		}
	}
	ns, err := st.neighbors(ctx, f.ID, []int{id}, 12)
	if err != nil {
		return nil, err
	}
	links, err := describeNeighbors(ctx, st, ns)
	if err != nil {
		return nil, err
	}
	out.Links = links
	rows, err := st.db.QueryContext(ctx, `SELECT e.name, e.kind,
		(SELECT COUNT(DISTINCT m2.file) FROM mentions m2 WHERE m2.entity=e.id AND m2.file<>$1)
		FROM mentions m JOIN entities e ON e.id=m.entity WHERE m.file=$1 AND m.section=$2 ORDER BY e.name LIMIT 50`, f.ID, id)
	if err != nil {
		return nil, fmt.Errorf("kb: read entities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		e := fileEntity{Sections: 1}
		if err := rows.Scan(&e.Name, &e.Kind, &e.Files); err != nil {
			return nil, fmt.Errorf("kb: read entity: %w", err)
		}
		out.Entities = append(out.Entities, e)
	}
	return out, rows.Err()
}

// describeNeighbors renders graph neighbors with their files and section paths.
func describeNeighbors(ctx context.Context, st *store, ns []neighbor) ([]sectionLink, error) {
	tocs := map[string]map[int]section{}
	out := []sectionLink{}
	for _, n := range ns {
		f, err := st.file(ctx, n.file)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		toc, ok := tocs[n.file]
		if !ok {
			secs, err := st.sectionsOf(ctx, n.file, false)
			if err != nil {
				return nil, err
			}
			toc = map[int]section{}
			for _, s := range secs {
				toc[s.id] = s
			}
			tocs[n.file] = toc
		}
		s, ok := toc[n.section]
		if !ok {
			continue
		}
		out = append(out, sectionLink{File: briefOf(f), Section: sectionBrief{ID: s.id, Title: s.title, Path: trail(toc, s.id)},
			Kind: n.kind, Label: clip(n.label, 200)})
	}
	return out, nil
}

// ---- ToC-first retrieval ----

// retrieveIn is POST /v1/knowledge/files/retrieve.
type retrieveIn struct {
	// Query is the question to answer. Required.
	Query string `json:"query"`
	// Files are the files the question is about — the files a chat message
	// references. Empty asks the whole workspace. The graph may still reach
	// files beyond them.
	Files []string `json:"files,omitempty"`
	// Project restricts retrieval to files indexed under one project scope.
	Project string `json:"project,omitempty"`
	// Limit bounds the passages drilled out of the chosen sections. Default 8,
	// maximum 24. The graph adds up to four more.
	Limit int `json:"limit,omitempty"`
}

// pickedSection is a section the table of contents pointed retrieval at.
type pickedSection struct {
	// File is the file it is in.
	File fileBrief `json:"file"`
	// Section is where it is.
	Section sectionBrief `json:"section"`
	// Summary is its one-line account.
	Summary string `json:"summary,omitempty"`
}

// retrieveOut is what an answer is grounded in.
type retrieveOut struct {
	// Sections are the sections chosen from the candidate documents' tables of
	// contents.
	Sections []pickedSection `json:"sections"`
	// Passages are the passages drilled out of those sections, then those the
	// graph reached, each citing file › section › paragraph.
	Passages []citedPassage `json:"passages"`
	// Picked says how the sections were chosen: whole when the documents were
	// short enough to read every section, model when a model read the tables of
	// contents, search when the sections of the best passages stood in — the
	// documents had no headings to choose by, or no model answered.
	Picked string `json:"picked"`
	// Degraded is true when a search leg failed along the way.
	Degraded bool `json:"degraded,omitempty"`
}

const pickPrompt = `You choose where in a set of documents the answer to a question is found. Below are the tables of contents of the candidate documents. Each section is labeled D<doc>.S<section> with its title and a one-line summary; indentation shows nesting. Reply with ONLY a JSON object {"pick":["D1.S4","D2.S0"]} naming up to %d sections most likely to contain the answer. Prefer the most specific section; name a parent only when the answer spans its subsections.

Question: %s
`

// RetrieveFiles grounds an answer in the caller's org files, table of contents
// first: candidate documents are found by searching their passages; a model
// reads their tables of contents — titles and one-line summaries — and picks
// the sections the answer is in; hybrid search drills into the passages of
// those sections; and the graph expands to the sections they link to or name
// the same entities as, in any file of the workspace. Every passage cites its
// file › section › paragraph, so an answer can say exactly where it came from.
// This is what a chat runs before it answers about an attached file, and what
// an agent runs before it answers about a workspace.
//
// Example: {"query": "how long are backups retained, and who audits that?", "files": ["f3a9c0d1e2b4f5a6b7c8"]}
func (o ops) retrieveFiles(ctx context.Context, in *retrieveIn) (*retrieveOut, error) {
	org, st, err := tenantStore(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, zip.ErrBadRequest("query is required")
	}
	return idx.retrieve(ctx, st, org, strings.TrimSpace(in.Project), query, in.Files, limitOf(in.Limit, 8, 24))
}

func (x *indexer) retrieve(ctx context.Context, st *store, org, project, query string, ids []string, limit int) (*retrieveOut, error) {
	ctx = withQueryVectors(ctx)
	files, err := inReach(ctx, st, ids, project)
	if err != nil {
		return nil, err
	}
	out := &retrieveOut{Sections: []pickedSection{}, Passages: []citedPassage{}, Picked: "search"}
	if len(files) == 0 {
		return out, nil
	}
	// 1. Candidates: the documents the best passages are in, the named ones first.
	seed, degraded, err := x.hybrid(ctx, st, org, project, query, scope{files: files}, 24)
	if err != nil {
		return nil, err
	}
	out.Degraded = degraded
	var docs []string
	add := func(id string) {
		if id != "" && !contains(docs, id) && len(docs) < 5 {
			docs = append(docs, id)
		}
	}
	for _, id := range ids {
		if _, ok := files[strings.TrimSpace(id)]; ok {
			add(strings.TrimSpace(id))
		}
	}
	for _, h := range seed {
		add(h.file)
	}
	// 2. The tables of contents, and a model's pick.
	tocs := map[string]map[int]section{}
	ordered := map[string][]section{}
	for _, id := range docs {
		secs, err := st.sectionsOf(ctx, id, false)
		if err != nil {
			return nil, err
		}
		toc := map[int]section{}
		for _, s := range secs {
			toc[s.id] = s
		}
		tocs[id], ordered[id] = toc, secs
	}
	// A handful of sections is read whole: a model choosing among them costs
	// seconds and can only leave one out. Documents with no headings of their
	// own (a log, a page of plain text: parts cut by size) give a model nothing
	// to choose by, and the search's sections stand in.
	var picked []string
	switch {
	case sectionsIn(ordered) <= wholeMax:
		for _, id := range docs {
			for _, s := range ordered[id] {
				picked = append(picked, id+"#"+strconv.Itoa(s.id))
			}
		}
		out.Picked = "whole"
	case headings(ordered) > 0:
		if picked = x.pick(ctx, org, project, query, docs, files, ordered, seed); len(picked) > 0 {
			out.Picked = "model"
		}
	}
	// The search's best sections are read beside the model's pick: a pick can
	// pass over the section the words matched, and the passage that answers must
	// not be lost to it. With no pick, the search's sections are the whole read.
	room := 6
	if len(picked) > 0 {
		room = len(picked) + 2
	}
	for _, h := range seed {
		k := h.file + "#" + strconv.Itoa(h.section)
		if _, ok := tocs[h.file]; ok && !contains(picked, k) && len(picked) < room {
			picked = append(picked, k)
		}
	}
	// 3. Drill into the passages of each chosen section and its subsections,
	// and take them in turn — every section's best passage, then every
	// section's second — so a section the model chose is never crowded out by
	// another whose words happen to match the question more often.
	chosen := map[string][]int{}
	var each [][]phit
	for _, k := range picked {
		file, n, _ := strings.Cut(k, "#")
		id, _ := strconv.Atoi(n)
		s, ok := tocs[file][id]
		if !ok {
			continue
		}
		one := scope{files: map[string]File{file: files[file]}, sections: map[string]map[int]bool{file: {}}}
		for _, d := range ordered[file] {
			if d.id == id || d.lo >= s.lo && d.hi <= s.hi && d.level > s.level {
				one.sections[file][d.id] = true
			}
		}
		// A section holding most of a large file (a log's root, its thousands of
		// parts under it) is searched as the file is: through the index, not
		// section by section.
		if len(one.sections[file]) > drillMax {
			one.sections = nil
		}
		hits, deg, err := x.hybrid(ctx, st, org, project, query, one, limit)
		if err != nil {
			return nil, err
		}
		out.Degraded = out.Degraded || deg
		each = append(each, hits)
		chosen[file] = append(chosen[file], id)
		out.Sections = append(out.Sections, pickedSection{File: briefOf(files[file]),
			Section: sectionBrief{ID: id, Title: s.title, Path: trail(tocs[file], id)}, Summary: s.summary})
	}
	drilled := inTurn(each, limit)
	if len(drilled) == 0 {
		drilled = seed[:min(len(seed), limit)]
	}
	ps, err := cite(ctx, st, files, drilled, "toc")
	if err != nil {
		return nil, err
	}
	out.Passages = append(out.Passages, ps...)
	// 4. Expand along the graph, into any file of the workspace.
	seen := map[string]bool{}
	for _, h := range drilled {
		seen[h.key()] = true
	}
	all, err := inReach(ctx, st, nil, project)
	if err != nil {
		return nil, err
	}
	graphed := 0
	for file, secs := range chosen {
		ns, err := st.neighbors(ctx, file, secs, 6)
		if err != nil {
			return nil, err
		}
		for _, n := range ns {
			if graphed == 4 {
				break
			}
			f, ok := all[n.file]
			if !ok {
				continue
			}
			// A link to a whole file (a citation of it by name) is followed into
			// its best passage for the question; a link to a section, into that
			// section.
			nsc, k := scope{files: map[string]File{n.file: f}}, 3
			if n.section > 0 {
				nsc.sections, k = map[string]map[int]bool{n.file: {n.section: true}}, 1
			}
			hits, _, err := x.hybrid(ctx, st, org, project, query, nsc, k)
			if err != nil {
				return nil, err
			}
			// Into a whole file, the best passage under a heading — never the
			// document's bare title, which answers nothing.
			for i, h := range hits {
				if h.section > 0 {
					hits = hits[i : i+1]
					break
				}
			}
			hits = hits[:min(len(hits), 1)]
			if len(hits) == 0 {
				var h phit
				err := st.db.QueryRowContext(ctx, `SELECT seq,section,part,lo,hi FROM file_passages WHERE file=$1 AND section=$2 ORDER BY seq LIMIT 1`,
					n.file, n.section).Scan(&h.seq, &h.section, &h.part, &h.lo, &h.hi)
				if err != nil {
					continue
				}
				h.file = n.file
				hits = []phit{h}
			}
			if seen[hits[0].key()] {
				continue
			}
			seen[hits[0].key()] = true
			gp, err := cite(ctx, st, all, hits, "graph")
			if err != nil {
				return nil, err
			}
			for i := range gp {
				gp[i].Why = n.kind + ": " + clip(n.label, 120)
			}
			out.Passages = append(out.Passages, gp...)
			graphed += len(gp)
		}
	}
	return out, nil
}

// drillMax is the most sections one drill reads itself; a section with more
// under it is searched as its whole file is.
const drillMax = 200

// wholeMax is how many sections the candidate documents may hold together and
// be read whole, with no model choosing among them.
const wholeMax = 8

// headings counts the candidate documents' own headings: the sections below a
// document's root that its text named, not ones cut by size.
func headings(ordered map[string][]section) int {
	n := 0
	for _, secs := range ordered {
		for _, s := range secs {
			if s.parent >= 0 && !s.synthetic {
				n++
			}
		}
	}
	return n
}

// sectionsIn counts the sections of the candidate documents.
func sectionsIn(ordered map[string][]section) int {
	n := 0
	for _, secs := range ordered {
		n += len(secs)
	}
	return n
}

// pickWait bounds how long retrieval waits for the model's ToC pick. A pick of
// a few dozen sections answers in about a second (measured: 0.8–1.0 s on
// zen5-flash); past four, the search's own sections answer sooner than the
// model would.
const pickWait = 4 * time.Second

// pick asks a model which sections of the candidate documents hold the answer,
// answering "file#section" keys, or none when no model answers usefully.
func (x *indexer) pick(ctx context.Context, org, project, query string, docs []string, files map[string]File, ordered map[string][]section, seed []phit) []string {
	if x.chat == nil || len(docs) == 0 {
		return nil
	}
	const most = 6
	hot := map[string]bool{}
	for _, h := range seed {
		hot[h.file+"#"+strconv.Itoa(h.section)] = true
	}
	var b strings.Builder
	fmt.Fprintf(&b, pickPrompt, most, query)
	for i, id := range docs {
		secs := ordered[id]
		byID := map[int]section{}
		for _, s := range secs {
			byID[s.id] = s
		}
		// The sections shown: every shallow one, and every one a passage the
		// search found sits in, with its ancestors — at most 80.
		show := map[int]bool{}
		for _, s := range secs {
			if s.level <= 2 {
				show[s.id] = true
			}
		}
		for _, s := range secs {
			if hot[id+"#"+strconv.Itoa(s.id)] {
				for p, ok := s, true; ok; p, ok = byID[p.parent] {
					show[p.id] = true
					if p.parent < 0 {
						break
					}
				}
			}
		}
		fmt.Fprintf(&b, "\nD%d: %s\n", i+1, files[id].Name)
		// What the search found, and the sections above it, are shown first: a
		// long file has more shallow sections than the 80 lines hold, and a pick
		// that never sees the section the words matched cannot choose it.
		lit := map[int]bool{}
		for _, s := range secs {
			if hot[id+"#"+strconv.Itoa(s.id)] {
				for p, ok := s, true; ok; p, ok = byID[p.parent] {
					lit[p.id] = true
					if p.parent < 0 {
						break
					}
				}
			}
		}
		n := len(lit)
		for _, s := range secs {
			if !lit[s.id] && (!show[s.id] || n >= 80) {
				continue
			}
			if !lit[s.id] {
				n++
			}
			fmt.Fprintf(&b, "%sD%d.S%d %s — %s\n", strings.Repeat("  ", s.level), i+1, s.id, s.title, clip(s.summary, 160))
		}
	}
	// The pick waits a bounded while: an answer waits on retrieval, and the
	// search's own sections are a good stand-in for a model that is slow.
	pctx, cancel := context.WithTimeout(ctx, pickWait)
	defer cancel()
	res, err := x.chat.ChatCompletion(pctx, &infer.ChatRequest{
		Model: filesModel(), Prompt: b.String(), Org: org, Payer: cloud.PayerOf(ctx).Wallet, Project: project, MaxTokens: 3000,
	})
	if err != nil {
		x.log.Warn("kb retrieve: the ToC pick failed; the search's sections stand in", "org", org, "err", err)
		return nil
	}
	var reply struct {
		Pick []string `json:"pick"`
	}
	if err := json.Unmarshal([]byte(jsonOf(res.Content)), &reply); err != nil {
		return nil
	}
	var out []string
	for _, label := range reply.Pick {
		var d, s int
		if _, err := fmt.Sscanf(strings.TrimSpace(label), "D%d.S%d", &d, &s); err != nil || d < 1 || d > len(docs) {
			continue
		}
		k := docs[d-1] + "#" + strconv.Itoa(s)
		if !contains(out, k) && len(out) < most {
			out = append(out, k)
		}
	}
	return out
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// inTurn takes passages from each list in turn — every list's first, then every
// list's second — once each, until limit.
func inTurn(each [][]phit, limit int) []phit {
	var out []phit
	seen := map[string]bool{}
	for round := 0; len(out) < limit; round++ {
		more := false
		for _, hits := range each {
			if round >= len(hits) {
				continue
			}
			more = true
			if h := hits[round]; !seen[h.key()] && len(out) < limit {
				seen[h.key()] = true
				out = append(out, h)
			}
		}
		if !more {
			break
		}
	}
	return out
}
