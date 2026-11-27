// Package compliance is your KYC/KYB onboarding, accreditation records, and the
// evidence trail behind them.
//
// It mounts the ORG-SCOPED operations surface (/v1/compliance): the company's own
// KYC/KYB onboarding verification, accreditation STATE TRACKING, and a
// compliance read of the tamper-evident audit trail (the SOC 2 posture
// surface). It is the platform TOOLING a company's compliance team uses to
// orchestrate licensed verification providers and keep an evidence trail — with
// professionals in the loop.
//
// THE BOUNDARY (a design invariant baked into the data model and every response).
// This subsystem ORCHESTRATES providers and TRACKS what they report. It never
// asserts that a subject, or the org, "is compliant": there is no such value in the
// model. A verification status is exactly what the licensed provider reported
// (provider_verified / provider_rejected / manual_review) or pending; an
// accreditation record states who asserted or confirmed what, by which method — not
// a platform certification. The legal/regulatory determination belongs to the
// registered entity and its counsel, never to this platform. Every status-bearing
// response carries Disclaimer to keep that honest on the wire.
//
// WHAT IT COMPOSES (DRY — it forks none of these):
//   - apps/idv           the ONE identity/business verification client (Persona /
//     Onfido / Stripe Identity behind a fail-closed interface;
//     the honest Manual provider by default).
//   - audit.Recorder     the ONE tamper-evident audit plane (deps.Audit). Every
//     privileged action (start / decide / callback) is recorded
//     there, referencing opaque ids ONLY — never subject PII.
//   - cek                encryption at rest for the per-deployment store's file
//   - internal/org       the columns that hold subject PII (name, email, ref) or a
//     verification link are sealed values on either engine, keyed
//     from the same master (deps.MasterKey).
package compliance
