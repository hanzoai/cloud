package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

// filegraph.go is the third layer: a graph over the org's files whose nodes are
// documents, sections and entities, and whose edges are what the text itself
// says — a link to another file or to an anchor, a cross-reference to a section
// ("see Section 4.2"), a citation of another file by name — plus the entities
// two sections both name. Edges live in the links table, entities in mentions
// (written by the toc stage); a reference whose target has not been uploaded yet
// is kept unresolved and resolved when that file arrives.

// Link kinds.
const (
	linkHref  = "href"  // a hyperlink in the text
	linkXref  = "xref"  // a cross-reference to a section of the same document
	linkCites = "cites" // the text names another file
)

var (
	xrefRe = regexp.MustCompile(`(?i)\b(section|sections|§|chapter|appendix|clause|article|annex|schedule)\s+([0-9]+(?:\.[0-9]+)*|[A-Z])\b`)
	citeRe = regexp.MustCompile(`(?i)\b[\w][\w.\- ]{0,80}?\.(pdf|docx|doc|md|txt|html?|xlsx|csv|pptx|json|ya?ml)\b`)
)

// sectionAt is the section whose body holds offset at.
func sectionAt(secs []section, at int) int {
	i := sort.Search(len(secs), func(i int) bool { return secs[i].lo > at })
	if i == 0 {
		return 0
	}
	return secs[i-1].id
}

// graphStage resolves a file's links, finds its cross-references and
// citations a page of sections at a time, and resolves every reference other
// files already make to it.
func (x *indexer) graphStage(ctx context.Context, st *store, org string, f File) error {
	_, outline, err := st.outline(ctx, f.ID)
	if err != nil {
		return err
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM links WHERE file=$1 AND kind<>$2`, f.ID, linkHref); err != nil {
		return fmt.Errorf("kb: drop links: %w", err)
	}
	names, err := st.names(ctx)
	if err != nil {
		return err
	}
	// Hyperlinks: an anchor in this document, or another file by name.
	rows, err := st.db.QueryContext(ctx, `SELECT section,label FROM links WHERE file=$1 AND kind=$2`, f.ID, linkHref)
	if err != nil {
		return fmt.Errorf("kb: read links: %w", err)
	}
	type pending struct {
		section int
		label   string
	}
	var hrefs []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.section, &p.label); err != nil {
			_ = rows.Close()
			return fmt.Errorf("kb: read link: %w", err)
		}
		hrefs = append(hrefs, p)
	}
	_ = rows.Close()
	for _, h := range hrefs {
		file, anchor := hrefTarget(h.label)
		target := ""
		switch {
		case file == "" && anchor != "":
			target = f.ID
		case file != "":
			target = names[strings.ToLower(file)]
		}
		if target == "" {
			continue
		}
		sec, err := st.anchorSection(ctx, target, anchor)
		if err != nil {
			return err
		}
		if _, err := st.db.ExecContext(ctx,
			resolveSQL, target, sec, f.ID, h.section, linkHref, h.label); err != nil {
			return fmt.Errorf("kb: resolve link: %w", err)
		}
	}
	// Cross-references and citations, read off each section's text.
	for from := -1; ; {
		page, err := st.page(ctx, f.ID, from)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			break
		}
		if err := x.readLinks(ctx, st, f, outline, names, page); err != nil {
			return err
		}
		from = page[len(page)-1].id
		if err := st.progress(ctx, f.ID, f.etag, int64(from+1), int64(len(outline))); err != nil {
			return err
		}
	}
	return resolveInbound(ctx, st, f)
}

// readLinks records the cross-references and citations in one page of a file's
// sections.
func (x *indexer) readLinks(ctx context.Context, st *store, f File, outline []section, names map[string]string, page []section) error {
	for _, s := range page {
		for _, m := range xrefRe.FindAllStringSubmatch(s.body, 50) {
			if tgt := numbered(outline, m[2], s.id); tgt >= 0 {
				if err := st.link(ctx, f.ID, s.id, linkXref, f.ID, tgt, m[0]); err != nil {
					return err
				}
			}
		}
		for _, m := range citeRe.FindAllString(s.body, 50) {
			name := strings.ToLower(strings.TrimSpace(m))
			if i := strings.LastIndexAny(name, " /"); i >= 0 && names[name] == "" {
				name = strings.TrimSpace(name[i+1:])
			}
			if name == strings.ToLower(f.Name) {
				continue
			}
			if err := st.link(ctx, f.ID, s.id, linkCites, names[name], 0, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveInbound resolves what other files already said about this one: their
// citations of its name and their hyperlinks to it.
func resolveInbound(ctx context.Context, st *store, f File) error {
	// What other files already said about this one.
	lower := strings.ToLower(f.Name)
	if _, err := st.db.ExecContext(ctx,
		`UPDATE links SET target_file=$1 WHERE target_file='' AND kind=$2 AND label=$3 AND file<>$1
AND NOT EXISTS (SELECT 1 FROM links o WHERE o.file=links.file AND o.section=links.section AND o.kind=links.kind
  AND o.target_file=$1 AND o.target_section=links.target_section AND o.label=links.label)`,
		f.ID, linkCites, lower); err != nil {
		return fmt.Errorf("kb: resolve citations: %w", err)
	}
	rows, err := st.db.QueryContext(ctx,
		`SELECT file,section,label FROM links WHERE target_file='' AND kind=$1 AND file<>$2 AND lower(label) LIKE lower($3)`,
		linkHref, f.ID, "%"+likeEscape(lower)+"%")
	if err != nil {
		return fmt.Errorf("kb: read links: %w", err)
	}
	type inbound struct {
		file    string
		section int
		label   string
	}
	var in []inbound
	for rows.Next() {
		var r inbound
		if err := rows.Scan(&r.file, &r.section, &r.label); err != nil {
			_ = rows.Close()
			return fmt.Errorf("kb: read link: %w", err)
		}
		in = append(in, r)
	}
	_ = rows.Close()
	for _, r := range in {
		file, anchor := hrefTarget(r.label)
		if strings.ToLower(file) != lower {
			continue
		}
		sec, err := st.anchorSection(ctx, f.ID, anchor)
		if err != nil {
			return err
		}
		if _, err := st.db.ExecContext(ctx,
			resolveSQL, f.ID, sec, r.file, r.section, linkHref, r.label); err != nil {
			return fmt.Errorf("kb: resolve link: %w", err)
		}
	}
	return nil
}

// resolveSQL points one unresolved hyperlink at its target, unless a link by that name
// already points there: the key of a link holds its target, so the move would be a second
// row of the same key, and the link is left unresolved beside the one that is.
const resolveSQL = `UPDATE links SET target_file=$1, target_section=$2 WHERE file=$3 AND section=$4 AND kind=$5 AND label=$6 AND target_file=''
AND NOT EXISTS (SELECT 1 FROM links o WHERE o.file=$3 AND o.section=$4 AND o.kind=$5 AND o.label=$6 AND o.target_file=$1 AND o.target_section=$2)`

func likeEscape(s string) string {
	return strings.NewReplacer(`%`, ``, `_`, ``).Replace(s)
}

// link writes one edge; target "" keeps it unresolved under its label.
func (s *store) link(ctx context.Context, file string, section int, kind, target string, tsec int, label string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO links (file,section,kind,target_file,target_section,label) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`,
		file, section, kind, target, tsec, clean(clip(strings.ToLower(strings.TrimSpace(label)), 200)))
	if err != nil {
		return fmt.Errorf("kb: write link: %w", err)
	}
	return nil
}

// names maps every file's lower-cased name to its id (the most recent wins).
func (s *store) names(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name FROM files ORDER BY updated`)
	if err != nil {
		return nil, fmt.Errorf("kb: read file names: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("kb: read file name: %w", err)
		}
		out[strings.ToLower(name)] = id
	}
	return out, rows.Err()
}

// anchorSection is the section of a file whose anchor is anchor, or 0 — the
// document itself — when there is none.
func (s *store) anchorSection(ctx context.Context, file, anchor string) (int, error) {
	if anchor == "" {
		return 0, nil
	}
	var id int
	err := s.db.QueryRowContext(ctx, `SELECT id FROM sections WHERE file=$1 AND anchor=$2 LIMIT 1`, file, anchor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// hrefTarget is the file name and fragment a hyperlink names: the last segment
// of its path, decoded, and its anchor.
func hrefTarget(href string) (file, anchor string) {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", ""
	}
	if p := u.Path; p != "" && !strings.HasSuffix(p, "/") {
		file = path.Base(p)
		if dec, err := url.PathUnescape(file); err == nil {
			file = dec
		}
	}
	return file, u.Fragment
}

// numbered is the section of secs a cross-reference label names: one whose
// title begins with the label as a number ("4.2 Retention", "Section 4.2 …",
// "Appendix A …"), never the section the reference sits in.
func numbered(secs []section, label string, from int) int {
	label = strings.ToLower(label)
	for _, s := range secs {
		if s.id == from || s.id == 0 {
			continue
		}
		t := strings.ToLower(s.title)
		for _, prefix := range []string{"", "section ", "§ ", "§", "chapter ", "appendix ", "clause ", "article ", "annex ", "schedule ", "part "} {
			if rest, ok := strings.CutPrefix(t, prefix+label); ok && (rest == "" || strings.IndexAny(rest[:1], " .:)—-") == 0) {
				return s.id
			}
		}
	}
	return -1
}

// ---- reading it ----

// neighbor is a section the graph reaches from another, and why.
type neighbor struct {
	file    string
	section int
	kind    string // href, xref, cites, or entity
	label   string // the link's text, or the entities the two sections share
	weight  int
}

// neighbors are the sections the graph reaches from the given sections of one
// file: what they link to, what links to them, and the sections of other files
// that name the most of the same entities.
func (s *store) neighbors(ctx context.Context, file string, secs []int, limit int) ([]neighbor, error) {
	if len(secs) == 0 {
		return nil, nil
	}
	// The sections are numbered once, after the file, and every statement that names them
	// names those marks.
	in := dialect.Marks(2, len(secs))
	args := func(head ...any) []any {
		for _, v := range secs {
			head = append(head, v)
		}
		return head
	}
	var out []neighbor
	q := func(sqlText string, a []any, scan func(*sql.Rows) (neighbor, error)) error {
		rows, err := s.db.QueryContext(ctx, sqlText, a...)
		if err != nil {
			return fmt.Errorf("kb: read graph: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			n, err := scan(rows)
			if err != nil {
				return fmt.Errorf("kb: read graph: %w", err)
			}
			if n.file != file || !containsInt(secs, n.section) {
				out = append(out, n)
			}
		}
		return rows.Err()
	}
	if err := q(`SELECT target_file,target_section,kind,label FROM links WHERE file=$1 AND section IN (`+in+`) AND target_file<>''`,
		args(file), func(r *sql.Rows) (neighbor, error) {
			n := neighbor{weight: 3}
			return n, r.Scan(&n.file, &n.section, &n.kind, &n.label)
		}); err != nil {
		return nil, err
	}
	if err := q(`SELECT file,section,kind,label FROM links WHERE target_file=$1 AND (target_section=0 OR target_section IN (`+in+`))`,
		args(file), func(r *sql.Rows) (neighbor, error) {
			n := neighbor{weight: 2}
			return n, r.Scan(&n.file, &n.section, &n.kind, &n.label)
		}); err != nil {
		return nil, err
	}
	if err := q(`SELECT m2.file, m2.section, COUNT(*) AS n, string_agg(e.name, ', ' ORDER BY e.name)
		FROM mentions m1 JOIN mentions m2 ON m1.entity=m2.entity JOIN entities e ON e.id=m1.entity
		WHERE m1.file=$1 AND m1.section IN (`+in+`) AND NOT (m2.file=$1 AND m2.section IN (`+in+`))
		GROUP BY m2.file, m2.section ORDER BY n DESC, m2.file, m2.section LIMIT $`+strconv.Itoa(len(secs)+2),
		append(args(file), limit), func(r *sql.Rows) (neighbor, error) {
			n := neighbor{kind: "entity"}
			return n, r.Scan(&n.file, &n.section, &n.weight, &n.label)
		}); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].weight > out[j].weight })
	seen := map[string]bool{}
	dedup := out[:0]
	for _, n := range out {
		k := n.file + "#" + strconv.Itoa(n.section)
		if seen[k] {
			continue
		}
		seen[k] = true
		dedup = append(dedup, n)
	}
	if len(dedup) > limit {
		dedup = dedup[:limit]
	}
	return dedup, nil
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---- the surface ----

// fileLink is one edge between this file and another.
type fileLink struct {
	// File is the other file, by id.
	File string `json:"file"`
	// Name is the other file's name.
	Name string `json:"name"`
	// Direction is out when this file links to the other, in when the other
	// links to this one, and shared when the two name the same entities.
	Direction string `json:"direction"`
	// Kind is href (a hyperlink), cites (the text names the file), xref (a
	// cross-reference inside one document) or entity (shared mentions).
	Kind string `json:"kind"`
	// Label is the link's own text, or the entities the two files share.
	Label string `json:"label"`
	// Weight is how many edges of this kind join the two; for shared entities,
	// how many entities they share.
	Weight int `json:"weight"`
}

// fileEntity is one entity a file names.
type fileEntity struct {
	// Name is the entity as the text names it.
	Name string `json:"name"`
	// Kind is person, org, product, place or term.
	Kind string `json:"kind"`
	// Sections is how many of the file's sections name it.
	Sections int `json:"sections"`
	// Files is how many other files of the org name it too.
	Files int `json:"files"`
}

// fileGraph is a file's place in the org's graph.
type fileGraph struct {
	// File is the file the graph is of.
	File File `json:"file"`
	// Links are the other files it is joined to, strongest first.
	Links []fileLink `json:"links"`
	// Entities are the entities it names, most mentioned first.
	Entities []fileEntity `json:"entities"`
}

// FileGraph answers one of the caller's org files' place in the org's graph:
// the files it links to and is linked from (a hyperlink, a citation by name),
// the files that name the same entities, and the entities it names. It is how
// an agent finds the other documents a question about this one needs.
func (o ops) fileGraph(ctx context.Context, in *fileRef) (*fileGraph, error) {
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
	out := &fileGraph{File: f, Links: []fileLink{}, Entities: []fileEntity{}}
	queries := []struct {
		dir, q string
	}{
		// Each edge's labels are listed once apiece, in order: the rows are grouped by label
		// first and the labels joined after, since neither engine takes a DISTINCT list
		// with a separator.
		{"out", `SELECT t.other, f.name, t.kind, string_agg(t.label, ',' ORDER BY t.label), CAST(SUM(t.n) AS BIGINT)
			FROM (SELECT l.target_file AS other, l.kind AS kind, l.label AS label, COUNT(*) AS n FROM links l
			  WHERE l.file=$1 AND l.target_file<>'' AND l.target_file<>l.file GROUP BY l.target_file, l.kind, l.label) t
			JOIN files f ON f.id=t.other GROUP BY t.other, f.name, t.kind`},
		{"in", `SELECT t.other, f.name, t.kind, string_agg(t.label, ',' ORDER BY t.label), CAST(SUM(t.n) AS BIGINT)
			FROM (SELECT l.file AS other, l.kind AS kind, l.label AS label, COUNT(*) AS n FROM links l
			  WHERE l.target_file=$1 AND l.file<>l.target_file GROUP BY l.file, l.kind, l.label) t
			JOIN files f ON f.id=t.other GROUP BY t.other, f.name, t.kind`},
		{"shared", `SELECT s.other, f.name, 'entity', string_agg(e.name, ',' ORDER BY e.name), COUNT(*)
			FROM (SELECT DISTINCT m2.file AS other, m1.entity AS entity
			  FROM mentions m1 JOIN mentions m2 ON m1.entity=m2.entity AND m2.file<>m1.file WHERE m1.file=$1) s
			JOIN entities e ON e.id=s.entity JOIN files f ON f.id=s.other
			GROUP BY s.other, f.name ORDER BY 5 DESC, s.other LIMIT 25`},
	}
	for _, q := range queries {
		rows, err := st.db.QueryContext(ctx, q.q, f.ID)
		if err != nil {
			return nil, fmt.Errorf("kb: read graph: %w", err)
		}
		for rows.Next() {
			l := fileLink{Direction: q.dir}
			if err := rows.Scan(&l.File, &l.Name, &l.Kind, &l.Label, &l.Weight); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("kb: read graph: %w", err)
			}
			l.Label = clip(l.Label, 300)
			out.Links = append(out.Links, l)
		}
		_ = rows.Close()
	}
	sort.SliceStable(out.Links, func(i, j int) bool { return out.Links[i].Weight > out.Links[j].Weight })
	rows, err := st.db.QueryContext(ctx, `SELECT e.name, e.kind, COUNT(DISTINCT m.section),
		(SELECT COUNT(DISTINCT m2.file) FROM mentions m2 WHERE m2.entity=e.id AND m2.file<>$1)
		FROM mentions m JOIN entities e ON e.id=m.entity WHERE m.file=$1 GROUP BY e.id, e.name, e.kind ORDER BY 3 DESC, 4 DESC, e.name LIMIT 50`, f.ID)
	if err != nil {
		return nil, fmt.Errorf("kb: read entities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var e fileEntity
		if err := rows.Scan(&e.Name, &e.Kind, &e.Sections, &e.Files); err != nil {
			return nil, fmt.Errorf("kb: read entity: %w", err)
		}
		out.Entities = append(out.Entities, e)
	}
	return out, rows.Err()
}
