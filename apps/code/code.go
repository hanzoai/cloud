// Package code is search and symbols across your repos, for you and your agents.
//
// A native, per-org code-intelligence engine for AI coding agents and the
// hanzo.app UI. Retrieval is HYBRID — three orthogonal tiers fused with
// reciprocal-rank fusion, the SOTA lesson that embeddings alone under-serve
// code search:
//
//   - lexical  (store.go/tokenize.go) — FTS5 trigram over code-tokenized text
//     (camelCase/snake_case split, operators kept); substring + regex (Zoekt model).
//   - symbolic (parse.go)             — go/parser for Go (real def/ref edges) and
//     compact lexical extractors for TS/JS/Python/Rust/Solidity: go-to-symbol +
//     a def→ref edge table.
//   - semantic (embed.go/search.go)   — AST-boundary chunks embedded via the SAME
//     gateway /embeddings clients/knowledge uses, ranked by cosine over a float32
//     vector table (the sqlite-vec `vec0` drop-in client).
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

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
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
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("code.Use:  nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("code.Use:  empty DataDir")
	}
	b := cloud.NewBase(deps, "code")
	s := &service{
		dataDir: cloud.DataDir(),
		embed:   newEmbedder(deps.Embed, ""),           // embeddings ride the read-only (pk-) embed credential
		synth:   newSynth(deps.AI, cloud.DefaultModel), // synthesis is chat completion → M2M
		log:     b.Log,
		stores:  cloud.NewOrgStore(b, "code", openStore),
	}
	mounted = s

	exposeIndex()

	if err := routes(app, s); err != nil {
		return err
	}

	s.log.Info("code surface mounted (native)",
		"brand", cloud.Brand(), "semantic", s.embed.Enabled(), "synth", s.synth.Enabled())
	return nil
}

// routes registers the /v1/code surface. It is a FUNCTION rather than inline in
// Mount so this package's own tests drive the REAL registration instead of a
// reconstruction of it that can drift from what the binary serves.
func routes(app cloud.Router, s *service) error {
	// The two operations named in cloud's paidReads are here, so this surface's
	// READS spend: /search embeds the query and /ask synthesizes the answer, both
	// against the caller's balance. account's control asks the same money rule the
	// balance gate does and applies only to the requests it says cost something —
	// so /tree and /file, which are free, pass untouched, and a caller presenting
	// any credential pays nothing for it either.
	g := app.Group("/v1/code", account.RequireCSRFOnSpend())
	// cloud.Bridge is not installed here: the composer installs it once at the
	// root, after the identity check that mints the validated org and before any
	// subsystem registers a route — an order only the whole program can assert.
	// The ops below read what it parks off the context.

	// Every route is a TYPED op: one registry entry, which is what the OpenAPI
	// operation, the MCP tool, the CLI command and every generated SDK method are
	// all projected from. This surface is built FOR coding agents, so the MCP tool
	// list is not a side benefit of typing it — it is the point.
	zip.Get(g, "/search", s.search)
	zip.Post(g, "/context", s.context)
	zip.Get(g, "/ask", s.askGet)
	zip.Post(g, "/ask", s.askPost)
	zip.Post(g, "/index", s.index)
	// Repo-inspection primitives (the zread contract over the org's own index):
	// tree = get_repo_structure, file = read_file.
	zip.Get(g, "/tree", s.tree)
	zip.Get(g, "/file", s.file)

	return nil
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

// storeFor is the ONE way this package reaches a store: it names the database
// through cloud.OrgNamespace — the single path a validated org takes —
// and asks the registry for that name. Nothing else here resolves a store, so
// "which file does this request touch" has one answer from one input.
//
// org MUST already be validated: principal.Org for a request, or the caller's
// own server-side resolution for an in-process client.
//
// code is org-scoped: it carries no project axis.
func (s *service) storeFor(org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.stores.For(ns)
}

func (s *service) engineFor(org, billingOrg, project string) (*engine, error) {
	st, err := s.storeFor(org)
	if err != nil {
		return nil, err
	}
	return &engine{store: st, embed: s.embed, org: org, billingOrg: billingOrg, project: project}, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/code openapi`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ── the identity client ────────────────────────────────────────────────────────

// meter resolves the two facts a retrieval charges and scopes against, beyond
// the tenant — the ONE reason this package reaches for the REQUEST.
//
// The PAYER is principal.Ledger: the SELECTED billing org, which a SuperAdmin
// masquerade deliberately moves OFF the effective org, so it is not what
// principal.OrgFrom carries. The PROJECT is X-Project-Id, a scope the gateway and
// cloud.SanitizeIdentity mint SERVER-SIDE from a validated claim after stripping
// any client copy; a caller-supplied one would let a request bill and scope an
// embedding call under a project no minter ever validated, so neither may be an
// In field.
//
// Off the HTTP path both are empty, which is the unbilled, default-project
// answer — and principal.Acting has already refused before any op reaches here.
func meter(ctx context.Context) (billingOrg, project string) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", ""
	}
	return principal.Ledger(c), principal.Project(c)
}

// ── HTTP shapes ──────────────────────────────────────────────────────────────

type fileInput struct {
	// Path is the file's repo-relative path, e.g. "internal/store/db.go".
	Path string `json:"path"`
	// Content is the file's full text. Max 1 MiB per file; binary files should
	// simply be omitted rather than sent.
	Content string `json:"content"`
}

// indexIn is one (re)index pass over a repo. Every field is BODY-only
// (`url:"-"`): zip's binder fills an In field from the query string as well, and
// this route has never taken a repo or a prune flag there — without the opt-out
// `?prune=1` would delete files the body never asked to remove.
type indexIn struct {
	// Repo is the repository label to index under. Required, max 200 bytes. It is
	// a stored column value, not a filesystem path.
	Repo string `json:"repo" url:"-"`
	// Files is the full set of files to index. Required and non-empty; max 20000
	// files, 1 MiB per file and 1 GiB in total. Unchanged files are skipped by
	// content hash, so re-sending the whole tree is cheap.
	Files []fileInput `json:"files" url:"-"`
	// Prune deletes indexed files that are NOT in this request — which makes the
	// call a full sync of the repo rather than an upsert. Only pass it when Files
	// is the complete tree.
	Prune bool `json:"prune,omitempty" url:"-"`
}

type indexResult struct {
	// Repo is the repository that was indexed.
	Repo string `json:"repo"`
	// Indexed is how many files were parsed and written on this pass.
	Indexed int `json:"indexed"`
	// Skipped is how many files were unchanged by content hash and left alone.
	Skipped int `json:"skipped"`
	// Pruned is how many stored files were deleted because prune was set and they
	// were absent from the request.
	Pruned int `json:"pruned"`
	// Files is how many files the repo holds after this pass.
	Files int `json:"files"`
	// Symbols is how many symbol definitions the repo holds after this pass.
	Symbols int `json:"symbols"`
	// Chunks is how many AST-boundary chunks the repo holds after this pass.
	Chunks int `json:"chunks"`
	// Vectors is how many of those chunks carry an embedding.
	Vectors int `json:"vectors"`
	// Semantic reports whether the semantic tier was available for this pass. When
	// false the index is lexical + symbolic only and hybrid search still works.
	Semantic bool `json:"semantic"`
}

// contextIn asks for a budget-packed context bundle. Every field is BODY-only
// (`url:"-"`), the way c.Bind read them.
type contextIn struct {
	// Query is what to retrieve context for. Required, max 4000 bytes.
	Query string `json:"query" url:"-"`
	// BudgetTokens caps the bundle's size. Clamped to [256, 32000]; 0 or absent
	// uses 4000.
	BudgetTokens int `json:"budgetTokens,omitempty" url:"-"`
	// Repo narrows retrieval to one repository. Empty searches every repo the org
	// has indexed.
	Repo string `json:"repo,omitempty" url:"-"`
}

// searchIn is one hybrid-search request. All three come from the query string.
type searchIn struct {
	// Q is the search query. Required, max 4000 bytes. For type=regex it is a
	// regular expression; for type=symbol it is a symbol name.
	Q string `json:"q"`
	// Type selects the retrieval tier: "text" (FTS5 trigram), "regex",
	// "symbol" (definitions), "semantic" (embeddings) or "hybrid". Anything
	// else — including empty — reads as hybrid.
	Type string `json:"type"`
	// Repo narrows to one repository. Empty searches every repo the org has indexed.
	Repo string `json:"repo"`
	// Limit caps how many spans come back: default 20, maximum 100. A value that
	// is not a positive integer reads as the default.
	Limit int `json:"limit"`
}

// searchResults is what a search answers.
type searchResults struct {
	// Degraded is true when retrieval failed and the empty result set is an
	// outage rather than a real absence of matches. Absent on a healthy answer.
	Degraded bool `json:"degraded,omitempty"`
	// Query echoes the query that was run.
	Query string `json:"query"`
	// Results are the matching spans, best first. Never null — an empty search is
	// an empty array.
	Results []Span `json:"results"`
	// Type echoes the retrieval tier that ran, after defaulting.
	Type string `json:"type"`
}

// treeIn addresses one repository's file structure.
type treeIn struct {
	// Repo is the repository to walk. REQUIRED — a tree is repo-scoped.
	Repo string `json:"repo"`
}

// repoTree is a repository's structure as the index knows it.
type repoTree struct {
	// Files are the repo's indexed files in path order, each with its language
	// and how many symbols it defines. Never null.
	Files []TreeEntry `json:"files"`
	// Repo echoes the repository that was walked.
	Repo string `json:"repo"`
}

// fileIn addresses one indexed file.
type fileIn struct {
	// Path is the file's repo-relative path. Required.
	Path string `json:"path"`
	// Repo is the repository the file belongs to. REQUIRED.
	Repo string `json:"repo"`
}

// fileContent is one indexed file as the index holds it.
type fileContent struct {
	// Content is the file's text as the index stored it. It is NOT guaranteed
	// byte-verbatim — the git object plane is the source of record for exact
	// bytes, history and blame.
	Content string `json:"content"`
	// Lang is the detected language.
	Lang string `json:"lang"`
	// Path echoes the file that was read.
	Path string `json:"path"`
	// Repo echoes the repository it came from.
	Repo string `json:"repo"`
}

// askIn is a question for the GET form, which reads it from the query string.
type askIn struct {
	// Q is the question to answer. Required, max 4000 bytes.
	Q string `json:"q"`
	// Repo narrows retrieval to one repository. Empty searches every repo the org
	// has indexed.
	Repo string `json:"repo"`
}

// askPostIn is a question for the POST form, which takes it in the BODY while
// still honouring the query string the GET form uses.
//
// The two halves are spelled separately on purpose. This route has always read
// `?q=` and `?repo=` FIRST and let a non-empty body field override them — the
// opposite of zip's binding order, which fills a field from the body and then
// lets the query overwrite it. One field per source, each opted out of the other
// half with `json:"-"` / `url:"-"`, is what lets the handler reproduce the
// original precedence exactly instead of inverting it.
type askPostIn struct {
	// Q is the question, from the QUERY STRING. The body's `query` wins over it
	// when non-empty.
	Q string `json:"-" url:"q"`
	// RepoQuery is the repository narrowing, from the QUERY STRING. The body's
	// `repo` wins over it when non-empty.
	RepoQuery string `json:"-" url:"repo"`
	// Query is the question, from the BODY. Takes precedence over `?q=`.
	Query string `json:"query" url:"-"`
	// Repo is the repository narrowing, from the BODY. Takes precedence over `?repo=`.
	Repo string `json:"repo" url:"-"`
}

// ── ops ──────────────────────────────────────────────────────────────────────

// search finds code in the caller org's index across three orthogonal retrieval
// tiers fused by reciprocal-rank fusion: lexical (FTS5 trigram over
// code-tokenized text), symbolic (real definition and reference edges), and
// semantic (embedding cosine over AST-boundary chunks). Pick one tier with
// `type`, or leave it to run all three as hybrid, which is what a coding agent
// usually wants. It is FAIL-HONEST: a retrieval outage answers 200 with an empty
// result set and "degraded": true rather than a 5xx, so an agent degrades instead
// of stalling. A malformed regex is a 400.
//
// Example: {"q": "func openStore", "type": "hybrid", "repo": "cloud", "limit": 20}
func (s *service) search(ctx context.Context, in *searchIn) (*searchResults, error) {
	org, err := principal.Acting(ctx)
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
	billingOrg, project := meter(ctx)
	eng, err := s.engineFor(org, billingOrg, project)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	spans, err := eng.search(ctx, repo, typ, query, clampSearchLimit(in.Limit))
	if err != nil {
		if typ == "regex" {
			return nil, zip.ErrBadRequest("invalid regex: " + err.Error())
		}
		// Fail-honest: a retrieval outage returns empty, never a 5xx to the agent.
		s.log.Warn("code search failed", "org", org, "type", typ, "err", err)
		return &searchResults{Query: query, Type: typ, Results: []Span{}, Degraded: true}, nil
	}
	if spans == nil {
		spans = []Span{}
	}
	return &searchResults{Query: query, Type: typ, Results: spans}, nil
}

// context packs the most relevant code for a query into a token budget — THE
// primitive for a coding agent that has to decide what to put in a prompt. It
// retrieves seed spans, expands each with the definitions it calls and its key
// callers, then greedily fills the budget, so the answer is a coherent slice of
// the codebase rather than a list of disconnected matches. The top match is
// always included, truncated if it alone overflows, so a matched query never
// comes back empty. A retrieval outage answers 200 with an empty bundle rather
// than a 5xx.
//
// Example: {"query": "how does the store open a per-org database", "budgetTokens": 4000, "repo": "cloud"}
func (s *service) context(ctx context.Context, in *contextIn) (*ContextBundle, error) {
	org, err := principal.Acting(ctx)
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
	billingOrg, project := meter(ctx)
	eng, err := s.engineFor(org, billingOrg, project)
	if err != nil {
		return nil, zip.ErrInternal("open index")
	}
	bundle, err := eng.packContext(ctx, repo, query, clampBudget(in.BudgetTokens))
	if err != nil {
		s.log.Warn("code context failed", "org", org, "err", err)
		return &ContextBundle{Query: query, Repo: repo, BudgetTokens: clampBudget(in.BudgetTokens), Spans: []Span{}}, nil
	}
	if bundle.Spans == nil {
		bundle.Spans = []Span{}
	}
	return &bundle, nil
}

// tree returns one repository's file structure with a per-file symbol count —
// get_repo_structure over the org's own index, with no git checkout involved. A
// repository that has not been indexed answers an empty tree rather than an
// error, so an agent can tell "nothing here" without handling a failure.
//
// Example: {"repo": "cloud"}
func (s *service) tree(ctx context.Context, in *treeIn) (*repoTree, error) {
	org, err := principal.Acting(ctx)
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
		return &repoTree{Repo: repo, Files: []TreeEntry{}}, nil
	}
	if entries == nil {
		entries = []TreeEntry{}
	}
	return &repoTree{Repo: repo, Files: entries}, nil
}

// file returns the INDEXED content of one file — read_file over the chunks the
// search tiers hold, for pulling up code an agent just found. It is NOT
// byte-verbatim: the git object plane is the source of record for exact bytes,
// history and blame. A file absent from the index is a 404, so an agent can tell
// "not indexed" from "empty file".
//
// Example: {"repo": "cloud", "path": "apps/code/store.go"}
func (s *service) file(ctx context.Context, in *fileIn) (*fileContent, error) {
	org, err := principal.Acting(ctx)
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
	return &fileContent{Content: content, Lang: lang, Path: path, Repo: repo}, nil
}

// askGet answers a question about the caller org's code with a CITED answer:
// retrieval packs grounding context, then the synthesizer writes the answer over
// exactly those spans, which come back alongside it. It never answers without
// grounding — with no matched code the answer is empty and says so, and with no
// synthesizer available the citations still come back with "degraded": true so
// the caller can reason over the spans itself.
//
// Example: {"q": "where is the per-org SQLite file opened", "repo": "cloud"}
func (s *service) askGet(ctx context.Context, in *askIn) (*AskAnswer, error) {
	return s.answer(ctx, in.Q, in.Repo)
}

// askPost is askGet with the question in the request BODY, for a question too
// long or too awkward to put in a URL. `query` and `repo` in the body take
// precedence over `?q=` and `?repo=`; either source works alone.
//
// Example: {"query": "where is the per-org SQLite file opened", "repo": "cloud"}
func (s *service) askPost(ctx context.Context, in *askPostIn) (*AskAnswer, error) {
	query, repo := in.Q, in.RepoQuery
	if q := strings.TrimSpace(in.Query); q != "" {
		query = q
	}
	if in.Repo != "" {
		repo = in.Repo
	}
	return s.answer(ctx, query, repo)
}

// answer is the ONE cited-RAG path both /ask forms take, so the two verbs can
// never drift into two behaviours.
func (s *service) answer(ctx context.Context, rawQuery, rawRepo string) (*AskAnswer, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(rawQuery)
	if query == "" {
		return nil, zip.ErrBadRequest("q is required")
	}
	if len(query) > maxQueryLen {
		return nil, zip.ErrBadRequest("q too long")
	}
	cleanedRepo, err := cleanRepo(rawRepo, false)
	if err != nil {
		return nil, err
	}
	billingOrg, project := meter(ctx)
	eng, err := s.engineFor(org, billingOrg, project)
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

// index (re)indexes a repository for the caller's org, incrementally: files whose
// content hash is unchanged are skipped, so re-sending a whole tree is cheap.
// Each file is parsed for symbols, split at AST boundaries and — when the
// semantic tier is available — embedded, which is what makes it searchable across
// all three retrieval tiers. Pass `prune` to also DELETE indexed files absent
// from the request, which turns the call into a full sync; without it the call is
// an upsert. The index is written to the caller org's own physically separate
// database.
//
// Example: {"repo": "cloud", "files": [{"path": "main.go", "content": "package main\n"}], "prune": true}
func (s *service) index(ctx context.Context, in *indexIn) (*indexResult, error) {
	org, err := principal.Acting(ctx)
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
	billingOrg, project := meter(ctx)
	res, err := s.indexRepo(ctx, org, billingOrg, project, store, repo, in.Files, in.Prune)
	if err != nil {
		s.log.Warn("code index failed", "org", org, "repo", repo, "err", err)
		return nil, zip.ErrInternal("index failed")
	}
	return &res, nil
}

// File is one file to index: its repo-relative path and content. The exported
// shape the git plane's push→index reactor hands in (it avoids importing the
// unexported fileInput).
type File struct {
	Path    string
	Content string
}

// IndexResult reports what an index pass wrote, for the reactor's log line.
type IndexResult struct {
	Repo     string
	Indexed  int
	Skipped  int
	Pruned   int
	Symbols  int
	Chunks   int
	Vectors  int
	Semantic bool
}

// IndexFiles indexes a repo's files into the org's code index — the package-level
// client the git plane's lifecycle reactor calls on push (clients/git owns the repo
// bytes; clients/code owns the index; neither imports the other, so the reactor
// reads the tree and hands it here). It reuses the exact per-file pipeline the
// POST /v1/code/index handler runs, with prune=true so a push is a full-tree
// reconcile (deleted files leave the index). A nil/unmounted service is a no-op —
// the reactor is best-effort and must never block the push/deploy path. Over-limit
// inputs are bounded, not rejected: indexing is a background enrichment, so a huge
// push indexes what fits rather than failing the whole repo.
func IndexFiles(ctx context.Context, org, billingOrg, project, repo string, files []File) (IndexResult, error) {
	s := mounted
	if s == nil || org == "" || repo == "" {
		return IndexResult{}, nil
	}
	in := make([]fileInput, 0, len(files))
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
		in = append(in, fileInput{Path: f.Path, Content: f.Content})
	}
	if len(in) == 0 {
		return IndexResult{Repo: repo}, nil
	}
	store, err := s.storeFor(org)
	if err != nil {
		return IndexResult{}, err
	}
	res, err := s.indexRepo(ctx, org, billingOrg, project, store, repo, in, true /* prune: full-tree reconcile */)
	if err != nil {
		return IndexResult{}, err
	}
	return IndexResult{
		Repo: res.Repo, Indexed: res.Indexed, Skipped: res.Skipped, Pruned: res.Pruned,
		Symbols: res.Symbols, Chunks: res.Chunks, Vectors: res.Vectors, Semantic: res.Semantic,
	}, nil
}

// indexRepo runs the pipeline per file: skip-if-unchanged (content hash) → parse
// → embed chunks → atomically replace the file's artifacts. prune removes indexed
// files absent from the payload (a full-tree reconcile).
func (s *service) indexRepo(ctx context.Context, org, billingOrg, project string, store *Store, repo string, files []fileInput, prune bool) (indexResult, error) {
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
			return indexResult{}, err
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
	return indexResult{
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

// clampSearchLimit bounds a requested page size to (0, maxSearchLimit],
// defaulting anything that is not a positive integer. A `?limit=` value zip
// could not parse as an int arrives here as 0, which is exactly the "absent or
// unusable" case the untyped strconv.Atoi branch answered with the default — so
// the wire is unchanged.
func clampSearchLimit(n int) int {
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
