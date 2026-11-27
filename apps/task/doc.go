// Package task is Hanzo Task: boards, the work items on them, and the filters
// that make a board.
//
// ONE NAME, and it is the word for the thing a row IS. The package was
// `tracker`, then `todo` — and `todo` said what the durable engine next door was
// already called, so one concept had two words and one word had two planes. The
// engine is `workflow` (apps/workflow) and this is `task`: intent here,
// execution there. The SPA's host and image still spell it the old way and are
// not this repo's to move.
//
// It mounts the Hanzo Cloud /v1/task/* surface. THE FORGE IS THE STORE
// (source.go): a board is a repository on git.hanzo.ai and an issue is its issue,
// so the reads below are reads OF the forge and nothing here mirrors them — a
// second copy of a work item is a second answer to what its state is. The local
// per-org SQLite (store.go) holds only what the forge cannot answer: the org-wide
// index every source lands in (github_sink.go), which is what makes "is anyone
// tracking X" a question with an answer.
//
// Org isolation is enforced SERVER-SIDE on every request: the org is
// principal.Org(c) — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and NEVER a client-supplied header. Every
// store query filters WHERE org=?, so one org can never read or mutate
// another's projects or issues.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/task/projects                          list boards             -> [Project]
//	GET    /v1/task/projects/:key                     board detail            -> Project
//	POST   /v1/task/projects                          405, named at the forge
//	PATCH  /v1/task/projects/:key                     405, named at the forge
//	DELETE /v1/task/projects/:key                     405, named at the forge
//	GET    /v1/task/projects/:key/issues[?status=&kind=&repo=&label=&source=&scheduled=]  -> [Issue]
//	POST   /v1/task/projects/:key/issues              file an issue           -> Issue (201)
//	GET    /v1/task/projects/:key/issues/:num         one issue, in full      -> Issue
//	PATCH  /v1/task/projects/:key/issues/:num         update an issue         -> Issue
//	GET    /v1/task/board[?status=&kind=&repo=&label=&source=&scheduled=]         -> [Issue]
//	GET    /v1/task/issues                            search across the org   -> [Issue]
//	POST   /v1/task/projects/:key/issues/:num/claim   take a provider of work
//
// THE UI IS NOT HERE. todo.hanzo.ai serves the SPA at its own root from its own
// image (hanzoai/admin apps/todo -> ghcr.io/hanzoai/todo), and this binary used
// to serve a SECOND copy of the same bundle at /todo/*. Two builds of one source
// on two origins drift, and they did: measured live, the two answered different
// asset hashes under different titles. The API is cross-origin from the page by
// design, so nothing about the request path wanted them in one process.
//
// Order 129: binds /v1/task/* before the AI subsystem's /v1/* catch-all
// (150). serve.go auto-registers GET /v1/task/health.
package task
