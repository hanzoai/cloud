package code

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// Span is one ranked result — the unit both /search and /context return. For
// search, Snippet is a bounded excerpt; for context, it is the full AST chunk the
// agent pastes into its window.
type Span struct {
	Repo    string  `json:"repo"`
	File    string  `json:"file"`
	Line    int     `json:"line"`
	EndLine int     `json:"endLine"`
	Kind    string  `json:"kind,omitempty"`
	Symbol  string  `json:"symbol,omitempty"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
	Tier    string  `json:"tier,omitempty"`
	Role    string  `json:"role,omitempty"` // context: match | definition | caller
}

const (
	rrfK              = 60.0
	candidatePool     = 3 // per-tier candidate multiple of the requested limit
	maxVectorScan     = 50000
	maxRegexScan      = 5000
	maxRegexCandidate = 2000
	snippetMaxLines   = 40
	snippetMaxChars   = 2000
)

// engine composes the three retrieval tiers over one org store plus the shared
// embedder. It holds no per-request state; the store IS the org.
type engine struct {
	store *Store
	embed Embedder
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

// searchText runs the trigram lexical tier: code-tokenized query → FTS5 MATCH,
// bm25-ordered.
func (e *engine) searchText(ctx context.Context, repo, query string, limit int) ([]chunkRow, error) {
	match, ok := buildFTSMatch(query)
	if !ok {
		return nil, nil
	}
	return e.store.ftsSearch(ctx, repo, match, limit)
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
		var quoted []string
		for _, a := range atoms {
			quoted = append(quoted, `"`+strings.ReplaceAll(a, `"`, `""`)+`"`)
		}
		cands, err = e.store.ftsSearch(ctx, repo, strings.Join(quoted, " OR "), maxRegexCandidate)
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

// searchSemantic runs the vector tier: embed the query, brute-force cosine over
// the repo's vectors, and return the top chunks. Disabled (no embedder) or an
// unembedded index yields an empty result — the hybrid caller degrades to
// lexical + symbolic rather than failing.
func (e *engine) searchSemantic(ctx context.Context, repo, query string, limit int) ([]chunkRow, error) {
	if e.embed == nil || !e.embed.Enabled() {
		return nil, nil
	}
	qv, err := e.embed.Embed(ctx, []string{query})
	if err != nil || len(qv) == 0 {
		return nil, err
	}
	ids, vecs, err := e.store.loadVectors(ctx, repo, maxVectorScan)
	if err != nil {
		return nil, err
	}
	scored := make([]scoredID, 0, len(ids))
	for i, id := range ids {
		// Drop non-positive similarity: an unrelated chunk must not surface just
		// because the corpus is small (it would pollute fusion + context seeds).
		if sim := cosine(qv[0], vecs[i]); sim > 0 {
			scored = append(scored, scoredID{id: id, score: sim})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].score > scored[j].score })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	var out []chunkRow
	for _, s := range scored {
		row, err := e.store.getChunk(ctx, s.id)
		if err != nil {
			continue
		}
		row.Score = s.score
		out = append(out, row)
	}
	return out, nil
}

type scoredID struct {
	id    int64
	score float64
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
	fused := rrf([][]chunkRow{text, symChunks, sem}, limit)
	return e.rowsToSpans(ctx, fused, "hybrid"), nil
}

// rrf fuses ranked lists by reciprocal-rank fusion: score(d)=Σ 1/(k+rank). k=60
// (the standard constant) damps the contribution of low-ranked items so a strong
// consensus across tiers beats a single tier's top hit. Ties break by the fused
// score already; the first row seen for a key supplies its display fields.
func rrf(lists [][]chunkRow, limit int) []chunkRow {
	score := map[int64]float64{}
	first := map[int64]chunkRow{}
	var order []int64
	for _, list := range lists {
		for rank, row := range list {
			if _, ok := first[row.ID]; !ok {
				first[row.ID] = row
				order = append(order, row.ID)
			}
			score[row.ID] += 1.0 / (rrfK + float64(rank+1))
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return score[order[i]] > score[order[j]] })
	if len(order) > limit {
		order = order[:limit]
	}
	out := make([]chunkRow, 0, len(order))
	for _, id := range order {
		row := first[id]
		row.Score = score[id]
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
	Query        string `json:"query"`
	Repo         string `json:"repo,omitempty"`
	BudgetTokens int    `json:"budgetTokens"`
	UsedTokens   int    `json:"usedTokens"`
	Spans        []Span `json:"spans"`
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

	bundle := ContextBundle{Query: query, Repo: repo, BudgetTokens: budget}
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
	return rrf([][]chunkRow{text, symChunks, sem}, limit)
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
