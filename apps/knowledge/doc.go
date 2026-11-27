// Package knowledge is your team's wiki and your agents' memory, searchable by
// meaning.
//
// Wiki pages, memories and connector-ingested documents are ONE framework
// document store, cut into passages and embedded into the org's own database on
// every save, and read back as hybrid search, a link graph, or an imported vault.
//
// The model is declared here as DocType fixtures on the framework engine
// (apps/framework), and index.go wires the ONE per-org passage path that turns
// every knowledge document into retrievable org memory. It is the FOURTH app lane
// after cms/erp/help and the one that makes "a Notion-like wiki + agent memory"
// just another module on Base — no new Base, no new database.
//
// Like cms/erp/help, the content model is fixtures: a wiki page IS a framework
// document (module "kb"), a memory IS a framework document, an ingested doc from
// a connector IS a framework document. CRUD, permissions, tenant isolation, and
// install are the framework's generic, already-live surface (/v1/framework/*) and
// the SAME generic @hanzo/ui DocType renderer that draws CMS and ERP.
//
// UNLIKE the pure-fixture lanes, kb attaches BEHAVIOR (hooks.go): on every save of
// a knowledge document the after_save hook replaces its passages in the org's
// store, and on trash it removes them. Human wiki + AI memory therefore share
// ONE per-org knowledge store, indexed once. kb ALSO mounts a thin retrieval +
// ingestion control-plane subsystem (subsystem.go, /v1/knowledge/*): hybrid search
// (the RAG entry point an agent/chat calls) and app connectors (Slack/GitHub/
// Google) that ingest external docs INTO the same store. Connectors are just
// producers of framework documents — they never fork the passage-write path.
//
// Names are slug-style and bare — "page", not "kb-page". The module namespaces
// them, so the CMS may have a Page of its own and neither lane needs to spell the
// other out of the way. A document is addressed by the pair: kb.page.
package knowledge
