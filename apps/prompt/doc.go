// Package prompts is your prompt library, versioned, so nothing changes silently.
//
// Every prompt belongs to exactly one org (the gateway-minted X-Org-Id,
// HIP-0026); tenant isolation is the org column, enforced on every query, so
// one tenant can never read or mutate another's prompts. Creating a prompt
// whose name already exists appends a new version — real, inspectable history,
// never a fabricated rollup.
//
// Surface (all org-scoped; the shape console's PromptsModule consumes):
//
//	GET    /v1/prompt            list current prompts for the org   -> {data:[PromptMeta]}
//	POST   /v1/prompt            create or add-a-version            -> PromptDetail
//	GET    /v1/prompt/metrics    real per-prompt stats              -> {data:[...]}
//	GET    /v1/prompt/catalog    the embedded read-only starter set -> {data:[...]}
//	GET    /v1/prompt/:name      prompt detail + version history    -> PromptDetail
//	DELETE /v1/prompt/:name      delete a prompt (+ its versions)
//
// The store is SQLite in cloud.DataDir() (Base/SQLite-only mandate); it holds only
// template text + taxonomy, never a secret.
package prompt
