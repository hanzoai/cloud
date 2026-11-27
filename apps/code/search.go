package code

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/search/rank"
	"github.com/hanzoai/cloud/internal/vec"
	"github.com/hanzoai/account"
)

// Span is one ranked result — the unit both /search and /context return. For
// search, Snippet is a bounded excerpt; for context, it is the full AST chunk the
// agent pastes into its window.
type Span struct {
	// Repo is the indexed repository the span was found in, as it was indexed
	// ("owner/name"). A search may be scoped to one repo or run across all of them,
	// so this is how a caller tells the results apart.
	Repo string `json:"repo"`
	// File is the path inside the repo, relative to its root and never absolute.
	File string `json:"file"`
	// Line is where the span starts, 1-based, as an editor counts.
	Line int `json:"line"`
	// EndLine is the last line of the span, inclusive. It equals Line for a
	// one-line span rather than being zero or absent.
	EndLine int `json:"endLine"`
	// Kind is what the indexer decided this chunk IS — "func", "method", "type",
	// "struct", "interface", "var", "const", or "block" for a run of code that
	// declares nothing. Absent when the chunker could not classify it.
	Kind string `json:"kind,omitempty"`
	// Symbol is the declared name, when the span declares one. Absent on a block.
	Symbol string `json:"symbol,omitempty"`
	// Snippet is the code itself: a bounded excerpt on /search, the whole chunk on
	// /context — which is why the same type serves both and why a /context span is
	// the one an agent pastes into its window.
	Snippet string `json:"snippet"`
	// Score ranks this span against the OTHERS IN THE SAME RESPONSE and means
	// nothing across responses or between tiers: the hybrid tier's number is a
	// reciprocal-rank fusion sum (Σ 1/(60+rank), so tenths at best), the symbol
	// tier's is a descending position count, and the text and semantic tiers pass
	// through bm25 and cosine. Compare within a list; never threshold on it.
	Score float64 `json:"score"`
	// Tier is which retrieval produced the span: "hybrid" (the default — all three
	// fused), "text" (trigram/FTS), "regex", "semantic" (vector), or "symbol". It is
	// what explains a Score, so the two travel together.
	Tier string `json:"tier,omitempty"`
	Role string `json:"role,omitempty"` // context: match | definition | caller
}

const (
	candidatePool     = 3     // per-tier candidate multiple of the requested limit
	maxVectorScan     = 50000 // vectors the semantic tier reads per query; past it the answer says capped
	maxRegexScan      = 5000
	maxRegexCandidate = 2000
	snippetMaxLines   = 40
	snippetMaxChars   = 2000
)

// engine composes the three retrieval tiers over one org store plus the shared
// embedder. It is built per request (engineFor); the store IS the org.
type engine struct {
	store *Store
	embed Embedder
	// org/project are the billing scope for the semantic tier's query embeddings —
	// set per-request by engineFor so searchSemantic meters the query embed against
	// the caller's org (the same tenant its store is opened for).
	org     string
	payer   account.Account
	project string
	// capped is set when the semantic tier stopped at maxVectorScan with vectors
	// left unread. The ops report it on the answer; it is never hidden.
	capped bool
	// refused is the semantic tier's refusal when the org's index is of another
	// space than the embedder's (*vec.MixedError). A hybrid search answers by its
	// other tiers, as it does for any tier's failure, and the ops log it, since
	// only a rebuild of the index ends it.
	refused error
}

// search dispatches to a single tier or the hybrid fusion. An unknown type
// defaults to hybrid — the best general answer for an agent that does not care.
func (e *engine) search(ctx context.Context, repo, typ, query string, limit int) ([]Span, error) {
	switch typ {
	case "text":
		rows, err := e.searchText(ctx, repo, query, limit)
		return e.rowsToSpans(ctx, rows, "text"), err
	case "regex":
		rows, err := e.searchRegex(ctx, repo, query, limit)
		return e.rowsToSpans(ctx, rows, "regex"), err
	case "symbol":
		return e.searchSymbolSpans(ctx, repo, query, limit)
	case "semantic":
		rows, err := e.searchSemantic(ctx, repo, query, limit)
		return e.rowsToSpans(ctx, rows, "semantic"), err
	default:
		return e.searchHybrid(ctx, repo, query, limit)
	}
}

// searchText runs the trigram lexical tier: primary (unsplit) tokens joined by
// AND first so exact identifiers (e.g. "loadHistory") intersect trigrams in <1ms,
// then broad subword OR expansion to fill any remaining slots up to limit.
func (e *engine) searchText(ctx context.Context, repo, query string, limit int) ([]chunkRow, error) {
	primary := primaryTerms(query)
	var out []chunkRow
	seen := map[int64]bool{}
	if len(primary) > 0 {
		rows, err := e.store.ftsSearch(ctx, repo, primary, true, limit)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			seen[r.ID] = true
			out = append(out, r)
		}
		if len(out) >= limit {
			return out[:limit], nil
		}
	}
	terms := ftsTerms(query)
	// One term is the same question asked either way, so it is not asked twice.
	if len(terms) == 0 || len(terms) == 1 && slices.Equal(terms, primary) {
		return out, nil
	}
	more, err := e.store.ftsSearch(ctx, repo, terms, false, limit)
	if err != nil {
		return out, err
	}
	for _, r := range more {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// searchRegex is the Zoekt model: extract the pattern's literal atoms, use the
// trigram index to pre-filter candidate chunks, then verify each with Go's regexp
// engine (exact). A pattern with no ≥3-char literal falls back to a bounded scan.
func (e *engine) searchRegex(ctx context.Context, repo, query string, limit int) ([]chunkRow, error) {
	re, err := regexp.Compile(query)
	if err != nil {
		return nil, err
	}
	var cands []chunkRow
	if atoms := literalAtoms(query); len(atoms) > 0 {
		cands, err = e.store.ftsSearch(ctx, repo, atoms, false, maxRegexCandidate)
	} else {
		cands, err = e.store.scanChunks(ctx, repo, maxRegexScan)
	}
	if err != nil {
		return nil, err
	}
	var out []chunkRow
	for _, c := range cands {
		if loc := re.FindStringIndex(c.Text); loc != nil {
			line := c.StartLine + strings.Count(c.Text[:loc[0]], "\n")
			c.StartLine = line // report the matched line, not the chunk head
			out = append(out, c)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

// searchSymbolSpans resolves go-to-symbol and returns each definition as a span,
// centred on the definition line.
func (e *engine) searchSymbolSpans(ctx context.Context, repo, query string, limit int) ([]Span, error) {
	syms, err := e.store.symbolSearch(ctx, repo, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Span, 0, len(syms))
	for i, s := range syms {
		snip := s.Signature
		if cr, ok := e.store.chunkForSymbol(ctx, s); ok {
			snip = snippet(cr.Text)
		}
		out = append(out, Span{
			Repo: s.Repo, File: s.Path, Line: s.Line, EndLine: s.EndLine, Kind: s.Kind,
			Symbol: s.Name, Snippet: snip, Score: float64(len(syms) - i), Tier: "symbol",
		})
	}
	return out, nil
}

// searchSemantic runs the vector tier: embed the query, score it against the
// repo's vectors (Store.nearest: a bounded heap, capped at maxVectorScan and
// reporting the cap on e.capped), and return the top chunks. Disabled (no
// embedder) or an unembedded index yields an empty result — the hybrid caller
// degrades to lexical + symbolic rather than failing. An index of another space
// than the embedder's is an error (*vec.MixedError), never a score: the hybrid
// caller degrades on it the same way, and a semantic-only search returns it.
func (e *engine) searchSemantic(ctx context.Context, repo, query string, limit int) ([]chunkRow, error) {
	if e.embed == nil || !e.embed.Enabled() || !e.store.hasVectors(ctx, repo) {
		return nil, nil
	}
	// An index of another space refuses before the query's vector is bought;
	// nearest asks again with the dimension the model answered.
	sp := e.embed.Space()
	if err := codeIndex.Admit(ctx, e.store.db, sp); err != nil {
		return nil, e.refuse(err)
	}
	ectx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	qv, _, err := sp.Query(query, func(in []string) ([][]float32, error) {
		return e.embed.Embed(ectx, e.org, e.payer, e.project, in)
	})
	if err != nil || len(qv) == 0 {
		return nil, err
	}
	top, capped, err := e.store.nearest(ctx, repo, qv, sp, limit, maxVectorScan)
	if err != nil {
		return nil, e.refuse(err)
	}
	e.capped = e.capped || capped
	var out []chunkRow
	for _, h := range top {
		row, err := e.store.getChunk(ctx, h.Key)
		if err != nil {
			continue
		}
		row.Score = h.Score
		out = append(out, row)
	}
	return out, nil
}

// refuse keeps err on the engine when it is a refusal of another space, and
// answers it.
func (e *engine) refuse(err error) error {
	if errors.Is(err, vec.ErrMixed) {
		e.refused = err
	}
	return err
}

// searchHybrid fuses all three tiers with reciprocal-rank fusion. Each tier
// contributes an independent ranking; a chunk surfaced by several tiers rises.
// Per-tier failures degrade to empty (best-effort union) so one outage never
// fails the search.
func (e *engine) searchHybrid(ctx context.Context, repo, query string, limit int) ([]Span, error) {
	pool := limit * candidatePool
	text, _ := e.searchText(ctx, repo, query, pool)
	sem, _ := e.searchSemantic(ctx, repo, query, pool)

	var symChunks []chunkRow
	if syms, err := e.store.symbolSearch(ctx, repo, query, pool); err == nil {
		for _, s := range syms {
			if cr, ok := e.store.chunkForSymbol(ctx, s); ok {
				symChunks = append(symChunks, cr)
			}
		}
	}
	fused := fuse(limit, text, symChunks, sem)
	return e.rowsToSpans(ctx, fused, "hybrid"), nil
}

// fuse ranks the tiers' chunks by rank.RRF on the chunk id — the codebase's one
// fusion — so a chunk several tiers agree on beats any single tier's top hit.
// The first row seen for an id supplies its display fields; its Score becomes the
// fused one.
func fuse(limit int, tiers ...[]chunkRow) []chunkRow {
	lists := make([]rank.List[int64], len(tiers))
	first := map[int64]chunkRow{}
	for i, rows := range tiers {
		lists[i].Keys = make([]int64, len(rows))
		for j, r := range rows {
			lists[i].Keys[j] = r.ID
			if _, ok := first[r.ID]; !ok {
				first[r.ID] = r
			}
		}
	}
	fused := rank.RRF(lists, limit)
	out := make([]chunkRow, 0, len(fused))
	for _, f := range fused {
		row := first[f.Key]
		row.Score = f.Score
		out = append(out, row)
	}
	return out
}

func (e *engine) rowsToSpans(_ context.Context, rows []chunkRow, tier string) []Span {
	out := make([]Span, 0, len(rows))
	for _, r := range rows {
		out = append(out, Span{
			Repo: r.Repo, File: r.Path, Line: r.StartLine, EndLine: r.EndLine, Kind: r.Kind,
			Symbol: r.Symbol, Snippet: snippet(r.Text), Score: r.Score, Tier: tier,
		})
	}
	return out
}

// ── /context budget packing ──────────────────────────────────────────────────

// ContextBundle is the token-budgeted context for a coding agent's window: the
// most relevant spans plus the definitions they call and their key callers,
// packed greedily until the budget is spent. It is deliberately NOT whole files.
type ContextBundle struct {
	// Query is the ask this bundle was packed for, echoed back so a cached or
	// forwarded bundle still says what it answers.
	Query string `json:"query"`
	// Repo narrows the retrieval to one repository. Absent means every indexed repo
	// was searched.
	Repo string `json:"repo,omitempty"`
	// BudgetTokens is the ceiling the caller asked for. Packing stops under it, so
	// this is a bound and not a target.
	BudgetTokens int `json:"budgetTokens"`
	// UsedTokens is what the returned spans actually cost, by the same estimate the
	// packer used (roughly one token per four characters — an estimate, not a
	// tokenizer's count, so size a real window with headroom).
	UsedTokens int `json:"usedTokens"`
	// Spans are the packed chunks, most relevant first, each expanded with the
	// definitions it calls and its notable callers. The top match is always present
	// even if it had to be truncated to fit, so a matched query never comes back
	// with nothing.
	Spans []Span `json:"spans"`
	// Capped is true when the semantic tier stopped at its scan cap — it compares
	// a query with at most 50000 stored vectors — with more left unread, so code
	// past the cap could not be picked by meaning. The lexical and symbol tiers
	// read the whole index either way. Absent otherwise.
	Capped bool `json:"capped,omitempty"`
}

const contextSeedPool = 12

// packContext retrieves the most relevant chunks, expands each with its callee
// definitions and key callers, then greedily packs spans until budgetTokens is
// spent. The top match is always included (truncated if it alone overflows) so a
// matched query never returns an empty bundle.
func (e *engine) packContext(ctx context.Context, repo, query string, budget int) (ContextBundle, error) {
	seeds := e.hybridRows(ctx, repo, query, contextSeedPool)

	type cand struct {
		row  chunkRow
		role string
	}
	var cands []cand
	pushed := map[int64]bool{}
	push := func(r chunkRow, role string) {
		if r.ID == 0 || pushed[r.ID] {
			return
		}
		pushed[r.ID] = true
		cands = append(cands, cand{row: r, role: role})
	}

	for _, s := range seeds {
		push(s, "match")
	}
	// Expand each seed with the definitions it calls and its key callers — the
	// symbol graph an agent needs to reason about the match, not just the match.
	for _, s := range seeds {
		if s.Symbol == "" {
			continue
		}
		for _, callee := range e.calleeDefs(ctx, repo, s.Symbol, 3) {
			push(callee, "definition")
		}
		if callers, err := e.store.callersOf(ctx, repo, s.Symbol, 3); err == nil {
			for _, cl := range callers {
				if def, ok := e.store.lookupSymbol(ctx, repo, symbolBase(cl.From)); ok {
					if cr, ok := e.store.chunkForSymbol(ctx, def); ok {
						push(cr, "caller")
					}
				}
			}
		}
	}

	bundle := ContextBundle{Query: query, Repo: repo, BudgetTokens: budget, Capped: e.capped}
	for _, c := range cands {
		text := c.row.Text
		toks := estimateTokens(text)
		if len(bundle.Spans) > 0 && bundle.UsedTokens+toks > budget {
			continue // full budget span — skip and keep trying smaller ones
		}
		if len(bundle.Spans) == 0 && toks > budget {
			text = truncateToTokens(text, budget) // top match alone overflows: truncate
			toks = estimateTokens(text)
		}
		bundle.UsedTokens += toks
		bundle.Spans = append(bundle.Spans, Span{
			Repo: c.row.Repo, File: c.row.Path, Line: c.row.StartLine, EndLine: c.row.EndLine,
			Kind: c.row.Kind, Symbol: c.row.Symbol, Snippet: text, Score: c.row.Score, Role: c.role,
		})
		if bundle.UsedTokens >= budget {
			break
		}
	}
	return bundle, nil
}

// hybridRows is searchHybrid without the span projection — context packing needs
// the raw chunk rows (symbol names, ids) to walk the symbol graph.
func (e *engine) hybridRows(ctx context.Context, repo, query string, limit int) []chunkRow {
	pool := limit * candidatePool
	text, _ := e.searchText(ctx, repo, query, pool)
	sem, _ := e.searchSemantic(ctx, repo, query, pool)
	var symChunks []chunkRow
	if syms, err := e.store.symbolSearch(ctx, repo, query, pool); err == nil {
		for _, s := range syms {
			if cr, ok := e.store.chunkForSymbol(ctx, s); ok {
				symChunks = append(symChunks, cr)
			}
		}
	}
	return fuse(limit, text, symChunks, sem)
}

// calleeDefs returns the defining chunks of the symbols that `from` calls (the
// edges out of `from`), so the agent gets the code a match depends on.
func (e *engine) calleeDefs(ctx context.Context, repo, from string, limit int) []chunkRow {
	names, err := e.store.calleesOf(ctx, repo, from, limit)
	if err != nil {
		return nil
	}
	var out []chunkRow
	for _, name := range names {
		if def, ok := e.store.lookupSymbol(ctx, repo, name); ok {
			if cr, ok := e.store.chunkForSymbol(ctx, def); ok {
				out = append(out, cr)
			}
		}
	}
	return out
}

// symbolBase strips a receiver/class qualifier (Foo.bar → bar) so a caller's
// enclosing symbol resolves against the symbol table.
func symbolBase(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// ── snippet helpers ──────────────────────────────────────────────────────────

func snippet(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) > snippetMaxLines {
		lines = lines[:snippetMaxLines]
	}
	s := strings.Join(lines, "\n")
	if len(s) > snippetMaxChars {
		s = s[:snippetMaxChars]
	}
	return s
}

func truncateToTokens(text string, budget int) string {
	// Reserve one token for the estimate's +1 so the packed span never exceeds the
	// budget (estimateTokens = len/4+1, so (budget-1)*4 chars ⇒ ≤ budget tokens).
	max := (budget - 1) * 4
	if max < 0 {
		max = 0
	}
	if len(text) <= max {
		return text
	}
	return text[:max]
}

var atomRe = regexp.MustCompile(`[A-Za-z0-9_]{3,}`)

// literalAtoms pulls the ≥3-char literal runs from a regex to seed the trigram
// pre-filter. It is a recall aid only — the exact regexp verify is authoritative,
// so an over-broad atom set never causes a false match.
func literalAtoms(pattern string) []string {
	return atomRe.FindAllString(pattern, -1)
}
