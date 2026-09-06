package graph

// semantic.go turns a SOURCE into assertions: it reads a document and files what
// the document says into the plane the rest of this package keeps.
//
// WHAT IT ADDS. POST /v1/graph takes assertions a caller has already stated as
// (entity, relation, value). These two take a DOCUMENT and find them — ingest the
// text, cut it into sections, read the relations each section states — and hand
// the result to the same admission the hand-filed assertion goes through. The
// stages are github.com/hanzoai/semantic's Ingester, Splitter and Extractor, so
// replacing one of them moves none of the others.
//
// WHY IT IS NOT ITS OWN CAPABILITY, and therefore not its own address. A capability
// here is a binary and a store (HIP-0139 §1); the address is that binary's name
// (§3, ratcheted in openapi/misfiled.txt). This pipeline owns no store and its
// output is only ever holdable by the assertion plane, so it is not a thing that
// could be deployed, scaled or reasoned about apart from the graph — it is how a
// document is WRITTEN to the graph. A row of its own would also be a second process
// opening the same per-org assertion file, which is the two-writer arrangement
// cloud.OrgStore spends its whole shutdown path preventing. So it is two operations
// under /v1/graph, and nothing here opens a store, resolves a tenant or admits a
// fact on its own: it calls the operation that does.
//
// NOTHING HERE INFERS. An extractor that guessed at relations would file claims
// nobody made into a store with no delete and no update. This one reads what a
// source STATES: `relation:: value` is a stated relation, prose is not, and
// `[[key]]` is how the author says the value names another entity rather than
// being a scalar. A source that needs a reader to interpret it gets no assertions
// and is told so.

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/semantic"
	"github.com/zap-proto/zip"
)

// ── the stages ───────────────────────────────────────────────────────────────

// body is the Ingester. The text arrives ON the request, so there is nothing to
// fetch: a source that has to be fetched is apps/crawl's work, and its output is
// text that reaches this surface the same way anything else does. Fetching here
// would be a second egress path, with a second answer to what this deployment may
// reach.
type body struct{ text string }

func (b body) Ingest(_ context.Context, ref string) ([]semantic.Doc, error) {
	if strings.TrimSpace(b.text) == "" {
		return nil, zip.ErrBadRequest("text is required: there is no source to read")
	}
	return []semantic.Doc{{ID: ref, Source: ref, Text: b.text}}, nil
}

// sections is the Splitter, and it cuts where the SUBJECT changes: a heading names
// the thing the lines beneath it are about, so one section is one heading and its
// body. A fixed window or a blank line would cut between a statement and the
// heading that says whom it is about, and the extractor would then have to carry
// that across chunks to put it back — state the interface does not have and should
// not grow.
type sections struct{}

func (sections) Split(_ context.Context, d semantic.Doc) ([]semantic.Chunk, error) {
	var out []semantic.Chunk
	var cur []string
	cut := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, semantic.Chunk{DocID: d.ID, Index: len(out), Text: strings.Join(cur, "\n")})
		cur = nil
	}
	for _, line := range strings.Split(d.Text, "\n") {
		if heading(line) != "" {
			cut()
		}
		cur = append(cur, line)
	}
	cut()
	return out, nil
}

// stated is the Extractor. subject is what a section with no heading of its own is
// about — the caller's name for the source — so a document that states everything
// before its first heading still says whom it is about.
//
// The object is emitted VERBATIM, brackets and all. Whether a value names an
// entity is a fact about the assertion, not about the extraction, so it is read
// where the assertion is built (graphTriple) and this stage stays a faithful
// record of what the line said.
type stated struct{ subject string }

func (s stated) Extract(_ context.Context, c semantic.Chunk) ([]semantic.Triple, error) {
	subject := s.subject
	var out []semantic.Triple
	for _, line := range strings.Split(c.Text, "\n") {
		if h := heading(line); h != "" {
			subject = h
			continue
		}
		// A leading bullet is list punctuation, not part of the relation: the same
		// statement wearing a dash is the same statement.
		relation, object, ok := strings.Cut(strings.TrimLeft(strings.TrimSpace(line), "-*+ "), "::")
		if !ok {
			continue
		}
		relation, object = strings.TrimSpace(relation), strings.TrimSpace(object)
		if subject == "" || relation == "" || object == "" {
			continue
		}
		// Score is 1 because the reading is EXACT — the line states this and nothing
		// was inferred. It is confidence in the reading, never in the source: ranking
		// one publisher above another is a vocabulary this plane does not hold, which
		// is why Fact's own Rank is constant.
		out = append(out, semantic.Triple{
			Subject: subject, Predicate: relation, Object: object, From: c, Score: 1,
		})
	}
	return out, nil
}

// heading is the subject an ATX heading names, or "" for every other line. The
// space after the hashes is required by the syntax and is what separates a heading
// from a `#tag`, which names no subject and must not become one.
func heading(line string) string {
	t := strings.TrimSpace(line)
	n := len(t) - len(strings.TrimLeft(t, "#"))
	if n == 0 || n > 6 || n == len(t) || t[n] != ' ' {
		return ""
	}
	return strings.TrimSpace(t[n:])
}

// named reads the author's declaration that a value NAMES another entity rather
// than being a scalar: `[[key]]`, the wikilink the org's own vaults are already
// written in (apps/knowledge/links.go). An alias or an anchor is trimmed to the
// bare key, because the assertion points at the entity and not at how a page chose
// to render it.
func named(v string) (string, bool) {
	if !strings.HasPrefix(v, "[[") || !strings.HasSuffix(v, "]]") {
		return v, false
	}
	key := strings.TrimSuffix(strings.TrimPrefix(v, "[["), "]]")
	key, _, _ = strings.Cut(key, "|")
	key, _, _ = strings.Cut(key, "#")
	return strings.TrimSpace(key), true
}

// run runs the pipeline. Parse is nil and the library skips it: the text arrives
// as text, and a stage with nothing to do is a stage that is not there.
func run(ctx context.Context, in *graphSourceIn) ([]semantic.Triple, error) {
	return semantic.Pipeline{
		Ingest:  body{text: in.Text},
		Split:   sections{},
		Extract: stated{subject: strings.TrimSpace(in.Subject)},
	}.Run(ctx, strings.TrimSpace(in.Source))
}

// ── the wire ─────────────────────────────────────────────────────────────────

// graphSourceIn is one source to read. Both operations take it, because they take the
// SAME thing and differ only in what they do with what they found.
type graphSourceIn struct {
	// Source names where the text came from — a URL, a document id, a page title.
	// It is stamped on every assertion as its source, and with the section number
	// as its evidence, so a claim can be traced back to the passage that made it.
	// Required.
	Source string `json:"source"`
	// Text is the document. Relations are read from it and from nothing else: a
	// line written `relation:: value` states one, and prose states none. Required.
	Text string `json:"text"`
	// At is when what the source says was so, RFC 3339. Required by ingest — which
	// records — and read by nothing in extract, which records nothing. Required for
	// the same reason /v1/graph requires it: it is part of the assertion's content
	// address, so re-reading one source at one instant records one set of rows
	// however many times it is delivered. A clock read here instead would append
	// the whole document again on every re-read.
	At string `json:"at"`
	// Subject is the entity the text is about before any heading names one. A
	// document that states relations above its first heading needs it; one whose
	// every section is headed does not.
	Subject string `json:"subject,omitempty"`
}

// graphTriple is one relation the source states.
type graphTriple struct {
	// Subject is the entity the statement is about: the nearest heading above the
	// line, or the request's own subject where no heading has appeared yet.
	Subject string `json:"subject"`
	// Predicate is the relation, exactly as the line spells it before the `::`.
	// This surface holds no vocabulary, so it renames nothing.
	Predicate string `json:"predicate"`
	// Object is what the relation points at. When Names is true it has been
	// unwrapped from its `[[…]]` and is another entity's key.
	Object string `json:"object"`
	// Names is the author's declaration that Object is an entity and the assertion
	// is an EDGE, written `[[key]]`. Absent, the relation is a property of Subject.
	// It is read from the notation and never guessed from the value's shape.
	Names bool `json:"names,omitempty"`
	// Section is which section of the source stated it, counting from zero. It is
	// the second half of every resulting assertion's evidence, `<source>#<section>`.
	Section int `json:"section"`
}

// graphExtractOut is what the source states.
type graphExtractOut struct {
	// Triples are the relations found, in the order the document states them.
	Triples []graphTriple `json:"triples"`
}

// GraphExtract reads a source and returns the relations it states, recording
// nothing. It is how a caller sees what a document would file before the plane —
// which has no update and no delete — has anything filed into it.
//
// A relation is stated as `relation:: value` on its own line; prose states none. A
// value written `[[key]]` names another entity, which makes the assertion an edge.
// The subject is the nearest heading above the line, or the request's `subject`
// until a heading names one.
//
// Example: {"source": "wiki/api", "at": "2026-09-01T00:00:00Z", "text": "# acme/svc/api\nowner:: [[acme/team/core]]\ntier:: 1"}
func (o ops) extract(ctx context.Context, in *graphSourceIn) (*graphExtractOut, error) {
	// The org this caller acts for, refused for the one reason a graph read is
	// refused. Nothing below touches the store, so this asks the principal rather
	// than opening a tenant file to authorize a read that will not use it.
	if _, err := principal.Acting(ctx); err != nil {
		return nil, err
	}
	// A whole-request field that is wrong is refused HERE, as one 400, rather than
	// as one refusal per relation further down: a caller who omitted the source has
	// one mistake and should be told it once.
	if strings.TrimSpace(in.Source) == "" {
		return nil, zip.ErrBadRequest("source is required: an assertion has to name the record it came from")
	}
	found, err := run(ctx, in)
	if err != nil {
		return nil, err
	}
	out := &graphExtractOut{Triples: make([]graphTriple, 0, len(found))}
	for _, t := range found {
		object, names := named(t.Object)
		out.Triples = append(out.Triples, graphTriple{
			Subject: t.Subject, Predicate: t.Predicate, Object: object,
			Names: names, Section: t.From.Index,
		})
	}
	return out, nil
}

// GraphIngest reads a source and records what it states into the calling
// organization's graph — the same store, the same admission and the same content
// address as POST /v1/graph, because this operation ends by calling that one.
//
// Every assertion carries the source it came from and, as its evidence, the
// section that stated it: `<source>#<section>`. Delivering the same source at the
// same `at` twice therefore records one set of rows and reports the rest as
// duplicates, which is the property a retrying importer depends on.
//
// A source that states no relation is refused rather than recorded as an empty
// success: a caller that wrote its document in prose has been told nothing by a
// 200 that filed nothing.
//
// Example: {"source": "wiki/api", "at": "2026-09-01T00:00:00Z", "text": "# acme/svc/api\nowner:: [[acme/team/core]]\ntier:: 1"}
func (o ops) ingest(ctx context.Context, in *graphSourceIn) (*graphAssertOut, error) {
	// Same reason as the source check in extract, and the same parse the assert
	// below will make: an `at` that is not RFC 3339 is one wrong field, not one
	// wrong assertion per relation the document states.
	if _, err := instant(in.At); err != nil {
		return nil, err
	}
	found, err := o.extract(ctx, in)
	if err != nil {
		return nil, err
	}
	if len(found.Triples) == 0 {
		return nil, zip.ErrBadRequest("the source states no relations: a line states one when it is written `relation:: value`")
	}
	source := strings.TrimSpace(in.Source)
	batch := make([]graphFact, 0, len(found.Triples))
	for _, t := range found.Triples {
		batch = append(batch, graphFact{
			Entity: t.Subject, Relation: t.Predicate, Value: t.Object, Names: t.Names,
			At: in.At, Source: source,
			Evidence: fmt.Sprintf("%s#%d", source, t.Section),
			// The reading is exact, so the confidence in it is total. It is a
			// tie-breaker within the order and never a substitute for it.
			Confidence: 1,
		})
	}
	// The one write path. Admission, the derived knowable instant, the content
	// address, the asserter and the tenant file are all resolved there, so an
	// extracted assertion and a hand-filed one cannot be admitted by two rules.
	return o.assert(ctx, &graphAssertIn{Assertions: batch})
}
