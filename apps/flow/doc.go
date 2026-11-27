// Package flow is Hanzo Flow: build an agent workflow on a visual canvas, run it,
// and read every run.
//
// It is visual AI workflow orchestration — build, manage, and run agent workflows
// on the unified /v1 plane.
//
// PRODUCT-REPO MODEL. The product lives in github.com/hanzoai/flow (Python/
// FastAPI — the visual builder, the graph engine, the component library). This
// subsystem does NOT reimplement any of it: every op is a TYPED PASSTHROUGH to
// the flow service, the same posture apps/iam takes for its Go product — except
// flow is Python, so the client is HTTP to the in-cluster service instead of an
// in-process handler. cloud adds exactly three things: IAM auth, the tenant
// boundary, and the unified surface (OpenAPI/MCP/CLI/SDK projection).
//
// THE HONEST SLICE. hanzoai/openapi once authored 87 paths for this product and
// deleted them as UNSERVED (nothing answered them anywhere). What is mounted
// here is the subset the product's server GENUINELY answers today, each op
// proven against a live flow v1.8.x backend: workflows CRUD (the product's
// Flow objects), synchronous runs, run records, and a reachability lens. The
// rest of the authored intent (providers/app-connections/triggers/templates/…) is
// Activepieces-shaped surface this product does not serve — it gets NO route,
// and typed_wire_test.go pins that refusal ledger so reviving a family is a
// deliberate edit, never an accident.
//
// TENANT ISOLATION. The org is the VALIDATED principal's org, and it is NEVER
// an In field: principal.OrgFrom reads what cloud.Bridge parked from
// principal.Org — the X-Org-Id the identity boundary minted from a verified
// credential. The flow service is a single shared deployment reached with ONE
// platform credential (FLOW_API_KEY, KMS-synced env), so the org boundary is
// enforced HERE, on the product's own project primitive: each org's workflows
// live in a flow project named by the org id, resolved server-side per request
// (project, below). Creates pin the project id server-side; reads and
// mutations of one workflow verify the workflow is in the caller's project and
// answer 404 otherwise — never another org's workflow, and never a hint that a
// foreign id exists.
//
// FAIL-CLOSED. No validated principal → 403 before any upstream byte. An
// upstream that refuses the platform credential (its 401/403) is a deployment
// fault, not the caller's — reported 503, so a misconfigured credential can
// never look like a caller-side auth bug. An unreachable upstream is 503.
package flow
