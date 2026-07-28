package flags

// The evaluator — github.com/hanzoai/flags/go, a stateless PostHog-compatible
// engine (rollout hash, property operators, variants, payloads) compiled into
// this binary. Evaluation is a pure in-memory function of (definitions JSON,
// evaluation context JSON): no KV, no network, no state — SQLite-backed
// definitions come from the Go side and the result returns in microseconds.
//
// This used to be a Rust staticlib linked over cgo, which meant the flags path
// could only be built with cgo enabled and only after a prebuilt
// libhanzo_flags.a had been staged on the link line. The Go port is pinned to
// that implementation's answers by a 621-case parity table in its own repo, so
// nothing about the semantics moved when the language did.

import (
	"encoding/json"
	"fmt"

	eval "github.com/hanzoai/flags/go"
)

// engineEvaluate runs the evaluator over one definitions array and one
// evaluation context (both JSON) and returns the PostHog-shaped response.
func engineEvaluate(defsJSON, ctxJSON []byte) (json.RawMessage, error) {
	out, err := eval.EvaluateJSON(defsJSON, ctxJSON)
	if err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	return json.RawMessage(out), nil
}
