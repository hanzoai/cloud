// Package code mounts the Hanzo Cloud /v1/code/* surface: a native, per-org
// code-intelligence engine for AI coding agents and the hanzo.app UI. Retrieval
// is HYBRID — three orthogonal tiers fused with reciprocal-rank fusion, the SOTA
// lesson that embeddings alone under-serve code search:
//
//   - lexical  (store.go/tokenize.go) — FTS5 trigram over code-tokenized text
//     (camelCase/snake_case split, operators kept); substring + regex (Zoekt model).
//   - symbolic (parse.go)             — go/parser for Go (real def/ref edges) and
//     compact lexical extractors for TS/JS/Python/Rust/Solidity: go-to-symbol +
//     a def→ref edge table.
//   - semantic (embed.go/search.go)   — AST-boundary chunks embedded via the SAME
//     gateway /embeddings clients/knowledge uses, ranked by cosine over a float32
//     vector table (the sqlite-vec `vec0` drop-in seam).
//
// Storage is ONE SQLite file per org at {DataDir}/orgs/{slug}/code.db (HIP-0302):
// the org boundary is PHYSICAL — a query in one org's file can never reach
// another org's rows. Every request resolves its org through principal.Org
// (the ONE gate): no validated principal ⇒ 403, and a client X-Org-Id is never
// trusted.
//
// Surface (all org-scoped; /v1 only):
//
//	GET  /v1/code/search   ?q=&type=text|regex|symbol|semantic|hybrid&repo=&limit=
//	POST /v1/code/context  {query,budgetTokens,repo}  → budget-packed context bundle
//	GET  /v1/code/ask      ?q=&repo=  (or POST {query,repo})  → cited RAG answer
//	POST /v1/code/index    {repo,files:[{path,content}],prune}  → (re)index, incremental
//
// Order 134: binds /v1/code/* before the AI subsystem's /v1/* catch-all (150).
package code

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Bounds keep one request from amplifying the shared file or the gateway.
const (
	maxIndexFiles        = 20000
	maxFileBytes         = 1 << 20 // 1 MiB per file
	maxTotalBytes        = 1 << 30 // 1 GiB per index call
	defaultSearchLimit   = 20
	maxSearchLimit       = 100
	defaultContextBudget = 4000
	minContextBudget     = 256
	maxContextBudget     = 32000
	maxRepoLen           = 200
	maxQueryLen          = 4000
)

// service is the process-wide subsystem: the shared embedder + synthesizer and a
// lazily-opened, cached per-org store. It holds no org in a field — the org is a
// parameter on every call, so one service serves all orgs and an org can never
// be captured from stale state.
type service struct {
	dataDir string
	embed   Embedder
	synth   Synthesizer
	log     luxlog.Logger

	// stores is the shared per-org SQLite cache: one org-scoped code.db per
	// org, opened once via cloud.OrgDB. dataDir is retained only so the
	// physical path convention stays inspectable (tests) — the cache owns opens.
	stores *cloud.OrgStore[*Store]
}

var mounted *service

// Mount wires /v1/code/* onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("code.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("code.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("code.Mount: empty DataDir")
	}
	s := &service{
		dataDir: deps.DataDir,
		embed:   newEmbedder(deps.Embed, ""),            // embeddings ride the read-only (pk-) embed credential
		synth:   newSynth(deps.AI, deps.AIDefaultModel), // synthesis is chat completion → M2M
		log:     deps.Logger.New("subsystem", "code"),
		stores:  cloud.NewOrgStore(deps.DataDir, "code", openStore),
	}
	mounted = s

	routes(app, s)

	s.log.Info("code surface mounted (native)",
		"brand", deps.Brand, "semantic", s.embed.Enabled(), "synth", s.synth.Enabled())
	return nil
}

// routes is the ONE place the surface is wired. Every route is a zip TYPED op, so
// the REST route, the OpenAPI document, the MCP tool and the CLI command all come
// from the one declaration; the bridge goes on FIRST because a typed op is handed
// only a context, so the request (and the validated principal it proves) is parked
// there.
//
// /ask is registered twice on purpose: it takes its question from `?q=` on GET and
// from a `{query}` body on POST, which are two different input shapes and so two
// declarations over one implementation.
func routes(app cloud.Router, s *service) {
	z := cloud.ZipApp(app)
	app.Group("/v1/code").Use(cloud.Bridge())

	zip.Get(z, "/v1/code/search", s.search, zip.WithOperationID("searchCode"))
	zip.Post(z, "/v1/code/context", s.context, zip.WithOperationID("packCodeContext"))
	zip.Get(z, "/v1/code/ask", s.askByQuery, zip.WithOperationID("askCode"))
	zip.Post(z, "/v1/code/ask", s.ask, zip.WithOperationID("askCodePost"))
	zip.Post(z, "/v1/code/index", s.index, zip.WithOperationID("indexCode"))
	// Repo-inspection primitives (the zread contract over the org's own index):
	// tree = get_repo_structure, file = read_file.
	zip.Get(z, "/v1/code/tree", s.tree, zip.WithOperationID("codeTree"))
	zip.Get(z, "/v1/code/file", s.file, zip.WithOperationID("codeFile"))
}

// shutdown closes every open per-org store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.stores.CloseAll()
	mounted = nil
	return err
}

// storeFor lazily opens (and caches) the org's SQLite file through the shared
// cloud.OrgStore cache. The physical path is {DataDir}/orgs/{orgSlug}/code.db
// (org-scoped; code carries no project axis), where orgSlug = cloud.SanitizeOrg,
// the codebase's ONE injective org-slug normalizer (shared with S3/KMS/knowledge),
// so two distinct orgs never fold onto one file.
func (s *service) storeFor(org string) (*Store, error) {
	return s.stores.For(org, "")
}

func (s *service) engineFor(org, billingOrg, project string) (*engine, error) {
	st, err := s.storeFor(org)
	if err != nil {
		return nil, err
	}
	return &engine{store: st, embed: s.embed, org: org, billingOrg: billingOrg, project: project}, nil
}

// caller is the ONE gate: it resolves the org for a request, but ONLY for a
// validated principal (SanitizeIdentity set it from a verified credential), and
// hands back the request the billing ledger and project sub-scope are read off.
// Both are facts of the REQUEST, never of the input — an input is what the caller
// says about itself. An unvalidated or org-less request gets no org, so the op
// answers 403 — never another org's code. This is the SAME gate clients/eval and
// clients/knowledge use, and off the HTTP path there is neither fact, so the op
// refuses exactly as an anonymous caller is refused.
func caller(ctx context.Context) (string, *zip.Ctx, error) {
	o, ok := principal.OrgFrom(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("valid principal required")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", nil, zip.ErrForbidden("valid principal required")
	}
	return o, c, nil
}

// ── HTTP shapes ──────────────────────────────────────────────────────────────

// IndexRequest is one index pass: the repo label, the files, and whether files
// absent from the payload are pruned (a full-tree reconcile).
type IndexRequest struct {
	// Repo is the repo label the files are indexed under. Required.
	Repo string `json:"repo"`
	// Files are the files to index; at most 20000, 1 MiB each, 1 GiB in total.
	Files []File `json:"files"`
	// Prune removes indexed files absent from this payload, making the pass a
	// full-tree reconcile.
	Prune bool `json:"prune,omitempty"`
}

// ContextRequest asks for a budget-packed context bundle over the org's index.
type ContextRequest struct {
	// Query is what the bundle is packed to answer. Required, max 4000 chars.
	Query string `json:"query"`
	// BudgetTokens caps the bundle; 0 means 4000, and it is clamped to 256..32000.
	BudgetTokens int `json:"budgetTokens,omitempty"`
	// Repo narrows the search to one indexed repo; empty searches all of them.
	Repo string `json:"repo,omitempty"`
}

// AskRequest is the POST form of a cited RAG question.
type AskRequest struct {
	// Query is the question to answer. Required, max 4000 chars.
	Query string `json:"query"`
	// Repo narrows the retrieval to one indexed repo; empty searches all of them.
	Repo string `json:"repo,omitempty"`
}

// AskQuery is the GET form of a cited RAG question, taken from the URL.
type AskQuery struct {
	// Q is the question to answer. Required, max 4000 chars.
	Q string `json:"q"`
	// Repo narrows the retrieval to one indexed repo; empty searches all of them.
	Repo string `json:"repo,omitempty"`
}

// SearchQuery is one hybrid search over the org's index.
type SearchQuery struct {
	// Q is the search query. Required, max 4000 chars.
	Q string `json:"q"`
	// Type selects the tier: text, regex, symbol, semantic or hybrid (the default,
	// and what any unknown value falls back to).
	Type string `json:"type,omitempty"`
	// Repo narrows the search to one indexed repo; empty searches all of them.
	Repo string `json:"repo,omitempty"`
	// Limit caps the spans returned; 0 means 20 and nothing above 100 is honoured.
	Limit int `json:"limit,omitempty"`
}

// SearchResults is a hybrid search's answer, echoing the query it ran.
type SearchResults struct {
	// Query echoes the query that was run.
	Query string `json:"query"`
	// Type echoes the tier that was run, after defaulting.
	Type string `json:"type"`
	// Results are the matching spans, ranked; empty when nothing matched.
	Results []Span `json:"results"`
	// Degraded is true when retrieval failed and the empty result is an outage,
	// not an absence of matches.
	Degraded bool `json:"degraded,omitempty"`
}

// RepoQuery addresses one indexed repo.
type RepoQuery struct {
	// Repo is the repo label to inspect. Required.
	Repo string `json:"repo"`
}

// Tree is a repo's indexed file structure.
type Tree struct {
	// Repo echoes the repo the tree is of.
	Repo string `json:"repo"`
	// Files is one entry per indexed file, with its per-file symbol count; empty
	// for a repo that has not been indexed.
	Files []TreeEntry `json:"files"`
}

// FileQuery addresses one indexed file within a repo.
type FileQuery struct {
	// Repo is the repo the file belongs to. Required.
	Repo string `json:"repo"`
	// Path is the file's repo-relative path. Required.
	Path string `json:"path"`
}

// FileContent is one file as the INDEX holds it — the chunks the search tiers
// carry, not the byte-verbatim blob (the git object plane owns exact bytes).
type FileContent struct {
	// Repo echoes the repo the file belongs to.
	Repo string `json:"repo"`
	// Path echoes the file's repo-relative path.
	Path string `json:"path"`
	// Lang is the language the parser detected.
	Lang string `json:"lang"`
	// Content is the indexed text, reassembled from the file's chunks.
	Content string `json:"content"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

// search runs one hybrid search over the caller org's code index.
//
// The tier is chosen by type — text, regex, symbol, semantic or hybrid (the
// default, which fuses all three with reciprocal-rank fusion).
// A retrieval outage answers 200 with an empty result and degraded true, never a
// 5xx to the agent; only a malformed regex is a 400.
//
// Response: {"query": "greet", "type": "hybrid", "results": []}
func (s *service) search(ctx context.Context, in *SearchQuery) (*SearchResults, error) {
	org, c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(in.Q)
	if query == "" {
		return nil, zip.ErrBadRequest("q is required")
	}
	if len(query) > maxQueryLen {
		return nil, zip.ErrBadRequest("q too long")
	}
	typ := searchType(in.Type)
	repo, err := cleanRepo(in.Repo, false)
	if err != nil {
		return nil, err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	spans, err := eng.search(ctx, repo, typ, query, searchLimit(in.Limit))
	if err != nil {
		if typ == "regex" {
			return nil, zip.ErrBadRequest("invalid regex: " + err.Error())
		}
		// Fail-honest: a retrieval outage returns empty, never a 5xx to the agent.
		s.log.Warn("code search failed", "org", org, "type", typ, "err", err)
		return &SearchResults{Query: query, Type: typ, Results: []Span{}, Degraded: true}, nil
	}
	if spans == nil {
		spans = []Span{}
	}
	return &SearchResults{Query: query, Type: typ, Results: spans}, nil
}

// context packs the org's index into a token-budgeted bundle for one question.
//
// It is THE agent primitive: retrieval, ranking and packing in one call, so an
// agent spends its context window on the spans most likely to answer the query.
// A retrieval outage answers 200 with an empty bundle rather than a 5xx.
//
// Example: {"query": "how does hello work", "budgetTokens": 4000, "repo": "svc"}
func (s *service) context(ctx context.Context, in *ContextRequest) (*ContextBundle, error) {
	org, c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return nil, zip.ErrBadRequest("query is required")
	}
	if len(query) > maxQueryLen {
		return nil, zip.ErrBadRequest("query too long")
	}
	repo, err := cleanRepo(in.Repo, false)
	if err != nil {
		return nil, err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	budget := clampBudget(in.BudgetTokens)
	bundle, err := eng.packContext(ctx, repo, query, budget)
	if err != nil {
		s.log.Warn("code context failed", "org", org, "err", err)
		return &ContextBundle{Query: query, Repo: repo, BudgetTokens: budget, Spans: []Span{}}, nil
	}
	if bundle.Spans == nil {
		bundle.Spans = []Span{}
	}
	return &bundle, nil
}

// tree returns a repo's indexed file structure with per-file symbol counts.
//
// It is get_repo_structure over the org's OWN indexed corpus — no git checkout.
// A repo that has not been indexed returns an empty tree, never an error.
//
// Example: {"repo": "svc"}
// Response: {"repo": "svc", "files": []}
func (s *service) tree(ctx context.Context, in *RepoQuery) (*Tree, error) {
	org, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := cleanRepo(in.Repo, true) // required: a tree is repo-scoped
	if err != nil {
		return nil, err
	}
	store, err := s.storeFor(org)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	entries, err := store.tree(ctx, repo)
	if err != nil {
		s.log.Warn("code tree failed", "org", org, "repo", repo, "err", err)
		return &Tree{Repo: repo, Files: []TreeEntry{}}, nil
	}
	if entries == nil {
		entries = []TreeEntry{}
	}
	return &Tree{Repo: repo, Files: entries}, nil
}

// file returns the INDEXED content of one file, as the search tiers hold it.
//
// It is a fast "show the code the index knows" for context, and it is NOT
// byte-verbatim: the git object plane is the source of record for exact bytes,
// history and blame.
// A file absent from the index is a 404, so an agent can tell "not indexed" from
// an empty file.
//
// Example: {"repo": "svc", "path": "greeter.go"}
func (s *service) file(ctx context.Context, in *FileQuery) (*FileContent, error) {
	org, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := cleanRepo(in.Repo, true)
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(in.Path)
	if path == "" {
		return nil, zip.ErrBadRequest("path is required")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	content, lang, err := store.fileContent(ctx, repo, path)
	if err != nil {
		s.log.Warn("code file failed", "org", org, "repo", repo, "path", path, "err", err)
		return nil, zip.ErrInternal("read file")
	}
	if content == "" && lang == "" {
		return nil, zip.ErrNotFound("file not indexed: " + path)
	}
	return &FileContent{Repo: repo, Path: path, Lang: lang, Content: content}, nil
}

// askByQuery answers a question about the org's code from the URL, with citations.
//
// It is the GET form of ask: the question rides `?q=` instead of a body, and the
// answer is identical.
//
// Example: {"q": "how does hello work", "repo": "svc"}
func (s *service) askByQuery(ctx context.Context, in *AskQuery) (*AskAnswer, error) {
	return s.answer(ctx, in.Q, in.Repo)
}

// ask answers a question about the org's code, with citations into the index.
//
// Retrieval runs over the caller org's index and the answer is synthesised from
// what it found, so every claim carries the span it came from.
// A synthesis outage answers 200 with degraded true and no citations, never a 5xx.
//
// Example: {"query": "how does hello work", "repo": "svc"}
func (s *service) ask(ctx context.Context, in *AskRequest) (*AskAnswer, error) {
	return s.answer(ctx, in.Query, in.Repo)
}

// answer is the ONE ask implementation both declarations run: the GET and POST
// forms differ only in where the question rides.
func (s *service) answer(ctx context.Context, question, repo string) (*AskAnswer, error) {
	org, c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(question)
	if query == "" {
		return nil, zip.ErrBadRequest("q is required")
	}
	if len(query) > maxQueryLen {
		return nil, zip.ErrBadRequest("q too long")
	}
	cleanedRepo, err := cleanRepo(repo, false)
	if err != nil {
		return nil, err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	ans, err := eng.ask(ctx, s.synth, cleanedRepo, query)
	if err != nil {
		s.log.Warn("code ask failed", "org", org, "err", err)
		return &AskAnswer{Question: query, Citations: []Citation{}, Degraded: true}, nil
	}
	return &ans, nil
}

// index (re)indexes a repo for the caller org, skipping files whose content is unchanged.
//
// The pass is incremental by content hash, and prune makes it a full-tree
// reconcile: files absent from the payload leave the index.
//
// Example: {"repo": "svc", "files": [{"path": "greeter.go", "content": "package svc"}], "prune": true}
// Response: {"repo": "svc", "indexed": 1, "skipped": 0, "pruned": 0, "files": 1, "symbols": 2, "chunks": 1, "vectors": 1, "semantic": true}
func (s *service) index(ctx context.Context, in *IndexRequest) (*IndexReport, error) {
	org, c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	repo, err := cleanRepo(in.Repo, true)
	if err != nil {
		return nil, err
	}
	if len(in.Files) == 0 {
		return nil, zip.ErrBadRequest("files is required")
	}
	if len(in.Files) > maxIndexFiles {
		return nil, zip.ErrBadRequest(fmt.Sprintf("too many files (max %d)", maxIndexFiles))
	}
	var total int
	for _, f := range in.Files {
		if strings.TrimSpace(f.Path) == "" {
			return nil, zip.ErrBadRequest("every file needs a path")
		}
		if len(f.Content) > maxFileBytes {
			return nil, zip.ErrBadRequest("file too large: " + f.Path)
		}
		total += len(f.Content)
		if total > maxTotalBytes {
			return nil, zip.ErrBadRequest("index payload too large")
		}
	}
	store, err := s.storeFor(org)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	res, err := s.indexRepo(ctx, org, principal.Ledger(c), principal.Project(c), store, repo, in.Files, in.Prune)
	if err != nil {
		s.log.Warn("code index failed", "org", org, "repo", repo, "err", err)
		return nil, zip.ErrInternal("index failed")
	}
	return &res, nil
}

// File is one file to index: its repo-relative path and content. It is BOTH the
// wire element of an index request and the shape the git plane's push→index
// reactor hands in — one file shape, one set of names.
type File struct {
	// Path is the file's repo-relative path.
	Path string `json:"path"`
	// Content is the file's text.
	Content string `json:"content"`
}

// IndexReport is what one index pass wrote — the HTTP answer and the reactor's
// log line read the same value.
type IndexReport struct {
	// Repo echoes the repo that was indexed.
	Repo string `json:"repo"`
	// Indexed counts the files this pass wrote.
	Indexed int `json:"indexed"`
	// Skipped counts the files whose content hash was unchanged.
	Skipped int `json:"skipped"`
	// Pruned counts the indexed files removed because the payload omitted them.
	Pruned int `json:"pruned"`
	// Files is the repo's total indexed file count after the pass.
	Files int `json:"files"`
	// Symbols is the repo's total symbol count after the pass.
	Symbols int `json:"symbols"`
	// Chunks is the repo's total chunk count after the pass.
	Chunks int `json:"chunks"`
	// Vectors is the repo's total embedded-chunk count after the pass.
	Vectors int `json:"vectors"`
	// Semantic is whether the embedder was enabled for this pass.
	Semantic bool `json:"semantic"`
}

// IndexFiles indexes a repo's files into the org's code index — the package-level
// seam the git plane's lifecycle reactor calls on push (clients/git owns the repo
// bytes; clients/code owns the index; neither imports the other, so the reactor
// reads the tree and hands it here). It reuses the exact per-file pipeline the
// POST /v1/code/index handler runs, with prune=true so a push is a full-tree
// reconcile (deleted files leave the index). A nil/unmounted service is a no-op —
// the reactor is best-effort and must never block the push/deploy path. Over-limit
// inputs are bounded, not rejected: indexing is a background enrichment, so a huge
// push indexes what fits rather than failing the whole repo.
func IndexFiles(ctx context.Context, org, billingOrg, project, repo string, files []File) (IndexReport, error) {
	s := mounted
	if s == nil || org == "" || repo == "" {
		return IndexReport{}, nil
	}
	in := make([]File, 0, len(files))
	var total int
	for _, f := range files {
		if strings.TrimSpace(f.Path) == "" || len(f.Content) > maxFileBytes {
			continue // skip an unnamed or oversized file rather than fail the push
		}
		if total += len(f.Content); total > maxTotalBytes {
			break // index what fits; a giant push is bounded, not dropped
		}
		if len(in) >= maxIndexFiles {
			break
		}
		in = append(in, f)
	}
	if len(in) == 0 {
		return IndexReport{Repo: repo}, nil
	}
	store, err := s.storeFor(org)
	if err != nil {
		return IndexReport{}, err
	}
	return s.indexRepo(ctx, org, billingOrg, project, store, repo, in, true /* prune: full-tree reconcile */)
}

// indexRepo runs the pipeline per file: skip-if-unchanged (content hash) → parse
// → embed chunks → atomically replace the file's artifacts. prune removes indexed
// files absent from the payload (a full-tree reconcile).
func (s *service) indexRepo(ctx context.Context, org, billingOrg, project string, store *Store, repo string, files []File, prune bool) (IndexReport, error) {
	now := time.Now().Unix()
	var indexed, skipped int
	present := make(map[string]bool, len(files))
	for _, f := range files {
		path := strings.TrimSpace(f.Path)
		present[path] = true
		h := sha256Hex(f.Content)
		if prev, err := store.fileHash(ctx, repo, path); err == nil && prev == h {
			skipped++
			continue
		}
		parsed := Parse(path, f.Content)
		var vecs [][]float32
		if s.embed.Enabled() && len(parsed.Chunks) > 0 {
			texts := make([]string, len(parsed.Chunks))
			for i, ch := range parsed.Chunks {
				texts[i] = ch.Text
			}
			if v, err := s.embed.Embed(ctx, org, billingOrg, project, texts); err != nil {
				s.log.Warn("embed failed, indexing lexical-only", "repo", repo, "path", path, "err", err)
			} else {
				vecs = v
			}
		}
		if err := store.writeFile(ctx, repo, path, int64(len(f.Content)), h, now, parsed, vecs); err != nil {
			return IndexReport{}, err
		}
		indexed++
	}
	var pruned int
	if prune {
		if existing, err := store.listFilePaths(ctx, repo); err == nil {
			for _, p := range existing {
				if !present[p] {
					if err := store.deleteFile(ctx, repo, p); err == nil {
						pruned++
					}
				}
			}
		}
	}
	nf, ns, nc, nv := store.stats(ctx, repo)
	return IndexReport{
		Repo: repo, Indexed: indexed, Skipped: skipped, Pruned: pruned,
		Files: nf, Symbols: ns, Chunks: nc, Vectors: nv, Semantic: s.embed.Enabled(),
	}, nil
}

// ── request helpers ──────────────────────────────────────────────────────────

func searchType(t string) string {
	switch t {
	case "text", "regex", "symbol", "semantic", "hybrid":
		return t
	default:
		return "hybrid"
	}
}

// searchLimit bounds a caller's page size: absent or non-positive means
// defaultSearchLimit, and nothing above maxSearchLimit is honoured.
func searchLimit(n int) int {
	if n <= 0 {
		return defaultSearchLimit
	}
	if n > maxSearchLimit {
		return maxSearchLimit
	}
	return n
}

func clampBudget(n int) int {
	if n <= 0 {
		return defaultContextBudget
	}
	if n < minContextBudget {
		return minContextBudget
	}
	if n > maxContextBudget {
		return maxContextBudget
	}
	return n
}

// cleanRepo trims + bounds a repo label. It is a stored column value (never a
// filesystem path — the org file already isolates the org), so it needs only
// length bounding and, when required, non-emptiness.
func cleanRepo(repo string, required bool) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		if required {
			return "", zip.ErrBadRequest("repo is required")
		}
		return "", nil
	}
	if len(repo) > maxRepoLen {
		return "", zip.ErrBadRequest("repo too long")
	}
	return repo, nil
}
