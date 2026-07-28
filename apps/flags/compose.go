package flags

// compose.go — the in-process COMPOSITION seam other subsystems evaluate flags
// through, one-way (they import flags; flags imports none of them). It is the same
// pattern SetPlatformSwitch/Bool/Register expose for admission: exported functions
// over the process-wide `mounted` client, so a composing subsystem reaches the ONE
// evaluator without an HTTP hop and without a second engine.
//
// The experiments primitive (clients/experiments) is the first consumer: an
// experiment's ASSIGNMENT is a flags evaluation (Assign), its assignment flag is a
// flags definition it registers (PutDef) and rewrites on decision (GetDef+PutDef).
// Subject -> variant stays a PURE deterministic function of (key, subject,
// definition) computed by engineEvaluate — there is exactly one bucketing.

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Assignment is one subject's deterministic evaluation of a single flag: the
// multivariate Variant key (empty when the subject is not enrolled — the flag
// evaluated to boolean false), whether the flag is On (a variant, or boolean true),
// and the matched Payload (featureFlagPayloads[key], may be nil). It is the value
// the experiments primitive composes for assignment.
type Assignment struct {
	Variant string          `json:"variant"`
	On      bool            `json:"on"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Assign evaluates one (org, project) flag for one subject and returns its
// deterministic assignment. This is THE assignment seam: subject -> variant is a
// pure function of (key, subject, definition) via the same engineEvaluate the
// /v1/flags surface runs — no second bucketing engine, no assignment store.
// personProps is the optional PostHog person_properties bag the flag's targeting
// groups read (nil when the experiment targets everyone). Fails CLOSED: an
// unconfigured engine returns an error, never a fabricated variant.
func Assign(org, project, key, subject string, personProps json.RawMessage) (Assignment, error) {
	if mounted == nil || !mounted.configured() {
		return Assignment{}, fmt.Errorf("flags: assignment engine not configured")
	}
	if subject == "" {
		return Assignment{}, fmt.Errorf("flags: assign needs a subject")
	}
	ctx := map[string]json.RawMessage{
		"distinct_id": json.RawMessage(strconv.Quote(subject)),
	}
	if len(personProps) > 0 {
		ctx["person_properties"] = personProps
	}
	ctxJSON, err := json.Marshal(ctx)
	if err != nil {
		return Assignment{}, err
	}
	res, err := mounted.evaluateProject(org, project, ctxJSON)
	if err != nil {
		return Assignment{}, err
	}
	var out struct {
		FeatureFlags        map[string]json.RawMessage `json:"featureFlags"`
		FeatureFlagPayloads map[string]json.RawMessage `json:"featureFlagPayloads"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return Assignment{}, err
	}
	raw, ok := out.FeatureFlags[key]
	if !ok {
		return Assignment{}, nil // flag absent for this subject -> not enrolled
	}
	a := Assignment{Payload: out.FeatureFlagPayloads[key]}
	// featureFlags[key] is one of: false | true | "<variant>". A JSON string is
	// always a variant; a JSON bool is on/off — switch on the decoded type so a
	// variant literally named "true"/"false" is impossible to confuse.
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return Assignment{}, err
	}
	switch t := v.(type) {
	case string:
		a.Variant, a.On = t, true
	case bool:
		a.On = t
	}
	return a, nil
}

// PutDef writes (creates or overwrites) a flag definition for (org, project) and
// records the change in the flag's activity log. The experiments primitive composes
// it to register an experiment's multivariate assignment flag on create, and to
// rewrite the variant weights on decide (promote a winner to 100%). One definition
// store, one evaluator — no duplication.
func PutDef(org, project, key string, definition json.RawMessage, actor string) error {
	if mounted == nil || mounted.stores == nil {
		return fmt.Errorf("flags: not mounted")
	}
	st, err := mounted.stores.For(org, project)
	if err != nil {
		return err
	}
	return st.Upsert(key, definition, actor)
}

// GetDef reads a flag definition for (org, project). ok is false when no such flag
// exists. The experiments primitive composes it on decide to read the current
// assignment flag before rewriting its variant weights.
func GetDef(org, project, key string) (definition json.RawMessage, ok bool, err error) {
	if mounted == nil || mounted.stores == nil {
		return nil, false, fmt.Errorf("flags: not mounted")
	}
	st, err := mounted.stores.For(org, project)
	if err != nil {
		return nil, false, err
	}
	row, found, err := st.Get(key)
	if err != nil || !found {
		return nil, false, err
	}
	return row.Definition, true, nil
}
