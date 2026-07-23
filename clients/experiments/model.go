// Package experiments is cloud's unified EXPERIMENT primitive: A/B testing as ONE
// value whatever the variant KIND is — a feature flag, an ad creative, an email
// subject, a model id. It is a COMPOSITION of three planes that already exist, never
// a fourth engine:
//
//	ASSIGNMENT  = flags     — subject -> variant is a deterministic flags evaluation
//	                          (engineEvaluate, sha1 rollout hash). No 2nd bucketing.
//	MEASUREMENT = analytics — a subject's outcome events are already captured by
//	                          distinct_id in hanzo.events. No 2nd event store.
//	EVIDENCE    = research   — per-variant samples land as immutable evidence rows
//	                          (kind "ab"); significance is a pure function over them.
//
// The experiment is the VALUE that composes them:
//
//	Experiment = { id, org, name, subjectKind, variants (payload is variant-kind
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
// — the primitive does not care. clients/campaign composes THIS to run a creative
// A/B; it does not reinvent assignment or evidence.
//
// Mounted into the unified cloud binary via apps.go ({Name:"experiments", Mount}).
package experiments

import "encoding/json"

// SubjectKind is the unit an experiment assigns and measures: a user, an org, a
// session, or a named audience. It selects the distinct_id grain the flags
// assignment hashes and the analytics outcomes fold on.
type SubjectKind string

const (
	SubjectUser     SubjectKind = "user"
	SubjectOrg      SubjectKind = "org"
	SubjectSession  SubjectKind = "session"
	SubjectAudience SubjectKind = "audience"
)

func (k SubjectKind) valid() bool {
	switch k {
	case SubjectUser, SubjectOrg, SubjectSession, SubjectAudience:
		return true
	}
	return false
}

// Status is an experiment's lifecycle state: running (assigning + measuring) or
// decided (a winner promoted to 100% of the rollout).
type Status string

const (
	StatusRunning Status = "running"
	StatusDecided Status = "decided"
)

// Variant is one arm of an experiment: a key, its rollout Weight within the
// experiment (percentages across variants sum to 100), whether it is the Control
// (baseline) arm, and a variant-kind-AGNOSTIC Payload the assignment carries —
// a feature config, an ad-creative id, an email subject, a model id. The experiment
// primitive never interprets the payload; the consumer does.
type Variant struct {
	Key     string          `json:"key"`
	Weight  float64         `json:"weight"`
	Control bool            `json:"control,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Experiment is the primitive: the definition + lifecycle of one controlled
// experiment. Project + ID are the server-stamped identity (project from the
// validated principal, never a client field). FlagKey links the assignment plane;
// MetricEvent + ExposureEvent link the measurement plane.
type Experiment struct {
	Project       string      `json:"project"`
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	SubjectKind   SubjectKind `json:"subjectKind"`
	FlagKey       string      `json:"flagKey"`
	ExposureEvent string      `json:"exposureEvent"`
	MetricEvent   string      `json:"metricEvent"`
	Variants      []Variant   `json:"variants"`
	Status        Status      `json:"status"`
	Winner        string      `json:"winner,omitempty"`
	CreatedBy     string      `json:"createdBy,omitempty"`
	CreatedAt     string      `json:"createdAt,omitempty"`
	DecidedBy     string      `json:"decidedBy,omitempty"`
	DecidedAt     string      `json:"decidedAt,omitempty"`
}

// controlKey returns the experiment's control variant key: the arm flagged
// Control, else the FIRST variant (deterministic baseline). "" only when there are
// no variants (a create-time invariant forbids that).
func (e Experiment) controlKey() string {
	for _, v := range e.Variants {
		if v.Control {
			return v.Key
		}
	}
	if len(e.Variants) > 0 {
		return e.Variants[0].Key
	}
	return ""
}

// hasVariant reports whether key names one of the experiment's variants.
func (e Experiment) hasVariant(key string) bool {
	for _, v := range e.Variants {
		if v.Key == key {
			return true
		}
	}
	return false
}

// defaultExposureEvent is the PostHog-convention exposure marker SDKs emit when a
// flag is evaluated. An experiment with no explicit ExposureEvent measures its
// denominator (enrolled subjects) from it.
const defaultExposureEvent = "$feature_flag_called"
