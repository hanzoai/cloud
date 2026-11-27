// Package functions is your serverless code: publish it, call it over HTTP,
// watch every run and what it cost.
//
// The /v1/function registry is per-org — one function, one owner. Every
// function belongs to exactly one org (the
// gateway-minted X-Org-Id, HIP-0026); org isolation is the org column,
// enforced on every query. The registry stores a function's runtime, source,
// resource limits, and the NAMES of the secrets it mounts — never a secret
// value (values live in KMS by reference, the Secret-Manager principle).
//
// Surface (the shape console's FunctionsModule / functions.ts consume):
//
//	GET    /v1/function                    list functions           -> {functions:[...]}
//	POST   /v1/function                    create / redeploy        -> ServerlessFunction
//	GET    /v1/function/metrics            invocations chart + donut -> {series,status,costCents}
//	GET    /v1/function/triggers           all triggers (HTTP)      -> {triggers:[...]}
//	GET    /v1/function/deployments        current deployments      -> {functions:[...]}
//	GET    /v1/function/secrets            mounted secret NAMES      -> {secrets:[...]}
//	GET    /v1/function/:name              detail + triggers + calls -> FunctionDetail
//	DELETE /v1/function/:name              delete (+ its invocations)
//	GET    /v1/function/:name/invocations  recent invocations       -> {invocations:[...]}
//	GET    /v1/function/:name/logs         last invocation output   -> {logs:"..."}
//	POST   /v1/function/:name/invoke       run the function {input} -> Invocation
//
// Invoke delegates to a SANDBOX through apps/exec — this
// binary NEVER runs org code in-process. When the sandbox is not configured
// invoke fails closed (503) and fabricates nothing. Every metric the Overview
// shows is DERIVED from real invocation rows; there is no invented rollup.
package functions
