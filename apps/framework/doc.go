// Package framework is document types you define: describe a record once, then
// create, list, submit and cancel documents against it.
//
// The DocType routes live at /v1/framework and are per org, with the engine's
// own permission calculus deciding every access.
//
// The engine itself is github.com/hanzoai/framework, built on the metadata
// model github.com/hanzoai/doctype. Neither knows what HTTP is. This package is
// the ADAPTER — the "place" in the engine's layering:
//
//	doctype    the VALUE  — schema, coercion, naming, permission calculus.
//	framework  the ENGINE — store, hooks, leases, operations. No transport.
//	this pkg   the PLACE  — /v1/framework/* over zip, cek-encrypted storage,
//	                        principal-derived tenancy, engine Code → HTTP status.
//
// It does four things and nothing else:
//
//  1. opens the engine with cloud's storage policy (cek, encrypted at rest);
//  2. turns a validated request principal into an engine Caller;
//  3. binds each engine operation to a TYPED OP and maps the error Code;
//  4. re-exports the engine vocabulary so the app lanes (cms, erp, help,
//     knowledge, content, guide) keep ONE import and compile unchanged.
//
// (3) is a typed op — zip.Get[In, Out] and friends — for every route but the two
// document WRITES: one registry entry, and the REST route, the OpenAPI operation,
// the MCP tool, the CLI command and the generated SDK method all follow from it.
// The two exceptions are in Mount, with the reason.
//
// The engine enforces permissions itself, so there is no authorization logic
// here — a second copy would be a second answer.
package framework
