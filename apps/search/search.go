package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/search/rank"
	"github.com/hanzoai/cloud/client"
	codepeer "github.com/hanzoai/cloud/client/code"
	indexpeer "github.com/hanzoai/cloud/client/index"
	knowledgepeer "github.com/hanzoai/cloud/client/knowledge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Backend names. One constant per leg so the wire value is declared once and the
// provenance a client reads always matches the status it reads.
const (
	BackendIndex  = "index"  // lexical, apps/index
	BackendVector = "vector" // semantic, apps/knowledge's passages in the org's own store
	BackendCode   = "code"   // the org's own repositories, apps/code
	BackendFiles  = "files"  // the org's workspace files' passages, apps/knowledge's file index
	BackendRerank = "rerank" // the cross-encoder pass over the fused window, through the AI gateway
)

// Backend statuses — four DISTINCT operational facts, never collapsed:
//   - ok        the leg ran and answered.
//   - degraded  the leg is configured but FAILED. Carries the error.
//   - disabled  the leg is not provisioned in this deployment. Not a fault.
//   - skipped   the caller's mode excluded the leg. Not a fault.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
	StatusDisabled = "disabled"
	StatusSkipped  = "skipped"
)

// Search modes. `auto` is the default and resolves to hybrid when both kinds of
// retrieval are available, else to whichever is.
//
// The modes name RETRIEVAL KINDS, not legs: `text` runs every lexical leg (the
// index and the code corpus), `semantic` runs the vector one. A caller chooses how
// to search, never which subsystem answers — which is the whole point of one address.
const (
	ModeAuto     = "auto"
	ModeText     = "text"
	ModeSemantic = "semantic"
	ModeHybrid   = "hybrid"
)

// defaultIndex is the lexical index a query uses when the caller names none.
const defaultIndex = "kb"

// Request is the ONE query shape. There is deliberately no `org` field: the tenant
// is the validated principal, so a caller can never search another org by asking.
type Request struct {
	// Query is the natural-language or keyword query. Required.
	Query string `json:"query"`
	// Mode selects the legs: auto (default) | text | semantic | hybrid.
	Mode string `json:"mode,omitempty"`
	// Project narrows to one project scope within the org.
	Project string `json:"project,omitempty"`
	// DocTypes restricts the semantic leg to a subset of indexed knowledge types.
	DocTypes []string `json:"doctypes,omitempty"`
	// Index names the lexical index to query. Defaults to "kb".
	Index string `json:"index,omitempty"`
	// Limit bounds the FUSED result set (default 10, max 50).
	Limit int `json:"limit,omitempty"`
	// Offset pages the fused result set.
	Offset int `json:"offset,omitempty"`
}

// Match is PROVENANCE: one backend's contribution to one result. A fused ranking
// without this is undebuggable — you cannot distinguish a hit both legs agreed on
// from a hit only one leg saw, nor tell a healthy leg from one quietly returning
// nothing.
type Provenance struct {
	// Backend is the leg that contributed this match: "index" (lexical), "vector"
	// (semantic), "code" (the org's repositories) or "rerank" (the cross-encoder
	// pass, whose Score is the relevance it assigned). It is the same name that
	// leg reports itself under in Fusion.Backends, so a hit can be traced to a
	// status.
	Backend string `json:"backend"`
	// Rank is this document's 1-based position in THAT leg's own result list,
	// before fusion — 1 is the leg's best hit. It is the only input to the fused
	// score: RRF adds 1/(60+rank) per leg, which is why a document two legs ranked
	// second beats one a single leg ranked first.
	Rank int `json:"rank"`
	// Score is the leg's NATIVE score, on that leg's own scale, reported for
	// explanation and never used in ranking — the scales are incomparable (a cosine
	// similarity against a term-match count), which is why fusion works on ranks.
	// The vector leg reports the cosine similarity of the document's best-matching
	// passage; the lexical leg exposes no per-row score and reports 0, meaning
	// "unscored", not "scored zero".
	Score float64 `json:"score"`
}

// Hit is one fused hit. Score is the FUSED score (see fuse.go); each backend's
// native score stays in Matched, because the two are different things and
// flattening them loses the ability to explain a ranking.
type Hit struct {
	// ID is the document's identity inside its corpus — the KB document name from
	// the semantic leg, or a lexical row's own name/id/_id (falling back to
	// "row-<n>" when the row carries none). It is unique with DocType, not alone:
	// the pair is the key the two legs are fused on.
	ID string `json:"id"`
	// Corpus is which store the document lives in: "kb" for a document either
	// knowledge leg returned, "code" for a span out of one of the org's own
	// repositories, "files" for a passage of one of the org's workspace files
	// (ID is then the file's id and Title its citation). It is PROVENANCE — read it to say where a hit came from, not
	// to branch on: the fused ranking is what decides order, and a caller that
	// filters by corpus wants the backend's own endpoint instead.
	Corpus string `json:"corpus"`
	// DocType is the knowledge doctype: kb.page, kb.memory or kb.source from the
	// semantic leg, and a lexical row's own doctype/type field otherwise. Absent
	// when the row carried neither.
	DocType string `json:"doctype,omitempty"`
	// Title is the document's display title — the indexed title from the semantic
	// leg, the row's title (falling back to its name) from the lexical one. Absent
	// when the document has none.
	Title string `json:"title,omitempty"`
	// URL is where the document can be opened, carried from the indexed payload —
	// the link back to the app a connector ingested it from. Absent for anything
	// written in the product, which has no external address.
	URL string `json:"url,omitempty"`
	// Project is the project scope the document was indexed under. Absent for a
	// document saved with none; Request.Project filters the semantic leg on it.
	Project string `json:"project,omitempty"`
	// Score is the FUSED score, not a relevance or a similarity: Reciprocal Rank
	// Fusion sums 1/(60+rank) over each leg that returned the document, so it is
	// bounded by roughly 1/61 per leg (about 0.033 for a document two legs put
	// first) and hits are ordered by it, descending. Being built from ranks, it is
	// comparable only WITHIN one response — never across queries, and never against
	// a backend's own score, which stays in Matched.
	Score float64 `json:"score"`
	// Matched is one entry per leg that returned this document, with that leg's
	// rank and native score. More than one entry means the legs AGREED, which is
	// exactly why the hit outranks one a single leg found. Never empty on a
	// returned hit.
	Matched []Provenance `json:"matched"`
	// Passage is the matching passage, for a hit from the files corpus: the span
	// of the file's section that matched, about 2000 bytes. Absent for every
	// other corpus, whose hits name a document rather than a span of one.
	Passage string `json:"passage,omitempty"`
	// text is what a reranker reads: the document as its leg carried it.
	text string
}

// BackendStatus reports one leg's outcome. It is present for EVERY leg on EVERY
// response, including the ones that were skipped, so a client never has to infer
// from absence.
type BackendStatus struct {
	// Name is which leg this reports: "index", the lexical store, "vector", the
	// semantic one, "code", the org's own repositories, or "rerank", the
	// relevance pass over the fused window. Match.Backend uses the same names.
	Name string `json:"name"`
	// Status is one of ok, degraded, disabled, skipped — four distinct operational
	// facts that are never collapsed. It ran and answered; it is configured and
	// FAILED (Error says how, and only this one is a fault); this deployment never
	// provisioned it; or the request's mode excluded it.
	Status string `json:"status"`
	// Hits is how many results this leg returned, counted BEFORE fusion, so it is
	// not the number that survived into Fusion.Hits — fusion merges what both
	// legs found and the caller's limit and offset then page it. 0 for a leg that
	// did not run.
	Hits int `json:"hits"`
	// TookMS is how long this leg took, in milliseconds, timed around its own call
	// and excluding fusion. 0 for a leg that was skipped or is disabled, since
	// nothing was called.
	TookMS int64 `json:"took_ms"`
	// Error is the failure text from a leg whose status is degraded — the reason a
	// configured backend could not answer. Absent otherwise.
	Error string `json:"error,omitempty"`
	// Capped is true when the leg answered from only PART of its corpus because it
	// stopped at its scan cap — the code leg compares a query with at most 50000
	// stored vectors — so a match past the cap was never seen. The status is still
	// ok: what it returned is right, it is just not the whole. Absent otherwise.
	Capped bool `json:"capped,omitempty"`
}

// Fusion is the ONE result shape: the fused, ranked set plus the per-leg
// report that says which legs produced it. It was `Response`, which is a word
// every HTTP client already owns -- the Dart client put this model in the same
// library as package:http and shadowed http.Response, so 7,452 references to
// statusCode and body stopped resolving and the client had never compiled.
type Fusion struct {
	// Status is the query's overall honesty signal:
	//   ok          every consulted leg answered.
	//   partial     at least one leg failed; Hits holds the survivors' results.
	//   unavailable every consulted leg failed; Hits is empty AND that is stated.
	Status string `json:"status"`
	// Mode is the mode actually used after `auto` resolution.
	Mode string `json:"mode"`
	// Hits is the fused, ranked result set.
	Hits []Hit `json:"hits"`
	// Backends is the per-leg report. Always populated.
	Backends []BackendStatus `json:"backends"`
	// TookMS is the whole query's wall time in milliseconds — every leg it
	// consulted, plus fusion and paging. Each leg's own share is in
	// Backends[].TookMS; the legs run in sequence, so this is at least their sum.
	TookMS int64 `json:"took_ms"`
}

// Mount wires the surface. Every route is a typed op, so it projects to OpenAPI,
// MCP tools and the generated CLI from the SAME registration — a Router without
// the op registry cannot carry it, and the mount fails loudly rather than
// registering routes no projection would know about.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return errors.New("search: nil app")
	}
	b := cloud.NewBase(deps, "search")
	log = b.Log
	ai = deps.Embed
	search := app.Group("/v1/search")
	search.Post("", Query,
		zip.WithOperationID("search"),
		zip.WithSummary("Hybrid search over the org's own corpora"),
		zip.WithTags("search"))
	mountInventory(app)
	b.Log.Info("search surface mounted")
	return nil
}

// log is the surface's logger, set at mount. Degradation is logged as well as
// returned: the response tells the CALLER, the log tells the OPERATOR, and the
// five-day outage happened because neither was told.
var log luxlog.Logger

// Query is the typed op behind POST /v1/search. It does exactly two things the
// in-process entry point must not do: resolve the tenant from the validated
// principal, and refuse when there is none. Everything else is ForOrg.
func Query(ctx context.Context, in *Request) (*Fusion, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("valid principal required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, principal.Refused(c)
	}
	// The documents a person owns are keyed on SubjectOf, as knowledge keys
	// them (apps/knowledge hooks.go subject), never on the token's `sub`: a
	// member's key and an application named after them share that.
	p, _ := principal.Minted(c)
	subject, _ := principal.SubjectOf(p)
	return ForOrg(ctx, org, subject, in)
}

// ForOrg is the composition itself, for callers that have ALREADY established the
// tenant by some other means than an HTTP principal — notably the Team transactor,
// which runs in this same binary and holds a session whose space is its org.
// Such a caller gets the identical fused answer with no HTTP hop and no second
// retrieval path.
//
// org MUST be a tenant the caller has authenticated. This function does not and
// cannot check that; it is the caller's boundary, exactly as it is for every other
// in-process store API in the codebase.
//
// subject is the asking person, or "" for a caller that is the org itself; it
// decides whose owned knowledge documents are in reach and nothing else.
func ForOrg(ctx context.Context, org, subject string, in *Request) (*Fusion, error) {
	if strings.TrimSpace(org) == "" {
		return nil, zip.ErrForbidden("valid principal required")
	}
	if in == nil || strings.TrimSpace(in.Query) == "" {
		return nil, zip.ErrBadRequest("query is required")
	}
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	// Each leg is asked for the full window (offset+limit) because fusion reorders
	// across legs — paging after fusion is the only correct order of operations.
	window := limit + in.Offset

	start := time.Now()
	mode := resolveMode(ctx, org, in.Mode)
	wantText := mode == ModeText || mode == ModeHybrid
	wantVec := mode == ModeSemantic || mode == ModeHybrid

	// lists feeds fusion (ranks only); payload maps a fused key back to the row to
	// return. Splitting them is what lets rank/ stay a leaf that knows nothing
	// about documents.
	var lists []rank.List[string]
	payload := map[string]Hit{}
	backends := make([]BackendStatus, 0, 3)

	// ---- lexical leg ----
	st := BackendStatus{Name: BackendIndex, Status: StatusSkipped}
	if wantText {
		t0 := time.Now()
		out, err := indexpeer.IndexQuery(cloud.For(ctx, org),
			&client.IndexQueryIn{UID: indexUID(in.Index), Q: in.Query, Users: []string{"", subject}, Limit: window})
		st.TookMS = time.Since(t0).Milliseconds()
		var rows []json.RawMessage
		if err == nil {
			rows = out.Rows
		}
		if errors.Is(err, plane.ErrNoPeer) {
			st.Status = StatusDisabled
		} else if err != nil {
			st.Status, st.Error = StatusDegraded, err.Error()
			log.Warn("search leg failed", "backend", BackendIndex, "org", org, "err", err)
		} else {
			l := lexicalList(rows, payload)
			st.Status, st.Hits = StatusOK, len(l.Keys)
			lists = append(lists, l)
		}
	}
	backends = append(backends, st)

	// ---- semantic leg ----
	st = BackendStatus{Name: BackendVector, Status: StatusSkipped}
	if wantVec {
		t0 := time.Now()
		got, err := knowledgepeer.KnowledgeSemantic(cloud.For(ctx, org), &client.SemanticIn{
			Query: in.Query, Project: in.Project, DocTypes: in.DocTypes,
			Limit: window, Subject: subject, Payer: cloud.PayerOf(ctx).Wallet.String(),
		})
		st.TookMS = time.Since(t0).Milliseconds()
		var hits []client.SemanticHit
		if err == nil {
			hits = got.Hits
		}
		if errors.Is(err, plane.ErrNoPeer) || (err == nil && !got.Ready) {
			st.Status = StatusDisabled
		} else if err != nil {
			st.Status, st.Error = StatusDegraded, err.Error()
			log.Warn("search leg failed", "backend", BackendVector, "org", org, "err", err)
		} else {
			l := semanticList(hits, payload)
			st.Status, st.Hits = StatusOK, len(l.Keys)
			lists = append(lists, l)
		}
	}
	backends = append(backends, st)

	// ---- code leg ----
	//
	// It runs whenever text is wanted, because a code search IS a text search over
	// a corpus this org owns — the same reason the lexical leg runs. A deployment
	// without the code index reports `disabled`, which is not a fault and does not
	// make the answer partial.
	st = BackendStatus{Name: BackendCode, Status: StatusSkipped}
	if wantText {
		t0 := time.Now()
		got, err := codepeer.CodeSearch(cloud.For(ctx, org), &client.CodeSearchIn{
			Query: in.Query, Limit: window, Payer: cloud.PayerOf(ctx).Wallet.String(),
		})
		st.TookMS = time.Since(t0).Milliseconds()
		var spans []client.CodeSpan
		var capped bool
		if err == nil {
			spans, capped = got.Spans, got.Capped
		}
		if errors.Is(err, plane.ErrNoPeer) || (err == nil && !got.Ready) {
			st.Status = StatusDisabled
		} else if err != nil {
			st.Status, st.Error = StatusDegraded, err.Error()
			log.Warn("search leg failed", "backend", BackendCode, "org", org, "err", err)
		} else {
			l := codeList(spans, payload)
			st.Status, st.Hits, st.Capped = StatusOK, len(l.Keys), capped
			lists = append(lists, l)
		}
	}
	backends = append(backends, st)

	// ---- files leg ----
	//
	// The org's workspace files, cut into passages under their sections. The
	// file index lives in knowledge's process, so the leg asks it over the
	// plane, and the answer is already both kinds of retrieval fused — it runs
	// in every mode. A deployment without knowledge reports it disabled.
	st = BackendStatus{Name: BackendFiles, Status: StatusSkipped}
	if wantText || wantVec {
		t0 := time.Now()
		got, err := knowledgepeer.KnowledgeFiles(cloud.For(ctx, org), &client.FileQuery{Query: in.Query, Limit: window})
		st.TookMS = time.Since(t0).Milliseconds()
		switch {
		case errors.Is(err, plane.ErrNoPeer):
			st.Status = StatusDisabled
		case err != nil:
			st.Status, st.Error = StatusDegraded, err.Error()
			log.Warn("search leg failed", "backend", BackendFiles, "org", org, "err", err)
		default:
			l := filesList(got.Hits, payload)
			st.Status, st.Hits = StatusOK, len(l.Keys)
			if got.Degraded {
				st.Error = "a leg of the file search failed; these are what the other found"
			}
			lists = append(lists, l)
		}
	}
	backends = append(backends, st)

	fused := rank.RRF(lists, window)

	// ---- rerank ----
	//
	// Reciprocal-rank fusion orders by agreement between legs; a cross-encoder
	// reads the query against each document's text and orders by relevance. It
	// runs over the whole window before paging, for the same reason the legs are
	// asked for the whole window. Without a client it is `disabled`, and a failed
	// call leaves the fused order standing and says so — never a silent skip.
	st = BackendStatus{Name: BackendRerank, Status: StatusSkipped}
	switch {
	case ai == nil:
		st.Status = StatusDisabled
	case len(fused) > 1:
		t0 := time.Now()
		ordered, err := rerank(ctx, org, in.Project, in.Query, fused, payload)
		st.TookMS = time.Since(t0).Milliseconds()
		if err != nil {
			st.Status, st.Error = StatusDegraded, err.Error()
			log.Warn("search leg failed", "backend", BackendRerank, "org", org, "err", err)
		} else {
			fused = ordered
			st.Status, st.Hits = StatusOK, len(ordered)
		}
	}
	backends = append(backends, st)

	if in.Offset > 0 {
		if in.Offset >= len(fused) {
			fused = nil
		} else {
			fused = fused[in.Offset:]
		}
	}
	hits := make([]Hit, 0, len(fused))
	for _, f := range fused {
		r := payload[f.Key]
		r.Score = f.Score
		for _, o := range f.Origins {
			r.Matched = append(r.Matched, Provenance{Backend: o.Source, Rank: o.Rank, Score: o.Score})
		}
		hits = append(hits, r)
	}

	return &Fusion{
		Status:   overall(backends),
		Mode:     mode,
		Hits:     hits,
		Backends: backends,
		TookMS:   time.Since(start).Milliseconds(),
	}, nil
}

// overall folds the per-leg statuses into the response's honesty signal. A leg
// that was skipped or is unprovisioned does not make a query partial — only a
// CONFIGURED leg that FAILED does. When every consulted leg failed the answer is
// `unavailable`, which a caller must not read as "no results".
func overall(bs []BackendStatus) string {
	consulted, failed := 0, 0
	for _, b := range bs {
		switch b.Status {
		case StatusOK:
			consulted++
		case StatusDegraded:
			consulted++
			failed++
		}
	}
	switch {
	case failed == 0:
		return StatusOK
	case failed == consulted:
		return "unavailable"
	default:
		return "partial"
	}
}

// resolveMode turns the requested mode into the one actually used. `auto` prefers
// hybrid and falls back to whichever leg this deployment actually has, so a
// single-store deployment answers instead of half-answering.
func resolveMode(ctx context.Context, org, m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case ModeText:
		return ModeText
	case ModeSemantic:
		return ModeSemantic
	case ModeHybrid:
		return ModeHybrid
	default:
		vec := semanticReady(ctx, org)
		switch {
		case vec && indexReady(ctx, org):
			return ModeHybrid
		case vec:
			return ModeSemantic
		default:
			return ModeText
		}
	}
}

// semanticReady asks knowledge whether it has a vector leg. An absent peer
// reads as not ready.
func semanticReady(ctx context.Context, org string) bool {
	got, err := knowledgepeer.KnowledgeSemantic(cloud.For(ctx, org), &client.SemanticIn{})
	return err == nil && got.Ready
}

// indexReady asks the lexical index whether it answers. An absent peer reads
// as not ready.
func indexReady(ctx context.Context, org string) bool {
	_, err := indexpeer.IndexQuery(cloud.For(ctx, org), &client.IndexQueryIn{UID: defaultIndex, Limit: 1})
	return err == nil
}

func indexUID(uid string) string {
	if u := strings.TrimSpace(uid); u != "" {
		return u
	}
	return defaultIndex
}

// ai is the read-scope inference client, the one knowledge embeds through; nil
// in a deployment that has none, which the rerank stage reports as disabled.
var ai infer.Client

// rerankModel is the served SKU the cross-encoder answers as.
func rerankModel() string { return env.Or("CLOUD_RERANK_MODEL", "zen-rerank") }

// rerank scores every fused row's text against the query and returns the rows
// in relevance order, each carrying the rerank as one more origin beside the
// legs that found it. A row with no text is scored on its title, so a document
// is never dropped for what a leg failed to carry.
func rerank(ctx context.Context, org, project, query string, fused []rank.Fused[string], payload map[string]Hit) ([]rank.Fused[string], error) {
	docs := make([]string, len(fused))
	for i, f := range fused {
		h := payload[f.Key]
		docs[i] = h.text
		if docs[i] == "" {
			docs[i] = h.Title
		}
	}
	scores, err := ai.Rerank(ctx, &infer.RerankRequest{Model: rerankModel(), Query: query, Documents: docs, Org: org, Payer: cloud.PayerOf(ctx).Wallet, Project: project})
	if err != nil {
		return nil, err
	}
	if len(scores) != len(fused) {
		return nil, fmt.Errorf("rerank: %d scores for %d documents", len(scores), len(fused))
	}
	out := make([]rank.Fused[string], len(fused))
	for i, f := range fused {
		out[i] = rank.Fused[string]{Key: f.Key, Score: scores[i], Origins: append([]rank.Origin(nil), f.Origins...)}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	for i := range out {
		out[i].Origins = append(out[i].Origins, rank.Origin{Source: BackendRerank, Rank: i + 1, Score: out[i].Score})
	}
	return out, nil
}

// semanticList adapts vector hits to fusion input and records each hit's payload.
// The Key is doctype+name — the document's identity in the KB store — so the same
// document found by both legs fuses into ONE reinforced result rather than
// appearing twice.
func semanticList(hits []client.SemanticHit, payload map[string]Hit) rank.List[string] {
	l := rank.List[string]{Source: BackendVector}
	for _, h := range hits {
		key := h.DocType + "/" + h.Name
		l.Keys = append(l.Keys, key)
		l.Scores = append(l.Scores, h.Score)
		if _, seen := payload[key]; !seen {
			payload[key] = Hit{
				ID: h.Name, Corpus: "kb", DocType: h.DocType,
				Title: h.Title, URL: h.URL, Project: h.Project,
				text: h.Text,
			}
		}
	}
	return l
}

// lexicalList adapts index rows to fusion input. Rows are opaque JSON documents,
// so identity comes from the document's own id/name field. The store ranks by
// match count and exposes no per-row score, so no score is reported: an invented
// number here would be precision the store never had.
func lexicalList(rows []json.RawMessage, payload map[string]Hit) rank.List[string] {
	l := rank.List[string]{Source: BackendIndex}
	for i, raw := range rows {
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			continue
		}
		name := firstString(d, "name", "id", "_id")
		if name == "" {
			name = fmt.Sprintf("row-%d", i)
		}
		doctype := firstString(d, "doctype", "type")
		key := doctype + "/" + name
		l.Keys = append(l.Keys, key)
		if _, seen := payload[key]; !seen {
			payload[key] = Hit{
				ID: name, Corpus: "kb", DocType: doctype,
				Title:   firstString(d, "title", "name"),
				URL:     firstString(d, "url"),
				Project: firstString(d, "project"),
				text:    firstString(d, "text"),
			}
		}
	}
	return l
}

// codeList turns the code leg's spans into a ranked list plus the hits to return.
// A span's key is its ADDRESS — repo, file and start line — because that is what
// makes it the same span across queries, and the corpus is "code" so a caller can
// tell where a hit came from without branching on it.
//
// Scores are 0 across the board, meaning UNSCORED rather than "scored zero": the
// engine returns spans in its own best-first order and exposes no per-span score,
// which is the same thing the lexical leg reports and exactly why fusion ranks
// rather than compares.
func codeList(spans []client.CodeSpan, payload map[string]Hit) rank.List[string] {
	l := rank.List[string]{Source: BackendCode, Keys: make([]string, 0, len(spans))}
	for _, sp := range spans {
		id := sp.Repo + ":" + sp.File + ":" + strconv.Itoa(sp.Line)
		key := BackendCode + ":" + id
		title := sp.Symbol
		if title == "" {
			title = sp.File
		}
		l.Keys = append(l.Keys, key)
		l.Scores = append(l.Scores, 0)
		if _, seen := payload[key]; !seen {
			payload[key] = Hit{ID: id, Corpus: BackendCode, DocType: sp.Kind, Title: title, text: sp.Snippet}
		}
	}
	return l
}

// filesList turns the files leg's passages into a ranked list plus the hits to
// return. A passage's key is its citation — file › section › paragraph — which
// is also what the hit is titled by.
func filesList(hits []client.FileHit, payload map[string]Hit) rank.List[string] {
	l := rank.List[string]{Source: BackendFiles, Keys: make([]string, 0, len(hits))}
	for _, h := range hits {
		key := BackendFiles + ":" + h.File + ":" + h.Cite
		l.Keys = append(l.Keys, key)
		l.Scores = append(l.Scores, h.Score)
		if _, seen := payload[key]; !seen {
			payload[key] = Hit{ID: h.File, Corpus: BackendFiles, DocType: "file", Title: h.Cite, Passage: h.Text, text: h.Text}
		}
	}
	return l
}

func firstString(d map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := d[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
