package knowledge

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/hanzoai/cloud/apps/framework"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
)

// index.go is the ONE per-org vector-write + search path for KB knowledge. Every
// knowledge document — a manual wiki page, an AI memory, a connector-ingested doc
// — flows through THIS file and nowhere else: the save and trash hooks (hooks.go)
// and a build (build.go) call sync, and retrieval (retrieve.go, and Semantic for
// /v1/search) calls searchDoc. Connectors never fork this path; they produce
// framework documents and the SAME hook indexes them.
//
// Store: the org's own SQLite file (store.go). There is no vector database: a
// document is cut into passages (split.go), each passage embedded and kept as one
// row, and a search scores the org's passages in Go (internal/vec).
//
// Embeddings: the ai plane (deps.Embed, the read-scope credential), the SAME model
// for index and query, sent vec.BatchSize passages at a time, in ONE space
// (internal/vec): the model at its revision, the dimension it answers, the
// normalization and the task prefixes. The org's store records that space with
// its first vector and refuses a write or a query of any other, so changing
// CLOUD_EMBED_MODEL or CLOUD_EMBED_REVISION leaves the vector leg refused (and
// the surfaces degraded to the lexical leg) until POST /v1/knowledge/reindex
// rebuilds it — never compared across spaces.
//
// THE FAILURE CONTRACT, in one direction for both surfaces:
//
//   - A WRITE never fails on the index. The hooks log an indexing error and the
//     document stands. A document whose passages could not be embedded is left
//     with NONE — its old passages are removed rather than kept describing text it
//     no longer has — so it is absent from the vector leg, never wrong in it, until
//     it is saved again or the org is reindexed.
//
//   - A READ returns the error. searchDoc and Semantic fail when the query cannot
//     be embedded, the store cannot be read or has not been built yet (build.go),
//     and never answer an empty that a caller would take for "nothing matches".
//     What the caller SEES is the surface's decision: /v1/knowledge/search answers
//     200 with degraded:true and whatever the lexical leg found; /v1/search reports
//     the vector leg degraded beside the legs that answered.
//
//   - A READ answers documents, not rows. Both legs are indexes OF the framework
//     store, so every hit is read back from the document it names (present in
//     retrieve.go) and dropped when the store holds no such document or the asker
//     may not see it.
//
//   - A process that links this package without mounting it has no vector leg: a
//     hook there hands the document to the knowledge peer (hooks.go), and a read
//     is refused, which both callers report as disabled. plugin/framework is that
//     process for the hooks, plugin/search for Semantic (mounted_gate_test.go).

// indexer is the vector leg of one process: the ai plane it embeds through, the
// model it embeds with, the org stores it keeps passages in, the per-document
// locks its writes take, and the builds in flight. It holds no org: the org is a
// parameter on every call, so one indexer serves every tenant and an org can
// never be captured from a stale field.
type indexer struct {
	ai infer.Client
	// chat reads a picture and a recording for a file's ingest (extract.go): the
	// completions client, which carries vision and speech where the read-only
	// embed client does not.
	chat infer.Client
	// model is the embedding model asked for; space is what its vectors are in
	// (vec.Policy), with the dimension it answers.
	model  string
	space  vec.Space
	stores *cloud.OrgStore[*store]
	log    luxlog.Logger

	docs stripes

	// ctx bounds every build to the mount: Shutdown cancels it and waits on wg.
	ctx    context.Context
	stop   context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	builds map[string]chan struct{} // org → closed when that org's build ends

	q queue // the file ingest's durable queue (ingest.go)
}

// stripes is a fixed set of mutexes that a document's identity hashes onto, so
// two writes of one document always meet on one mutex, and the set does not grow
// with the documents it has seen.
type stripes [64]sync.Mutex

func (s *stripes) of(org string, dt framework.ID, name string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(org + "\x00" + dt.String() + "\x00" + name))
	return &s[h.Sum32()%uint32(len(s))]
}

// idx is this process's vector leg, set by Use. It is nil in a process that
// links knowledge without mounting it: the hooks there hand their documents to
// the knowledge peer instead (hooks.go), and enabled and deindexProvider answer
// for a nil one.
var idx *indexer

// enabled reports whether this process can embed: knowledge is mounted and has an
// ai plane client. Without one there is no vector leg — indexing is a no-op and a
// search is refused — which the surfaces report as disabled, never as broken.
func (x *indexer) enabled() bool { return x != nil && x.ai != nil }

// errNoVectors refuses a vector read in a process with no vector leg.
var errNoVectors = errors.New("kb: no vector leg in this process")

// storeFor opens the org's store through orgns.OrgNamespace, the one path a
// validated org takes to a file.
func (x *indexer) storeFor(org string) (*store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return x.stores.For(ns)
}

// open is the org's store, and the build of one that has never been built: it
// starts that build, or finds it running, and answers the channel that closes
// when it ends. A built store, or one in a process that cannot embed, answers a
// nil channel.
func (x *indexer) open(org string) (*store, <-chan struct{}, error) {
	st, err := x.storeFor(org)
	if err != nil || st.built.Load() || !x.enabled() {
		return st, nil, err
	}
	return st, x.building(org, st), nil
}

// docText renders a knowledge document to the plain text the indexer embeds. It is
// doctype-aware (title + the body/content field) and strips a Lexical RichText body
// to its text runs so the embedding is over prose, not editor JSON. Empty text ⇒
// nothing to index.
func docText(dt framework.ID, title string, data map[string]any) string {
	var b strings.Builder
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n\n")
	}
	switch dt {
	case DTPage:
		b.WriteString(lexicalText(str(data["body"])))
	case DTMemory:
		b.WriteString(str(data["content"]))
	case DTSource:
		b.WriteString(str(data["body"]))
	}
	return strings.TrimSpace(b.String())
}

// sync makes one document's two legs match the framework store: its lexical row
// and its passages, both from ONE read of the document, under the document's own
// lock. gone is a document the trash hook is deleting, whose legs go whatever the
// store still holds.
//
// It reads the store rather than taking the text a hook was handed, because that
// is what orders two overlapping saves of one document: either may finish
// embedding first, but the one that takes the lock last reads the store last, so
// the text that lands is the text the document has. A build takes the same lock
// and the same read, so a build and a save converge in either order.
func (x *indexer) sync(ctx context.Context, org string, dt framework.ID, name string, gone bool) error {
	mu := x.docs.of(org, dt, name)
	mu.Lock()
	defer mu.Unlock()
	var (
		doc framework.Document
		ok  bool
	)
	if !gone {
		docs, err := current(ctx, org, dt, []string{name})
		if err != nil {
			return err
		}
		doc, ok = docs[name]
	}
	if !ok {
		return errors.Join(lexicalRemove(ctx, org, dt, name), x.drop(ctx, org, dt, name))
	}
	return errors.Join(lexicalPut(ctx, org, dt, name, doc.Data), x.indexDoc(ctx, org, dt, name, doc.Data))
}

// current reads the named documents of one doctype as the framework store holds
// them now, by name; a name the store does not hold is absent from the answer.
func current(ctx context.Context, org string, dt framework.ID, names []string) (map[string]framework.Document, error) {
	docs, err := framework.Among(ctx, org, dt, map[string][]string{"name": names}, len(names))
	if err != nil {
		return nil, err
	}
	out := make(map[string]framework.Document, len(docs))
	for _, d := range docs {
		out[d.Name] = d
	}
	return out, nil
}

// indexDoc makes a document's passages match its data: split, embed, and replace
// whatever passages it had, in one transaction. sync calls it under the
// document's lock; see the failure contract above for what an error leaves
// behind. A document with no text has no passages.
func (x *indexer) indexDoc(ctx context.Context, org string, dt framework.ID, name string, data map[string]any) error {
	if !x.enabled() {
		return nil
	}
	st, _, err := x.open(org)
	if err != nil {
		return err
	}
	title := str(data["title"])
	text := docText(dt, title, data)
	spans := split(text)
	if len(spans) == 0 {
		return st.drop(ctx, dt.String(), name)
	}
	passages := make([]string, len(spans))
	for i, sp := range spans {
		passages[i] = text[sp.lo:sp.hi]
	}
	// An index of another space refuses before an embedding is bought that no
	// write would keep; the passages go, as for any write that cannot embed.
	if err := kbIndex.Admit(ctx, st.db, x.space); err != nil {
		return errors.Join(fmt.Errorf("kb: %w", err), st.drop(ctx, dt.String(), name))
	}
	vecs, err := x.embed(ctx, org, "", passages)
	if err != nil {
		return errors.Join(fmt.Errorf("embed: %w", err), st.drop(ctx, dt.String(), name))
	}
	if err := st.put(ctx, dt.String(), name, metaOf(data), x.space, spans, vecs); err != nil {
		if errors.Is(err, vec.ErrMixed) {
			return errors.Join(err, st.drop(ctx, dt.String(), name))
		}
		return err
	}
	return nil
}

// drop removes a document's passages. It needs no ai plane, so a mounted store is
// cleaned on delete even where nothing can be embedded.
func (x *indexer) drop(ctx context.Context, org string, dt framework.ID, name string) error {
	st, _, err := x.open(org)
	if err != nil {
		return err
	}
	return st.drop(ctx, dt.String(), name)
}

// deindexProvider removes every passage a connector's documents contributed (used
// on connector disconnect).
func (x *indexer) deindexProvider(ctx context.Context, org, provider string) error {
	if x == nil {
		return nil
	}
	st, err := x.storeFor(org)
	if err != nil {
		return err
	}
	return st.dropProvider(ctx, provider)
}

// hit is one semantic-search result: enough to cite and open the source, never a
// full copy of the document body.
type hit struct {
	// DocType is which kind of knowledge matched, by address: kb.page (a wiki
	// page), kb.memory (a unit of agent memory) or kb.source (a document a
	// connector ingested).
	// Those three are the whole indexed set, and searchIn.DocTypes filters on them.
	DocType string `json:"doctype"`
	// Name is the document's name in the framework store — the id to read or open
	// it with. Unique per (org, doctype), so it identifies the document with
	// DocType and not alone.
	Name string `json:"name"`
	// Title is the document's title. Empty for a document saved without one; it
	// is a label to show, never the id (that is Name).
	Title string `json:"title"`
	// Project is the project scope the document was saved under. Absent for a
	// document saved with none, which is also why a project-scoped query cannot
	// reach it.
	Project string `json:"project,omitempty"`
	// Provider is the connector that ingested the document — github, slack, google
	// or notion. Absent for a page or memory written in the product, which came
	// from no connector.
	Provider string `json:"provider,omitempty"`
	// URL is the document's link back into the app it was ingested from. Absent
	// when the document carries none, which is the normal case for pages and
	// memories.
	URL string `json:"url,omitempty"`
	// Text is the passage that matched, read from the document by present for a
	// reranker to score; it is never on the wire.
	Text string `json:"-"`
	// Score is the cosine similarity between the query's embedding and the
	// document's BEST-matching passage, from -1 to 1, higher being closer. A
	// document is cut into passages of about 2000 bytes, each embedded on its own,
	// so a fact deep in a long page scores as high as one in its first paragraph.
	// 0 for a document only the lexical leg found, which has no similarity to
	// report. There is no absolute cutoff: what counts as a good score moves with
	// the query and the embedding model, so compare scores within one response and
	// not across queries.
	Score float64 `json:"score"`

	// lo and hi are the matching passage's byte span of the text the document had
	// when it was indexed.
	lo, hi int
}

// passage is a hit's matching passage of the document's CURRENT text. A document
// whose latest save has not reached its passages yet may have moved since it was
// indexed; a span that no longer lands on the text is not guessed at, and the
// whole text stands in for it.
func (h hit) passage(text string) string {
	if h.lo < 0 || h.hi > len(text) || h.lo >= h.hi || !utf8.RuneStart(text[h.lo]) ||
		h.hi < len(text) && !utf8.RuneStart(text[h.hi]) {
		return text
	}
	return text[h.lo:h.hi]
}

// searchReq is a parsed, org-scoped retrieval query. org is set by the handler from
// principal.Org — NEVER from a client field — so a caller can only ever search
// its OWN knowledge.
type searchReq struct {
	org      string
	query    string
	limit    int
	project  string   // optional scope filter
	doctypes []string // optional restriction (default: all indexed doctypes)
	// subject is the asking person, or "" for the org itself; it decides whose
	// owned documents are in reach. Set by the caller from the validated
	// principal, never from a client field.
	subject string
}

// window is the number of documents a request asks for: its limit, defaulting to
// 10 and bounded at 50.
func (r searchReq) window() int {
	if r.limit <= 0 || r.limit > 50 {
		return 10
	}
	return r.limit
}

// searchDoc runs the org-scoped vector leg: embed the query with the SAME model,
// then score the passages in reach in the org's own store (store.nearest), each
// document answered once by its best passage. An error is returned, never an
// empty, when the query cannot be embedded, the store cannot be read, or its
// build has not ended within buildWait.
func (x *indexer) searchDoc(ctx context.Context, req searchReq) ([]hit, error) {
	if !x.enabled() {
		return nil, errNoVectors
	}
	if strings.TrimSpace(req.query) == "" {
		return []hit{}, nil
	}
	st, pending, err := x.open(req.org)
	if err != nil {
		return nil, err
	}
	if err := await(ctx, st, pending); err != nil {
		return nil, err
	}
	// An index of another space refuses before the query's vector is bought;
	// nearest asks again with the dimension the model answered.
	if err := kbIndex.Admit(ctx, st.db, x.space); err != nil {
		return nil, fmt.Errorf("kb: %w", err)
	}
	q, err := x.embedQuery(ctx, req.org, req.project, req.query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	return st.nearest(ctx, q, reachOf{
		space:    x.space,
		project:  req.project,
		doctypes: req.doctypes,
		subject:  req.subject,
	}, req.window())
}

// embed returns one vector per text, embedded as documents of x.space, through
// the ai plane — the SAME org/project-aligned, metered path chat and apps/code
// run through; there is no embed-specific key or endpoint. Every vector must be
// non-empty and all of one dimension, or none is kept: no space holds a batch of
// mixed dimension.
func (x *indexer) embed(ctx context.Context, org, project string, texts []string) ([][]float32, error) {
	vecs, _, err := x.space.Documents(texts, x.batch(ctx, org, project))
	if err != nil {
		return nil, fmt.Errorf("kb: embeddings: %w", err)
	}
	if len(vecs) != len(texts) {
		return nil, fmt.Errorf("kb: embeddings: %d vectors for %d texts", len(vecs), len(texts))
	}
	return vecs, nil
}

// embedQuery is a query's vector, embedded as a query of x.space.
func (x *indexer) embedQuery(ctx context.Context, org, project, query string) ([]float32, error) {
	q, _, err := x.space.Query(query, x.batch(ctx, org, project))
	if err != nil {
		return nil, fmt.Errorf("kb: embeddings: %w", err)
	}
	if q == nil {
		return nil, errors.New("kb: embeddings: no vector for the query")
	}
	return q, nil
}

// batch sends texts to the ai plane vec.BatchSize at a time.
func (x *indexer) batch(ctx context.Context, org, project string) func([]string) ([][]float32, error) {
	return func(texts []string) ([][]float32, error) {
		return vec.Batch(texts, func(batch []string) ([][]float32, error) {
			return x.ai.Embed(ctx, &infer.EmbedRequest{Model: x.model, Inputs: batch, Org: org, Payer: cloud.PayerOf(ctx).Wallet, Project: project})
		})
	}
}
