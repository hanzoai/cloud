// Package lsp is live semantic code intelligence — definitions, references,
// types, hover, outline and diagnostics — over a repository AND its resolved
// dependencies, with no toolchain on the caller's machine.
//
// # Where it sits
//
// At /v1/lsp, beside code rather than inside it. code and lsp are two reads of
// ONE repository: code is lexical, symbolic and semantic search — fast,
// approximate, always available — and lsp is a real language server — exact,
// typed, and able to follow a symbol out of the repository and into a
// dependency. An agent searches with code and is certain with lsp. Two reads of
// one thing are two capabilities, and each answers at the address its own name
// spells; they are cross-referenced siblings, which is the link a reader
// actually follows.
//
// # What this package is
//
// A PROXY. The language servers run in hanzoai/lsp, a jailed daemon on its own
// deployment, because answering a cross-dependency question means running a
// third-party toolchain over untrusted bytes (see daemon.go). This side owns the
// three things the daemon must never hold: the tenant, the repository and the
// ledger.
//
//   - TENANT. Every request resolves its org from the validated principal, and
//     that org is the daemon's isolation key. A caller supplies a repo SLUG,
//     never an owner and never a URL, so there is no input from which one tenant
//     could name another tenant's repository.
//   - REPOSITORY. The revision and the tree come from the forge, read AS THE
//     CALLER, so the forge's own ACL decides which repositories answer. The
//     daemon holds no git credential — one that could fetch any repository is
//     exactly what must not exist next to an unjailed compiler — so the tree is
//     pushed to it, never pulled by it.
//   - LEDGER. The gate runs before the work and the debit after it (meter.go).
//
// # Positions are the LSP's, not a translation of them
//
// line and character are 0-BASED, and character counts UTF-16 code units, per the
// LSP specification. That is deliberately not the 1-based line an editor shows a
// human: these callers are agents and editors that already speak LSP, and a
// service that silently re-based positions would corrupt every multi-byte line —
// an emoji before the cursor is one UTF-16 unit in the protocol's arithmetic and
// two in Go's. Positions pass through untouched, so the protocol's answer is the
// answer.
package lsp
