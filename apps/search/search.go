// Package search is one ranked result set over everything your org has stored.
//
// It answers "what is RELEVANT" over a tenant's own data: it owns no store and
// fuses the three retrieval stores the platform already runs — the lexical index
// (apps/index), the vector index (apps/knowledge) and the org's own repositories
// (apps/code) — into one ranked result set.
//
// THIS IS THE ENTRY POINT. A caller asks here and does not choose a backend. The
// other three addresses stay exactly what they are — each backend's own surface, with
// the shape and the vocabulary that backend's clients need — and none of them is
// an address a caller should have to pick between. /v1/index speaks the
// Meilisearch dialect because a Meilisearch client repoints by changing one host;
// /v1/code/search answers spans with repo and line because that is what a coding
// agent wants; /v1/knowledge/search is the KB's own semantic read. Fusing them is
// not deleting them.
//
// IT IS MOUNTED. manifest/apps.go carries the row and plugin/search is built, so
// POST /v1/search reaches the wire. This comment said the opposite for as long as
// that was true and was never re-read when the row landed — the same rot the
// money refusals had, and the reason a claim about the fleet belongs in a test
// rather than a paragraph.
//
// WHAT IS STILL TRUE, and is the real problem: a caller has to know which of
// /v1/search, /v1/index/indexes/:uid/search, /v1/knowledge/search and
// /v1/code/search holds their answer, and gets a different request shape and a
// different score scale from each. This package exists to end that and has not
// yet: it fuses the lexical and vector legs, and the other two endpoints still
// answer beside it.
//
// WHAT BELONGS HERE. A query whose honest answer has a SCORE. A query whose
// honest answer has a TRUTH VALUE — the definition of a symbol, the callers of a
// function, a dependency edge — belongs to /v1/code (apps/code) and must not be
// forced through a relevance-ranked shape: a definition is not 0.87 relevant, it
// either is the definition or it is not.
//
// NOT HERE: /v1/websearch/search. That searches the PUBLIC WEB, not the
// customer's data. It has a different tenancy model (no org-scoped corpus), a
// different cost model (per-call to an external provider) and a different failure
// mode. It stays separate — do not fold it in.
//
// DEGRADATION IS THE CONTRACT. Every response names every backend it consulted
// and that backend's status. A leg that is down produces results from the
// surviving legs plus an explicit `degraded` entry carrying the error — never a
// silent empty. This is not a nicety: a silent empty is indistinguishable from a
// query the corpus genuinely has no answer for, so a leg that stops working is
// invisible for exactly as long as nobody thinks to ask it directly.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/code"
	"github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/apps/knowledge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/search/rank"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Backend names. One constant per leg so the wire value is declared once and the
// provenance a client reads always matches the status it reads.
const (
	BackendIndex  = "index"  // lexical, apps/index
	BackendVector = "vector" // semantic, apps/knowledge → hanzoai/vector
	BackendCode   = "code"   // the org's own repositories, apps/code
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
type Match struct {
	// Backend is the leg that contributed this match: "index" (lexical), "vector"
	// (semantic) or "code" (the org's repositories). It is the same name that leg
	// reports itself under in Response.Backends, so a hit can be traced to a
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
	// The vector leg reports Qdrant's cosine similarity; the lexical leg exposes no
	// per-row score and reports 0, meaning "unscored", not "scored zero".
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
	// repositories. It is PROVENANCE — read it to say where a hit came from, not
	// to branch on: the fused ranking is what decides order, and a caller that
	// filters by corpus wants the backend's own endpoint instead.
	Corpus string `json:"corpus"`
	// DocType is the knowledge doctype: kb-page, kb-memory or kb-source from the
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
	Matched []Match `json:"matched"`
}

// BackendStatus reports one leg's outcome. It is present for EVERY leg on EVERY
// response, including the ones that were skipped, so a client never has to infer
// from absence.
type BackendStatus struct {
	// Name is which leg this reports: "index", the lexical store, "vector", the
	// semantic one, or "code", the org's own repositories. Match.Backend uses the
	// same three names.
	Name string `json:"name"`
	// Status is one of ok, degraded, disabled, skipped — four distinct operational
	// facts that are never collapsed. It ran and answered; it is configured and
	// FAILED (Error says how, and only this one is a fault); this deployment never
	// provisioned it; or the request's mode excluded it.
	Status string `json:"status"`
	// Hits is how many results this leg returned, counted BEFORE fusion, so it is
	// not the number that survived into Response.Hits — fusion merges what both
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
}

// Response is the ONE result shape.
type Response struct {
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
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("search.Mount: nil app")
	}
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("search.Mount: %T does not expose the typed-op registry", app)
	}
	b := cloud.NewBase(deps, "search")
	log = b.Log
	zip.Post(z, "/v1/search", Query,
		zip.WithOperationID("search"),
		zip.WithSummary("Hybrid search over the org's own corpora"),
		zip.WithTags("search"))
	mountInventory(z)
	b.Log.Info("search surface mounted", "vector", knowledge.SemanticReady(), "index", index.Ready())
	return nil
}

// log is the surface's logger, set at mount. Degradation is logged as well as
// returned: the response tells the CALLER, the log tells the OPERATOR, and the
// five-day outage happened because neither was told.
var log luxlog.Logger

// Query is the typed op behind POST /v1/search. It does exactly two things the
// in-process entry point must not do: resolve the tenant from the validated
// principal, and refuse when there is none. Everything else is ForOrg.
func Query(ctx context.Context, in *Request) (*Response, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("valid principal required")
	}
	org, ok := principal.Org(c)
	if !ok {
		return nil, zip.ErrForbidden("valid principal required")
	}
	return ForOrg(ctx, org, in)
}

// ForOrg is the composition itself, for callers that have ALREADY established the
// tenant by some other means than an HTTP principal — notably the Team transactor,
// which runs in this same binary and holds a session whose workspace is its org.
// Such a caller gets the identical fused answer with no HTTP hop and no second
// retrieval path.
//
// org MUST be a tenant the caller has authenticated. This function does not and
// cannot check that; it is the caller's boundary, exactly as it is for every other
// in-process store API in the codebase.
func ForOrg(ctx context.Context, org string, in *Request) (*Response, error) {
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
	mode := resolveMode(in.Mode)
	wantText := mode == ModeText || mode == ModeHybrid
	wantVec := mode == ModeSemantic || mode == ModeHybrid

	// lists feeds fusion (ranks only); payload maps a fused key back to the row to
	// return. Splitting them is what lets rank/ stay a leaf that knows nothing
	// about documents.
	var lists []rank.List
	payload := map[string]Hit{}
	backends := make([]BackendStatus, 0, 3)

	// ---- lexical leg ----
	st := BackendStatus{Name: BackendIndex, Status: StatusSkipped}
	if wantText {
		switch {
		case !index.Ready():
			st.Status = StatusDisabled
		default:
			t0 := time.Now()
			rows, err := index.Query(ctx, org, indexUID(in.Index), in.Query, window, 0)
			st.TookMS = time.Since(t0).Milliseconds()
			if err != nil {
				st.Status, st.Error = StatusDegraded, err.Error()
				log.Warn("search leg failed", "backend", BackendIndex, "org", org, "err", err)
			} else {
				l := lexicalList(rows, payload)
				st.Status, st.Hits = StatusOK, len(l.Keys)
				lists = append(lists, l)
			}
		}
	}
	backends = append(backends, st)

	// ---- semantic leg ----
	st = BackendStatus{Name: BackendVector, Status: StatusSkipped}
	if wantVec {
		switch {
		case !knowledge.SemanticReady():
			st.Status = StatusDisabled
		default:
			t0 := time.Now()
			hits, err := knowledge.Semantic(ctx, knowledge.SemanticReq{
				Org: org, Query: in.Query, Project: in.Project,
				DocTypes: in.DocTypes, Limit: window,
			})
			st.TookMS = time.Since(t0).Milliseconds()
			if err != nil {
				st.Status, st.Error = StatusDegraded, err.Error()
				log.Warn("search leg failed", "backend", BackendVector, "org", org, "err", err)
			} else {
				l := semanticList(hits, payload)
				st.Status, st.Hits = StatusOK, len(l.Keys)
				lists = append(lists, l)
			}
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
		switch {
		case !code.Ready():
			st.Status = StatusDisabled
		default:
			t0 := time.Now()
			spans, err := code.Search(ctx, org, in.Query, window)
			st.TookMS = time.Since(t0).Milliseconds()
			if err != nil {
				st.Status, st.Error = StatusDegraded, err.Error()
				log.Warn("search leg failed", "backend", BackendCode, "org", org, "err", err)
			} else {
				l := codeList(spans, payload)
				st.Status, st.Hits = StatusOK, len(l.Keys)
				lists = append(lists, l)
			}
		}
	}
	backends = append(backends, st)

	fused := rank.Fuse(lists, window)
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
			r.Matched = append(r.Matched, Match{Backend: o.Source, Rank: o.Rank, Score: o.Score})
		}
		hits = append(hits, r)
	}

	return &Response{
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
func resolveMode(m string) string {
	switch strings.ToLower(strings.TrimSpace(m)) {
	case ModeText:
		return ModeText
	case ModeSemantic:
		return ModeSemantic
	case ModeHybrid:
		return ModeHybrid
	default:
		switch {
		case knowledge.SemanticReady() && index.Ready():
			return ModeHybrid
		case knowledge.SemanticReady():
			return ModeSemantic
		default:
			return ModeText
		}
	}
}

func indexUID(uid string) string {
	if u := strings.TrimSpace(uid); u != "" {
		return u
	}
	return defaultIndex
}

// semanticList adapts vector hits to fusion input and records each hit's payload.
// The Key is doctype+name — the document's identity in the KB store — so the same
// document found by both legs fuses into ONE reinforced result rather than
// appearing twice.
func semanticList(hits []knowledge.Hit, payload map[string]Hit) rank.List {
	l := rank.List{Source: BackendVector}
	for _, h := range hits {
		key := h.DocType + "/" + h.Name
		l.Keys = append(l.Keys, key)
		l.Scores = append(l.Scores, h.Score)
		if _, seen := payload[key]; !seen {
			payload[key] = Hit{
				ID: h.Name, Corpus: "kb", DocType: h.DocType,
				Title: h.Title, URL: h.URL, Project: h.Project,
			}
		}
	}
	return l
}

// lexicalList adapts index rows to fusion input. Rows are opaque JSON documents,
// so identity comes from the document's own id/name field. The store ranks by
// match count and exposes no per-row score, so no score is reported: an invented
// number here would be precision the store never had.
func lexicalList(rows []json.RawMessage, payload map[string]Hit) rank.List {
	l := rank.List{Source: BackendIndex}
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
func codeList(spans []code.Span, payload map[string]Hit) rank.List {
	l := rank.List{Source: BackendCode, Keys: make([]string, 0, len(spans))}
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
			payload[key] = Hit{ID: id, Corpus: BackendCode, DocType: sp.Kind, Title: title}
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
