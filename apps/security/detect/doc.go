// Package detect is the pure, dependency-free secret-detection engine behind
// Hanzo's native code-security surface. It scans source for hardcoded secrets
// with zero external tooling (no semgrep/gitleaks binary, no network) — the one
// Semgrep-class capability that ships complete today. The logic ports the
// concept behind hanzoai/guard (the LLM-boundary redactor) to code at rest, and
// is the substrate a native AST/SAST engine (on hanzoai/ast) grows onto per the
// plan of record in hanzoai/security POSTURE.md.
//
// It is deliberately a LEAF: only the standard library is imported, and the API
// is pure functions over (path, content) → findings — no I/O, no store, no
// HTTP, no cloud deps. That decomplection is the point: the HTTP subsystem
// (clients/security) AND the `hanzo security scan` CLI both consume THIS engine,
// so the detection logic exists once and neither surface drags the other in.
//
// THE ONE INVARIANT: a Finding NEVER carries the raw secret. It carries a masked
// preview (first/last few chars, middle starred) plus a SHA-256 fingerprint of
// the secret — enough to locate it, dedupe identical occurrences, and confirm a
// rotation happened, and nothing more. Persisting the plaintext would make the
// findings DB a secret store, exactly the thing we scan to prevent (global rule:
// never store secrets in the clear).
package detect
