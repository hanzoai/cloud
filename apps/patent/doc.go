// Package patent is the record of who thought of what, and when the machine was
// asked to write it.
//
// THE QUESTION THIS PLANE ANSWERS. A patent claim is a legal account of a
// technical solution, and its strength does not turn on the code compiling or on
// the code being novel. It turns on one fact the code itself can never carry:
// whether a human formed the complete operative idea BEFORE the tool was applied
// to express it. The USPTO's Revised Inventorship Guidance for AI-Assisted
// Inventions (2025-11-28, which rescinded the February 2024 guidance in full)
// treats AI as an instrument — a compiler, a CAD package — and Thaler v. Vidal,
// 43 F.4th 1207 (Fed. Cir. 2022) holds that only a natural person may be named.
// Conception, per Pfaff v. Wells, 525 U.S. 55 (1998), refers to "the inventor's
// conception rather than to ... a physical embodiment of that idea", so what has
// to be provable is a definite and permanent idea in a human mind, and what makes
// it provable is a contemporaneous record with an order that cannot be
// retro-fitted. That last clause is the whole design: a log whose sequence its
// author controls is not evidence, it is a draft. So every field that decides the
// argument — who, when, in which order — is the SERVER's value, and no client
// field is trusted with any of them.
//
// THREE KINDS AND THE EDGE BETWEEN THEM.
//
//   - Idea — one human conception entry: the problem, what was tried and
//     rejected, the mechanism being claimed, in the author's own prose. This is
//     the record an examiner or a court reads as evidence of conception.
//   - Implementation — the machine's part: a commit, a model id, a prompt
//     digest, and the idea ids it implements. The edge is the point. An
//     implementation with no cited ideas says "AI first, human after", which is
//     the pattern that loses.
//   - Disclosure — the assembled document, citing idea and implementation ids
//     beneath it, plus role-gated approvals (the CTO who signs the research log,
//     the attorney who reviewed it).
//
// Proof is read, never written: GET /v1/patent/proof folds the org's records in
// append order into a root, answers whether every cited idea PRECEDES the
// implementation that leans on it, and states out loud what it cannot establish.
//
// THE PLANE IS NOT A SECOND STORE. Ideas, implementations, disclosures and
// approvals ride the same per-org SQLite, the same orm `_entities` records, the
// same server-computed content hash as version identity, the same server-assigned
// monotone append `seq`, and the same private-by-default visibility as Hanzo
// Research (HIP-0512). A correction APPENDS a version and the prior one is
// RETAINED, never mutated — which is the property that makes an invention record
// usable as evidence at all: an entry that could be edited in place proves
// nothing about the moment it was written.
//
// WHAT IT DOES NOT CLAIM. This is a record-keeping tool, not an opinion on
// patentability, and it files nothing. It cannot make an idea inventive and it
// cannot substitute for a registered attorney's judgment about where the core
// insight actually originated; 35 U.S.C. §115's oath is still signed by a human.
// On a deployment with no anchoring backend wired, the proof bundle's anchor
// status is `unanchored`: the chain is internally consistent and, beyond this
// platform's own honesty, corroborated by nothing yet. That is reported as a gap
// rather than dressed up as a pass.
//
// Its own binary (plugin/patent) states Name/Price/Mount; the host learns the
// prefix from its manifest row. Every route is a typed op, so one registry entry
// is simultaneously the OpenAPI operation, the MCP tool a coding agent sees, the
// CLI subcommand and the SDK method — which is how the conception-first order
// ends up inside the tools people are actually holding rather than in a policy
// document nobody opens.
package patent
