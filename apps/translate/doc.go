// Package translate is text in, the same text out in the language you asked for.
//
// POST /v1/translate is the ONE translation surface: two tiers behind one
// endpoint, one auth path, one meter (HIP-0516).
//
//	POST /v1/translate  { text | batch[], target, source?, tier?, model?, glossary?, format? }
//	                 -> { translations[], detected_source?, tier, model?, usage }
//
// `tier` picks the engine and defaults to quality:
//
//   - quality routes to the model plane (deps.AI — zen through the gateway), which
//     carries context, terminology and tone. `model` names the model it runs on and
//     defaults to aimodel.DefaultModel; an org's own alias reaches a model the org
//     serves, through the same plane, meter and access check as the default.
//   - bulk routes to MADLAD-400 under CTranslate2 (engine.go), for high-volume,
//     low-latency work where a model is overkill. A deployment with no bulk backend
//     answers 503; bulk NEVER falls back to quality, so a caller is never quietly
//     served — or charged — at a tier it did not ask for.
//
// The translation memory (memory.go) is NORMATIVE, not a cache. Every string keys
// on (source_text, source language, target, glossary_version, tier, format, model);
// a hit returns the stored value unchanged, so only new or changed source strings
// reach an engine. That is what makes a locale rebuild idempotent under a
// non-deterministic model, and it is what makes the bill proportional to what
// actually changed.
//
// The review lane rides the same memory: an entry carries a state on the ladder
// machine -> suggested -> approved -> published, and human work is IMMUNE to
// machine churn (memory.put). A rebuild can never silently revert an approved
// string.
//
// Tenancy: org-scoped by the validated principal. The memory is a per-org SQLite
// file (HIP-0302 physical isolation), so a request cannot reach another org's
// memory. Submitted text is customer content, held only in that org's own memory.
package translate
