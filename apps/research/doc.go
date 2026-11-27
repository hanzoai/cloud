// Package research is every experiment you have ever run, kept and comparable.
//
// The R&D EVIDENCE plane (HIP-0512 §"Hanzo Research"). Every experiment across
// every product — a benchmark run is ONE kind; kernel-perf (hanzo-engine),
// training (hanzo-ml), ablations, and policy-evals are the others — accrues
// here as VERSIONED, append-only evidence under one discriminator, kind ∈
// benchmark | kernel-perf | training | ablation | policy-eval.
//
// Two planes, never one (HIP-0512): each org's transactional SQLite (store.go, per
// HIP-0302, physically file-isolated) is the local source of truth; it rolls up into
// hanzoai/datastore — the column-oriented OLAP aggregate plane (datastore.go) — for the
// unified cross-project query surface. Durability today is VERSIONED · APPEND-ONLY;
// cloud mirroring is ROLLING OUT (the roll-up is best-effort; retry + reconciliation +
// replication + backup/restore are still on the critical path, so the stronger
// "immutable · replicated · recovery-tested" claim is NOT yet made).
//
// Versioned, not write-once: a correction APPENDS a new version under the same stable id
// and the prior version is RETAINED (superseded), never mutated. RETAINED is the full
// history (the truth); CANONICAL is the deterministic deduped view over it — so dedup
// never reads as loss. faulted/failed runs are retained (negative results are evidence).
// Ingest is idempotent by content (measurement + provenance) — re-running the backfill
// appends nothing.
//
// Provenance is first-class + queryable: project, git (sha/branch/dirty), and
// lib_versions travel as columns/structured fields, so the board answers "which lib
// version regressed LiveCodeBench" over the longitudinal record.
//
// Private-by-default: an upload records visibility='private' and grants NO training or
// commons-publication rights. Public board visibility, training rights, and commons
// publication are each a SEPARATE authorized grant (POST /v1/research/grants), never
// implied by uploading a run.
//
// Surface (every route org-scoped by the validated principal, but that a caller with
// no org reads the run listing for the org ?org= names, its public runs alone;
// project is a sub-dimension column, so the org's ops board reads across its
// projects in one query):
//
//	POST /v1/research/experiments   ingest a batch (idempotent) → SQLite → roll up
//	GET  /v1/research/experiments   list canonical (?project= ?kind=)
//	GET  /v1/research/projects      every project + real totals (canonical + retained)
//	GET  /v1/research/totals        headline aggregate + per-kind (?project=)
//	POST /v1/research/grants        set visibility/consent for a stable id (separate auth)
//	POST /v1/research/artifacts     record a diary artifact, content-addressed
//	GET  /v1/research/artifacts     the diary feed, newest first (metadata only)
//	GET  /v1/research/artifacts/:sha256  the artifact's bytes, by content hash
//	POST /v1/research/benchmarks    record benchmark definitions (the task itself)
//	GET  /v1/research/benchmarks    list definitions (?id= ?project=)
//	POST /v1/research/runs          record executions; completion is DERIVED
//	GET  /v1/research/runs          list runs (?benchmark= ?system= ?completion= ?since= … ?org=)
//	POST /v1/research/studies       record the question a set of runs answers
//	GET  /v1/research/studies       list studies (?id= ?project=)
//	POST /v1/research/papers        record writing that cites runs
//	GET  /v1/research/papers        list papers (?id= ?study= ?cites=)
//	GET  /v1/research/compare       two runs side by side, with what stops the comparison
//
// and the decision thread (decision.go): the evidence under /v1/research/evidence,
// and the DDT decisions, evaluations, actions and commitments that rest on it.
//
// A run is its own record (record.go): the task it measured against, the system
// under test, every knob and digest needed to run it again, the commit it was
// frozen at, the numbers with their intervals — and questions/answered/ended,
// from which `completion` is DERIVED. That last one is not decoration. A harness
// that checkpoints as it goes reports whatever has been answered so far, so a run
// read at question 57 of 282 is indistinguishable from a finished one unless the
// record carries the denominator; here it does, `?completion=partial` finds it,
// and a comparison refuses to put it beside a finished run without saying so.
//
// Its own binary (plugin/research) states Name/Price/Mount; the host learns the
// prefix from its manifest row.
package research
