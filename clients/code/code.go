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

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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

	g := app.Group("/v1/code")
	g.Get("/search", s.handleSearch)
	g.Post("/context", s.handleContext)
	g.Get("/ask", s.handleAsk)
	g.Post("/ask", s.handleAsk)
	g.Post("/index", s.handleIndex)
	// Repo-inspection primitives (the zread contract over the org's own index):
	// tree = get_repo_structure, file = read_file.
	g.Get("/tree", s.handleTree)
	g.Get("/file", s.handleFile)

	s.log.Info("code surface mounted (native)",
		"brand", deps.Brand, "semantic", s.embed.Enabled(), "synth", s.synth.Enabled())
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

// org resolves the org for a request, but ONLY for a validated principal
// (c.User() set by SanitizeIdentity from a verified credential). An unvalidated
// or org-less request gets no org, so the caller returns 403 — never another
// org's code. This is the SAME gate clients/eval + clients/knowledge use.
func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// ── HTTP shapes ──────────────────────────────────────────────────────────────

type fileInput struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type indexReq struct {
	Repo  string      `json:"repo"`
	Files []fileInput `json:"files"`
	Prune bool        `json:"prune,omitempty"`
}

type indexResult struct {
	Repo     string `json:"repo"`
	Indexed  int    `json:"indexed"`
	Skipped  int    `json:"skipped"`
	Pruned   int    `json:"pruned"`
	Files    int    `json:"files"`
	Symbols  int    `json:"symbols"`
	Chunks   int    `json:"chunks"`
	Vectors  int    `json:"vectors"`
	Semantic bool   `json:"semantic"`
}

type contextReq struct {
	Query        string `json:"query"`
	BudgetTokens int    `json:"budgetTokens,omitempty"`
	Repo         string `json:"repo,omitempty"`
}

type askReq struct {
	Query string `json:"query"`
	Repo  string `json:"repo,omitempty"`
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleSearch is the unified hybrid search entry point.
func (s *service) handleSearch(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	query := strings.TrimSpace(c.Query("q"))
	if query == "" {
		return zip.ErrBadRequest("q is required")
	}
	if len(query) > maxQueryLen {
		return zip.ErrBadRequest("q too long")
	}
	typ := searchType(c.Query("type"))
	repo, err := cleanRepo(c.Query("repo"), false)
	if err != nil {
		return err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return zip.ErrInternal("open index")
	}
	spans, err := eng.search(c.Context(), repo, typ, query, searchLimit(c))
	if err != nil {
		if typ == "regex" {
			return zip.ErrBadRequest("invalid regex: " + err.Error())
		}
		// Fail-honest: a retrieval outage returns empty, never a 5xx to the agent.
		s.log.Warn("code search failed", "org", org, "type", typ, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"query": query, "type": typ, "results": []Span{}, "degraded": true})
	}
	if spans == nil {
		spans = []Span{}
	}
	return c.JSON(http.StatusOK, map[string]any{"query": query, "type": typ, "results": spans})
}

// handleContext is THE agent primitive: a budget-packed context bundle.
func (s *service) handleContext(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	var body contextReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		return zip.ErrBadRequest("query is required")
	}
	if len(query) > maxQueryLen {
		return zip.ErrBadRequest("query too long")
	}
	repo, err := cleanRepo(body.Repo, false)
	if err != nil {
		return err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return zip.ErrInternal("open index")
	}
	bundle, err := eng.packContext(c.Context(), repo, query, clampBudget(body.BudgetTokens))
	if err != nil {
		s.log.Warn("code context failed", "org", org, "err", err)
		return c.JSON(http.StatusOK, ContextBundle{Query: query, Repo: repo, BudgetTokens: clampBudget(body.BudgetTokens), Spans: []Span{}})
	}
	if bundle.Spans == nil {
		bundle.Spans = []Span{}
	}
	return c.JSON(http.StatusOK, bundle)
}

// handleTree returns a repo's file structure with per-file symbol counts —
// get_repo_structure over the org's own indexed corpus, no git checkout. An
// unindexed repo returns an empty tree, never an error.
func (s *service) handleTree(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	repo, err := cleanRepo(c.Query("repo"), true) // required: a tree is repo-scoped
	if err != nil {
		return err
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.ErrInternal("open index")
	}
	entries, err := store.tree(c.Context(), repo)
	if err != nil {
		s.log.Warn("code tree failed", "org", org, "repo", repo, "err", err)
		return c.JSON(http.StatusOK, map[string]any{"repo": repo, "files": []TreeEntry{}})
	}
	if entries == nil {
		entries = []TreeEntry{}
	}
	return c.JSON(http.StatusOK, map[string]any{"repo": repo, "files": entries})
}

// handleFile returns the INDEXED content of one file (the chunks the search tiers
// hold) — a fast "show the code the index knows" for context. It is NOT byte-
// verbatim (see Store.fileContent): the git object plane (clients/git, S3-backed)
// is the source of record for exact bytes, history, and blame. A file absent from
// the index is a 404 so the agent can tell "not indexed" from an empty file.
func (s *service) handleFile(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	repo, err := cleanRepo(c.Query("repo"), true)
	if err != nil {
		return err
	}
	path := strings.TrimSpace(c.Query("path"))
	if path == "" {
		return zip.ErrBadRequest("path is required")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.ErrInternal("open index")
	}
	content, lang, err := store.fileContent(c.Context(), repo, path)
	if err != nil {
		s.log.Warn("code file failed", "org", org, "repo", repo, "path", path, "err", err)
		return zip.ErrInternal("read file")
	}
	if content == "" && lang == "" {
		return zip.ErrNotFound("file not indexed: " + path)
	}
	return c.JSON(http.StatusOK, map[string]any{"repo": repo, "path": path, "lang": lang, "content": content})
}

// handleAsk is the cited RAG answer over the org's index.
func (s *service) handleAsk(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	query := strings.TrimSpace(c.Query("q"))
	repo := c.Query("repo")
	if c.Method() == http.MethodPost {
		var body askReq
		if err := c.Bind(&body); err != nil {
			return err
		}
		if q := strings.TrimSpace(body.Query); q != "" {
			query = q
		}
		if body.Repo != "" {
			repo = body.Repo
		}
	}
	if query == "" {
		return zip.ErrBadRequest("q is required")
	}
	if len(query) > maxQueryLen {
		return zip.ErrBadRequest("q too long")
	}
	cleanedRepo, err := cleanRepo(repo, false)
	if err != nil {
		return err
	}
	eng, err := s.engineFor(org, principal.Ledger(c), principal.Project(c))
	if err != nil {
		return zip.ErrInternal("open index")
	}
	ans, err := eng.ask(c.Context(), s.synth, cleanedRepo, query)
	if err != nil {
		s.log.Warn("code ask failed", "org", org, "err", err)
		return c.JSON(http.StatusOK, AskAnswer{Question: query, Citations: []Citation{}, Degraded: true})
	}
	return c.JSON(http.StatusOK, ans)
}

// handleIndex (re)indexes a repo for the caller's org, incrementally (unchanged
// files are skipped by content hash).
func (s *service) handleIndex(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	var body indexReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	repo, err := cleanRepo(body.Repo, true)
	if err != nil {
		return err
	}
	if len(body.Files) == 0 {
		return zip.ErrBadRequest("files is required")
	}
	if len(body.Files) > maxIndexFiles {
		return zip.ErrBadRequest(fmt.Sprintf("too many files (max %d)", maxIndexFiles))
	}
	var total int
	for _, f := range body.Files {
		if strings.TrimSpace(f.Path) == "" {
			return zip.ErrBadRequest("every file needs a path")
		}
		if len(f.Content) > maxFileBytes {
			return zip.ErrBadRequest("file too large: " + f.Path)
		}
		total += len(f.Content)
		if total > maxTotalBytes {
			return zip.ErrBadRequest("index payload too large")
		}
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.ErrInternal("open index")
	}
	res, err := s.indexRepo(c.Context(), org, principal.Ledger(c), principal.Project(c), store, repo, body.Files, body.Prune)
	if err != nil {
		s.log.Warn("code index failed", "org", org, "repo", repo, "err", err)
		return zip.ErrInternal("index failed")
	}
	return c.JSON(http.StatusOK, res)
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
// seam the git plane's lifecycle reactor calls on push (clients/git owns the repo
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

func searchLimit(c *zip.Ctx) int {
	n, err := strconv.Atoi(strings.TrimSpace(c.Query("limit")))
	if err != nil || n <= 0 {
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
