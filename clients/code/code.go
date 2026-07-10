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
// the tenant boundary is PHYSICAL — a query in one org's file can never reach
// another org's rows. Every request resolves its org through principal.Tenant
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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/clients/provisioning"
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
// parameter on every call, so one service serves all tenants and an org can never
// be captured from stale state.
type service struct {
	dataDir string
	embed   Embedder
	synth   Synthesizer
	log     luxlog.Logger

	mu     sync.Mutex
	stores map[string]*Store // sanitized slug → open store
}

var mounted *service

// Mount wires /v1/code/* onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("code.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("code.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("code.Mount: empty DataDir")
	}
	if err := os.MkdirAll(filepath.Join(deps.DataDir, "orgs"), 0o755); err != nil {
		return fmt.Errorf("code.Mount: data dir: %w", err)
	}
	s := &service{
		dataDir: deps.DataDir,
		embed:   newEmbedder(),
		synth:   newSynth(deps.AI, deps.AIDefaultModel),
		log:     deps.Logger.New("subsystem", "code"),
		stores:  map[string]*Store{},
	}
	mounted = s

	app.Get("/v1/code/search", s.handleSearch)
	app.Post("/v1/code/context", s.handleContext)
	app.Get("/v1/code/ask", s.handleAsk)
	app.Post("/v1/code/ask", s.handleAsk)
	app.Post("/v1/code/index", s.handleIndex)

	s.log.Info("code surface mounted (native)",
		"brand", deps.Brand, "semantic", s.embed.Enabled(), "synth", s.synth.Enabled())
	return nil
}

func init() {
	cloud.RegisterWithShutdown("code", 134, cloud.Typed(Mount), shutdown)
}

// shutdown closes every open per-org store. Idempotent.
func shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	err := mounted.closeAll()
	mounted = nil
	return err
}

// closeAll releases every open per-org store handle.
func (s *service) closeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, st := range s.stores {
		if err := st.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.stores = map[string]*Store{}
	return firstErr
}

// storeFor lazily opens (and caches) the org's SQLite file. The physical path is
// {DataDir}/orgs/{SanitizeOrg(org)}/code.db — provisioning.SanitizeOrg is the
// codebase's ONE org-slug normalizer (shared with S3/KMS/knowledge), INJECTIVE in
// the owner so two distinct orgs never fold onto one file.
func (s *service) storeFor(org string) (*Store, error) {
	slug := provisioning.SanitizeOrg(org)
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.stores[slug]; ok {
		return st, nil
	}
	dir := filepath.Join(s.dataDir, "orgs", slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("org dir: %w", err)
	}
	st, err := openStore(filepath.Join(dir, "code.db"))
	if err != nil {
		return nil, err
	}
	s.stores[slug] = st
	return st, nil
}

func (s *service) engineFor(org string) (*engine, error) {
	st, err := s.storeFor(org)
	if err != nil {
		return nil, err
	}
	return &engine{store: st, embed: s.embed}, nil
}

// tenant resolves the org for a request, but ONLY for a validated principal
// (c.User() set by SanitizeIdentity from a verified credential). An unvalidated
// or org-less request gets no tenant, so the caller returns 403 — never another
// org's code. This is the SAME gate clients/eval + clients/knowledge use.
func tenant(c *zip.Ctx) (string, bool) { return principal.Tenant(c) }

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
	org, ok := tenant(c)
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
	eng, err := s.engineFor(org)
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
	org, ok := tenant(c)
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
	eng, err := s.engineFor(org)
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

// handleAsk is the cited RAG answer over the org's index.
func (s *service) handleAsk(c *zip.Ctx) error {
	org, ok := tenant(c)
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
	eng, err := s.engineFor(org)
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
	org, ok := tenant(c)
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
	res, err := s.indexRepo(c.Context(), store, repo, body.Files, body.Prune)
	if err != nil {
		s.log.Warn("code index failed", "org", org, "repo", repo, "err", err)
		return zip.ErrInternal("index failed")
	}
	return c.JSON(http.StatusOK, res)
}

// indexRepo runs the pipeline per file: skip-if-unchanged (content hash) → parse
// → embed chunks → atomically replace the file's artifacts. prune removes indexed
// files absent from the payload (a full-tree reconcile).
func (s *service) indexRepo(ctx context.Context, store *Store, repo string, files []fileInput, prune bool) (indexResult, error) {
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
			if v, err := s.embed.Embed(ctx, texts); err != nil {
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
// filesystem path — the org file already isolates the tenant), so it needs only
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
