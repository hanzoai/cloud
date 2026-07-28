//go:build !cgo

package flags

// Pure-Go builds carry no native evaluator (the same posture as the SQLCipher
// at-rest layer: cgo builds get the real engine, !cgo builds degrade loudly).
// Every evaluation errors, so resolve() falls back to env/default and the HTTP
// surface answers 503 — fail-safe, never fail-wrong.

import (
	"encoding/json"
	"fmt"
)

const engineAvailable = false

func engineEvaluate(_, _ []byte) (json.RawMessage, error) {
	return nil, fmt.Errorf("flags: native evaluator not built (cgo disabled)")
}
