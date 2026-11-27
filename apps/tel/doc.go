// Package tel is the telecommunications surface: phone numbers, calls and
// messages, on whatever carrier the deployment is configured for.
//
// It is white-label by construction. Nothing in this package names a carrier or a
// brand — `Carrier` (carrier.go) is the whole contract, the concrete one is built
// from configuration at mount, and a brand that terminates on a different network
// in a different jurisdiction changes an environment variable rather than a line
// of code. lux.tel is the first brand to run it; it is not the only one it can
// serve.
//
// Assistants are OURS. A call handed to an agent is answered by a Hanzo assistant
// on Hanzo inference (agent.go) — the carrier moves the audio and does not decide
// what is said. Which model answers is the catalog's decision behind the AI endpoint,
// not a constant in a telecom package.
//
// Tenant isolation is enforced SERVER-SIDE on every request: the org is
// principal.Acting — the value SanitizeIdentity minted from the VALIDATED
// bearer owner claim (HIP-0026) — and never a client-supplied header. Every store
// query filters on it, so one tenant can neither read nor mutate another's
// numbers, calls or messages.
//
// Surface (all org-scoped; /v1 only):
//
//	GET    /v1/tel/numbers/available   search the carrier's inventory   -> {data:[…]}
//	GET    /v1/tel/numbers             numbers this org holds           -> {data:[…]}
//	POST   /v1/tel/numbers             buy one                          -> Number (201)
//	DELETE /v1/tel/numbers/:id         release one
//	GET    /v1/tel/calls               call records                     -> {data:[…]}
//	POST   /v1/tel/calls               place a call                     -> Call (201)
//	DELETE /v1/tel/calls/:id           hang up
//	GET    /v1/tel/messages            message records                  -> {data:[…]}
//	POST   /v1/tel/messages            send one                         -> SMS (201)
//	GET    /v1/tel/summary             per-org roll-up
//
// Every route is a TYPED op — one registry entry, which is what the OpenAPI
// operation, the MCP tool, the CLI command and every generated SDK method are
// projected from. That is also why an agent in a sandbox can drive this surface
// without anybody writing a tool by hand: the tools ARE these ops.
package tel
