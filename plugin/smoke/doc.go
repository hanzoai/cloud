// Command smoke is the durable, authenticated functional smoke for the unified
// cloud API (api.hanzo.ai). It replaces the boot-only "did it reach listening"
// check with a REAL per-subsystem probe: it hits ONE side-effect-free read on each
// core product surface and asserts each returns a WORKING code.
//
// THE INVARIANT IT GUARDS (why it exists). A release must never ship if a core
// endpoint is broken. Two failure classes are ALWAYS a hard fail, on EVERY probe,
// because this is a read-only smoke:
//
//   - 402 on a read — the "balance gate over-blocks reads" regression: a
//     $0-balance org must be able to VIEW its own resources (models, chats, usage,
//     keys, buckets, deployments). A read never spends, so a 402 here is the bug.
//   - 5xx (500/502/504) — a crash / upstream fault (e.g. the /v1/billing/usage
//     self-dispatch 500 this smoke was written to catch).
//
// Beyond that, each probe declares how it SHOULD behave:
//   - health  : liveness, must be 200.
//   - public  : public data (marketing aggregates, catalogs, /health), 200 (a
//     staged/uninitialised subsystem may answer 503 — tolerated).
//   - authed  : an org-scoped read — 401/403 without a token; 2xx WITH a valid
//     token (strict), or 2xx/401/403 when the token may not validate in
//     this env (default). 503 tolerated (subsystem staged).
//   - tolerant: preview/SuperAdmin/cross-org reads whose code is config-dependent
//     ({200,401,403}); still never 402/5xx.
//
// USAGE
//
//	SMOKE_BASE_URL=http://127.0.0.1:8000 SMOKE_TOKEN=<bearer> go run ./plugin/smoke
//	go run ./plugin/smoke -base https://api.hanzo.ai -token "$T" -strict
//
// Env: SMOKE_BASE_URL, SMOKE_TOKEN, SMOKE_STRICT=1, SMOKE_TIMEOUT (seconds).
// Flags (-base/-token/-strict/-timeout) override env. Exit code is non-zero iff any
// probe fails, so it drops straight into a release gate.
package main
