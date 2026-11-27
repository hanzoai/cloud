// Package agents is autonomous agents for your org: define them, run them, keep
// every run.
//
// An agent is a model + a system prompt (instructions) + a set of tool names;
// running one executes a real chat completion through the in-process AI client
// (the SAME gateway path the rest of the console uses) and records the run.
//
// Tenant isolation is the FILE: each org's records live in its own SQLite at
// {DataDir}/orgs/{slug}/agents.db (HIP-0302), named from the gateway-minted
// X-Org-Id (HIP-0026) and nothing else. One tenant cannot read, run or delete
// another's agents because the query never reaches the database they are in.
// tenancy.go is the whole of that argument and is the only file that resolves a
// store; the org predicate every statement still carries is what makes a
// mis-resolved store fail closed rather than answer.
//
// Surface (all org-scoped; console's AgentsModule reads {agents:[...]}):
//
//	GET    /v1/agent               list agents for the org      -> {agents:[...]}
//	POST   /v1/agent               create an agent              -> Agent
//	GET    /v1/agent/:ref          agent detail + recent runs   -> AgentDetail
//	PATCH  /v1/agent/:ref          update an agent              -> Agent
//	DELETE /v1/agent/:ref          delete an agent (+ its runs)
//	POST   /v1/agent/:ref/run      run the agent {input}        -> RunResult
//	GET    /v1/agent/:ref/runs     run history                  -> {runs:[...]}
//	POST   /v1/agent/a2a           A2A 1.0 JSON-RPC (a2a.go)    -> JSON-RPC response
//
// :ref is either the agent's public id (the `agent_...` handle create and list
// return) OR its org-unique name — resolved by Store.Resolve, so a created agent
// is immediately gettable and runnable by whatever create/list handed back.
//
// The stores are per-org SQLite under cloud.DataDir() (Base/SQLite-only), opened
// through cloud.OrgStore like every other per-org subsystem. They hold
// definitions and run I/O only — never a secret; tool credentials live in KMS by
// reference.
package agents
