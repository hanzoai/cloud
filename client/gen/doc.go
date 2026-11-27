// Command gen emits one typed client package per app that declares plane ops.
//
//	go run ./client/gen
//
// # Why this is a projection and not 45 hand-written clients
//
// An app already declares everything a caller needs to reach it:
//
//	cloud.Plane().Post("/finance/balance",
//	    planeBalance, zip.WithOperationID(client.FinanceBalance))
//
// That one expression names the app (its directory), the op, the request type
// and the response type. Cloud already turns the same registry into an OpenAPI
// document, a CLI, an MCP tool list and a routing declaration. A typed Go client
// for a peer call is ONE MORE PROJECTION of that document — which is exactly why
// it belongs here rather than being written out by hand once per caller.
//
// What it buys is a compiler check that no amount of care buys today. A hand-
// written peer call is five independent facts that must agree at RUN time:
//
//	cloud.Ask[client.BalanceIn, client.Balance](ctx, "commerce", client.FinanceBalance, in)
//	          ^ In            ^ Out            ^ app        ^ op
//
// Nothing stops a caller pairing commerce's op with iam's app name, or
// BalanceIn with Txns. The generated wrapper fixes all four to each other at the
// point of declaration, so the mismatch stops compiling:
//
//	commerce.FinanceBalance(ctx, &client.BalanceIn{...})
//
// # It reads the SOURCE, and a test checks it against the LIVE registry
//
// zip's own [zip.Declaration] argues a declaration must come from the live
// router, never the AST, because an AST cannot cross a repository boundary. That
// argument is about a HOST discovering a plugin it does not build. This
// generator emits Go source into the same module as the declaration it reads, so
// the boundary does not exist here — and reading source buys hermeticity the
// runtime path cannot: no store is opened, no boot order matters, no app has to
// mount before it can be described. zipdoc, the repo's other Go emitter, reads
// these same call sites the same way.
//
// The doctrine is still honoured, by a gate rather than by the mechanism:
// client/client_registry_test.go MOUNTS an app and asserts the ops this generator
// found are exactly the ops the live plane registry holds. Source is the input;
// the running registry is the judge.
package main
