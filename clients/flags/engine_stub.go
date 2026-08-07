//go:build !cgo || !flags_native

package flags

// The default build carries no native evaluator. Every evaluation errors, so
// resolve() falls back to env/default and the HTTP surface answers 503 —
// fail-safe, never fail-wrong.
//
// This is the DEFAULT half of the pair, and deliberately so. The native
// evaluator is a Rust staticlib that has to be compiled before Go can link it,
// so making it the default made `go build ./cmd/cloud` fail on a fresh clone
// with a linker error about a missing .a file — a Rust toolchain as the price of
// admission to a Go project, and a first impression that reads as "the tree is
// broken". Feature-flag evaluation is one subsystem; it should not be able to
// stop the binary from being built at all.
//
// So the native engine moved behind its own tag (`-tags flags_native`, or
// `make native`) and the fallback became what you get by default. Note that the
// tag is what selects it, not cgo: cgo is ON in a default build and links plenty
// of C already, so keying on cgo alone could never express "cgo, but no Rust".

import (
	"encoding/json"
	"fmt"
)

const engineAvailable = false

func engineEvaluate(_, _ []byte) (json.RawMessage, error) {
	return nil, fmt.Errorf("flags: native evaluator not built (build with -tags flags_native after `make native`)")
}
