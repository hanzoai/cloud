// Package legal is the paperwork your company needs, drafted, signed and filed.
//
// The ORG-SCOPED legal-document surface (/v1/legal): a versioned,
// org-overridable library of standardized templates, a PURE merge-field
// generation engine that renders those templates from the org's own
// company/cap-table data, a KMS-sealed store for the generated documents, and
// the e-signature + filing clients that carry a document to execution — all on
// the shared audit plane.
//
// THE BOUNDARY (a design invariant, enforced in the engine and the data model). Hanzo
// Legal MANAGES documents; it does NOT give legal advice, and it does NOT determine
// that a document is legally valid or sufficient. A generated document is a DRAFT
// rendered from the org's inputs; the legal determination belongs to the org's
// counsel. Formation and securities templates carry a mandatory counsel-review notice
// (CounselReview) that the engine prepends to every rendered document — the platform
// can never emit such a document without it. There is no "legally_valid" state in the
// model; a document is draft / out_for_signature / signed / voided, nothing more.
//
// WHAT IT COMPOSES (DRY):
//   - text/template  the PURE renderer (deterministic: no clock, no I/O — same inputs
//     yield identical bytes, so a rendered contract is reproducible).
//   - cek            encryption at rest for the document store (a rendered contract
//     carries names + terms and is sealed on disk).
//   - the Esign / Filing clients (providers.go) — provider-agnostic, honest-stub default,
//     config-driven real provider — mirroring the company formation clients.
//   - audit.Recorder the ONE tamper-evident trail (deps.Audit); every generate / sign /
//     file action is recorded, referencing opaque document ids.
package legal
