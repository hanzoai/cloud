// Command index fills the code index that /v1/code and /v1/lsp read from.
//
// THE INDEX IS A PUSH: cloud never clones. /v1/code/index takes a repo label and
// the files themselves, which is what makes one index able to hold repositories
// from anywhere without cloud holding a credential for any of them. It also means
// nothing is indexed until something posts, and until today nothing did — the
// endpoint had no caller in the fleet outside its own tests, so a search that
// spans every repo spanned none.
//
// WHY A SWEEP AND NOT A HOOK, TO START. Per-push indexing is the steady state and
// this is built to become that: the server skips unchanged files by content hash,
// so posting a whole tree costs about what posting the diff costs, and one repo's
// sweep IS the per-push job. A sweep first because a hook only ever indexes what
// changes after it is installed, and the question being asked is about everything
// that already exists.
//
//	index                 # every repo the forge lists
//	index hanzoai/cloud   # named repos only
//
// Env: FORGE (host, default git.hanzo.ai), FORGE_TOKEN, API (default
// https://api.hanzo.ai), HANZO_API_KEY, WORKERS (default 8).
package main
