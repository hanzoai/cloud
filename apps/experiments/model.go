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
// — the primitive does not care. apps/campaign composes THIS to run a creative
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

// Arm is one arm of an experiment: a key, its rollout Weight within the
// experiment (percentages across variants sum to 100), whether it is the Control
// (baseline) arm, and a variant-kind-AGNOSTIC Payload the assignment carries —
// a feature config, an ad-creative id, an email subject, a model id. The experiment
// primitive never interprets the payload; the consumer does.
type Arm struct {
	Key     string          `json:"key"`               // the arm's slug, unique within the experiment
	Weight  float64         `json:"weight"`            // its share of the rollout; the arms sum to 100
	Control bool            `json:"control,omitempty"` // true on the baseline arm every other arm is compared to
	Payload json.RawMessage `json:"payload,omitempty"` // opaque JSON the arm carries, which the consumer interprets
}

// Trial is the primitive: the definition + lifecycle of one controlled
// experiment. Project + ID are the server-stamped identity (project from the
// validated principal, never a client field). FlagKey links the assignment plane;
// MetricEvent + ExposureEvent link the measurement plane.
type Trial struct {
	Project       string      `json:"project"`             // the sub-scope within the org, stamped from the principal
	ID            string      `json:"id"`                  // the experiment's slug, unique within the project
	Name          string      `json:"name"`                // free text for a reader
	SubjectKind   SubjectKind `json:"subjectKind"`         // the unit assigned and measured: user, org, session or audience
	FlagKey       string      `json:"flagKey"`             // the assignment flag this experiment drives
	ExposureEvent string      `json:"exposureEvent"`       // the event that enrols a subject — the analysis denominator
	MetricEvent   string      `json:"metricEvent"`         // the event that counts as a conversion — the numerator
	Arms          []Arm       `json:"variants"`            // the arms, weighted, one of them the control
	Status        Status      `json:"status"`              // running while it assigns and measures, decided once a winner is promoted
	Winner        string      `json:"winner,omitempty"`    // the arm promoted to the whole rollout
	CreatedBy     string      `json:"createdBy,omitempty"` // the credential that registered it
	CreatedAt     string      `json:"createdAt,omitempty"` // when it started assigning
	DecidedBy     string      `json:"decidedBy,omitempty"` // the credential that promoted the winner
	DecidedAt     string      `json:"decidedAt,omitempty"` // when the promotion took effect
}

// controlKey returns the experiment's control variant key: the arm flagged
// Control, else the FIRST variant (deterministic baseline). "" only when there are
// no variants (a create-time invariant forbids that).
func (e Trial) controlKey() string {
	for _, v := range e.Arms {
		if v.Control {
			return v.Key
		}
	}
	if len(e.Arms) > 0 {
		return e.Arms[0].Key
	}
	return ""
}

// hasVariant reports whether key names one of the experiment's variants.
func (e Trial) hasVariant(key string) bool {
	for _, v := range e.Arms {
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
