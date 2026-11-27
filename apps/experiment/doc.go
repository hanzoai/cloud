// Package experiments is A/B testing anything: a flag, an ad, a subject line, a
// model.
//
// It is the unified EXPERIMENT primitive — ONE value whatever the variant KIND
// is — and a COMPOSITION of three planes that already exist, never a fourth
// engine:
//
//	ASSIGNMENT  = flags     — subject -> variant is a deterministic flags evaluation
//	                          (engineEvaluate, sha1 rollout hash). No 2nd bucketing.
//	MEASUREMENT = analytics — a subject's outcome events are already captured by
//	                          distinct_id in event.event. No 2nd event store.
//	EVIDENCE    = research   — per-variant samples land as immutable evidence rows
//	                          (kind "ab"); significance is a pure function over them.
//
// The experiment is the VALUE that composes them:
//
//	Trial = { id, org, name, subjectKind, variants (payload is variant-kind
//	               AGNOSTIC), flagKey (-> the assignment def), metric/exposure events
//	               (-> the analytics outcome grain), status, winner }
//
// The lifecycle: create registers a multivariate flag def (flags.PutDef); assign is
// a flags evaluation (flags.Assign, deterministic); analyze folds analytics outcomes
// per variant and computes lift + significance, persisting the samples to research;
// decide promotes a winner by rewriting the flag's variant weights to 100% for it.
//
// The variant KIND is orthogonal: variant.payload can be a feature config (feature
// experiment), an ad-creative id (campaign experiment), an email subject, a model id
// — the primitive does not care. apps/campaigns composes THIS to run a creative
// A/B; it does not reinvent assignment or evidence.
//
// Mounted into the unified cloud binary via apps.go ({Name:"experiments", Mount}).
package experiment
