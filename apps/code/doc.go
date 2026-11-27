// Package code is search and symbols across your repos, for you and your agents.
//
// A native, per-org code-intelligence engine for AI coding agents and the
// hanzo.app UI. Retrieval is HYBRID — three orthogonal tiers fused with
// reciprocal-rank fusion, the SOTA lesson that embeddings alone under-serve
// code search:
//
//   - lexical  (store.go/tokenize.go) — a substring index over code-tokenized text
//     (camelCase/snake_case split, operators kept; FTS5 trigram on SQLite, pg_trgm on
//     hanzo-sql); substring + regex (Zoekt model).
//   - symbolic (parse.go)             — go/parser for Go (real def/ref edges) and
//     compact lexical extractors for TS/JS/Python/Rust/Solidity: go-to-symbol +
//     a def→ref edge table.
//   - semantic (embed.go/search.go)   — AST-boundary chunks embedded via the SAME
//     gateway /embeddings apps/knowledge uses, kept as float32 blobs in the org's
//     own file and ranked by cosine in Go (internal/vec: a bounded best-k over at
//     most maxVectorScan vectors, and the answer says when it stopped there).
//
// Storage is ONE database per org (HIP-0302): a SQLite file at
// {DataDir}/orgs/{slug}/code.db, or the org's own schema on hanzo-sql. The org
// boundary is PHYSICAL — a query in one org's database can never reach another
// org's rows. Every request resolves its org through principal.Org
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
